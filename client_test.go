// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	pionturn "github.com/pion/turn/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/proto"
)

const testAddr = "127.0.0.1:3478"

func buildMsg(
	transactionID [stun.TransactionIDSize]byte,
	msgType stun.MessageType,
	additional ...stun.Setter,
) []stun.Setter {
	return append([]stun.Setter{&stun.Message{TransactionID: transactionID}, msgType}, additional...)
}

func createListeningTestClient(t *testing.T, loggerFactory logging.LoggerFactory) (*Client, net.PacketConn, bool) {
	t.Helper()

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
	assert.NoError(t, err)

	c, err := NewClient(&ClientConfig{
		Conn:          conn,
		Software:      "TEST SOFTWARE",
		LoggerFactory: loggerFactory,
	})
	assert.NoError(t, err)
	assert.NoError(t, c.Listen())

	return c, conn, true
}

func createListeningTestClientWithSTUNServ(t *testing.T, loggerFactory logging.LoggerFactory) ( // nolint:lll
	*Client, net.PacketConn,
	bool,
) {
	t.Helper()

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
	assert.NoError(t, err)

	addr := "stun1.l.google.com:19302"

	c, err := NewClient(&ClientConfig{
		STUNServerAddr: addr,
		Conn:           conn,
		LoggerFactory:  loggerFactory,
	})
	assert.NoError(t, err)
	assert.NoError(t, c.Listen())

	return c, conn, true
}

func TestClientWithSTUN(t *testing.T) {
	loggerFactory := logging.NewDefaultLoggerFactory()
	log := loggerFactory.NewLogger("test")

	t.Run("SendBindingRequest", func(t *testing.T) {
		client, pc, ok := createListeningTestClientWithSTUNServ(t, loggerFactory)
		if !ok {
			return
		}
		defer client.Close()

		resp, err := client.SendBindingRequest()
		assert.NoError(t, err, "should succeed")
		log.Debugf("mapped-addr: %s", resp)
		assert.Equal(t, 0, client.trMap.Size(), "should be no transaction left")
		assert.NoError(t, pc.Close())
	})

	t.Run("SendBindingRequestTo Parallel", func(t *testing.T) {
		client, pc, ok := createListeningTestClient(t, loggerFactory)
		if !ok {
			return
		}
		defer client.Close()

		// Simple channel fo go routine start signaling
		started := make(chan struct{})
		finished := make(chan struct{})
		var err1 error

		to, err := net.ResolveUDPAddr("udp4", "stun1.l.google.com:19302")
		assert.NoError(t, err)

		// stun1.l.google.com:19302, more at https://gist.github.com/zziuni/3741933#file-stuns-L5
		go func() {
			close(started)
			_, err1 = client.SendBindingRequestTo(to)
			close(finished)
		}()

		// Block until go routine is started to make two almost parallel requests
		<-started

		_, err = client.SendBindingRequestTo(to)
		assert.NoError(t, err)

		<-finished
		assert.NoErrorf(t, err1, "should succeed: %v", err)
		assert.NoError(t, pc.Close())
	})

	t.Run("NewClient should fail if Conn is nil", func(t *testing.T) {
		_, err := NewClient(&ClientConfig{
			LoggerFactory: loggerFactory,
		})
		assert.Error(t, err, "should fail")
	})

	t.Run("SendBindingRequestTo timeout", func(t *testing.T) {
		c, pc, ok := createListeningTestClient(t, loggerFactory)
		if !ok {
			return
		}
		defer c.Close()

		to, err := net.ResolveUDPAddr("udp4", "127.0.0.1:9")
		assert.NoError(t, err)

		c.rto = 10 * time.Millisecond // Force short timeout

		_, err = c.SendBindingRequestTo(to)
		assert.NotNil(t, err)
		assert.NoError(t, pc.Close())
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
		Conn:           conn,
		STUNServerAddr: testAddr,
		TURNServerAddr: testAddr,
		Username:       "foo",
		Password:       "pass",
	})
	assert.NoError(t, err)
	assert.NoError(t, client.Listen())

	allocation, err := client.Allocate()
	assert.NoError(t, err)

	_, err = allocation.WriteTo([]byte{0x00}, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080})
	assert.NoError(t, err)

	// Shutdown
	assert.NoError(t, allocation.Close())
	assert.NoError(t, conn.Close())
	assert.NoError(t, server.Close())
}

