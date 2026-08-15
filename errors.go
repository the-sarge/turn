// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turn

import "errors"

var (
	errNilConn                       = errors.New("turn: conn cannot not be nil")
	errAlreadyListening              = errors.New("turn: already listening")
	errFailedToRetransmitTransaction = errors.New("turn: failed to retransmit transaction")
	errAllRetransmissionsFailed      = errors.New("all retransmissions failed for")
	errChannelBindNotFound           = errors.New("no binding found for channel")
	errSTUNServerAddressNotSet       = errors.New("STUN server address is not set for the client")
	errOneAllocateOnly               = errors.New("only one Allocate() caller is allowed")
	errAlreadyAllocated              = errors.New("already allocated")
	errUDPAllocationNotFound         = errors.New("UDP allocation not found")
	errNonSTUNMessage                = errors.New("non-STUN message from STUN server")
	errFailedToDecodeSTUN            = errors.New("failed to decode STUN message")
	errUnexpectedSTUNRequestMessage  = errors.New("unexpected STUN request message")
)
