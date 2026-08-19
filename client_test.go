// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/client"
	"github.com/the-sarge/turn/v5/internal/proto"
	"github.com/the-sarge/turn/v5/turntest"
)

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

	newClient := func(t *testing.T) (*Client, *observerConn) {
		t.Helper()
		conn := newObserverConn()

		c, err := NewClient(&ClientConfig{Conn: conn, Server: server, RTO: time.Hour})
		require.NoError(t, err)
		t.Cleanup(c.Close)

		return c, conn
	}

	// pendingResponse starts work through the Client's transaction seam and
	// returns the matching success plus the observable waiter result.
	pendingResponse := func(t *testing.T, c *Client, conn *observerConn) ([]byte, <-chan client.TransactionResult) {
		t.Helper()
		req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		res, err := stun.Build(
			stun.NewTransactionIDSetter(req.TransactionID),
			stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse),
		)
		require.NoError(t, err)

		waited := make(chan client.TransactionResult, 1)
		go func() {
			result, _ := c.performTransaction(req, false)
			waited <- result
		}()
		awaitWrite(t, conn, 1)

		return res.Raw, waited
	}

	expectDelivered := func(t *testing.T, waited <-chan client.TransactionResult) {
		t.Helper()
		select {
		case res := <-waited:
			assert.NoError(t, res.Err)
			assert.NotNil(t, res.Msg)
		case <-time.After(time.Second):
			assert.Fail(t, "waiter was not woken by a delivered server response")
		}
	}

	expectIgnored := func(t *testing.T, waited <-chan client.TransactionResult) {
		t.Helper()
		select {
		case res := <-waited:
			assert.Failf(t, "waiter was woken by an ignored datagram", "res: %+v", res)
		case <-time.After(100 * time.Millisecond):
		}
	}

	t.Run("server source is delivered", func(t *testing.T) {
		c, conn := newClient(t)
		raw, waited := pendingResponse(t, c, conn)
		assert.NoError(t, c.HandleInbound(raw, net.UDPAddrFromAddrPort(server)))
		expectDelivered(t, waited)
	})

	t.Run("IPv4-mapped 16-byte spelling of the server is delivered", func(t *testing.T) {
		c, conn := newClient(t)
		raw, waited := pendingResponse(t, c, conn)
		from := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478} // 16-byte mapped form, as a dual-stack socket reports it
		require.Len(t, from.IP, net.IPv6len)
		assert.NoError(t, c.HandleInbound(raw, from))
		expectDelivered(t, waited)
	})

	t.Run("other UDP source is ignored with zero delivery", func(t *testing.T) {
		c, conn := newClient(t)
		raw, waited := pendingResponse(t, c, conn)
		assert.NoError(t, c.HandleInbound(raw, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3479}))
		assert.NoError(t, c.HandleInbound(raw, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 3478}))
		expectIgnored(t, waited)
	})

	t.Run("non-UDP source is ignored with zero delivery", func(t *testing.T) {
		c, conn := newClient(t)
		raw, waited := pendingResponse(t, c, conn)
		assert.NoError(t, c.HandleInbound(raw, &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478}))
		assert.NoError(t, c.HandleInbound(raw, nil))
		expectIgnored(t, waited)
	})

	t.Run("non-protocol datagram: error from server, ignored from others", func(t *testing.T) {
		c, _ := newClient(t)
		junk := []byte("not stun, not channeldata")
		assert.ErrorIs(t, c.HandleInbound(junk, net.UDPAddrFromAddrPort(server)), errUnexpectedServerDatagram)
		assert.NoError(t, c.HandleInbound(junk, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 3478}))
	})
}