func TestClientReadTimout(t *testing.T) {
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

	stunClientConn, err := net.ListenPacket("udp4", "0.0.0.0:0") // nolint: noctx
	assert.NoError(t, err)

	client, err := NewClient(&ClientConfig{
		Conn:           stunClientConn,
		STUNServerAddr: testAddr,
		TURNServerAddr: testAddr,
		Username:       "foo",
		Password:       "pass",
	})
	assert.NoError(t, err)
	assert.NoError(t, client.Listen())

	allocation, err := client.Allocate()
	assert.NoError(t, err)

	assert.NoError(t, allocation.SetReadDeadline(time.Now().Add(time.Nanosecond)))
	_, _, err = allocation.ReadFrom(nil)
	assert.Contains(t, err.Error(), "i/o timeout")

	// Shutdown
	assert.NoError(t, allocation.Close())
	assert.NoError(t, stunClientConn.Close())
	assert.NoError(t, server.Close())

	_, _, err = allocation.ReadFrom(nil)
	assert.Contains(t, err.Error(), "use of closed network connection")
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

	t.Run("Explicit IPv4 in config", func(t *testing.T) {
		conn, err := net.ListenPacket("udp6", "[::]:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		config := &ClientConfig{
			Conn:                   conn,
			RequestedAddressFamily: proto.RequestedFamilyIPv4,
		}

		// Should use explicit config even though conn is IPv6
		family := getRequestedAddressFamily(log, config)
		assert.Equal(t, proto.RequestedFamilyIPv4, family)
	})

	t.Run("Explicit IPv6 in config", func(t *testing.T) {
		conn, err := net.ListenPacket("udp4", "0.0.0.0:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		config := &ClientConfig{
			Conn:                   conn,
			RequestedAddressFamily: proto.RequestedFamilyIPv6,
		}

		// Should use explicit config even though conn is IPv4
		family := getRequestedAddressFamily(log, config)
		assert.Equal(t, proto.RequestedFamilyIPv6, family)
	})

	t.Run("Infer IPv4 from connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp4", "0.0.0.0:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		config := &ClientConfig{
			Conn: conn,
			// RequestedAddressFamily not set (zero value)
		}

		// Should infer IPv4 from connection
		family := getRequestedAddressFamily(log, config)
		assert.Equal(t, proto.RequestedFamilyIPv4, family)
	})

	t.Run("Infer IPv6 from connection", func(t *testing.T) {
		conn, err := net.ListenPacket("udp6", "[::]:0") //nolint:noctx
		assert.NoError(t, err)
		defer conn.Close() //nolint:errcheck

		config := &ClientConfig{
			Conn: conn,
			// RequestedAddressFamily not set (zero value)
		}

		// Should infer IPv6 from connection
		family := getRequestedAddressFamily(log, config)
		assert.Equal(t, proto.RequestedFamilyIPv6, family)
	})
}

func TestAppendRequestedAddressFamilyOrReservation(t *testing.T) {
	t.Run("IPv4 omits attribute", func(t *testing.T) {
		setters := appendRequestedAddressFamilyOrReservation(
			[]stun.Setter{stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest)},
			proto.RequestedFamilyIPv4,
			nil,
		)

		msg, err := stun.Build(setters...)
		require.NoError(t, err)

		assert.False(t, msg.Contains(stun.AttrRequestedAddressFamily))
	})

	t.Run("IPv6 includes attribute", func(t *testing.T) {
		setters := appendRequestedAddressFamilyOrReservation(
			[]stun.Setter{stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest)},
			proto.RequestedFamilyIPv6,
			nil,
		)

		msg, err := stun.Build(setters...)
		require.NoError(t, err)

		var raf proto.RequestedAddressFamily
		require.NoError(t, raf.GetFrom(msg))
		assert.Equal(t, proto.RequestedFamilyIPv6, raf)
	})

	t.Run("Reservation token takes precedence", func(t *testing.T) {
		token := proto.ReservationToken{1, 2, 3, 4, 5, 6, 7, 8}
		setters := appendRequestedAddressFamilyOrReservation(
			[]stun.Setter{stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest)},
			proto.RequestedFamilyIPv6,
			token,
		)

		msg, err := stun.Build(setters...)
		require.NoError(t, err)

		var parsedToken proto.ReservationToken
		require.NoError(t, parsedToken.GetFrom(msg))
		assert.Equal(t, token, parsedToken)
		assert.False(t, msg.Contains(stun.AttrRequestedAddressFamily))
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
			STUNServerAddr:            testAddr,
			TURNServerAddr:            testAddr,
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

		allocationAddr, ok := allocation.LocalAddr().(*net.UDPAddr)
		assert.True(t, ok)

		sendPackets := func(src, dst net.PacketConn, port int) {
			const expectedPktCount = 25
			expectedPacket := []byte{0xDE, 0xAD, 0xBE, 0xEF}

			pktCount := atomic.Uint32{}
			go func() {
				buff := make([]byte, len(expectedPacket))
				for pktCount.Load() < expectedPktCount {
					i, _, readErr := dst.ReadFrom(buff)
					assert.NoError(t, readErr)

					assert.Equal(t, expectedPacket, buff[:i])
					pktCount.Add(1)
				}
			}()
			for pktCount.Load() < expectedPktCount {
				_, err = src.WriteTo(expectedPacket, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
				assert.NoError(t, err)

				time.Sleep(time.Millisecond * 25)
			}
		}

		sendPackets(allocation, remotePeerConn, remotePeerAddr.Port)
		sendPackets(remotePeerConn, allocation, allocationAddr.Port)

		assert.NotNil(t, client.TURNServerAddr())
		assert.NotNil(t, client.Username())
		assert.NotNil(t, client.Realm())

		// Shutdown
		assert.NoError(t, remotePeerConn.Close())
		assert.NoError(t, allocation.Close())
		assert.NoError(t, stunClientConn.Close())
		assert.NoError(t, server.Close())
	}

	doTest(true)
	doTest(false)
}
