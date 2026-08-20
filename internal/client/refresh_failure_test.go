// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refreshFailureHarness drives a NewUDPConn whose allocation refreshes fail
// under a scripted mock, observing the terminal seal and the lifetime-0
// deallocation emission.
type refreshFailureHarness struct {
	conn *UDPConn

	// waitedRefresh scripts the outcome of a waited Refresh
	// transaction. Read on every waited refresh.
	waitedRefresh func() (*stun.Message, error)
	// emitErr, when non-nil, fails the lifetime-zero Release emission.
	emitErr error

	waitedCount atomic.Int32
	emitCount   atomic.Int32
}

func newRefreshFailureHarness(
	t *testing.T, waited func() (*stun.Message, error), emitErr error,
) *refreshFailureHarness {
	t.Helper()

	harness := &refreshFailureHarness{waitedRefresh: waited, emitErr: emitErr}
	script := &testConnScript{
		performTransaction: func(msg *stun.Message) (*stun.Message, error) {
			switch msg.Type.Method {
			case stun.MethodRefresh:
				harness.waitedCount.Add(1)

				return harness.waitedRefresh()
			case stun.MethodCreatePermission:
				return stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
				), nil
			case stun.MethodChannelBind:
				return stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
				), nil
			default:
				return nil, errFake
			}
		},
		startTransaction: func(*stun.Message) error {
			harness.emitCount.Add(1)

			return harness.emitErr
		},
		writeTo: func(data []byte) (int, error) {
			return len(data), nil
		},
	}

	harness.conn = newTestConn(t, script)

	return harness
}

func turnErrorResponse(code stun.ErrorCode) *stun.Message {
	return stun.MustBuild(
		stun.NewType(stun.MethodRefresh, stun.ClassErrorResponse),
		stun.ErrorCodeAttribute{Code: code, Reason: []byte("error")},
	)
}

func staleNonceResponse() *stun.Message {
	return stun.MustBuild(
		stun.NewType(stun.MethodRefresh, stun.ClassErrorResponse),
		stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")},
		stun.NewNonce("fresh-nonce"),
	)
}

func TestRefreshFailureTerminalizes(t *testing.T) {
	peer := netip.MustParseAddrPort("127.0.0.1:1234")

	tests := []struct {
		name string
		// waited scripts every waited refresh outcome.
		waited func() (*stun.Message, error)
		// wantWaited is the number of waited refresh transactions the retry
		// loop is expected to run before the failure is permanent.
		wantWaited int32
		// underlying, when non-nil, must appear in the recorded terminal cause.
		underlying error
		// wantTurnError asserts the cause carries a typed *stun.TurnError.
		wantTurnError bool
	}{
		{
			name: "exhausted refresh transaction",
			waited: func() (*stun.Message, error) {
				return nil, fmt.Errorf("%w: transaction test", ErrTransactionTimeout)
			},
			wantWaited: 1,
			underlying: ErrTransactionTimeout,
		},
		{
			name: "well-formed non-438 error response",
			waited: func() (*stun.Message, error) {
				return turnErrorResponse(stun.CodeServerError), nil
			},
			wantWaited:    1,
			wantTurnError: true,
		},
		{
			name: "stale-nonce retry exhaustion",
			waited: func() (*stun.Message, error) {
				return staleNonceResponse(), nil
			},
			wantWaited: maxRetryAttempts,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newRefreshFailureHarness(t, tt.waited, nil)
			conn := harness.conn

			// A blocked reader must wake when the allocation terminalizes.
			readResult := make(chan error, 1)
			go func() {
				_, _, err := conn.ReadFrom(make([]byte, 64))
				readResult <- err
			}()
			time.Sleep(50 * time.Millisecond) // Let the reader block.

			// The refresh timer fires against a permanently failing server.
			conn.onRefreshTimers(timerIDRefreshAlloc)

			assert.Equal(t, tt.wantWaited, harness.waitedCount.Load(),
				"unexpected number of waited refresh transactions")
			assert.Equal(t, int32(1), harness.emitCount.Load(),
				"a refresh-failure seal must emit exactly one lifetime-0 refresh")

			select {
			case err := <-readResult:
				require.ErrorIs(t, err, net.ErrClosed, "post-seal ReadFrom must wrap net.ErrClosed")
				require.ErrorIs(t, err, ErrAllocationRefreshFailed, "post-seal ReadFrom must carry the terminal cause")
			case <-time.After(2 * time.Second):
				assert.Fail(t, "blocked ReadFrom did not wake on the refresh-failure seal")
			}

			_, err := conn.WriteTo([]byte("data"), peer)
			require.ErrorIs(t, err, net.ErrClosed)
			require.ErrorIs(t, err, ErrAllocationRefreshFailed)

			err = conn.PreparePeer(context.Background(), peer)
			require.ErrorIs(t, err, net.ErrClosed)
			require.ErrorIs(t, err, ErrAllocationRefreshFailed)

			// The caller's Close joins and returns the recorded terminal cause,
			// wrapping only the underlying failure — never a synthetic
			// net.ErrClosed.
			closeErr := conn.Close()
			require.Error(t, closeErr)
			require.ErrorIs(t, closeErr, ErrAllocationRefreshFailed)
			require.NotErrorIs(t, closeErr, net.ErrClosed,
				"the terminal cause must wrap the underlying failure, not a synthetic net.ErrClosed")
			if tt.underlying != nil {
				require.ErrorIs(t, closeErr, tt.underlying)
			}
			if tt.wantTurnError {
				var turnErr *stun.TurnError
				require.ErrorAs(t, closeErr, &turnErr,
					"a well-formed error response must surface as a typed *stun.TurnError")
			}

			require.ErrorIs(t, conn.Close(), net.ErrClosed,
				"a repeated caller Close returns net.ErrClosed")

			assert.Equal(t, int32(1), harness.emitCount.Load(),
				"the caller's Close after a self-seal must not emit a second lifetime-0 refresh")
		})
	}
}