func TestClientCloseIsAnAbortCutNotATerminalState(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)
	cl.Close()

	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	response := stun.MustBuild(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
	)
	type outcome struct {
		result client.TransactionResult
		err    error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		result, err := cl.performTransaction(request, false)
		resultCh <- outcome{result: result, err: err}
	}()
	awaitWrite(t, conn, 1)
	require.NoError(t, cl.HandleInbound(response.Raw, testServerNetAddr()))

	select {
	case got := <-resultCh:
		assert.NoError(t, got.err)
		assert.NotNil(t, got.result.Msg)
	case <-time.After(time.Second):
		assert.Fail(t, "transaction begun after Client.Close did not complete")
	}
}

func TestClientCloseWinsBlockedInitialSendWithoutRearm(t *testing.T) {
	conn := newObserverConn()
	conn.blockFrom = 1
	cl := newObservedClient(t, conn)
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)

	resultCh := make(chan error, 1)
	go func() {
		_, err := cl.performTransaction(request, false)
		resultCh <- err
	}()
	select {
	case <-conn.blocked:
	case <-time.After(time.Second):
		require.Fail(t, "initial write did not block")
	}
	cl.Close()
	close(conn.gate)

	select {
	case err := <-resultCh:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(time.Second):
		assert.Fail(t, "Client.Close did not wake the blocked begin caller")
	}
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), conn.writeCount.Load(), "a closed initial send must not arm a timer")
}

// Create an allocation, and then invalidate the server's nonce.
// The subsequent PreparePeer on the allocation will cause a CreatePermission
// and ChannelBind which will be forced to handle a stale nonce response; the
// write that follows travels as ChannelData over the confirmed binding.
func TestClientNonceExpiration(t *testing.T) {
	server, err := turntest.New(turntest.Options{
		Realm:    "pion.ly",
		Username: "foo",
		Password: "pass",
	})
	require.NoError(t, err)

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
	assert.NoError(t, err)

	client, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   server.Addr(),
		Username: "foo",
		Password: "pass",
	})
	assert.NoError(t, err)
	startTestPump(t, client, conn)

	allocation, err := client.Allocate(context.Background())
	assert.NoError(t, err)

	server.InjectStaleNonce()

	peer := netip.MustParseAddrPort("127.0.0.1:8080")
	assert.NoError(t, allocation.PreparePeer(context.Background(), peer))
	_, err = allocation.WriteTo([]byte{0x00}, peer)
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
	t.Run("Infer IPv4 from connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp4", "0.0.0.0:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		// Should infer IPv4 from connection
		family := getRequestedAddressFamily(conn)
		assert.Equal(t, proto.RequestedFamilyIPv4, family)
	})

	t.Run("Infer IPv6 from connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp6", "[::]:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		// Should infer IPv6 from connection
		family := getRequestedAddressFamily(conn)
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

// TestClientE2E is the ChannelData-path preservation gate, carried over from
// the removed upstream fixture and now run against turntest: after
// PreparePeer, every outbound relayed datagram travels as ChannelData over the
// confirmed binding, and inbound relayed datagrams are delivered to ReadFrom.
func TestClientE2E(t *testing.T) {
	server, err := turntest.New(turntest.Options{
		Realm:              "pion.ly",
		Username:           "foo",
		Password:           "pass",
		AllocationLifetime: time.Second,
		PermissionTimeout:  time.Millisecond * 100,
		ChannelBindTimeout: time.Millisecond * 100,
	})
	require.NoError(t, err)

	stunClientConn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
	assert.NoError(t, err)

	client, err := NewClient(&ClientConfig{
		Conn:                      stunClientConn,
		Server:                    server.Addr(),
		Username:                  "foo",
		Password:                  "pass",
		PermissionRefreshInterval: time.Millisecond * 50,
		bindingRefreshInterval:    time.Millisecond * 50,
		bindingCheckInterval:      time.Millisecond * 50,
	})
	assert.NoError(t, err)
	startTestPump(t, client, stunClientConn)

	allocation, err := client.Allocate(context.Background())
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
	require.NoError(t, allocation.PreparePeer(context.Background(), peer))
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
