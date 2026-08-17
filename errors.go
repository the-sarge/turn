// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turn

import (
	"errors"
	"net"

	"github.com/the-sarge/turn/v5/internal/client"
)

// ErrClosed is net.ErrClosed: errors.Is(err, ErrClosed) on any error from
// this package means the client or allocation is closed, or a wrapped cause
// was itself a socket close — never a synthetic stand-in for another failure.
var ErrClosed = net.ErrClosed

// ErrTransactionTimeout reports a STUN transaction whose retransmissions
// were all exhausted without a server response.
var ErrTransactionTimeout = client.ErrTransactionTimeout

// ErrAllocationRefreshFailed reports a permanent allocation-refresh failure:
// an exhausted refresh transaction, a well-formed non-438 error response, or
// stale-nonce retry exhaustion. The allocation seals itself with this cause;
// pending waiters wake, every subsequent operation returns ErrClosed wrapped
// with it, and the caller's Close returns it.
var ErrAllocationRefreshFailed = client.ErrAllocationRefreshFailed

// ErrPermissionRefreshFailed reports a permission refresh that kept failing
// after retries. Prepared peers terminalize with it: their writes fail
// rather than ever falling back to Send indications.
var ErrPermissionRefreshFailed = client.ErrPermissionRefreshFailed

// ErrChannelBindFailed reports a channel binding that could not be
// established or has failed for a prepared peer.
var ErrChannelBindFailed = client.ErrChannelBindFailed

// ErrChannelBindingExpired reports a confirmed channel binding whose
// server-side lifetime expired before the write.
var ErrChannelBindingExpired = client.ErrChannelBindingExpired

// ErrNotPrepared reports a WriteTo to a peer for which PreparePeer has not
// succeeded on this allocation. Nothing is sent: writes are ChannelData over
// a prepared binding or fail with zero network output.
var ErrNotPrepared = client.ErrNotPrepared

// ErrAlreadyAllocated is returned by Allocate when the client already owns a
// live allocation. Close that allocation before allocating again.
var ErrAlreadyAllocated = errors.New("turn: already allocated")

// ErrInvalidPeer reports a peer address outside the canonical netip.AddrPort
// domain: it must be unicast, unmapped or mappable by unmapping, zone-free,
// and carry a nonzero port.
var ErrInvalidPeer = errors.New("turn: invalid peer address")

// ErrInvalidRelayedAddress reports that the server's Allocate success carried
// a relayed transport address that cannot be canonicalized. The allocation is
// released with a lifetime-0 Refresh before Allocate returns this error.
var ErrInvalidRelayedAddress = errors.New("turn: server reported an invalid relayed address")

var (
	errNilConn       = errors.New("turn: conn cannot not be nil")
	errInvalidServer = errors.New(
		"turn: Server must be a canonical netip.AddrPort (unicast, unmapped, zone-free, nonzero port)")
	errNilContext                    = errors.New("turn: context must not be nil")
	errFailedToRetransmitTransaction = errors.New("turn: failed to retransmit transaction")
	errChannelBindNotFound           = errors.New("no binding found for channel")
	errOneAllocateOnly               = errors.New("only one Allocate() caller is allowed")
	errUnexpectedServerDatagram      = errors.New("turn: datagram from server is neither STUN nor ChannelData")
	errFailedToDecodeSTUN            = errors.New("failed to decode STUN message")
	errUnexpectedSTUNRequestMessage  = errors.New("unexpected STUN request message")
)
