// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	b64 "encoding/base64"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	pionturn "github.com/pion/turn/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/client"
	"github.com/the-sarge/turn/v5/internal/proto"
)

const testAddr = "127.0.0.1:3478"

func TestNewClientRejectsNilConn(t *testing.T) {
	_, err := NewClient(&ClientConfig{
		Server: netip.MustParseAddrPort("192.0.2.1:3478"),
	})
	assert.ErrorIs(t, err, errNilConn)
}

func TestNewClientRejectsNonCanonicalServer(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck

	mapped := netip.AddrPortFrom(netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 192, 13: 0, 14: 2, 15: 1}), 3478)
	for name, server := range map[string]netip.AddrPort{
		"zero value":       {},
		"IPv4-mapped IPv6": mapped,
		"zero port":        netip.MustParseAddrPort("192.0.2.1:0"),
		"unspecified":      netip.MustParseAddrPort("0.0.0.0:3478"),
		"multicast":        netip.MustParseAddrPort("224.0.0.1:3478"),
		"zoned link-local": netip.MustParseAddrPort("[fe80::1%eth0]:3478"),
	} {
		t.Run(name, func(t *testing.T) {
			_, newErr := NewClient(&ClientConfig{Conn: conn, Server: server})
			assert.ErrorIs(t, newErr, errInvalidServer)
		})
	}

	c, err := NewClient(&ClientConfig{Conn: conn, Server: netip.MustParseAddrPort("192.0.2.1:3478")})
	require.NoError(t, err)
	c.Close()
}

// TestHandleInboundAdmitsOnlyServer proves the server-source admission owned by
// HandleInbound: a datagram whose canonical source is not the configured
// Server is ignored with a nil error and zero delivery, while a datagram from
// the Server (in any *net.UDPAddr spelling of it) is delivered.
func TestHandleInboundAdmitsOnlyServer(t *testing.T) {
	server := netip.MustParseAddrPort("192.0.2.1:3478")

	newClient := func(t *testing.T) *Client {
		t.Helper()
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		c, err := NewClient(&ClientConfig{Conn: conn, Server: server})
		require.NoError(t, err)
		t.Cleanup(c.Close)

		return c
	}

	// pendingResponse inserts a pending transaction and returns the STUN
	// success response that completes it plus a channel carrying whatever the
	// waiter observes. Delivery is observable as the transaction leaving trMap
	// and the waiter receiving the message.
	pendingResponse := func(t *testing.T, c *Client) ([]byte, <-chan client.TransactionResult) {
		t.Helper()
		res, err := stun.Build(stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse))
		require.NoError(t, err)
		key := b64.StdEncoding.EncodeToString(res.TransactionID[:])
		tr := client.NewTransaction(&client.TransactionConfig{Key: key, Raw: nil, To: c.serverAddr, Interval: time.Hour})
		c.trMap.Insert(key, tr)

		waited := make(chan client.TransactionResult, 1)
		go func() { waited <- tr.WaitForResult() }()

		return res.Raw, waited
	}

	expectDelivered := func(t *testing.T, c *Client, waited <-chan client.TransactionResult) {
		t.Helper()
		select {
		case res := <-waited:
			assert.NoError(t, res.Err)
			assert.NotNil(t, res.Msg)
		case <-time.After(time.Second):
			assert.Fail(t, "waiter was not woken by a delivered server response")
		}
		assert.Equal(t, 0, c.trMap.Size(), "delivered transaction leaves the map")
	}

	expectIgnored := func(t *testing.T, c *Client, waited <-chan client.TransactionResult) {
		t.Helper()
		select {
		case res := <-waited:
			assert.Failf(t, "waiter was woken by an ignored datagram", "res: %+v", res)
		case <-time.After(100 * time.Millisecond):
		}
		assert.Equal(t, 1, c.trMap.Size(), "ignored datagram leaves the transaction pending")
	}

	t.Run("server source is delivered", func(t *testing.T) {
		c := newClient(t)
		raw, waited := pendingResponse(t, c)
		assert.NoError(t, c.HandleInbound(raw, net.UDPAddrFromAddrPort(server)))
		expectDelivered(t, c, waited)
	})

	t.Run("IPv4-mapped 16-byte spelling of the server is delivered", func(t *testing.T) {
		c := newClient(t)
		raw, waited := pendingResponse(t, c)
		from := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478} // 16-byte mapped form, as a dual-stack socket reports it
		require.Len(t, from.IP, net.IPv6len)
		assert.NoError(t, c.HandleInbound(raw, from))
		expectDelivered(t, c, waited)
	})

	t.Run("other UDP source is ignored with zero delivery", func(t *testing.T) {
		c := newClient(t)
		raw, waited := pendingResponse(t, c)
		assert.NoError(t, c.HandleInbound(raw, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3479}))
		assert.NoError(t, c.HandleInbound(raw, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 3478}))
		expectIgnored(t, c, waited)
	})

	t.Run("non-UDP source is ignored with zero delivery", func(t *testing.T) {
		c := newClient(t)
		raw, waited := pendingResponse(t, c)
		assert.NoError(t, c.HandleInbound(raw, &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478}))
		assert.NoError(t, c.HandleInbound(raw, nil))
		expectIgnored(t, c, waited)
	})

	t.Run("non-protocol datagram: error from server, ignored from others", func(t *testing.T) {
		c := newClient(t)
		junk := []byte("not stun, not channeldata")
		assert.ErrorIs(t, c.HandleInbound(junk, net.UDPAddrFromAddrPort(server)), errUnexpectedServerDatagram)
		assert.NoError(t, c.HandleInbound(junk, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 3478}))
	})
}

