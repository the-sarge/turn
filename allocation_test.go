// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/client"
	"github.com/the-sarge/turn/v5/internal/proto"
)

// allocHarness drives an Allocation over a UDPConn whose transactions are
// scripted to succeed, capturing every base-socket write.
type allocHarness struct {
	alloc     *Allocation
	permCount atomic.Int32
	bindCount atomic.Int32
	writes    struct {
		sync.Mutex
		data [][]byte
	}
}

func newAllocHarness(t *testing.T) *allocHarness {
	t.Helper()

	harness := &allocHarness{}
	conn := client.NewUDPConn(&client.AllocationConfig{
		WriteTo: func(data []byte, _ net.Addr) (int, error) {
			harness.writes.Lock()
			harness.writes.data = append(harness.writes.data, append([]byte(nil), data...))
			harness.writes.Unlock()

			return len(data), nil
		},
		PerformTransaction: func(msg *stun.Message, _ net.Addr, _ bool) (client.TransactionResult, error) {
			switch msg.Type.Method {
			case stun.MethodCreatePermission:
				harness.permCount.Add(1)

				return client.TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
				)}, nil
			case stun.MethodChannelBind:
				harness.bindCount.Add(1)

				return client.TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
				)}, nil
			default:
				return client.TransactionResult{}, nil
			}
		},
		OnDeallocated: func(net.Addr) {},
		RelayedAddr:   &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321},
		ServerAddr:    &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478},
		Username:      stun.NewUsername("user"),
		Realm:         stun.NewRealm("realm"),
		Integrity:     stun.NewShortTermIntegrity("pass"),
		Nonce:         stun.NewNonce("nonce"),
		Lifetime:      time.Hour,
		Log:           logging.NewDefaultLoggerFactory().NewLogger("test"),
	})
	t.Cleanup(func() { _ = conn.Close() })

	harness.alloc = newAllocation(conn, netip.MustParseAddrPort("127.0.0.1:54321"))

	return harness
}

func (harness *allocHarness) lastWrite() []byte {
	harness.writes.Lock()
	defer harness.writes.Unlock()

	if len(harness.writes.data) == 0 {
		return nil
	}

	return harness.writes.data[len(harness.writes.data)-1]
}

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
	harness := newAllocHarness(t)

	for name, peer := range invalidPeers() {
		t.Run("PreparePeer "+name, func(t *testing.T) {
			assert.ErrorIs(t, harness.alloc.PreparePeer(context.Background(), peer), ErrInvalidPeer)
		})
		t.Run("WriteTo "+name, func(t *testing.T) {
			_, err := harness.alloc.WriteTo([]byte("data"), peer)
			assert.ErrorIs(t, err, ErrInvalidPeer)
		})
	}

	assert.Equal(t, int32(0), harness.permCount.Load(), "rejected peers must not reach the permission machinery")
	assert.Equal(t, int32(0), harness.bindCount.Load(), "rejected peers must not reach the binding machinery")
}

// TestAllocationPeerAliasCanonicalizes is the positive anchor: an IPv4-mapped
// IPv6 spelling of a peer canonicalizes onto the same permission and channel
// binding as the IPv4 literal, so writes after PreparePeer are ChannelData.
func TestAllocationPeerAliasCanonicalizes(t *testing.T) {
	harness := newAllocHarness(t)

	mapped := netip.AddrPortFrom(
		netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 127, 13: 0, 14: 0, 15: 1}), 1234)
	require.True(t, mapped.Addr().Is4In6(), "test input must be the mapped spelling")

	assert.NoError(t, harness.alloc.PreparePeer(context.Background(), mapped))
	assert.Equal(t, int32(1), harness.permCount.Load())
	assert.Equal(t, int32(1), harness.bindCount.Load())

	n, err := harness.alloc.WriteTo([]byte("hello"), netip.MustParseAddrPort("127.0.0.1:1234"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.True(t, proto.IsChannelData(harness.lastWrite()),
		"write to the unmapped spelling of a prepared mapped peer must be ChannelData")
	assert.Equal(t, int32(1), harness.bindCount.Load(), "alias must not create a second binding")

	assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:54321"), harness.alloc.RelayedAddr())
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
	require.NoError(t, turnClient.Listen())
	defer turnClient.Close()

	alloc, err := turnClient.Allocate()
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
// PrepareUDPPeer and the deleted deadline/LocalAddr surface must not resolve.
func TestDeletedSurfaceDoesNotResolve(t *testing.T) {
	_, ok := reflect.TypeFor[*Client]().MethodByName("PrepareUDPPeer")
	assert.False(t, ok, "Client.PrepareUDPPeer moved to Allocation.PreparePeer")

	for _, name := range []string{"SetReadDeadline", "SetWriteDeadline", "SetDeadline", "LocalAddr"} {
		_, ok := reflect.TypeFor[*Allocation]().MethodByName(name)
		assert.False(t, ok, "Allocation must not expose %s", name)
	}
}
