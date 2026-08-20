// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package turntest provides a fork-owned, in-process scripted TURN responder
// for integration tests. It implements exactly the request subset this
// repository's client emits (Allocate, Refresh, CreatePermission, ChannelBind,
// ChannelData relaying, and Data indications for permitted-but-unbound peers)
// and drops everything else. It is a verification aid with an example-level
// guarantee: STUN framing and integrity are owned by pion/stun and TURN
// attributes by internal/proto — the fixture hand-parses nothing — and it
// claims no RFC conformance, relay quality, or interoperability. It must never
// be imported by non-test code.
package turntest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"

	"github.com/the-sarge/turn/v5/internal/proto"
)

const (
	defaultPermissionTimeout  = 5 * time.Minute
	defaultChannelBindTimeout = 10 * time.Minute
	sweepInterval             = 20 * time.Millisecond
	nonceSize                 = 16
	readBufferSize            = 1 << 16
)

// Options configures a Server. Realm, Username, and Password are the long-term
// credentials every request must authenticate with; the zero value of every
// other field selects the default behavior described on it.
type Options struct {
	Realm    string
	Username string
	Password string

	// IPv6 listens and relays on [::1] instead of 127.0.0.1.
	IPv6 bool

	// RelayIPOverride, when non-nil, is advertised as the relayed IP in the
	// Allocate response instead of the bound relay socket's IP. The relay
	// socket itself still binds the loopback address, so an override lets
	// tests advertise an address the client must reject.
	RelayIPOverride net.IP

	// AllocationLifetime is the lifetime granted to allocations and refreshes.
	// Defaults to the RFC 5766 default of 10 minutes.
	AllocationLifetime time.Duration

	// PermissionTimeout is the expiry of installed permissions. Defaults to 5
	// minutes.
	PermissionTimeout time.Duration

	// ChannelBindTimeout is the expiry of confirmed channel bindings.
	// Defaults to 10 minutes.
	ChannelBindTimeout time.Duration

	// DenyPermissions makes every CreatePermission request fail with 403.
	DenyPermissions bool

	// RejectChannelBind makes every ChannelBind request fail with 400.
	RejectChannelBind bool
}

// bindingEntry is one confirmed channel binding: the bound peer and the
// binding's server-side expiry.
type bindingEntry struct {
	peer      netip.AddrPort
	expiresAt time.Time
}

// allocationState is one live allocation, keyed by the client's five-tuple
// (its source address on the server's single listener).
type allocationState struct {
	clientAddr  net.Addr
	relay       net.PacketConn
	expiresAt   time.Time
	permissions map[netip.Addr]time.Time
	bindings    map[uint16]bindingEntry
}

// Server is a scripted in-process TURN responder bound to one loopback UDP
// listener. Close joins every server-owned goroutine.
type Server struct {
	opts      Options
	listener  net.PacketConn
	addr      netip.AddrPort
	integrity stun.MessageIntegrity

	mu     sync.Mutex
	nonce  string
	allocs map[string]*allocationState
	closed bool

	done chan struct{}
	wg   sync.WaitGroup
}

// New starts a Server listening on a loopback UDP socket (127.0.0.1, or [::1]
// with Options.IPv6) and returns it once it is serving.
func New(opts Options) (*Server, error) {
	network, listenAddr := "udp4", "127.0.0.1:0"
	if opts.IPv6 {
		network, listenAddr = "udp6", "[::1]:0"
	}

	listener, err := net.ListenPacket(network, listenAddr) //nolint:noctx // test fixture socket
	if err != nil {
		return nil, fmt.Errorf("turntest: listen: %w", err)
	}

	local, ok := listener.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = listener.Close()

		return nil, errNonUDPListener
	}

	srv := &Server{
		opts:      opts,
		listener:  listener,
		addr:      netip.AddrPortFrom(local.AddrPort().Addr().Unmap(), local.AddrPort().Port()),
		integrity: stun.NewLongTermIntegrity(opts.Username, opts.Realm, opts.Password),
		nonce:     newNonce(),
		allocs:    make(map[string]*allocationState),
		done:      make(chan struct{}),
	}

	srv.wg.Add(2)
	go srv.serve()
	go srv.sweep()

	return srv, nil
}

// Start is a convenience wrapper over New that fails the test on error and
// closes the server in test cleanup.
func Start(tb testing.TB, opts Options) *Server {
	tb.Helper()

	srv, err := New(opts)
	if err != nil {
		tb.Fatalf("turntest: %v", err)
	}
	tb.Cleanup(func() { _ = srv.Close() })

	return srv
}

var errNonUDPListener = errors.New("turntest: listener is not UDP")

// Addr returns the server's transport address, suitable as ClientConfig.Server.
func (s *Server) Addr() netip.AddrPort {
	return s.addr
}

// AllocationCount returns the number of live allocations.
func (s *Server) AllocationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.allocs)
}

// InjectStaleNonce invalidates the server's current nonce: the next
// authenticated request still carrying the previous nonce is answered with
// one 438 (Stale Nonce) response carrying the fresh nonce, and a retry with
// that nonce succeeds.
func (s *Server) InjectStaleNonce() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nonce = newNonce()
}