// Create an allocation, and then delete all nonces
// The subsequent Write on the allocation will cause a CreatePermission
// which will be forced to handle a stale nonce response.
func TestClientNonceExpiration(t *testing.T) {
	udpListener, err := net.ListenPacket("udp4", "0.0.0.0:3478") // nolint: noctx
	assert.NoError(t, err)

	server, err := pionturn.NewServer(pionturn.ServerConfig{
		AuthHandler: func(ra *pionturn.RequestAttributes) (userID string, key []byte, ok bool) {
			return ra.Username, pionturn.GenerateAuthKey(ra.Username, ra.Realm, "pass"), true
		},
		PacketConnConfigs: []pionturn.PacketConnConfig{
			{
				PacketConn: udpListener,
				RelayAddressGenerator: &pionturn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP("127.0.0.1"),
					Address:      "0.0.0.0",
				},
			},
		},
		Realm: "pion.ly",
	})
	assert.NoError(t, err)

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
	assert.NoError(t, err)

	client, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   netip.MustParseAddrPort(testAddr),
		Username: "foo",
		Password: "pass",
	})
	assert.NoError(t, err)
	assert.NoError(t, client.Listen())

	allocation, err := client.Allocate()
	assert.NoError(t, err)

	_, err = allocation.WriteTo([]byte{0x00}, netip.MustParseAddrPort("127.0.0.1:8080"))
	assert.NoError(t, err)

	// Shutdown
	assert.NoError(t, allocation.Close())
	assert.NoError(t, conn.Close())
	assert.NoError(t, server.Close())
}

func TestInferAddressFamilyFromConn(t *testing.T) {
	t.Run("IPv4 UDP connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp4", "0.0.0.0:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		family, err := inferAddressFamilyFromConn(conn)
		assert.NoError(t, err)
		assert.Equal(t, proto.RequestedFamilyIPv4, family)
	})

	t.Run("IPv6 UDP connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp6", "[::]:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		family, err := inferAddressFamilyFromConn(conn)
		assert.NoError(t, err)
		assert.Equal(t, proto.RequestedFamilyIPv6, family)
	})
}

func TestGetRequestedAddressFamily(t *testing.T) {
	log := logging.NewDefaultLoggerFactory().NewLogger("test")

	t.Run("Infer IPv4 from connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp4", "0.0.0.0:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		// Should infer IPv4 from connection
		family := getRequestedAddressFamily(log, conn)
		assert.Equal(t, proto.RequestedFamilyIPv4, family)
	})

	t.Run("Infer IPv6 from connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp6", "[::]:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		// Should infer IPv6 from connection
		family := getRequestedAddressFamily(log, conn)
		assert.Equal(t, proto.RequestedFamilyIPv6, family)
	})
}

func TestAppendRequestedAddressFamily(t *testing.T) {
	t.Run("IPv4 omits attribute", func(t *testing.T) {
		setters := appendRequestedAddressFamily(
			[]stun.Setter{stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest)},
			proto.RequestedFamilyIPv4,
		)

		msg, err := stun.Build(setters...)
		require.NoError(t, err)

		assert.False(t, msg.Contains(stun.AttrRequestedAddressFamily))
	})

	t.Run("IPv6 includes attribute", func(t *testing.T) {
		setters := appendRequestedAddressFamily(
			[]stun.Setter{stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest)},
			proto.RequestedFamilyIPv6,
		)

		msg, err := stun.Build(setters...)
		require.NoError(t, err)

		var raf proto.RequestedAddressFamily
		require.NoError(t, raf.GetFrom(msg))
		assert.Equal(t, proto.RequestedFamilyIPv6, raf)
	})
}

