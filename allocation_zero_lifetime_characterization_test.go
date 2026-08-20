// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/proto"
)

func allocateSuccessResponseWithLifetime(t *testing.T, req []byte, lifetime time.Duration) []byte {
	t.Helper()

	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse),
		proto.RelayedAddress{IP: net.ParseIP("127.0.0.1"), Port: 40000},
		proto.Lifetime{Duration: lifetime},
	)
	require.NoError(t, err)

	return msg.Raw
}

func refreshSuccessResponseWithLifetime(t *testing.T, req []byte, lifetime time.Duration) []byte {
	t.Helper()

	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodRefresh, stun.ClassSuccessResponse),
		proto.Lifetime{Duration: lifetime},
	)
	require.NoError(t, err)

	return msg.Raw
}

func requireRefreshLifetime(t *testing.T, raw []byte, want time.Duration) [stun.TransactionIDSize]byte {
	t.Helper()

	msg := &stun.Message{Raw: append([]byte(nil), raw...)}
	require.NoError(t, msg.Decode())
	require.Equal(t, stun.MethodRefresh, msg.Type.Method)

	var lifetime proto.Lifetime
	require.NoError(t, lifetime.GetFrom(msg))
	require.Equal(t, want, lifetime.Duration)

	return msg.TransactionID
}

func awaitNewRefresh(
	t *testing.T,
	conn *observerConn,
	excluded map[[stun.TransactionIDSize]byte]struct{},
) []byte {
	t.Helper()

	var raw []byte
	require.Eventually(t, func() bool {
		for i := int32(0); i < conn.writeCount.Load(); i++ {
			candidate := conn.write(int(i))
			if candidate == nil {
				continue
			}

			msg := &stun.Message{Raw: candidate}
			if msg.Decode() != nil || msg.Type.Method != stun.MethodRefresh {
				continue
			}
			if _, skip := excluded[msg.TransactionID]; skip {
				continue
			}

			raw = candidate

			return true
		}

		return false
	}, 5*time.Second, time.Millisecond, "new Refresh request never left the socket")

	return raw
}

func distinctMethodTransactions(
	t *testing.T,
	conn *observerConn,
	method stun.Method,
) map[[stun.TransactionIDSize]byte]struct{} {
	t.Helper()

	transactions := make(map[[stun.TransactionIDSize]byte]struct{})
	for i := int32(0); i < conn.writeCount.Load(); i++ {
		raw := conn.write(int(i))
		if raw == nil {
			continue
		}

		msg := &stun.Message{Raw: raw}
		require.NoError(t, msg.Decode())
		if msg.Type.Method == method {
			transactions[msg.TransactionID] = struct{}{}
		}
	}

	return transactions
}

func requireLaterAllocateReachesWire(t *testing.T, cl *Client, conn *observerConn) {
	t.Helper()

	retryCtx, cancelRetry := context.WithCancelCause(context.Background())
	retryResult := startObservedAllocate(cl, retryCtx)
	require.Eventually(t, func() bool {
		return len(distinctMethodTransactions(t, conn, stun.MethodAllocate)) >= 3
	}, 5*time.Second, time.Millisecond, "later Allocate did not reach the wire")
	cancelRetry(context.Canceled)
	select {
	case result := <-retryResult:
		require.ErrorIs(t, result.err, context.Canceled)
		require.Nil(t, result.alloc)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "canceled later Allocate did not return")
	}
}

type refreshGateConn struct {
	*observerConn

	refreshEntered  chan struct{}
	continueRefresh chan struct{}
	enterOnce       sync.Once
	continueOnce    sync.Once
}

func newRefreshGateConn() *refreshGateConn {
	return &refreshGateConn{
		observerConn:    newObserverConn(),
		refreshEntered:  make(chan struct{}),
		continueRefresh: make(chan struct{}),
	}
}

func (c *refreshGateConn) WriteTo(data []byte, to net.Addr) (int, error) {
	n, err := c.observerConn.WriteTo(data, to)
	if err != nil {
		return n, err
	}

	msg := &stun.Message{Raw: append([]byte(nil), data...)}
	if msg.Decode() == nil && msg.Type.Method == stun.MethodRefresh {
		var lifetime proto.Lifetime
		if lifetime.GetFrom(msg) == nil && lifetime.Duration > 0 {
			c.enterOnce.Do(func() {
				close(c.refreshEntered)
				<-c.continueRefresh
			})
		}
	}

	return n, nil
}

func (c *refreshGateConn) releaseRefresh() {
	c.continueOnce.Do(func() {
		close(c.continueRefresh)
	})
}

func refreshErrorResponse(t *testing.T, req []byte, code stun.ErrorCode) []byte {
	t.Helper()

	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodRefresh, stun.ClassErrorResponse),
		stun.ErrorCodeAttribute{Code: code, Reason: []byte("error")},
	)
	require.NoError(t, err)

	return msg.Raw
}