func TestConcurrentCallerCloses(t *testing.T) {
	harness := newRefreshFailureHarness(t, func() (*stun.Message, error) {
		return nil, errFake
	}, nil)
	conn := harness.conn

	const closers = 4
	results := make([]error, closers)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(closers)
	for i := range closers {
		go func() {
			defer done.Done()
			start.Wait()
			results[i] = conn.Close()
		}()
	}
	start.Done()
	done.Wait()

	var nilCount, closedCount int
	for _, err := range results {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, net.ErrClosed):
			closedCount++
		default:
			assert.Failf(t, "unexpected concurrent Close result", "err: %v", err)
		}
	}
	assert.Equal(t, 1, nilCount,
		"exactly one concurrent caller Close observes the successful release")
	assert.Equal(t, closers-1, closedCount,
		"every duplicate caller Close returns net.ErrClosed")
	assert.Equal(t, int32(1), harness.emitCount.Load(),
		"concurrent caller Closes must yield exactly one lifetime-0 emission")
}

func TestRefreshFailureSealVsCloseRace(t *testing.T) {
	harness := newRefreshFailureHarness(t, func() (*stun.Message, error) {
		return nil, errFake
	}, nil)
	conn := harness.conn

	sealDone := make(chan struct{})
	closeResult := make(chan error, 1)
	go func() {
		conn.onRefreshTimers(timerIDRefreshAlloc)
		close(sealDone)
	}()
	go func() { closeResult <- conn.Close() }()

	select {
	case <-sealDone:
	case <-time.After(5 * time.Second):
		require.Fail(t, "refresh-failure seal did not complete")
	}
	var closeErr error
	select {
	case closeErr = <-closeResult:
	case <-time.After(5 * time.Second):
		require.Fail(t, "Close did not return")
	}

	assert.Equal(t, int32(1), harness.emitCount.Load(),
		"a refresh-failure seal racing a caller Close must yield exactly one lifetime-0 emission")
	if closeErr != nil {
		// The self-seal won: Close observed the one recorded terminal cause.
		assert.ErrorIs(t, closeErr, ErrAllocationRefreshFailed)
	}
}

func TestSelfSealEmissionFailureJoinsCause(t *testing.T) {
	emitErr := errors.New("lifetime-0 write failed") //nolint:err113 // test-local cause
	harness := newRefreshFailureHarness(t, func() (*stun.Message, error) {
		return nil, errFake
	}, emitErr)
	conn := harness.conn

	conn.onRefreshTimers(timerIDRefreshAlloc)

	closeErr := conn.Close()
	require.Error(t, closeErr)
	require.ErrorIs(t, closeErr, ErrAllocationRefreshFailed,
		"the caller's Close must observe the refresh-failure cause")
	assert.ErrorIs(t, closeErr, emitErr,
		"a failed self-seal emission must be joined into the terminal cause")
}
