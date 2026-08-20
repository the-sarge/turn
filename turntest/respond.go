// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turntest

import (
	"net"
	"net/netip"
	"time"

	"github.com/pion/stun/v3"

	"github.com/the-sarge/turn/v5/internal/proto"
)

// handleSTUN decodes one STUN datagram and dispatches the request methods the
// client emits; every other class or method is dropped.
func (s *Server) handleSTUN(pkt []byte, from net.Addr) {
	msg := &stun.Message{Raw: pkt}
	if err := msg.Decode(); err != nil {
		return
	}
	if msg.Type.Class != stun.ClassRequest {
		return
	}

	switch msg.Type.Method {
	case stun.MethodAllocate:
		s.handleAllocate(msg, from)
	case stun.MethodRefresh:
		s.handleRefresh(msg, from)
	case stun.MethodCreatePermission:
		s.handleCreatePermission(msg, from)
	case stun.MethodChannelBind:
		s.handleChannelBind(msg, from)
	default: // Dropped: not part of the client's request subset.
	}
}

// send builds a response bound to the request's transaction ID and writes it
// to the client.
func (s *Server) send(from net.Addr, msg *stun.Message, msgType stun.MessageType, attrs ...stun.Setter) {
	setters := append([]stun.Setter{stun.NewTransactionIDSetter(msg.TransactionID), msgType}, attrs...)
	res, err := stun.Build(setters...)
	if err != nil {
		return
	}
	_, _ = s.listener.WriteTo(res.Raw, from)
}

// sendError sends an error response carrying code plus the server's current
// NONCE and REALM, so 401 challenges and 438 stale-nonce responses always
// deliver the credentials the client needs to retry.
func (s *Server) sendError(from net.Addr, msg *stun.Message, code stun.ErrorCode) {
	s.mu.Lock()
	nonce := s.nonce
	s.mu.Unlock()

	s.send(from, msg, errorType(msg),
		stun.ErrorCodeAttribute{Code: code},
		stun.NewNonce(nonce),
		stun.NewRealm(s.opts.Realm),
	)
}

// successType is the success-response type for a request message.
func successType(msg *stun.Message) stun.MessageType {
	return stun.NewType(msg.Type.Method, stun.ClassSuccessResponse)
}

// errorType is the error-response type for a request message.
func errorType(msg *stun.Message) stun.MessageType {
	return stun.NewType(msg.Type.Method, stun.ClassErrorResponse)
}

// authenticate verifies one request's long-term-credential authentication.
// It returns true when the request is authenticated; otherwise it has already
// sent the 401 challenge or the 438 stale-nonce response.
func (s *Server) authenticate(msg *stun.Message, from net.Addr) bool {
	if !msg.Contains(stun.AttrMessageIntegrity) {
		s.sendError(from, msg, stun.CodeUnauthorized)

		return false
	}

	var (
		username stun.Username
		realm    stun.Realm
		nonce    stun.Nonce
	)
	if username.GetFrom(msg) != nil || realm.GetFrom(msg) != nil || nonce.GetFrom(msg) != nil {
		s.sendError(from, msg, stun.CodeBadRequest)

		return false
	}
	if username.String() != s.opts.Username || realm.String() != s.opts.Realm || s.integrity.Check(msg) != nil {
		s.sendError(from, msg, stun.CodeUnauthorized)

		return false
	}

	s.mu.Lock()
	current := s.nonce
	s.mu.Unlock()
	if nonce.String() != current {
		s.sendError(from, msg, stun.CodeStaleNonce)

		return false
	}

	return true
}