type channelBindFilterConn struct {
	net.PacketConn

	doFilter bool
}

func (c *channelBindFilterConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	for {
		n, addr, err = c.PacketConn.ReadFrom(p)

		if c.doFilter {
			stunMsg := &stun.Message{Raw: p[:n]}
			if err := stunMsg.Decode(); err == nil && stunMsg.Type.Method == stun.MethodChannelBind {
				continue
			}
		}

		return
	}
}

func TestClientE2E(t *testing.T) {
	doTest := func(disableChannelBind bool) {
		udpListener, err := net.ListenPacket("udp4", "0.0.0.0:3478") // nolint: noctx
		assert.NoError(t, err)

		server, err := pionturn.NewServer(pionturn.ServerConfig{
			AuthHandler: func(ra *pionturn.RequestAttributes) (userID string, key []byte, ok bool) {
				return ra.Username, pionturn.GenerateAuthKey(ra.Username, ra.Realm, "pass"), true
			},
			PacketConnConfigs: []pionturn.PacketConnConfig{
				{
					PacketConn: &channelBindFilterConn{udpListener, disableChannelBind},
					RelayAddressGenerator: &pionturn.RelayAddressGeneratorStatic{
						RelayAddress: net.ParseIP("127.0.0.1"),
						Address:      "0.0.0.0",
					},
				},
			},
			Realm:              "pion.ly",
			AllocationLifetime: time.Second,
			PermissionTimeout:  time.Millisecond * 100,
			ChannelBindTimeout: time.Millisecond * 100,
		})
		assert.NoError(t, err)

		stunClientConn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
		assert.NoError(t, err)

		client, err := NewClient(&ClientConfig{
			Conn:                      stunClientConn,
			Server:                    netip.MustParseAddrPort(testAddr),
			Username:                  "foo",
			Password:                  "pass",
			PermissionRefreshInterval: time.Millisecond * 50,
			bindingRefreshInterval:    time.Millisecond * 50,
			bindingCheckInterval:      time.Millisecond * 50,
		})
		assert.NoError(t, err)
		assert.NoError(t, client.Listen())

		allocation, err := client.Allocate()
		assert.NoError(t, err)

		remotePeerConn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
		assert.NoError(t, err)

		remotePeerAddr, ok := remotePeerConn.LocalAddr().(*net.UDPAddr)
		assert.True(t, ok)

		relayedAddr := allocation.RelayedAddr()

		sendPackets := func(write func([]byte) error, read func([]byte) (int, error)) {
			const expectedPktCount = 25
			expectedPacket := []byte{0xDE, 0xAD, 0xBE, 0xEF}

			pktCount := atomic.Uint32{}
			go func() {
				buff := make([]byte, len(expectedPacket))
				for pktCount.Load() < expectedPktCount {
					i, readErr := read(buff)
					assert.NoError(t, readErr)

					assert.Equal(t, expectedPacket, buff[:i])
					pktCount.Add(1)
				}
			}()
			for pktCount.Load() < expectedPktCount {
				assert.NoError(t, write(expectedPacket))

				time.Sleep(time.Millisecond * 25)
			}
		}

		peer := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(remotePeerAddr.Port)) //nolint:gosec // test port
		sendPackets(
			func(p []byte) error {
				_, writeErr := allocation.WriteTo(p, peer)

				return writeErr
			},
			func(p []byte) (int, error) {
				n, _, readErr := remotePeerConn.ReadFrom(p)

				return n, readErr
			},
		)
		relayedUDP := net.UDPAddrFromAddrPort(relayedAddr)
		sendPackets(
			func(p []byte) error {
				_, writeErr := remotePeerConn.WriteTo(p, relayedUDP)

				return writeErr
			},
			func(p []byte) (int, error) {
				n, _, readErr := allocation.ReadFrom(p)

				return n, readErr
			},
		)

		// Shutdown
		assert.NoError(t, remotePeerConn.Close())
		assert.NoError(t, allocation.Close())
		assert.NoError(t, stunClientConn.Close())
		assert.NoError(t, server.Close())
	}

	doTest(true)
	doTest(false)
}
