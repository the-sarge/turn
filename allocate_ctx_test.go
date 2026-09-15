// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/internal/proto"
)

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

type fallbackLocalAddr string

func (a fallbackLocalAddr) Network() string { return "fallback" }

func (a fallbackLocalAddr) String() string { return string(a) }

func assertAllocateRequestShape(
	t *testing.T,
	raw []byte,
	wantAttrs []stun.AttrType,
	setters ...stun.Setter,
) {
	t.Helper()

	actual := &stun.Message{Raw: append([]byte(nil), raw...)}
	require.NoError(t, actual.Decode())

	gotAttrs := make([]stun.AttrType, 0, len(actual.Attributes))
	for _, attr := range actual.Attributes {
		gotAttrs = append(gotAttrs, attr.Type)
	}
	assert.Equal(t, wantAttrs, gotAttrs, "Allocate request attribute order")

	expectedSetters := make([]stun.Setter, 0, len(setters)+1)
	expectedSetters = append(expectedSetters, stun.NewTransactionIDSetter(actual.TransactionID))
	expectedSetters = append(expectedSetters, setters...)
	expected := stun.MustBuild(expectedSetters...)
	assert.Equal(t, expected.Raw, actual.Raw, "Allocate request normalized to the observed transaction ID")
}

func TestAllocateRequestWireShape(t *testing.T) {
	tests := []struct {
		name        string
		localAddr   net.Addr
		requestIPv6 bool
	}{
		{
			name:      "IPv4 UDP socket",
			localAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555},
		},
		{
			name:        "IPv6 UDP socket",
			localAddr:   &net.UDPAddr{IP: net.ParseIP("::"), Port: 5555},
			requestIPv6: true,
		},
		{
			name:      "non-UDP local address fallback",
			localAddr: fallbackLocalAddr("fallback"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newObserverConn()
			conn.localAddr = tt.localAddr
			cl := newObservedClient(t, conn)
			result := startObservedAllocate(cl, context.Background())

			anonymous := awaitWrite(t, conn, 1)
			anonymousAttrs := []stun.AttrType{stun.AttrRequestedTransport}
			anonymousSetters := []stun.Setter{
				stun.NewType(stun.MethodAllocate, stun.ClassRequest),
				proto.RequestedTransport{Protocol: proto.ProtoUDP},
			}
			if tt.requestIPv6 {
				anonymousAttrs = append(anonymousAttrs, stun.AttrRequestedAddressFamily)
				anonymousSetters = append(anonymousSetters, proto.RequestedFamilyIPv6)
			}
			anonymousAttrs = append(anonymousAttrs, stun.AttrFingerprint)
			anonymousSetters = append(anonymousSetters, stun.Fingerprint)
			assertAllocateRequestShape(t, anonymous, anonymousAttrs, anonymousSetters...)

			realm := stun.NewRealm("test-realm")
			nonce := stun.NewNonce("test-nonce")
			username := stun.NewUsername(testUsername)
			integrity := stun.NewLongTermIntegrity(testUsername, "test-realm", testPassword)
			require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, anonymous), testServerNetAddr()))
			authenticated := awaitRequestAfter(t, conn, 1, transactionID(t, anonymous))
			authenticatedAttrs := []stun.AttrType{
				stun.AttrRequestedTransport,
				stun.AttrUsername,
				stun.AttrRealm,
				stun.AttrNonce,
			}
			authenticatedSetters := []stun.Setter{
				stun.NewType(stun.MethodAllocate, stun.ClassRequest),
				proto.RequestedTransport{Protocol: proto.ProtoUDP},
				&username,
				&realm,
				&nonce,
			}
			if tt.requestIPv6 {
				authenticatedAttrs = append(authenticatedAttrs, stun.AttrRequestedAddressFamily)
				authenticatedSetters = append(authenticatedSetters, proto.RequestedFamilyIPv6)
			}
			authenticatedAttrs = append(authenticatedAttrs, stun.AttrMessageIntegrity)
			authenticatedSetters = append(authenticatedSetters, &integrity)
			authenticatedAttrs = append(authenticatedAttrs, stun.AttrFingerprint)
			authenticatedSetters = append(authenticatedSetters, stun.Fingerprint)
			assertAllocateRequestShape(t, authenticated, authenticatedAttrs, authenticatedSetters...)

			require.NoError(t, cl.HandleInbound(allocateSuccessResponse(t, authenticated), testServerNetAddr()))
			select {
			case outcome := <-result:
				require.NoError(t, outcome.err)
				require.NotNil(t, outcome.alloc)
				require.NoError(t, outcome.alloc.Close())
			case <-time.After(2 * time.Second):
				require.Fail(t, "Allocate did not return after the success response")
			}
		})
	}
}

