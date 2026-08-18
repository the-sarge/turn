// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

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

// prepareHarness drives a NewUDPConn against a scripted mock TURN server.
type prepareHarness struct {
	conn       *UDPConn
	mock       *mockClient
	peer       netip.AddrPort
	permCount  atomic.Int32
	bindCount  atomic.Int32
	bindGate   chan struct{} // If non-nil, ChannelBind transactions block on it
	permGate   chan struct{} // If non-nil, CreatePermission transactions block on it
	failPerms  atomic.Bool   // If set, CreatePermission transactions return 403
	staleNonce atomic.Bool   // If set, CreatePermission transactions return 438
	writes     struct {
		sync.Mutex
		data [][]byte
	}
}

func newPrepareHarness(t *testing.T, gateBinds bool) *prepareHarness {
	t.Helper()

	harness := &prepareHarness{
		peer: netip.MustParseAddrPort("127.0.0.1:1234"),
	}
	if gateBinds {
		harness.bindGate = make(chan struct{})
	}

	mock := &mockClient{
		performTransaction: func(msg *stun.Message, _ net.Addr, _ bool) (TransactionResult, error) {
			switch msg.Type.Method {
			case stun.MethodCreatePermission:
				harness.permCount.Add(1)
				if harness.permGate != nil {
					<-harness.permGate
				}
				if harness.failPerms.Load() {
					return TransactionResult{Msg: stun.MustBuild(
						stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Forbidden")},
					)}, nil
				}
				if harness.staleNonce.Load() {
					return TransactionResult{Msg: stun.MustBuild(
						stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")},
						stun.NewNonce("nonce2"),
					)}, nil
				}

				return TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
				)}, nil
			case stun.MethodChannelBind:
				harness.bindCount.Add(1)
				if harness.bindGate != nil {
					<-harness.bindGate
				}

				return TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
				)}, nil
			case stun.MethodRefresh:
				return TransactionResult{}, nil
			default:
				return TransactionResult{}, errFake
			}
		},
		writeTo: func(data []byte, _ net.Addr) (int, error) {
			harness.writes.Lock()
			harness.writes.data = append(harness.writes.data, append([]byte(nil), data...))
			harness.writes.Unlock()

			return len(data), nil
		},
	}

	harness.mock = mock
	harness.conn = NewUDPConn(&AllocationConfig{
		WriteTo:            mock.WriteTo,
		PerformTransaction: mock.PerformTransaction,
		OnDeallocated:      mock.OnDeallocated,
		RelayedAddr:        &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321},
		ServerAddr:         &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478},
		Username:           stun.NewUsername("user"),
		Realm:              stun.NewRealm("realm"),
		Integrity:          stun.NewShortTermIntegrity("pass"),
		Nonce:              stun.NewNonce("nonce"),
		Lifetime:           time.Hour,
	}, func() {})
	t.Cleanup(func() { _ = harness.conn.Close() })

	return harness
}

func (harness *prepareHarness) writeCount() int {
	harness.writes.Lock()
	defer harness.writes.Unlock()

	return len(harness.writes.data)
}

func (harness *prepareHarness) lastWrite() []byte {
	harness.writes.Lock()
	defer harness.writes.Unlock()

	if len(harness.writes.data) == 0 {
		return nil
	}

	return harness.writes.data[len(harness.writes.data)-1]
}

