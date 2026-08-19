// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/client"
	"github.com/the-sarge/turn/v5/internal/proto"
)

// invalidPeers is the rejection table for the canonical netip.AddrPort peer
// domain: every entry must fail PreparePeer and WriteTo with ErrInvalidPeer.
func invalidPeers() map[string]netip.AddrPort {
	return map[string]netip.AddrPort{
		"zero value":       {},
		"zero port":        netip.MustParseAddrPort("192.0.2.5:0"),
		"unspecified":      netip.MustParseAddrPort("0.0.0.0:2000"),
		"multicast":        netip.MustParseAddrPort("224.0.0.1:2000"),
		"zoned link-local": netip.MustParseAddrPort("[fe80::1%en0]:2000"),
	}
}

func TestAllocationRejectsInvalidPeers(t *testing.T) {
	script := &scriptedAllocationScript{}
	allocation, _ := newScriptedAllocation(t, script)

	for name, peer := range invalidPeers() {
		t.Run("PreparePeer "+name, func(t *testing.T) {
			assert.ErrorIs(t, allocation.PreparePeer(context.Background(), peer), ErrInvalidPeer)
		})
		t.Run("WriteTo "+name, func(t *testing.T) {
			_, err := allocation.WriteTo([]byte("data"), peer)
			assert.ErrorIs(t, err, ErrInvalidPeer)
		})
	}

	assert.Equal(t, int32(0), script.permissionCount.Load(), "rejected peers must not reach the permission machinery")
	assert.Equal(t, int32(0), script.bindingCount.Load(), "rejected peers must not reach the binding machinery")
}

// TestAllocationPeerAliasCanonicalizes is the positive anchor: an IPv4-mapped
// IPv6 spelling of a peer canonicalizes onto the same permission and channel
// binding as the IPv4 literal, so writes after PreparePeer are ChannelData.
func TestAllocationPeerAliasCanonicalizes(t *testing.T) {
	script := &scriptedAllocationScript{}
	allocation, _ := newScriptedAllocation(t, script)

	mapped := netip.AddrPortFrom(
		netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 127, 13: 0, 14: 0, 15: 1}), 1234)
	require.True(t, mapped.Addr().Is4In6(), "test input must be the mapped spelling")

	assert.NoError(t, allocation.PreparePeer(context.Background(), mapped))
	assert.Equal(t, int32(1), script.permissionCount.Load())
	assert.Equal(t, int32(1), script.bindingCount.Load())

	n, err := allocation.WriteTo([]byte("hello"), netip.MustParseAddrPort("127.0.0.1:1234"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.True(t, proto.IsChannelData(script.lastWrite()),
		"write to the unmapped spelling of a prepared mapped peer must be ChannelData")
	assert.Equal(t, int32(1), script.bindingCount.Load(), "alias must not create a second binding")

	assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:54321"), allocation.RelayedAddr())
}

// scriptInvalidRelayedServer answers Allocate requests on serverSock with a
// 401 challenge and then a success carrying an uncanonicalizable relayed
// address, and reports each observed Refresh lifetime on refreshLifetime.
func scriptInvalidRelayedServer(serverSock net.PacketConn, refreshLifetime chan<- time.Duration) {
	buf := make([]byte, 1500)
	for {
		n, from, readErr := serverSock.ReadFrom(buf)
		if readErr != nil {
			return
		}
		msg := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if decodeErr := msg.Decode(); decodeErr != nil {
			continue
		}
		switch msg.Type.Method {
		case stun.MethodAllocate:
			var res *stun.Message
			if msg.Contains(stun.AttrMessageIntegrity) {
				res = stun.MustBuild(
					stun.NewTransactionIDSetter(msg.TransactionID),
					stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse),
					proto.RelayedAddress{IP: net.IPv4zero, Port: 0},
					proto.Lifetime{Duration: 10 * time.Minute},
				)
			} else {
				res = stun.MustBuild(
					stun.NewTransactionIDSetter(msg.TransactionID),
					stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse),
					stun.ErrorCodeAttribute{Code: stun.CodeUnauthorized, Reason: []byte("Unauthorized")},
					stun.NewNonce("server-nonce"),
					stun.NewRealm("pion.ly"),
				)
			}
			_, _ = serverSock.WriteTo(res.Raw, from)
		case stun.MethodRefresh:
			var lifetime proto.Lifetime
			if getErr := lifetime.GetFrom(msg); getErr == nil {
				refreshLifetime <- lifetime.Duration
			}
		default: // The scripted exchange sends nothing else.
		}
	}
}