func TestSendAllocateRequestReturnsAllocationInputs(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)

	type outcome struct {
		exchange allocateExchange
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		exchange, err := cl.sendAllocateRequest(context.Background(), proto.ProtoUDP)
		result <- outcome{exchange: exchange, err: err}
	}()

	anonymous := awaitWrite(t, conn, 1)
	require.NoError(t, cl.HandleInbound(unauthorizedResponse(t, anonymous), testServerNetAddr()))
	authenticated := awaitRequestAfter(t, conn, 1, transactionID(t, anonymous))
	require.NoError(t, cl.HandleInbound(allocateSuccessResponse(t, authenticated), testServerNetAddr()))

	select {
	case got := <-result:
		require.NoError(t, got.err)
		assert.True(t, got.exchange.relayed.IP.Equal(net.ParseIP("127.0.0.1")))
		assert.Equal(t, 40000, got.exchange.relayed.Port)
		assert.Equal(t, 10*time.Minute, got.exchange.lifetime.Duration)
		assert.Equal(t, stun.NewRealm("test-realm"), got.exchange.realm)
		assert.Equal(t, stun.NewNonce("test-nonce"), got.exchange.nonce)
		assert.Equal(t, stun.NewLongTermIntegrity(testUsername, "test-realm", testPassword), got.exchange.integrity)
	case <-time.After(2 * time.Second):
		require.Fail(t, "Allocate exchange did not return after the success response")
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
	assert.Equal(t, int32(1), conn.admittedWrites.Load(), "rejected concurrent Allocate must not write")

	cause := errors.New("finish first Allocate") //nolint:err113 // test-local cause
	cancelFirst(cause)
	close(conn.gate)
	select {
	case outcome := <-firstResult:
		assert.Nil(t, outcome.alloc)
		require.ErrorIs(t, outcome.err, cause)
	case <-time.After(2 * time.Second):
		require.Fail(t, "first Allocate did not return after cancellation")
	}
	awaitWrite(t, conn, 1)

	writesBeforeRetry := conn.recordedCount()
	retryResult := startObservedAllocate(cl, context.Background())
	retry := completeObservedAllocate(t, cl, conn, writesBeforeRetry+1, retryResult)
	require.NoError(t, retry.Close())
}

func TestAllocateRejectsLiveAllocationAndAllowsAllocateAfterClose(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)

	firstResult := startObservedAllocate(cl, context.Background())
	first := completeObservedAllocate(t, cl, conn, 1, firstResult)

	writesBeforeReject := conn.admittedWrites.Load()
	rejected, err := cl.Allocate(context.Background())
	assert.Nil(t, rejected)
	assert.Equal(t, ErrAlreadyAllocated, err)
	assert.Equal(t, writesBeforeReject, conn.admittedWrites.Load(), "rejected live Allocate must not write")

	require.NoError(t, first.Close())
	writesBeforeRetry := conn.recordedCount()
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
		require.ErrorIs(t, err, cause)
		assert.Equal(t, int32(0), conn.admittedWrites.Load(), "canceled-before-send Allocate must not write")
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
			require.ErrorIs(t, err, cause)
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
			require.ErrorIs(t, err, cause)
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
			require.NoError(t, res.err, "a published success must win over cancellation")
			assert.NotNil(t, res.alloc)
			if res.alloc != nil {
				assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:40000"), res.alloc.RelayedAddr())
				require.NoError(t, res.alloc.Close(), "the raced Allocation must be closable")
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
		require.ErrorIs(t, err, cause)
		assert.Less(t, time.Since(start), 500*time.Millisecond,
			"cancellation must not wait behind caller-socket I/O")
	case <-time.After(2 * time.Second):
		assert.Fail(t, "canceled Allocate did not return while a retransmit write was blocked")
	}

	// Release the blocked write: it must complete without re-arming the timer
	// and without a further send.
	close(conn.gate)
	awaitWrite(t, conn, 2)
	time.Sleep(300 * time.Millisecond) // Several RTOs: a re-armed timer would have fired.
	assert.Equal(t, int32(2), conn.admittedWrites.Load(),
		"a retransmit completing after cancellation must not re-arm or send again")
	assert.Zero(t, conn.deadlineCalls.Load(), "the fork must never deadline the caller's socket")
	assert.Zero(t, conn.closeCalls.Load(), "the fork must never close the caller's socket")
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
		require.ErrorIs(t, err, cause)
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
		require.NoError(t, err)
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
		require.ErrorIs(t, err, net.ErrClosed, "a closed client must surface net.ErrClosed")
		require.NotErrorIs(t, err, cause, "closure must take precedence over the cancellation cause")
	case <-time.After(2 * time.Second):
		assert.Fail(t, "Allocate did not return after Client.Close")
	}

	writesBeforeRetry := conn.recordedCount()
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
		require.ErrorIs(t, err, cause)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "canceled Allocate did not return")
	}

	// The delayed authenticated success arrives after Allocate returned: it
	// must be discarded without blocking and without error.
	// Build the response on the test goroutine: allocateSuccessResponse asserts
	// with require, which may only fail the test from the test goroutine.
	lateSuccess := allocateSuccessResponse(t, authReq)
	done := make(chan error, 1)
	go func() { done <- cl.HandleInbound(lateSuccess, testServerNetAddr()) }()
	select {
	case err := <-done:
		require.NoError(t, err, "a late success for a departed waiter is silently discarded")
	case <-time.After(2 * time.Second):
		assert.Fail(t, "HandleInbound blocked delivering a late success")
	}

	// Documented consequence: the orphaned server-side allocation can answer a
	// same-Conn retry with 437 Allocation Mismatch, which surfaces as a value.
	writesBefore := conn.recordedCount()
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
		require.ErrorAs(t, err, &turnErr, "the 437 must surface as a typed value")
		if turnErr != nil {
			assert.Equal(t, codeAllocMismatch, turnErr.ErrorCodeAttr.Code)
		}
	case <-time.After(2 * time.Second):
		assert.Fail(t, "retry Allocate did not return")
	}
}
