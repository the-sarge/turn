// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/proto"
)

// observerConn is a caller-owned socket standing in for the silent-server
// pattern: writes reach a server that never responds on its own, driving the
// real transaction/retransmission machinery deterministically. It records
// outbound datagrams and counts the deadline and close calls that the fork
// must never make on a caller-owned socket. An optional gate blocks the Nth
// and later writes, modeling a retransmit write stuck in caller-socket I/O.
type observerConn struct {
	deadlineCalls atomic.Int32
	closeCalls    atomic.Int32
	writeCount    atomic.Int32
	blockFrom     int32         // 1-based write ordinal at which writes block; 0 = never
	gate          chan struct{} // Closed to release blocked writes
	blocked       chan struct{} // Signaled once a write is blocked on the gate

	mu           sync.Mutex
	writes       [][]byte
	destinations []string
}

func newObserverConn() *observerConn {
	return &observerConn{
		gate:    make(chan struct{}),
		blocked: make(chan struct{}, 8),
	}
}

func (o *observerConn) WriteTo(p []byte, to net.Addr) (int, error) {
	n := o.writeCount.Add(1)
	if o.blockFrom > 0 && n >= o.blockFrom {
		o.blocked <- struct{}{}
		<-o.gate
	}
	o.mu.Lock()
	o.writes = append(o.writes, append([]byte(nil), p...))
	o.destinations = append(o.destinations, to.String())
	o.mu.Unlock()

	return len(p), nil
}

func (o *observerConn) destination(i int) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.destinations) {
		return ""
	}

	return o.destinations[i]
}

func (o *observerConn) write(i int) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.writes) {
		return nil
	}

	return append([]byte(nil), o.writes[i]...)
}

func (o *observerConn) ReadFrom([]byte) (int, net.Addr, error) {
	select {} // The tests feed inbound datagrams through HandleInbound directly.
}

func (o *observerConn) Close() error {
	o.closeCalls.Add(1)

	return nil
}

func (o *observerConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555}
}

func (o *observerConn) SetDeadline(time.Time) error {
	o.deadlineCalls.Add(1)

	return nil
}

func (o *observerConn) SetReadDeadline(time.Time) error {
	o.deadlineCalls.Add(1)

	return nil
}

func (o *observerConn) SetWriteDeadline(time.Time) error {
	o.deadlineCalls.Add(1)

	return nil
}

func testServerAddrPort() netip.AddrPort {
	return netip.MustParseAddrPort("127.0.0.1:3478")
}

func newObservedClient(t *testing.T, conn *observerConn) *Client {
	t.Helper()

	cl, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   testServerAddrPort(),
		Username: "user",
		Password: "secret",
		RTO:      25 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)

	return cl
}

func testServerNetAddr() net.Addr {
	return net.UDPAddrFromAddrPort(testServerAddrPort())
}

// transactionID extracts the transaction ID from a recorded request datagram.
func transactionID(t *testing.T, raw []byte) [stun.TransactionIDSize]byte {
	t.Helper()

	msg := &stun.Message{Raw: append([]byte(nil), raw...)}
	require.NoError(t, msg.Decode())

	return msg.TransactionID
}

// unauthorizedResponse builds the 401 challenge for the recorded
// unauthenticated Allocate request, carrying the nonce and realm the client
// needs for its authenticated attempt.
func unauthorizedResponse(t *testing.T, req []byte) []byte {
	t.Helper()

	nonce := stun.NewNonce("test-nonce")
	realm := stun.NewRealm("test-realm")
	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse),
		stun.ErrorCodeAttribute{Code: stun.CodeUnauthorized, Reason: []byte("Unauthorized")},
		&nonce,
		&realm,
	)
	require.NoError(t, err)

	return msg.Raw
}

// allocateSuccessResponse builds the success for the recorded authenticated
// Allocate request, reporting a canonical relayed address.
func allocateSuccessResponse(t *testing.T, req []byte) []byte {
	t.Helper()

	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse),
		proto.RelayedAddress{IP: net.ParseIP("127.0.0.1"), Port: 40000},
		proto.Lifetime{Duration: 10 * time.Minute},
	)
	require.NoError(t, err)

	return msg.Raw
}