func TestZeroLifetimeAllocateNeverPublishes(t *testing.T) {
	conn := newObserverConn()
	cl, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   testServerAddrPort(),
		Username: "user",
		Password: "secret",
		RTO:      time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)

	resultCh := startObservedAllocate(cl, context.Background())

	first := awaitWrite(t, conn, 1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, first), testServerNetAddr()))
	authenticated := awaitRequestAfter(t, conn, 1, transactionID(t, first))
	require.NoError(t, cl.HandleInbound(
		allocateSuccessResponseWithLifetime(t, authenticated, 0),
		testServerNetAddr(),
	))

	select {
	case result := <-resultCh:
		require.ErrorIs(t, result.err, ErrAllocationRefreshFailed)
		require.Nil(t, result.alloc)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Allocate did not return after the zero-lifetime success response")
	}
	require.Nil(t, cl.relayedUDPConn())

	release := awaitNewRefresh(t, conn, nil)
	requireRefreshLifetime(t, release, 0)
	require.Len(t, distinctMethodTransactions(t, conn, stun.MethodRefresh), 1,
		"initial zero must create exactly one lifecycle Release transaction")

	requireLaterAllocateReachesWire(t, cl, conn)

	cl.Close()
	assert.Zero(t, conn.closeCalls.Load())
	assert.Zero(t, conn.deadlineCalls.Load())
	require.Len(t, distinctMethodTransactions(t, conn, stun.MethodRefresh), 1,
		"terminal initial zero must not create a later distinct Refresh transaction")
}

func TestZeroLifetimeRefreshSuccessTerminalizesPublishedAllocation(t *testing.T) {
	conn := newObserverConn()
	cl, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   testServerAddrPort(),
		Username: "user",
		Password: "secret",
		RTO:      time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)

	resultCh := startObservedAllocate(cl, context.Background())
	first := awaitWrite(t, conn, 1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, first), testServerNetAddr()))
	authenticated := awaitRequestAfter(t, conn, 1, transactionID(t, first))
	require.NoError(t, cl.HandleInbound(
		allocateSuccessResponseWithLifetime(t, authenticated, time.Second),
		testServerNetAddr(),
	))

	var allocation *Allocation
	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.alloc)
		allocation = result.alloc
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Allocate did not return after the positive-lifetime success response")
	}
	require.Same(t, allocation.conn, cl.relayedUDPConn())

	firstRefresh := awaitNewRefresh(t, conn, nil)
	require.Same(t, allocation.conn, cl.relayedUDPConn(),
		"the first Refresh must observe the published Allocation")
	firstRefreshID := requireRefreshLifetime(t, firstRefresh, time.Second)
	require.NoError(t, cl.HandleInbound(
		refreshSuccessResponseWithLifetime(t, firstRefresh, 0),
		testServerNetAddr(),
	))

	require.Eventually(t, func() bool {
		return cl.relayedUDPConn() == nil
	}, 2*time.Second, time.Millisecond, "zero-lifetime Refresh success did not clear publication")

	release := awaitNewRefresh(t, conn, map[[stun.TransactionIDSize]byte]struct{}{
		firstRefreshID: {},
	})
	requireRefreshLifetime(t, release, 0)

	_, err = allocation.WriteTo([]byte("data"), netip.MustParseAddrPort("127.0.0.1:5000"))
	assert.ErrorIs(t, err, net.ErrClosed)
	assert.ErrorIs(t, err, ErrAllocationRefreshFailed)
	assert.ErrorIs(t, allocation.Close(), ErrAllocationRefreshFailed)

	requireLaterAllocateReachesWire(t, cl, conn)

	cl.Close()
	assert.Zero(t, conn.closeCalls.Load())
	assert.Zero(t, conn.deadlineCalls.Load())
	require.Len(t, distinctMethodTransactions(t, conn, stun.MethodRefresh), 2,
		"ordinary zero success must create one waited Refresh and one lifecycle Release")
}

func TestFirstRefreshFailureObservesPublishedAllocation(t *testing.T) {
	conn := newRefreshGateConn()
	t.Cleanup(conn.releaseRefresh)
	cl, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   testServerAddrPort(),
		Username: "user",
		Password: "secret",
		RTO:      time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)

	resultCh := startObservedAllocate(cl, context.Background())
	first := awaitWrite(t, conn.observerConn, 1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, first), testServerNetAddr()))
	authenticated := awaitRequestAfter(t, conn.observerConn, 1, transactionID(t, first))
	require.NoError(t, cl.HandleInbound(
		allocateSuccessResponseWithLifetime(t, authenticated, time.Second),
		testServerNetAddr(),
	))

	var allocation *Allocation
	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.alloc)
		allocation = result.alloc
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Allocate did not return after the positive-lifetime success response")
	}

	select {
	case <-conn.refreshEntered:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "first Refresh did not reach the caller-owned socket")
	}
	firstRefresh := awaitNewRefresh(t, conn.observerConn, nil)
	require.Same(t, allocation.conn, cl.relayedUDPConn(),
		"the gated first Refresh must observe the published Allocation")
	require.NoError(t, cl.HandleInbound(
		refreshErrorResponse(t, firstRefresh, stun.CodeServerError),
		testServerNetAddr(),
	))
	conn.releaseRefresh()

	require.Eventually(t, func() bool {
		return cl.relayedUDPConn() == nil
	}, 2*time.Second, time.Millisecond, "permanent first Refresh failure did not clear publication")
	release := awaitNewRefresh(t, conn.observerConn, map[[stun.TransactionIDSize]byte]struct{}{
		transactionID(t, firstRefresh): {},
	})
	requireRefreshLifetime(t, release, 0)
	assert.ErrorIs(t, allocation.Close(), ErrAllocationRefreshFailed)
	requireLaterAllocateReachesWire(t, cl, conn.observerConn)

	cl.Close()
	assert.Zero(t, conn.closeCalls.Load())
	assert.Zero(t, conn.deadlineCalls.Load())
	require.Len(t, distinctMethodTransactions(t, conn.observerConn, stun.MethodRefresh), 2,
		"permanent first Refresh failure must create one waited Refresh and one lifecycle Release")
}
