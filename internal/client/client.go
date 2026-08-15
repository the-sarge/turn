// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package client implements the API for a TURN client
package client

import (
	"net"

	"github.com/pion/stun/v3"
)

// clientHooks are the three operations an allocation needs from the owning
// turn.Client: writing to the base socket, running a STUN transaction, and
// reporting deallocation. AllocationConfig carries them as exported func
// fields so the root package can supply closures over unexported methods
// without exporting a public interface.
type clientHooks struct {
	writeTo            func(data []byte, to net.Addr) (int, error)
	performTransaction func(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error)
	onDeallocated      func(relayedAddr net.Addr)
}