func TestPreparePeer(t *testing.T) { //nolint:maintidx,cyclop,gocyclo
	t.Run("readiness success then ChannelData writes", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Equal(t, int32(1), harness.permCount.Load())
		assert.Equal(t, int32(1), harness.bindCount.Load())

		n, err := harness.conn.WriteTo([]byte("hello"), harness.peer)
		assert.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.True(t, proto.IsChannelData(harness.lastWrite()),
			"write after successful PreparePeer must be ChannelData, not Send indication")
	})

	t.Run("capacity exhaustion follows permission and starts no ChannelBind", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		peerAddr := netip.MustParseAddr("192.0.2.1")
		for i := range maxChannelBindings {
			peer := netip.AddrPortFrom(peerAddr, uint16(i+1)) //nolint:gosec // Bounded by channel capacity.
			_, ok := harness.conn.bindingMgr.getOrCreate(peer)
			require.True(t, ok)
		}

		bindingsBefore := harness.conn.bindingMgr.all()
		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		assert.ErrorIs(t, err, ErrChannelBindFailed)
		assert.Equal(t, int32(1), harness.permCount.Load(), "permission phase remains first")
		assert.Equal(t, int32(0), harness.bindCount.Load(), "exhaustion must not start ChannelBind")
		assert.Len(t, harness.conn.bindingMgr.all(), len(bindingsBefore))
		_, found := harness.conn.bindingMgr.findByAddr(harness.peer)
		assert.False(t, found)
		perm, found := harness.conn.permMap.find(harness.peer)
		require.True(t, found)
		assert.Equal(t, permStatePermitted, perm.state())

		harness.failPerms.Store(true)
		rejectedPeer := netip.MustParseAddrPort("192.0.2.2:1234")
		err = harness.conn.PreparePeer(context.Background(), rejectedPeer)
		var turnErr *stun.TurnError
		require.ErrorAs(t, err, &turnErr)
		assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
		assert.NotErrorIs(t, err, ErrChannelBindFailed, "permission rejection retains its earlier outcome")
		assert.Equal(t, int32(0), harness.bindCount.Load(), "permission rejection must stop before binding")
		_, found = harness.conn.bindingMgr.findByAddr(rejectedPeer)
		assert.False(t, found)
	})

	t.Run("cancellation and closure retain their pre-binding outcomes", func(t *testing.T) {
		t.Run("cancellation", func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			cause := errors.New("caller canceled before preparation") //nolint:err113 // Test-local cause.
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(cause)

			err := harness.conn.PreparePeer(ctx, harness.peer)
			assert.ErrorIs(t, err, cause)
			assert.Equal(t, int32(0), harness.permCount.Load())
			assert.Equal(t, int32(0), harness.bindCount.Load())
		})

		t.Run("closure", func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			require.NoError(t, harness.conn.Close())

			err := harness.conn.PreparePeer(context.Background(), harness.peer)
			assert.ErrorIs(t, err, net.ErrClosed)
			assert.Equal(t, int32(0), harness.permCount.Load())
			assert.Equal(t, int32(0), harness.bindCount.Load())
		})
	})

	t.Run("terminal failure survives an in-flight bind success", func(t *testing.T) {
		harness := newPrepareHarness(t, true)

		bound, ok := harness.conn.bindingMgr.getOrCreate(harness.peer)
		require.True(t, ok)
		harness.conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return harness.bindCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		// Terminalize while the bind transaction is still in flight, then let
		// it succeed: the binding must stay failed.
		bound.prepared.Store(true)
		bound.terminalize(errFake)
		close(harness.bindGate)

		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return bound.attemptDone == nil
		}, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, bindingStateFailed, bound.state(),
			"completed bind attempt must not resurrect a terminalized binding")

		_, err := harness.conn.WriteTo([]byte("data"), harness.peer)
		assert.ErrorIs(t, err, errFake)
	})

	t.Run("same-peer callers coalesce onto one bind", func(t *testing.T) {
		harness := newPrepareHarness(t, true)

		const waiters = 4
		results := make(chan error, waiters)
		for range waiters {
			go func() {
				results <- harness.conn.PreparePeer(context.Background(), harness.peer)
			}()
		}

		// Let the first attempt start and the rest pile onto it.
		assert.Eventually(t, func() bool {
			return harness.bindCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)
		time.Sleep(100 * time.Millisecond)
		close(harness.bindGate)

		for range waiters {
			select {
			case err := <-results:
				assert.NoError(t, err)
			case <-time.After(5 * time.Second):
				assert.Fail(t, "timed out waiting for PreparePeer")
			}
		}
		assert.Equal(t, int32(1), harness.permCount.Load(), "permission transactions should coalesce")
		assert.Equal(t, int32(1), harness.bindCount.Load(), "ChannelBind transactions should coalesce")
	})

	t.Run("cancellation wakes only that waiter", func(t *testing.T) {
		harness := newPrepareHarness(t, true)

		ctxA, cancelA := context.WithCancelCause(context.Background())
		defer cancelA(nil)
		causeA := errors.New("waiter A gave up") //nolint:err113 // test-local cause

		resultA := make(chan error, 1)
		resultB := make(chan error, 1)
		go func() { resultA <- harness.conn.PreparePeer(ctxA, harness.peer) }()
		go func() { resultB <- harness.conn.PreparePeer(context.Background(), harness.peer) }()

		assert.Eventually(t, func() bool {
			return harness.bindCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		cancelA(causeA)
		select {
		case err := <-resultA:
			assert.ErrorIs(t, err, causeA, "canceled waiter must observe its cause")
		case <-time.After(2 * time.Second):
			assert.Fail(t, "canceled waiter did not wake promptly")
		}

		// The shared bind attempt must survive waiter A's cancellation.
		select {
		case err := <-resultB:
			assert.Failf(t, "waiter B finished early", "err: %v", err)
		case <-time.After(200 * time.Millisecond):
		}

		close(harness.bindGate)
		select {
		case err := <-resultB:
			assert.NoError(t, err, "surviving waiter should complete via the shared bind")
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for surviving waiter")
		}
		assert.Equal(t, int32(1), harness.bindCount.Load(), "cancellation must not restart or cancel the shared bind")
	})

	t.Run("cancellation wakes waiter during in-flight permission transaction", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		harness.permGate = make(chan struct{})

		// First caller's CreatePermission transaction is in flight (and holds
		// the permission mutex for its duration, as createPermission does).
		resultA := make(chan error, 1)
		go func() { resultA <- harness.conn.PreparePeer(context.Background(), harness.peer) }()
		assert.Eventually(t, func() bool {
			return harness.permCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		// A second caller for the same peer must wait on the attempt channel,
		// where its cancellation can wake it — not on the permission mutex.
		ctxB, cancelB := context.WithCancelCause(context.Background())
		defer cancelB(nil)
		resultB := make(chan error, 1)
		go func() { resultB <- harness.conn.PreparePeer(ctxB, harness.peer) }()
		time.Sleep(100 * time.Millisecond)

		cause := errors.New("waiter B gave up") //nolint:err113 // test-local cause
		cancelB(cause)
		select {
		case err := <-resultB:
			assert.ErrorIs(t, err, cause,
				"waiter must be cancelable while the permission transaction is in flight")
		case <-time.After(2 * time.Second):
			assert.Fail(t, "canceled waiter did not wake during in-flight permission transaction")
		}

		close(harness.permGate)
		select {
		case err := <-resultA:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for first caller")
		}
		assert.Equal(t, int32(1), harness.permCount.Load(), "permission transactions should coalesce")
	})

	t.Run("permission refresh failure fails writes, never Send indication", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))

		// Simulate the permission-refresh timer firing against a server that
		// now rejects the refresh.
		harness.failPerms.Store(true)
		harness.conn.onRefreshTimers(timerIDRefreshPerms)

		writesBefore := harness.writeCount()
		_, err := harness.conn.WriteTo([]byte("data"), harness.peer)
		assert.ErrorIs(t, err, ErrPermissionRefreshFailed)
		assert.Equal(t, writesBefore, harness.writeCount(),
			"failed write for a prepared peer must not emit anything (no Send indication fallback)")

		assert.ErrorIs(t, harness.conn.PreparePeer(context.Background(), harness.peer), ErrPermissionRefreshFailed,
			"readiness must be terminal after permission refresh failure")
	})

	t.Run("permission refresh success keeps prepared binding usable", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Equal(t, int32(1), harness.permCount.Load())

		harness.conn.onRefreshTimers(timerIDRefreshPerms)
		assert.Equal(t, int32(2), harness.permCount.Load(),
			"the consolidated receiver must refresh the existing permission")

		n, err := harness.conn.WriteTo([]byte("still ready"), harness.peer)
		assert.NoError(t, err)
		assert.Equal(t, len("still ready"), n)
		assert.True(t, proto.IsChannelData(harness.lastWrite()))
	})

	t.Run("bind failure surfaces to preparing caller", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		// First permission succeeds, but every ChannelBind transaction fails.
		mock := harness.mock
		inner := mock.performTransaction
		mock.performTransaction = func(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error) {
			if msg.Type.Method == stun.MethodChannelBind {
				harness.bindCount.Add(1)

				return TransactionResult{}, errFake
			}

			return inner(msg, to, dontWait)
		}

		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		assert.ErrorIs(t, err, errChannelBindTransactionFailed)
		assert.False(t, harness.conn.isClosed())
	})

	t.Run("server bind rejection surfaces typed TURN error", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		mock := harness.mock
		inner := mock.performTransaction
		mock.performTransaction = func(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error) {
			if msg.Type.Method == stun.MethodChannelBind {
				harness.bindCount.Add(1)

				return TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
					stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Forbidden")},
				)}, nil
			}

			return inner(msg, to, dontWait)
		}

		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		var turnErr *stun.TurnError
		if assert.ErrorAs(t, err, &turnErr) {
			assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
		}
		assert.ErrorIs(t, err, errCannotBindChannel)
		assert.False(t, harness.conn.isClosed())
	})

	t.Run("close joins in-flight bind workers", func(t *testing.T) {
		harness := newPrepareHarness(t, true)

		prepareResult := make(chan error, 1)
		go func() { prepareResult <- harness.conn.PreparePeer(context.Background(), harness.peer) }()

		assert.Eventually(t, func() bool {
			return harness.bindCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		closeResult := make(chan error, 1)
		go func() { closeResult <- harness.conn.Close() }()

		// The waiter unblocks promptly; Close must keep waiting for the worker.
		select {
		case err := <-prepareResult:
			assert.ErrorIs(t, err, net.ErrClosed)
		case <-time.After(2 * time.Second):
			assert.Fail(t, "PreparePeer waiter did not unblock on close")
		}
		select {
		case <-closeResult:
			assert.Fail(t, "Close returned while a bind worker was still in flight")
		case <-time.After(300 * time.Millisecond):
		}

		close(harness.bindGate)
		select {
		case err := <-closeResult:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "Close did not return after the bind worker finished")
		}
	})

	t.Run("attempt in flight during self-seal records the terminal cause", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		harness.permGate = make(chan struct{})
		harness.staleNonce.Store(true)

		// A permission attempt is mid-transaction when the allocation seals
		// itself. The stale-nonce reply sends the worker around its retry loop,
		// where it observes the seal; the result it records for any waiter that
		// joined the attempt must carry the recorded terminal cause, not bare
		// net.ErrClosed.
		perm := harness.conn.permMap.getOrCreate(harness.peer)
		done := harness.conn.ensurePermissionAttempt(perm, harness.peer)
		assert.NotNil(t, done)
		assert.Eventually(t, func() bool {
			return harness.permCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		harness.conn.startClose(errFake)
		close(harness.permGate)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			assert.Fail(t, "permission attempt did not finish after the seal")
		}

		perm.attemptMutex.Lock()
		err := perm.attemptErr
		perm.attemptMutex.Unlock()
		assert.ErrorIs(t, err, net.ErrClosed)
		assert.ErrorIs(t, err, errFake,
			"an in-flight attempt finishing after the seal must record the terminal cause")
	})

	t.Run("re-entry after binding expiry is terminal", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		bound, ok := harness.conn.bindingMgr.getOrCreate(harness.peer)
		require.True(t, ok)
		bound.setRefreshedAt(time.Now().Add(-channelBindingLifetime))

		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		assert.ErrorIs(t, err, ErrChannelBindingExpired,
			"a preparing caller re-entering an expired binding must observe the expiry")
	})
}