// allocateErrorResponse builds an error response with the given code for the
// recorded authenticated Allocate request.
func allocateErrorResponse(t *testing.T, req []byte, code stun.ErrorCode) []byte {
	t.Helper()

	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse),
		stun.ErrorCodeAttribute{Code: code, Reason: []byte("error")},
	)
	require.NoError(t, err)

	return msg.Raw
}

// awaitWrite waits until the observer has recorded at least n writes and
// returns the raw datagram at index n-1.
func awaitWrite(t *testing.T, conn *observerConn, n int32) []byte {
	t.Helper()

	require.Eventually(t, func() bool {
		return conn.writeCount.Load() >= n
	}, 5*time.Second, 5*time.Millisecond, "request %d never left the socket", n)

	return conn.write(int(n - 1))
}

// awaitRequestAfter waits for a request recorded at or after write index from
// whose transaction ID differs from excludeID (skipping retransmits of the
// excluded request), returning its raw datagram.
func awaitRequestAfter(t *testing.T, conn *observerConn, from int32, excludeID [stun.TransactionIDSize]byte) []byte {
	t.Helper()

	var raw []byte
	require.Eventually(t, func() bool {
		count := conn.writeCount.Load()
		for i := from; i < count; i++ {
			candidate := conn.write(int(i))
			if candidate == nil {
				continue
			}
			if transactionID(t, candidate) != excludeID {
				raw = candidate

				return true
			}
		}

		return false
	}, 5*time.Second, 5*time.Millisecond, "expected request never left the socket")

	return raw
}

// awaitAuthRequest feeds the 401 challenge for the first request and waits
// for the authenticated Allocate request that follows it, returning its raw
// datagram. Retransmits of the unauthenticated request share its transaction
// ID and are skipped.
func awaitAuthRequest(t *testing.T, cl *Client, conn *observerConn) []byte {
	t.Helper()

	first := awaitWrite(t, conn, 1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, first), testServerNetAddr()))

	return awaitRequestAfter(t, conn, 1, transactionID(t, first))
}

type allocateResult struct {
	alloc *Allocation
	err   error
}

func startObservedAllocate(cl *Client, ctx context.Context) <-chan allocateResult {
	result := make(chan allocateResult, 1)
	go func() {
		alloc, err := cl.Allocate(ctx)
		result <- allocateResult{alloc: alloc, err: err}
	}()

	return result
}

func completeObservedAllocate(
	t *testing.T,
	cl *Client,
	conn *observerConn,
	firstWriteOrdinal int32,
	result <-chan allocateResult,
) *Allocation {
	t.Helper()

	first := awaitWrite(t, conn, firstWriteOrdinal)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, first), testServerNetAddr()))
	authenticated := awaitRequestAfter(t, conn, firstWriteOrdinal, transactionID(t, first))
	require.NoError(t, cl.HandleInbound(allocateSuccessResponse(t, authenticated), testServerNetAddr()))

	select {
	case outcome := <-result:
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.alloc)

		return outcome.alloc
	case <-time.After(2 * time.Second):
		require.Fail(t, "Allocate did not return after the success response")

		return nil
	}
}

func TestAllocateRejectsConcurrentCallerWithoutNetworkOutput(t *testing.T) {
	conn := newObserverConn()
	conn.blockFrom = 1
	cl := newObservedClient(t, conn)

	firstCtx, cancelFirst := context.WithCancelCause(context.Background())
	defer cancelFirst(nil)
	firstResult := startObservedAllocate(cl, firstCtx)

	select {
	case <-conn.blocked:
	case <-time.After(5 * time.Second):
		require.Fail(t, "first Allocate write never blocked")
	}

	secondCtx, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	secondResult := startObservedAllocate(cl, secondCtx)
	select {
	case outcome := <-secondResult:
		assert.Nil(t, outcome.alloc)
		assert.Equal(t, ErrAlreadyAllocated, outcome.err)
	case <-time.After(2 * time.Second):
		require.Fail(t, "concurrent Allocate did not reject promptly")
	}
	assert.Equal(t, int32(1), conn.writeCount.Load(), "rejected concurrent Allocate must not write")

	cause := errors.New("finish first Allocate") //nolint:err113 // test-local cause
	cancelFirst(cause)
	close(conn.gate)
	select {
	case outcome := <-firstResult:
		assert.Nil(t, outcome.alloc)
		assert.ErrorIs(t, outcome.err, cause)
	case <-time.After(2 * time.Second):
		require.Fail(t, "first Allocate did not return after cancellation")
	}
	require.Eventually(t, func() bool {
		return conn.write(0) != nil
	}, time.Second, 5*time.Millisecond, "released first write did not finish recording")

	writesBeforeRetry := conn.writeCount.Load()
	retryResult := startObservedAllocate(cl, context.Background())
	retry := completeObservedAllocate(t, cl, conn, writesBeforeRetry+1, retryResult)
	require.NoError(t, retry.Close())
}