// TestAllocateRejectsInvalidRelayedAddress scripts a server whose Allocate
// success reports an uncanonicalizable relayed address. Allocate must release
// the server-side allocation with a lifetime-0 Refresh, clear the client's
// allocation pointer, and return ErrInvalidRelayedAddress.
func TestAllocateRejectsInvalidRelayedAddress(t *testing.T) {
	serverSock, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
	require.NoError(t, err)
	defer serverSock.Close()                                   //nolint:errcheck
	clientSock, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
	require.NoError(t, err)
	defer clientSock.Close() //nolint:errcheck

	refreshLifetime := make(chan time.Duration, 4)

	go scriptInvalidRelayedServer(serverSock, refreshLifetime)

	turnClient, err := NewClient(&ClientConfig{
		Conn:     clientSock,
		Server:   netip.MustParseAddrPort(serverSock.LocalAddr().String()),
		Username: "user",
		Password: "pass",
	})
	require.NoError(t, err)
	startTestPump(t, turnClient, clientSock)
	defer turnClient.Close()

	alloc, err := turnClient.Allocate(context.Background())
	assert.ErrorIs(t, err, ErrInvalidRelayedAddress)
	assert.Nil(t, alloc)

	select {
	case lifetime := <-refreshLifetime:
		assert.Equal(t, time.Duration(0), lifetime,
			"the rejected allocation must be released with a lifetime-0 Refresh")
	case <-time.After(5 * time.Second):
		assert.Fail(t, "no deallocate Refresh observed for the rejected allocation")
	}

	assert.Nil(t, turnClient.relayedUDPConn(), "the client's allocation pointer must be cleared")
}

// TestDeletedSurfaceDoesNotResolve pins the negative API contract: the moved
// PrepareUDPPeer, the deleted deadline/LocalAddr surface, the deleted Listen
// second idiom, the removed LoggerFactory seam, and internal destination
// authority must not resolve.
func TestDeletedSurfaceDoesNotResolve(t *testing.T) {
	_, ok := reflect.TypeFor[*Client]().MethodByName("PrepareUDPPeer")
	assert.False(t, ok, "Client.PrepareUDPPeer moved to Allocation.PreparePeer")

	for _, name := range []string{"SetReadDeadline", "SetWriteDeadline", "SetDeadline", "LocalAddr"} {
		_, found := reflect.TypeFor[*Allocation]().MethodByName(name)
		assert.False(t, found, "Allocation must not expose %s", name)
	}

	_, ok = reflect.TypeFor[*Client]().MethodByName("Listen")
	assert.False(t, ok, "Client.Listen is deleted: the consumer owns the read pump")

	_, ok = reflect.TypeFor[ClientConfig]().FieldByName("LoggerFactory")
	assert.False(t, ok, "ClientConfig.LoggerFactory is deleted: the module does not log")

	allocationConfig := reflect.TypeFor[client.AllocationConfig]()
	_, ok = allocationConfig.FieldByName("ServerAddr")
	assert.False(t, ok, "AllocationConfig.ServerAddr would restore internal destination authority")

	writeTo, ok := allocationConfig.FieldByName("WriteTo")
	require.True(t, ok)
	assert.Equal(t, 1, writeTo.Type.NumIn(), "Allocation raw writes accept bytes only")

	performTransaction, ok := allocationConfig.FieldByName("PerformTransaction")
	require.True(t, ok)
	assert.Equal(t, 2, performTransaction.Type.NumIn(), "Allocation transactions accept message and wait policy only")
}
