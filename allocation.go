// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turn

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/the-sarge/turn/v5/internal/client"
)

// Allocation is one live UDP relay allocation, returned by Client.Allocate.
// Peer addresses cross it as canonical netip.AddrPort values; inputs that
// cannot be canonicalized are rejected with ErrInvalidPeer. It delegates to
// the internal allocation, which remains the lifecycle owner.
type Allocation struct {
	conn        *client.UDPConn
	relayedAddr netip.AddrPort
}

func newAllocation(conn *client.UDPConn, relayedAddr netip.AddrPort) *Allocation {
	return &Allocation{conn: conn, relayedAddr: relayedAddr}
}

// RelayedAddr returns the canonical relayed transport address the server
// allocated, validated once at Allocate.
func (a *Allocation) RelayedAddr() netip.AddrPort {
	return a.relayedAddr
}

// PreparePeer creates a permission for peer on the allocation and waits until
// the TURN server confirms a channel binding for it. After it returns nil,
// writes to peer use ChannelData (or fail) for the lifetime of the
// allocation; they never fall back to Send indications. Concurrent calls for
// the same peer share one permission and one bind; canceling ctx wakes only
// that caller and leaves the shared work running.
func (a *Allocation) PreparePeer(ctx context.Context, peer netip.AddrPort) error {
	canonical, ok := canonicalAddrPort(peer, canonicalUnmap)
	if !ok {
		return fmt.Errorf("%w: %s", ErrInvalidPeer, peer)
	}

	return a.conn.PreparePeer(ctx, canonical)
}

// ReadFrom reads one relayed datagram, copying the payload into p. It returns
// the number of bytes copied and the canonical source peer, blocking until a
// datagram arrives or the allocation closes. Datagrams arriving while the
// receive queue is full are dropped, matching UDP semantics. After the
// allocation closes, ReadFrom returns net.ErrClosed, wrapped with the
// terminal cause when the allocation sealed itself (for example
// ErrAllocationRefreshFailed).
func (a *Allocation) ReadFrom(p []byte) (int, netip.AddrPort, error) {
	return a.conn.ReadFrom(p)
}

// WriteTo writes payload to peer via the relay as ChannelData over the
// channel binding PreparePeer confirmed. Writes are prepared-only: a peer for
// which PreparePeer has not succeeded on this allocation returns
// ErrNotPrepared with zero network output; a prepared binding that has since
// expired or failed returns its cause (for example ErrChannelBindingExpired
// or ErrPermissionRefreshFailed) with zero network output. WriteTo never
// creates a permission, starts a binding, or sends a Send indication.
func (a *Allocation) WriteTo(payload []byte, peer netip.AddrPort) (int, error) {
	canonical, ok := canonicalAddrPort(peer, canonicalUnmap)
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrInvalidPeer, peer)
	}

	return a.conn.WriteTo(payload, canonical)
}

// Close releases the allocation on the server and unblocks pending ReadFrom
// and WriteTo calls. It returns only after allocation-owned goroutines have
// finished; it never closes or deadlines the caller-owned base socket.
//
// Close always joins, then returns: the lifetime-0 release error (or nil)
// when this call performed the release — a socket-close cause satisfies
// errors.Is(err, net.ErrClosed); the recorded terminal cause (for example
// ErrAllocationRefreshFailed wrapping the underlying failure) when the
// allocation had already sealed itself; net.ErrClosed on repeated calls.
func (a *Allocation) Close() error {
	return a.conn.Close()
}