// handleAllocate answers an authenticated UDP Allocate with a per-allocation
// relay socket, or 437 for a second Allocate on the same five-tuple.
func (s *Server) handleAllocate(msg *stun.Message, from net.Addr) {
	if !s.authenticate(msg, from) {
		return
	}

	var transport proto.RequestedTransport
	if transport.GetFrom(msg) != nil || transport.Protocol != proto.ProtoUDP {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeUnsupportedTransProto})

		return
	}

	fromUDP, ok := from.(*net.UDPAddr)
	if !ok {
		return
	}
	if s.allocationExists(from.String()) {
		s.sendError(from, msg, stun.CodeAllocMismatch)

		return
	}

	relay, relayUDP, ok := s.openRelay()
	if !ok {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeServerError})

		return
	}

	alloc := &allocationState{
		clientAddr:  from,
		relay:       relay,
		expiresAt:   time.Now().Add(s.allocationLifetime()),
		permissions: make(map[netip.Addr]time.Time),
		bindings:    make(map[uint16]bindingEntry),
	}
	if !s.storeAllocation(from.String(), alloc) {
		_ = relay.Close()

		return
	}
	go s.relayLoop(alloc)

	advertisedIP := relayUDP.IP
	if s.opts.RelayIPOverride != nil {
		advertisedIP = s.opts.RelayIPOverride
	}
	s.send(from, msg, successType(msg),
		proto.RelayedAddress{IP: advertisedIP, Port: relayUDP.Port},
		proto.Lifetime{Duration: s.allocationLifetime()},
		stun.XORMappedAddress{IP: fromUDP.IP, Port: fromUDP.Port},
	)
}

// openRelay binds one per-allocation relay socket on the fixture's loopback
// address. It reports false when the socket cannot be bound.
func (s *Server) openRelay() (net.PacketConn, *net.UDPAddr, bool) {
	network, listenAddr := "udp4", "127.0.0.1:0"
	if s.opts.IPv6 {
		network, listenAddr = "udp6", "[::1]:0"
	}
	relay, err := net.ListenPacket(network, listenAddr) //nolint:noctx // test fixture socket
	if err != nil {
		return nil, nil, false
	}
	relayUDP, ok := relay.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = relay.Close()

		return nil, nil, false
	}

	return relay, relayUDP, true
}

// allocationExists reports whether the five-tuple already has an allocation.
func (s *Server) allocationExists(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.allocs[key]

	return ok
}

// storeAllocation registers alloc and reserves its relay reader in the close
// join. It reports false when the server is closing.
func (s *Server) storeAllocation(key string, alloc *allocationState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}
	s.allocs[key] = alloc
	s.wg.Add(1)

	return true
}

// handleRefresh updates the allocation's lifetime; a zero requested lifetime
// deletes the allocation.
func (s *Server) handleRefresh(msg *stun.Message, from net.Addr) {
	if !s.authenticate(msg, from) {
		return
	}

	var lifetime proto.Lifetime
	if lifetime.GetFrom(msg) != nil {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeBadRequest})

		return
	}

	if lifetime.Duration == 0 {
		s.deleteAllocation(from.String())
		s.send(from, msg, successType(msg), proto.Lifetime{})

		return
	}

	granted := s.allocationLifetime()
	if !s.extendAllocation(from.String(), granted) {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeAllocMismatch})

		return
	}
	s.send(from, msg, successType(msg), proto.Lifetime{Duration: granted})
}

// deleteAllocation removes the five-tuple's allocation, closing its relay
// socket. Deleting an absent allocation is a no-op.
func (s *Server) deleteAllocation(key string) {
	s.mu.Lock()
	alloc, ok := s.allocs[key]
	delete(s.allocs, key)
	s.mu.Unlock()

	if ok {
		_ = alloc.relay.Close()
	}
}

// extendAllocation renews the five-tuple's allocation expiry. It reports
// false when no allocation exists.
func (s *Server) extendAllocation(key string, lifetime time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	alloc, ok := s.allocs[key]
	if !ok {
		return false
	}
	alloc.expiresAt = time.Now().Add(lifetime)

	return true
}

// handleCreatePermission installs a permission for every XOR-PEER-ADDRESS in
// the request, or fails with 403 when Options.DenyPermissions is set.
func (s *Server) handleCreatePermission(msg *stun.Message, from net.Addr) {
	if !s.authenticate(msg, from) {
		return
	}
	if s.opts.DenyPermissions {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeForbidden})

		return
	}

	peers := xorPeerAddresses(msg)
	if len(peers) == 0 {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeBadRequest})

		return
	}
	if !s.permit(from.String(), peers) {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeAllocMismatch})

		return
	}
	s.send(from, msg, successType(msg))
}