// TestWriteToPreparedOnly is the finite state table for the prepared-only
// write invariant: a write to a peer emits ChannelData or fails with zero
// network output. Only the prepared-and-ready state produces a datagram, and
// that datagram is ChannelData; no state creates a permission or starts a
// bind on the write path.
func TestWriteToPreparedOnly(t *testing.T) {
	tests := []struct {
		name         string
		arrange      func(t *testing.T, harness *prepareHarness)
		wantErr      error
		wantWrites   int  // datagrams emitted by the WriteTo under test
		wantNoServer bool // no permission or bind transaction may run at all
	}{
		{
			name:         "unprepared peer",
			arrange:      func(*testing.T, *prepareHarness) {},
			wantErr:      ErrNotPrepared,
			wantWrites:   0,
			wantNoServer: true,
		},
		{
			name: "prepared and ready",
			arrange: func(t *testing.T, harness *prepareHarness) {
				t.Helper()
				assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
			},
			wantErr:    nil,
			wantWrites: 1,
		},
		{
			name: "prepared then terminal",
			arrange: func(t *testing.T, harness *prepareHarness) {
				t.Helper()
				assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
				harness.failPerms.Store(true)
				harness.conn.onRefreshTimers(timerIDRefreshPerms)
			},
			wantErr:    ErrPermissionRefreshFailed,
			wantWrites: 0,
		},
		{
			name: "prepared then binding expired",
			arrange: func(t *testing.T, harness *prepareHarness) {
				t.Helper()
				assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
				bound, ok := harness.conn.bindingMgr.getOrCreate(harness.peer)
				require.True(t, ok)
				bound.setRefreshedAt(time.Now().Add(-channelBindingLifetime))
			},
			wantErr:    ErrChannelBindingExpired,
			wantWrites: 0,
		},
		{
			name: "closed",
			arrange: func(t *testing.T, harness *prepareHarness) {
				t.Helper()
				assert.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
				assert.NoError(t, harness.conn.Close())
			},
			wantErr:    net.ErrClosed,
			wantWrites: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			tt.arrange(t, harness)

			writesBefore := harness.writeCount()
			n, err := harness.conn.WriteTo([]byte("payload"), harness.peer)
			writes := harness.writeCount() - writesBefore

			if tt.wantErr == nil {
				assert.NoError(t, err)
				assert.Equal(t, len("payload"), n)
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, 0, n, "a failed write reports zero bytes")
			}
			assert.Equal(t, tt.wantWrites, writes, "network output count")
			if writes > 0 {
				assert.True(t, proto.IsChannelData(harness.lastWrite()),
					"the only datagram a write may emit is ChannelData")
			}
			if tt.wantNoServer {
				assert.Equal(t, int32(0), harness.permCount.Load(),
					"WriteTo must not create a permission")
				assert.Equal(t, int32(0), harness.bindCount.Load(),
					"WriteTo must not start a bind")
			}
		})
	}
}