func TestAllocateRejectsLiveAllocationAndAllowsAllocateAfterClose(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)

	firstResult := startObservedAllocate(cl, context.Background())
	first := completeObservedAllocate(t, cl, conn, 1, firstResult)

	writesBeforeReject := conn.writeCount.Load()
	rejected, err := cl.Allocate(context.Background())
	assert.Nil(t, rejected)
	assert.Equal(t, ErrAlreadyAllocated, err)
	assert.Equal(t, writesBeforeReject, conn.writeCount.Load(), "rejected live Allocate must not write")

	require.NoError(t, first.Close())
	writesBeforeRetry := conn.writeCount.Load()
	retryResult := startObservedAllocate(cl, context.Background())
	retry := completeObservedAllocate(t, cl, conn, writesBeforeRetry+1, retryResult)
	require.NoError(t, retry.Close())
}

func TestAllocateTargetsConfiguredServer(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)

	type outcome struct {
		allocation *Allocation
		err        error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		allocation, err := cl.Allocate(context.Background())
		resultCh <- outcome{allocation: allocation, err: err}
	}()

	first := awaitWrite(t, conn, 1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, first), testServerNetAddr()))
	authenticated := awaitRequestAfter(t, conn, 1, transactionID(t, first))
	require.NoError(t, cl.HandleInbound(allocateSuccessResponse(t, authenticated), testServerNetAddr()))

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.allocation)
		t.Cleanup(func() { _ = result.allocation.Close() })
	case <-time.After(time.Second):
		require.Fail(t, "Allocate did not return after the success response")
	}

	want := testServerNetAddr().String()
	assert.Equal(t, want, conn.destination(0), "anonymous Allocate destination")
	assert.Equal(t, want, conn.destination(1), "authenticated Allocate destination")
}

