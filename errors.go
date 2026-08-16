// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turn

import "errors"

// ErrAlreadyAllocated is returned by Allocate when the client already owns a
// live allocation. Close that allocation before allocating again.
var ErrAlreadyAllocated = errors.New("turn: already allocated")

var (
	errNilConn       = errors.New("turn: conn cannot not be nil")
	errInvalidServer = errors.New(
		"turn: Server must be a canonical netip.AddrPort (unicast, unmapped, zone-free, nonzero port)")
	errAlreadyListening              = errors.New("turn: already listening")
	errFailedToRetransmitTransaction = errors.New("turn: failed to retransmit transaction")
	errAllRetransmissionsFailed      = errors.New("all retransmissions failed for")
	errChannelBindNotFound           = errors.New("no binding found for channel")
	errOneAllocateOnly               = errors.New("only one Allocate() caller is allowed")
	errUDPAllocationNotFound         = errors.New("UDP allocation not found")
	errUnexpectedServerDatagram      = errors.New("turn: datagram from server is neither STUN nor ChannelData")
	errFailedToDecodeSTUN            = errors.New("failed to decode STUN message")
	errUnexpectedSTUNRequestMessage  = errors.New("unexpected STUN request message")
)
