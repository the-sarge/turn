// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// Package turntest_test smoke-tests each Server knob through the real client:
// this external test package may import the root package, while turntest
// itself must never do so.
package turntest_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	turn "github.com/the-sarge/turn/v5"
	"github.com/the-sarge/turn/v5/turntest"
)

const (
	testRealm    = "turntest.local"
	testUsername = "user"
	testPassword = "secret"
)

func options() turntest.Options {
	return turntest.Options{Realm: testRealm, Username: testUsername, Password: testPassword}
}

// startClient returns a client bound to srv with a running read pump; the
// socket and client are closed in test cleanup.
func startClient(t *testing.T, srv *turntest.Server) *turn.Client {
	t.Helper()

	network, listen := "udp4", "0.0.0.0:0"
	if srv.Addr().Addr().Is6() {
		network, listen = "udp6", "[::1]:0"
	}
	conn, err := net.ListenPacket(network, listen) //nolint:noctx // test socket
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client, err := turn.NewClient(&turn.ClientConfig{
		Conn:     conn,
		Server:   srv.Addr(),
		Username: testUsername,
		Password: testPassword,
	})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}
			if handleErr := client.HandleInbound(buf[:n], from); handleErr != nil {
				return
			}
		}
	}()

	return client
}

// TestDenyPermissions proves the DenyPermissions knob: the 403 surfaces from
// PreparePeer as a typed TURN error.
func TestDenyPermissions(t *testing.T) {
	opts := options()
	opts.DenyPermissions = true
	srv := turntest.Start(t, opts)
	client := startClient(t, srv)

	alloc, err := client.Allocate(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = alloc.Close() })

	err = alloc.PreparePeer(context.Background(), netip.MustParseAddrPort("127.0.0.1:8080"))
	var turnErr *stun.TurnError
	require.ErrorAs(t, err, &turnErr)
	assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
}

// TestRejectChannelBind proves the RejectChannelBind knob: the 400 on a fresh
// binding closes the allocation per the client's existing rules, so later
// writes fail with the recorded cause.
func TestRejectChannelBind(t *testing.T) {
	opts := options()
	opts.RejectChannelBind = true
	srv := turntest.Start(t, opts)
	client := startClient(t, srv)

	alloc, err := client.Allocate(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = alloc.Close() })

	peer := netip.MustParseAddrPort("127.0.0.1:8080")
	require.Error(t, alloc.PreparePeer(context.Background(), peer))

	_, err = alloc.WriteTo([]byte{0x00}, peer)
	assert.ErrorIs(t, err, net.ErrClosed)
	assert.ErrorIs(t, err, turn.ErrChannelBindFailed)
}

// TestRelayIPOverride proves the RelayIPOverride knob: an unspecified
// advertised relayed address is rejected by Allocate, and the client's
// lifetime-0 release deletes the server-side allocation.
func TestRelayIPOverride(t *testing.T) {
	opts := options()
	opts.RelayIPOverride = net.IPv4zero
	srv := turntest.Start(t, opts)
	client := startClient(t, srv)

	_, err := client.Allocate(context.Background())
	require.ErrorIs(t, err, turn.ErrInvalidRelayedAddress)
	assert.Eventually(t, func() bool { return srv.AllocationCount() == 0 },
		2*time.Second, 10*time.Millisecond,
		"the rejected allocation should be released by the lifetime-0 refresh")
}

// TestIPv6 proves the IPv6 knob: the server listens on [::1] and relays on an
// IPv6 loopback address the client accepts as canonical.
func TestIPv6(t *testing.T) {
	opts := options()
	opts.IPv6 = true
	srv := turntest.Start(t, opts)
	require.True(t, srv.Addr().Addr().Is6())
	client := startClient(t, srv)

	alloc, err := client.Allocate(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = alloc.Close() })

	assert.Equal(t, netip.MustParseAddr("::1"), alloc.RelayedAddr().Addr())
}

// TestAllocationCount proves the AllocationCount observation: one live
// allocation, then zero after the client's Close sends the lifetime-0
// Refresh.
func TestAllocationCount(t *testing.T) {
	srv := turntest.Start(t, options())
	client := startClient(t, srv)

	alloc, err := client.Allocate(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, srv.AllocationCount())

	require.NoError(t, alloc.Close())
	assert.Eventually(t, func() bool { return srv.AllocationCount() == 0 },
		2*time.Second, 10*time.Millisecond,
		"the closed allocation should be deleted by the lifetime-0 refresh")
}