func TestAllocateContext(t *testing.T) {
	t.Run("cancel before send returns the cause without touching the socket", func(t *testing.T) {
		conn := newObserverConn()
		cl := newObservedClient(t, conn)

		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("caller gave up before send") //nolint:err113 // test-local cause
		cancel(cause)

		alloc, err := cl.Allocate(ctx)
		assert.Nil(t, alloc)
		assert.ErrorIs(t, err, cause)
		assert.Equal(t, int32(0), conn.writeCount.Load(), "canceled-before-send Allocate must not write")
		assert.Equal(t, int32(0), conn.deadlineCalls.Load(), "the fork must never deadline the caller's socket")
		assert.Equal(t, int32(0), conn.closeCalls.Load(), "the fork must never close the caller's socket")
	})

	t.Run("cancel during the unauthenticated wait returns promptly with the cause", func(t *testing.T) {
		conn := newObserverConn()
		cl := newObservedClient(t, conn)

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		result := make(chan error, 1)
		go func() {
			_, err := cl.Allocate(ctx)
			result <- err
		}()

		awaitWrite(t, conn, 1)
		cause := errors.New("caller gave up during unauthenticated wait") //nolint:err113 // test-local cause
		start := time.Now()
		cancel(cause)

		select {
		case err := <-result:
			assert.ErrorIs(t, err, cause)
			assert.Less(t, time.Since(start), 500*time.Millisecond,
				"cancellation must return well inside the retransmission budget")
		case <-time.After(2 * time.Second):
			assert.Fail(t, "canceled Allocate did not return")
		}
		assert.Equal(t, int32(0), conn.deadlineCalls.Load(), "the fork must never deadline the caller's socket")
		assert.Equal(t, int32(0), conn.closeCalls.Load(), "the fork must never close the caller's socket")
	})

	t.Run("cancel during the authenticated wait returns promptly with the cause", func(t *testing.T) {
		conn := newObserverConn()
		cl := newObservedClient(t, conn)

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		result := make(chan error, 1)
		go func() {
			_, err := cl.Allocate(ctx)
			result <- err
		}()

		awaitAuthRequest(t, cl, conn)
		cause := errors.New("caller gave up during authenticated wait") //nolint:err113 // test-local cause
		start := time.Now()
		cancel(cause)

		select {
		case err := <-result:
			assert.ErrorIs(t, err, cause)
			assert.Less(t, time.Since(start), 500*time.Millisecond,
				"cancellation must return well inside the retransmission budget")
		case <-time.After(2 * time.Second):
			assert.Fail(t, "canceled Allocate did not return")
		}
		assert.Equal(t, int32(0), conn.deadlineCalls.Load(), "the fork must never deadline the caller's socket")
		assert.Equal(t, int32(0), conn.closeCalls.Load(), "the fork must never close the caller's socket")
	})

	t.Run("a published success wins over cancellation", func(t *testing.T) {
		conn := newObserverConn()
		cl := newObservedClient(t, conn)

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		type allocateResult struct {
			alloc *Allocation
			err   error
		}
		result := make(chan allocateResult, 1)
		go func() {
			alloc, err := cl.Allocate(ctx)
			result <- allocateResult{alloc, err}
		}()

		authReq := awaitAuthRequest(t, cl, conn)
		// HandleInbound returns only after the success result is published to
		// the transaction's buffered channel, so the cancellation below always
		// races a result the producer already owns.
		require.NoError(t, cl.HandleInbound(allocateSuccessResponse(t, authReq), testServerNetAddr()))
		cancel(errors.New("canceled after the response was published")) //nolint:err113 // test-local cause

		select {
		case res := <-result:
			assert.NoError(t, res.err, "a published success must win over cancellation")
			assert.NotNil(t, res.alloc)
			if res.alloc != nil {
				assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:40000"), res.alloc.RelayedAddr())
				assert.NoError(t, res.alloc.Close(), "the raced Allocation must be closable")
			}
		case <-time.After(2 * time.Second):
			assert.Fail(t, "Allocate did not return after the success response")
		}
	})
}

func TestAllocateCancelDuringBlockedRetransmit(t *testing.T) {
	conn := newObserverConn()
	conn.blockFrom = 2 // The initial send passes; the first retransmit blocks.
	cl := newObservedClient(t, conn)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() {
		_, err := cl.Allocate(ctx)
		result <- err
	}()

	// Wait for the retransmit write to be blocked in caller-socket I/O.
	select {
	case <-conn.blocked:
	case <-time.After(5 * time.Second):
		require.Fail(t, "retransmit write never started")
	}

	cause := errors.New("caller gave up while a retransmit was blocked") //nolint:err113 // test-local cause
	start := time.Now()
	cancel(cause)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, cause)
		assert.Less(t, time.Since(start), 500*time.Millisecond,
			"cancellation must not wait behind caller-socket I/O")
	case <-time.After(2 * time.Second):
		assert.Fail(t, "canceled Allocate did not return while a retransmit write was blocked")
	}

	// Release the blocked write: it must complete without re-arming the timer
	// and without a further send.
	close(conn.gate)
	assert.Eventually(t, func() bool {
		return conn.writeCount.Load() == 2
	}, time.Second, 5*time.Millisecond, "released retransmit write did not complete")
	time.Sleep(300 * time.Millisecond) // Several RTOs: a re-armed timer would have fired.
	assert.Equal(t, int32(2), conn.writeCount.Load(),
		"a retransmit completing after cancellation must not re-arm or send again")
}