// xorPeerAddresses decodes every XOR-PEER-ADDRESS attribute in the request;
// a CreatePermission refresh carries one per permitted peer.
func xorPeerAddresses(msg *stun.Message) []netip.Addr {
	var peers []netip.Addr
	for _, attr := range msg.Attributes {
		if attr.Type != stun.AttrXORPeerAddress {
			continue
		}
		scratch := &stun.Message{TransactionID: msg.TransactionID}
		scratch.Add(stun.AttrXORPeerAddress, attr.Value)

		var peer proto.PeerAddress
		if peer.GetFrom(scratch) != nil {
			continue
		}
		if addr, ok := netip.AddrFromSlice(peer.IP); ok {
			peers = append(peers, addr.Unmap())
		}
	}

	return peers
}

// permit installs every peer IP's permission on the five-tuple's allocation.
// It reports false when no allocation exists.
func (s *Server) permit(key string, peers []netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	alloc, ok := s.allocs[key]
	if !ok {
		return false
	}
	expiry := time.Now().Add(s.permissionTimeout())
	for _, peer := range peers {
		alloc.permissions[peer] = expiry
	}

	return true
}

// handleChannelBind confirms a channel binding (installing the peer's
// permission alongside it), or fails with 400 when Options.RejectChannelBind
// is set.
func (s *Server) handleChannelBind(msg *stun.Message, from net.Addr) {
	if !s.authenticate(msg, from) {
		return
	}
	if s.opts.RejectChannelBind {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeBadRequest})

		return
	}

	var (
		number proto.ChannelNumber
		peer   proto.PeerAddress
	)
	if number.GetFrom(msg) != nil || peer.GetFrom(msg) != nil || !number.Valid() {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeBadRequest})

		return
	}
	peerIP, ok := netip.AddrFromSlice(peer.IP)
	if !ok {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeBadRequest})

		return
	}
	peerAddr := netip.AddrPortFrom(peerIP.Unmap(), uint16(peer.Port)) //nolint:gosec // attribute port fits uint16

	if !s.bindChannel(from.String(), uint16(number), peerAddr) {
		s.send(from, msg, errorType(msg), stun.ErrorCodeAttribute{Code: stun.CodeAllocMismatch})

		return
	}
	s.send(from, msg, successType(msg))
}

// bindChannel records the channel binding and refreshes the peer's
// permission. It reports false when no allocation exists.
func (s *Server) bindChannel(key string, number uint16, peer netip.AddrPort) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	alloc, ok := s.allocs[key]
	if !ok {
		return false
	}
	now := time.Now()
	alloc.bindings[number] = bindingEntry{peer: peer, expiresAt: now.Add(s.channelBindTimeout())}
	alloc.permissions[peer.Addr()] = now.Add(s.permissionTimeout())

	return true
}

// handleChannelData forwards one client ChannelData datagram to the bound
// peer; data on an unbound or expired channel is dropped.
func (s *Server) handleChannelData(pkt []byte, from net.Addr) {
	chData := &proto.ChannelData{Raw: pkt}
	if err := chData.Decode(); err != nil {
		return
	}

	relay, peer, ok := s.channelPeer(from.String(), uint16(chData.Number))
	if !ok {
		return
	}
	_, _ = relay.WriteTo(chData.Data, net.UDPAddrFromAddrPort(peer))
}

// channelPeer resolves a live channel binding to its relay socket and peer.
func (s *Server) channelPeer(key string, number uint16) (net.PacketConn, netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	alloc, ok := s.allocs[key]
	if !ok {
		return nil, netip.AddrPort{}, false
	}
	bound, ok := alloc.bindings[number]
	if !ok || time.Now().After(bound.expiresAt) {
		return nil, netip.AddrPort{}, false
	}

	return alloc.relay, bound.peer, true
}
