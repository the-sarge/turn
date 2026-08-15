// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net"

	"github.com/pion/stun/v3"
)

type mockClient struct {
	writeTo            func(data []byte, to net.Addr) (int, error)
	performTransaction func(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error)
	onDeallocated      func(relayedAddr net.Addr)
}

func (c *mockClient) WriteTo(data []byte, to net.Addr) (int, error) {
	if c.writeTo != nil {
		return c.writeTo(data, to)
	}

	return 0, nil
}

func (c *mockClient) PerformTransaction(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error) {
	if c.performTransaction != nil {
		return c.performTransaction(msg, to, dontWait)
	}

	return TransactionResult{}, errFake
}

func (c *mockClient) OnDeallocated(relayedAddr net.Addr) {
	if c.onDeallocated != nil {
		c.onDeallocated(relayedAddr)
	}
}

// hooks returns the mock's operations as the allocation's client hooks. The
// method values dispatch through the mock's fields at call time, so a test
// may rescript the mock after the allocation is built.
func (c *mockClient) hooks() clientHooks {
	return clientHooks{
		writeTo:            c.WriteTo,
		performTransaction: c.PerformTransaction,
		onDeallocated:      c.OnDeallocated,
	}
}