func TestAllocateCancelProducerRace(t *testing.T) {
	conn := newObserverConn()
	conn.blockFrom = 2
	cl := newObservedClient(t, conn)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() {
		_, err := cl.Allocate(ctx)
		result <- err
	}()

	select {
	case <-conn.blocked:
	case <-time.After(5 * time.Second):
		require.Fail(t, "retransmit write never started")
	}

	cause := errors.New("caller gave up mid-retransmit") //nolint:err113 // test-local cause
	cancel(cause)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, cause)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "canceled Allocate did not return")
	}

	// With the producer's socket write still blocked, Close and HandleInbound
	// must remain callable: no producer blocks while owning the registry.
	closeDone := make(chan struct{})
	go func() {
		cl.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		assert.Fail(t, "Client.Close blocked behind an in-flight retransmit write")
	}

	handled := make(chan error, 1)
	go func() {
		// A response for an unknown transaction is silently discarded.
		unknown, err := stun.Build(
			stun.TransactionID,
			stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse),
		)
		if err != nil {
			handled <- err

			return
		}
		handled <- cl.HandleInbound(unknown.Raw, testServerNetAddr())
	}()
	select {
	case err := <-handled:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "HandleInbound blocked behind an in-flight retransmit write")
	}

	close(conn.gate)
}

func TestAllocateCancelVsClientClose(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() {
		_, err := cl.Allocate(ctx)
		result <- err
	}()

	awaitWrite(t, conn, 1)

	// The closer removes and closes the transaction first; the cancellation
	// that follows must lose: the truthful cause is the close.
	cl.Close()
	cause := errors.New("cancellation after close") //nolint:err113 // test-local cause
	cancel(cause)

	select {
	case err := <-result:
		assert.ErrorIs(t, err, net.ErrClosed, "a closed client must surface net.ErrClosed")
		assert.NotErrorIs(t, err, cause, "closure must take precedence over the cancellation cause")
	case <-time.After(2 * time.Second):
		assert.Fail(t, "Allocate did not return after Client.Close")
	}

	writesBeforeRetry := conn.writeCount.Load()
	retryResult := startObservedAllocate(cl, context.Background())
	retry := completeObservedAllocate(t, cl, conn, writesBeforeRetry+1, retryResult)
	require.NoError(t, retry.Close())
}

func TestAllocateLateSuccessDiscarded(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() {
		_, err := cl.Allocate(ctx)
		result <- err
	}()

	authReq := awaitAuthRequest(t, cl, conn)
	cause := errors.New("caller gave up before the late success") //nolint:err113 // test-local cause
	cancel(cause)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, cause)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "canceled Allocate did not return")
	}

	// The delayed authenticated success arrives after Allocate returned: it
	// must be discarded without blocking and without error.
	done := make(chan error, 1)
	go func() { done <- cl.HandleInbound(allocateSuccessResponse(t, authReq), testServerNetAddr()) }()
	select {
	case err := <-done:
		assert.NoError(t, err, "a late success for a departed waiter is silently discarded")
	case <-time.After(2 * time.Second):
		assert.Fail(t, "HandleInbound blocked delivering a late success")
	}

	// Documented consequence: the orphaned server-side allocation can answer a
	// same-Conn retry with 437 Allocation Mismatch, which surfaces as a value.
	writesBefore := conn.writeCount.Load()
	retryResult := make(chan error, 1)
	go func() {
		_, err := cl.Allocate(context.Background())
		retryResult <- err
	}()
	retryFirst := awaitWrite(t, conn, writesBefore+1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, retryFirst), testServerNetAddr()))
	retryAuth := awaitRequestAfter(t, conn, writesBefore+1, transactionID(t, retryFirst))

	const codeAllocMismatch stun.ErrorCode = 437
	require.NoError(t, cl.HandleInbound(allocateErrorResponse(t, retryAuth, codeAllocMismatch), testServerNetAddr()))

	select {
	case err := <-retryResult:
		var turnErr *stun.TurnError
		assert.ErrorAs(t, err, &turnErr, "the 437 must surface as a typed value")
		if turnErr != nil {
			assert.Equal(t, codeAllocMismatch, turnErr.ErrorCodeAttr.Code)
		}
	case <-time.After(2 * time.Second):
		assert.Fail(t, "retry Allocate did not return")
	}
}