// Close shuts the server down: it closes the listener and every relay socket,
// then joins every server-owned goroutine. Close is idempotent; repeated
// calls return nil.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return nil
	}
	s.closed = true
	close(s.done)
	closers := []net.PacketConn{s.listener}
	for _, alloc := range s.allocs {
		closers = append(closers, alloc.relay)
	}
	s.allocs = make(map[string]*allocationState)
	s.mu.Unlock()

	var errs []error
	for _, conn := range closers {
		if err := conn.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	s.wg.Wait()

	return errors.Join(errs...)
}

// serve reads the listener and dispatches each datagram: STUN requests to the
// method handlers, ChannelData to the relay path, anything else is dropped.
func (s *Server) serve() {
	defer s.wg.Done()

	buf := make([]byte, readBufferSize)
	for {
		n, from, err := s.listener.ReadFrom(buf)
		if err != nil {
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		switch {
		case stun.IsMessage(pkt):
			s.handleSTUN(pkt, from)
		case proto.IsChannelData(pkt):
			s.handleChannelData(pkt, from)
		default: // Dropped: neither STUN nor ChannelData.
		}
	}
}

// sweep expires allocations, permissions, and channel bindings on a fixed
// fine-grained interval so shortened test timeouts are observed promptly.
func (s *Server) sweep() {
	defer s.wg.Done()

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case now := <-ticker.C:
			for _, relay := range s.expire(now) {
				_ = relay.Close()
			}
		}
	}
}

// expire removes expired state under the lock and returns the relay sockets
// of expired allocations for closing outside it.
func (s *Server) expire(now time.Time) []net.PacketConn {
	s.mu.Lock()
	defer s.mu.Unlock()

	var closers []net.PacketConn
	for key, alloc := range s.allocs {
		if now.After(alloc.expiresAt) {
			delete(s.allocs, key)
			closers = append(closers, alloc.relay)

			continue
		}
		for ip, expiry := range alloc.permissions {
			if now.After(expiry) {
				delete(alloc.permissions, ip)
			}
		}
		for number, bound := range alloc.bindings {
			if now.After(bound.expiresAt) {
				delete(alloc.bindings, number)
			}
		}
	}

	return closers
}

// relayLoop reads one allocation's relay socket and forwards each peer
// datagram toward the client.
func (s *Server) relayLoop(alloc *allocationState) {
	defer s.wg.Done()

	buf := make([]byte, readBufferSize)
	for {
		n, peerAddr, err := alloc.relay.ReadFrom(buf)
		if err != nil {
			return
		}
		s.forwardFromPeer(alloc, buf[:n], peerAddr)
	}
}

// forwardFromPeer delivers one peer datagram to the client: as ChannelData
// when the peer has a live channel binding, as a Data indication when the
// peer is permitted but unbound, and dropped otherwise.
func (s *Server) forwardFromPeer(alloc *allocationState, data []byte, peerAddr net.Addr) {
	peerUDP, ok := peerAddr.(*net.UDPAddr)
	if !ok {
		return
	}
	peer := netip.AddrPortFrom(peerUDP.AddrPort().Addr().Unmap(), peerUDP.AddrPort().Port())

	now := time.Now()
	s.mu.Lock()
	number, bound := uint16(0), false
	for num, entry := range alloc.bindings {
		if entry.peer == peer && now.Before(entry.expiresAt) {
			number, bound = num, true

			break
		}
	}
	expiry, havePerm := alloc.permissions[peer.Addr()]
	permitted := havePerm && now.Before(expiry)
	clientAddr := alloc.clientAddr
	s.mu.Unlock()

	switch {
	case bound:
		chData := &proto.ChannelData{Data: data, Number: proto.ChannelNumber(number)}
		chData.Encode()
		_, _ = s.listener.WriteTo(chData.Raw, clientAddr)
	case permitted:
		msg, err := stun.Build(
			stun.TransactionID,
			stun.NewType(stun.MethodData, stun.ClassIndication),
			proto.PeerAddress{IP: peerUDP.IP, Port: peerUDP.Port},
			proto.Data(data),
		)
		if err != nil {
			return
		}
		_, _ = s.listener.WriteTo(msg.Raw, clientAddr)
	default: // Dropped: no binding and no permission.
	}
}

// allocationLifetime returns the configured allocation lifetime or the RFC
// 5766 default.
func (s *Server) allocationLifetime() time.Duration {
	if s.opts.AllocationLifetime != 0 {
		return s.opts.AllocationLifetime
	}

	return proto.DefaultLifetime
}

// permissionTimeout returns the configured permission expiry or its default.
func (s *Server) permissionTimeout() time.Duration {
	if s.opts.PermissionTimeout != 0 {
		return s.opts.PermissionTimeout
	}

	return defaultPermissionTimeout
}

// channelBindTimeout returns the configured binding expiry or its default.
func (s *Server) channelBindTimeout() time.Duration {
	if s.opts.ChannelBindTimeout != 0 {
		return s.opts.ChannelBindTimeout
	}

	return defaultChannelBindTimeout
}

// newNonce returns a fresh random nonce value.
func newNonce() string {
	buf := make([]byte, nonceSize)
	_, _ = rand.Read(buf)

	return hex.EncodeToString(buf)
}
