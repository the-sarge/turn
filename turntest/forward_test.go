// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turntest

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/internal/proto"
)

// peerDeliveryConn captures synchronous fixture writes without a socket or sweeper.
type peerDeliveryConn struct {
	net.PacketConn
	packets [][]byte
	to      []net.Addr
}

func (c *peerDeliveryConn) WriteTo(data []byte, to net.Addr) (int, error) {
	c.packets = append(c.packets, append([]byte(nil), data...))
	c.to = append(c.to, to)

	return len(data), nil
}

func TestForwardFromPeerRequiresLivePermission(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		name := "before sweep"
		if sweep {
			name = "after sweep"
		}
		t.Run(name, func(t *testing.T) {
			srv, alloc, conn, peer := peerDeliveryFixture()
			alloc.permissions[peer.Addr()] = time.Now().Add(-time.Hour)
			if sweep {
				require.Empty(t, srv.expire(time.Now()))
				require.Empty(t, alloc.permissions)
			}
			require.Len(t, alloc.bindings, 1, "permission expiry must leave the live binding intact")

			srv.forwardFromPeer(alloc, []byte("expired permission"), net.UDPAddrFromAddrPort(peer))

			require.Empty(t, conn.packets, "a live channel binding must not bypass an expired or missing permission")
		})
	}
}

func TestForwardFromPeerWithLivePermission(t *testing.T) {
	for _, binding := range []string{"live", "expired", "absent"} {
		t.Run(binding+" binding", func(t *testing.T) {
			srv, alloc, conn, peer := peerDeliveryFixture()
			switch binding {
			case "expired":
				alloc.bindings[0x4000] = bindingEntry{peer: peer, expiresAt: time.Now().Add(-time.Hour)}
			case "absent":
				delete(alloc.bindings, 0x4000)
			}
			payload := []byte("permitted peer")

			srv.forwardFromPeer(alloc, payload, net.UDPAddrFromAddrPort(peer))

			require.Len(t, conn.packets, 1)
			require.Equal(t, alloc.clientAddr, conn.to[0])
			if binding == "live" {
				assertPeerChannelData(t, conn.packets[0], payload)

				return
			}
			msg := &stun.Message{Raw: conn.packets[0]}
			require.NoError(t, msg.Decode())
			require.Equal(t, stun.NewType(stun.MethodData, stun.ClassIndication), msg.Type)
			var gotPeer proto.PeerAddress
			require.NoError(t, gotPeer.GetFrom(msg))
			require.True(t, net.IP(peer.Addr().AsSlice()).Equal(gotPeer.IP))
			require.Equal(t, int(peer.Port()), gotPeer.Port)
			var gotData proto.Data
			require.NoError(t, gotData.GetFrom(msg))
			require.Equal(t, payload, []byte(gotData))
		})
	}
}

func TestChannelBindRefreshesPermissionForPeerDelivery(t *testing.T) {
	srv, alloc, conn, peer := peerDeliveryFixture()
	srv.opts.PermissionTimeout = 2 * time.Hour
	srv.opts.ChannelBindTimeout = 3 * time.Hour
	alloc.permissions[peer.Addr()] = time.Now().Add(-time.Hour)
	before := time.Now()

	require.True(t, srv.bindChannel(alloc.clientAddr.String(), 0x4000, peer))

	after := time.Now()
	require.False(t, alloc.permissions[peer.Addr()].Before(before.Add(srv.opts.PermissionTimeout)))
	require.False(t, alloc.permissions[peer.Addr()].After(after.Add(srv.opts.PermissionTimeout)))
	require.False(t, alloc.bindings[0x4000].expiresAt.Before(before.Add(srv.opts.ChannelBindTimeout)))
	require.False(t, alloc.bindings[0x4000].expiresAt.After(after.Add(srv.opts.ChannelBindTimeout)))
	payload := []byte("refreshed permission")
	srv.forwardFromPeer(alloc, payload, net.UDPAddrFromAddrPort(peer))
	require.Len(t, conn.packets, 1)
	require.Equal(t, alloc.clientAddr, conn.to[0])
	assertPeerChannelData(t, conn.packets[0], payload)
}

func assertPeerChannelData(t *testing.T, raw, payload []byte) {
	t.Helper()

	data := &proto.ChannelData{Raw: raw}
	require.NoError(t, data.Decode())
	require.Equal(t, proto.ChannelNumber(0x4000), data.Number)
	require.Equal(t, payload, data.Data)
}

func peerDeliveryFixture() (*Server, *allocationState, *peerDeliveryConn, netip.AddrPort) {
	now := time.Now()
	peer := netip.MustParseAddrPort("127.0.0.1:8080")
	conn := &peerDeliveryConn{}
	alloc := &allocationState{
		clientAddr: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:9000")),
		expiresAt:  now.Add(time.Hour),
		permissions: map[netip.Addr]time.Time{
			peer.Addr(): now.Add(time.Hour),
		},
		bindings: map[uint16]bindingEntry{
			0x4000: {peer: peer, expiresAt: now.Add(time.Hour)},
		},
	}
	srv := &Server{
		listener: conn,
		allocs:   map[string]*allocationState{alloc.clientAddr.String(): alloc},
	}

	return srv, alloc, conn, peer
}
