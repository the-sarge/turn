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
	"testing/synctest"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/internal/proto"
)

// prepareGate lets the test and its cleanup share release ownership.
type prepareGate struct {
	done    <-chan struct{}
	release func()
}

// Register after connection cleanup and before starting the gated worker:
// testing runs cleanups in reverse order, so release precedes Close's join.
func newPrepareGate(t *testing.T) *prepareGate {
	t.Helper()

	done := make(chan struct{})
	gate := &prepareGate{
		done:    done,
		release: sync.OnceFunc(func() { close(done) }),
	}
	t.Cleanup(gate.release)

	return gate
}

// prepareWaiter observes select operand evaluation after PreparePeer has
// captured its attempt. With a gated permission, call 1 is the permission wait
// and call 2 is the binding wait. With permission already ready, call 1 is the
// binding wait. These are the only paths used by the observed test callers.
type prepareWaiter struct {
	context.Context
	waits  atomic.Int32
	result chan error
}

func (w *prepareWaiter) Done() <-chan struct{} {
	w.waits.Add(1)

	return w.Context.Done()
}

func (harness *prepareHarness) startWaiter(ctx context.Context) *prepareWaiter {
	waiter := &prepareWaiter{Context: ctx, result: make(chan error, 1)}
	go func() { waiter.result <- harness.conn.PreparePeer(waiter, harness.peer) }()

	return waiter
}

// Called inside a synctest bubble with the result gate still closed. Wait
// establishes durable blocking; the counter proves this caller reached the
// expected attempt select, rather than merely starting its goroutine.
func requirePrepareWaiting(t *testing.T, waiter *prepareWaiter, waits int32) {
	t.Helper()
	synctest.Wait()
	require.Equal(t, waits, waiter.waits.Load(), "caller must join the intended attempt")
	select {
	case err := <-waiter.result:
		t.Fatalf("PreparePeer returned before the result gate opened: %v", err)
	default:
	}
}

// prepareHarness drives a NewUDPConn against a scripted mock TURN server.
type prepareHarness struct {
	conn       *UDPConn
	script     *testConnScript
	peer       netip.AddrPort
	permCount  atomic.Int32
	bindCount  atomic.Int32
	bindGate   *prepareGate // If non-nil, ChannelBind transactions block on it
	permGate   *prepareGate // If non-nil, CreatePermission transactions block on it
	failPerms  atomic.Bool  // If set, CreatePermission transactions return 403
	staleNonce atomic.Bool  // If set, CreatePermission transactions return 438
}

func newPrepareHarness(t *testing.T, gateBinds bool) *prepareHarness {
	t.Helper()

	harness := &prepareHarness{
		peer: netip.MustParseAddrPort("127.0.0.1:1234"),
	}
	script := &testConnScript{
		performTransaction: func(msg *stun.Message) (*stun.Message, error) {
			switch msg.Type.Method {
			case stun.MethodCreatePermission:
				harness.permCount.Add(1)
				if harness.permGate != nil {
					<-harness.permGate.done
				}
				if harness.failPerms.Load() {
					return stun.MustBuild(
						stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Forbidden")},
					), nil
				}
				if harness.staleNonce.Swap(false) {
					return stun.MustBuild(
						stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")},
						stun.NewNonce("nonce2"),
					), nil
				}

				return stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
				), nil
			case stun.MethodChannelBind:
				harness.bindCount.Add(1)
				if harness.bindGate != nil {
					<-harness.bindGate.done
				}

				return stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
				), nil
			default:
				return nil, errFake
			}
		},
	}

	harness.script = script
	harness.conn = newTestConn(t, script)
	t.Cleanup(func() { _ = harness.conn.Close() })
	if gateBinds {
		harness.bindGate = newPrepareGate(t)
	}

	return harness
}

func (harness *prepareHarness) writeCount() int {
	return harness.script.writeCount()
}

func (harness *prepareHarness) lastWrite() []byte {
	return harness.script.lastWrite()
}

func fillBindingManager(t *testing.T, mgr *bindingManager) {
	t.Helper()

	peerAddr := netip.MustParseAddr("192.0.2.1")
	for i := range maxChannelBindings {
		peer := netip.AddrPortFrom(peerAddr, uint16(i+1))
		_, ok := mgr.getOrCreate(peer)
		require.True(t, ok)
	}
}

func TestPrepareHarnessCleanup(t *testing.T) {
	for _, kind := range []string{"permission", "binding", "ad hoc attempt"} {
		t.Run(kind, func(t *testing.T) {
			for _, manualRelease := range []bool{false, true} {
				name := "cleanup only"
				if manualRelease {
					name = "manual release then cleanup"
				}
				t.Run(name, func(t *testing.T) {
					var harness *prepareHarness
					result := make(chan error, 1)
					if !t.Run("gated transaction", func(t *testing.T) {
						harness = newPrepareHarness(t, kind == "binding")
						gate := harness.bindGate
						count := &harness.bindCount
						switch kind {
						case "permission":
							harness.permGate = newPrepareGate(t)
							gate = harness.permGate
							count = &harness.permCount
						case "ad hoc attempt":
							gate = newPrepareGate(t)
							inner := harness.script.performTransaction
							harness.script.performTransaction = func(msg *stun.Message) (*stun.Message, error) {
								if msg.Type.Method == stun.MethodChannelBind {
									harness.bindCount.Add(1)
									<-gate.done

									return nil, errFake
								}

								return inner(msg)
							}
						}

						go func() { result <- harness.conn.PreparePeer(context.Background(), harness.peer) }()
						require.Eventually(t, func() bool {
							return count.Load() == 1
						}, 5*time.Second, 10*time.Millisecond)
						select {
						case err := <-result:
							t.Fatalf("preparing caller finished before gate release: %v", err)
						default:
						}
						if manualRelease {
							gate.release()
							gate.release()
						}
						// Otherwise return without the normal gate release, as an
						// early prerequisite failure would. Cleanup must join the worker.
					}) {
						return
					}

					require.ErrorIs(t, harness.conn.PreparePeer(context.Background(), harness.peer), net.ErrClosed)
					select {
					case err := <-result:
						// Gate release can finish preparation before Close seals it.
						if kind == "ad hoc attempt" && errors.Is(err, errChannelBindTransactionFailed) {
							return
						}
						if err != nil {
							require.ErrorIs(t, err, net.ErrClosed)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("preparing caller did not finish after harness cleanup")
					}
				})
			}
		})
	}
}

func TestPreparePeer(t *testing.T) {
	t.Run("readiness success then ChannelData writes", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Equal(t, int32(1), harness.permCount.Load())
		assert.Equal(t, int32(1), harness.bindCount.Load())

		n, err := harness.conn.WriteTo([]byte("hello"), harness.peer)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.True(t, proto.IsChannelData(harness.lastWrite()),
			"write after successful PreparePeer must be ChannelData, not Send indication")
	})

	t.Run("stale nonce retry retains successful permission membership", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		harness.staleNonce.Store(true)
		var rejectUnexpectedAttempt atomic.Bool
		var unexpectedAttempts atomic.Int32
		rejectUnexpectedAttempt.Store(true)
		performTransaction := harness.script.performTransaction
		harness.script.performTransaction = func(msg *stun.Message) (*stun.Message, error) {
			if msg.Type.Method == stun.MethodCreatePermission &&
				rejectUnexpectedAttempt.Load() && harness.permCount.Load() >= 2 {
				unexpectedAttempts.Add(1)

				return stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse),
					stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Unexpected retry")},
				), nil
			}

			return performTransaction(msg)
		}

		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Zero(t, unexpectedAttempts.Load(), "successful resolution must prevent another permission attempt")
		rejectUnexpectedAttempt.Store(false)
		assert.Equal(t, int32(2), harness.permCount.Load(), "CreatePermission retries once after stale nonce")

		harness.failPerms.Store(true)
		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Equal(t, int32(2), harness.permCount.Load(), "later preparation reuses the retained permission")

		harness.failPerms.Store(false)
		harness.conn.onRefreshTimers(timerIDRefreshPerms)
		assert.Equal(t, int32(3), harness.permCount.Load(), "retained permission remains a refresh member")
	})

	t.Run("failed shared attempt wakes waiters and the next caller starts fresh", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			harness.permGate = newPrepareGate(t)
			harness.failPerms.Store(true)

			first := harness.startWaiter(context.Background())
			requirePrepareWaiting(t, first, 1)
			require.Equal(t, int32(1), harness.permCount.Load())
			failedPerm := harness.conn.permMap.getOrCreate(harness.peer)
			second := harness.startWaiter(context.Background())
			requirePrepareWaiting(t, second, 1)
			harness.permGate.release()

			for _, waiter := range []*prepareWaiter{first, second} {
				var turnErr *stun.TurnError
				require.ErrorAs(t, <-waiter.result, &turnErr)
				assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
			}
			assert.Equal(t, int32(1), harness.permCount.Load(), "failed attempt is shared")
			assert.Empty(t, harness.conn.permMap.addrs(), "final failure deletes membership before waking waiters")
			nextPerm := harness.conn.permMap.getOrCreate(harness.peer)
			assert.NotSame(t, failedPerm, nextPerm, "the next attempt has fresh permission identity")

			harness.failPerms.Store(false)
			require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
			assert.Equal(t, int32(2), harness.permCount.Load(), "the next caller starts a fresh permission")
		})
	})

	t.Run("closing before worker registration resolves joined waiters", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		perm := harness.conn.permMap.getOrCreate(harness.peer)
		attempt, fresh := perm.beginOrJoin()
		require.True(t, fresh)
		joined, fresh := perm.beginOrJoin()
		assert.Equal(t, attempt, joined)
		assert.False(t, fresh)

		require.NoError(t, harness.conn.Close())
		harness.conn.runPermissionAttempt(perm, harness.peer)
		select {
		case <-attempt.done:
		case <-time.After(2 * time.Second):
			assert.Fail(t, "joined permission waiters did not wake after registration failed")
		}
		permitted, err := perm.readiness()
		assert.False(t, permitted)
		assert.ErrorIs(t, err, net.ErrClosed)
	})

	t.Run("capacity exhaustion follows permission and starts no ChannelBind", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		fillBindingManager(t, harness.conn.bindingMgr)

		bindingsBefore := harness.conn.bindingMgr.all()
		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		require.ErrorIs(t, err, ErrChannelBindFailed)
		assert.Equal(t, int32(1), harness.permCount.Load(), "permission phase remains first")
		assert.Equal(t, int32(0), harness.bindCount.Load(), "exhaustion must not start ChannelBind")
		assert.Len(t, harness.conn.bindingMgr.all(), len(bindingsBefore))
		_, found := harness.conn.bindingMgr.findByAddr(harness.peer)
		assert.False(t, found)
		require.ErrorIs(t, harness.conn.PreparePeer(context.Background(), harness.peer), ErrChannelBindFailed)
		assert.Equal(t, int32(1), harness.permCount.Load(), "confirmed permission is reused after capacity exhaustion")

		harness.failPerms.Store(true)
		rejectedPeer := netip.MustParseAddrPort("192.0.2.2:1234")
		err = harness.conn.PreparePeer(context.Background(), rejectedPeer)
		var turnErr *stun.TurnError
		require.ErrorAs(t, err, &turnErr)
		assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
		require.NotErrorIs(t, err, ErrChannelBindFailed, "permission rejection retains its earlier outcome")
		assert.Equal(t, int32(0), harness.bindCount.Load(), "permission rejection must stop before binding")
		_, found = harness.conn.bindingMgr.findByAddr(rejectedPeer)
		assert.False(t, found)
	})

	t.Run("capacity exhaustion preserves cancellation after permission", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		fillBindingManager(t, harness.conn.bindingMgr)

		harness.conn.bindingMgr.mutex.Lock()
		managerLocked := true
		defer func() {
			if managerLocked {
				harness.conn.bindingMgr.mutex.Unlock()
			}
		}()

		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("caller canceled while capacity was contended") //nolint:err113 // Test-local cause.
		result := make(chan error, 1)
		go func() { result <- harness.conn.PreparePeer(ctx, harness.peer) }()

		require.Eventually(t, func() bool {
			return harness.permCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)
		cancel(cause)
		harness.conn.bindingMgr.mutex.Unlock()
		managerLocked = false

		require.ErrorIs(t, <-result, cause)
		assert.Equal(t, int32(0), harness.bindCount.Load())
		assert.Len(t, harness.conn.bindingMgr.all(), maxChannelBindings)
		_, found := harness.conn.bindingMgr.findByAddr(harness.peer)
		assert.False(t, found)
	})

	t.Run("capacity exhaustion preserves closure after permission", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		fillBindingManager(t, harness.conn.bindingMgr)

		harness.conn.bindingMgr.mutex.Lock()
		managerLocked := true
		defer func() {
			if managerLocked {
				harness.conn.bindingMgr.mutex.Unlock()
			}
		}()

		result := make(chan error, 1)
		go func() { result <- harness.conn.PreparePeer(context.Background(), harness.peer) }()

		require.Eventually(t, func() bool {
			return harness.permCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, harness.conn.Close())
		harness.conn.bindingMgr.mutex.Unlock()
		managerLocked = false

		require.ErrorIs(t, <-result, net.ErrClosed)
		assert.Equal(t, int32(0), harness.bindCount.Load())
		assert.Len(t, harness.conn.bindingMgr.all(), maxChannelBindings)
		_, found := harness.conn.bindingMgr.findByAddr(harness.peer)
		assert.False(t, found)
	})

	t.Run("cancellation and closure retain their pre-binding outcomes", func(t *testing.T) {
		t.Run("cancellation", func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			cause := errors.New("caller canceled before preparation") //nolint:err113 // Test-local cause.
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(cause)

			err := harness.conn.PreparePeer(ctx, harness.peer)
			require.ErrorIs(t, err, cause)
			assert.Equal(t, int32(0), harness.permCount.Load())
			assert.Equal(t, int32(0), harness.bindCount.Load())
		})

		t.Run("closure", func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			require.NoError(t, harness.conn.Close())

			err := harness.conn.PreparePeer(context.Background(), harness.peer)
			require.ErrorIs(t, err, net.ErrClosed)
			assert.Equal(t, int32(0), harness.permCount.Load())
			assert.Equal(t, int32(0), harness.bindCount.Load())
		})
	})

	t.Run("terminal failure survives an in-flight bind success", func(t *testing.T) {
		harness := newPrepareHarness(t, true)

		bound, ok := harness.conn.bindingMgr.getOrCreate(harness.peer)
		require.True(t, ok)
		confirmedAt := time.Now().Add(-defaultBindingRefreshInterval - time.Minute)
		confirmBindingAt(t, bound, confirmedAt)
		final, err := bound.preparationAccess(confirmedAt)
		require.True(t, final)
		require.NoError(t, err)
		harness.conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return harness.bindCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		// Permission loss terminalizes while refresh is in flight, then the late
		// success must not resurrect the binding.
		permissionCause := fmt.Errorf("%w: %w", ErrPermissionRefreshFailed, errFake)
		require.True(t, bound.failPrepared(permissionCause))
		harness.bindGate.release()

		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)

		_, err = harness.conn.WriteTo([]byte("data"), harness.peer)
		assert.ErrorIs(t, err, permissionCause)
	})

	t.Run("same-peer callers coalesce onto one bind", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			harness := newPrepareHarness(t, true)

			harness.permGate = newPrepareGate(t)
			waiters := make([]*prepareWaiter, 4)
			for i := range waiters {
				waiters[i] = harness.startWaiter(context.Background())
			}
			for _, waiter := range waiters {
				requirePrepareWaiting(t, waiter, 1)
			}
			require.Equal(t, int32(1), harness.permCount.Load())
			harness.permGate.release()
			for _, waiter := range waiters {
				requirePrepareWaiting(t, waiter, 2)
			}
			require.Equal(t, int32(1), harness.bindCount.Load())
			harness.bindGate.release()
			for _, waiter := range waiters {
				require.NoError(t, <-waiter.result)
			}
			assert.Equal(t, int32(1), harness.permCount.Load(), "permission transactions should coalesce")
			assert.Equal(t, int32(1), harness.bindCount.Load(), "ChannelBind transactions should coalesce")
		})
	})

	t.Run("cancellation wakes only that waiter", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			harness := newPrepareHarness(t, true)
			require.NoError(t, harness.conn.awaitPermission(context.Background(), harness.peer))

			ctxA, cancelA := context.WithCancelCause(context.Background())
			defer cancelA(nil)
			causeA := errors.New("waiter A gave up") //nolint:err113 // test-local cause

			waiterA := harness.startWaiter(ctxA)
			waiterB := harness.startWaiter(context.Background())
			requirePrepareWaiting(t, waiterA, 1)
			requirePrepareWaiting(t, waiterB, 1)
			require.Equal(t, int32(1), harness.bindCount.Load())

			cancelA(causeA)
			select {
			case err := <-waiterA.result:
				require.ErrorIs(t, err, causeA, "canceled waiter must observe its cause")
			case <-time.After(2 * time.Second):
				assert.Fail(t, "canceled waiter did not wake promptly")
			}

			// Cancellation completed, but the surviving caller is still joined.
			requirePrepareWaiting(t, waiterB, 1)

			harness.bindGate.release()
			select {
			case err := <-waiterB.result:
				require.NoError(t, err, "surviving waiter should complete via the shared bind")
			case <-time.After(5 * time.Second):
				assert.Fail(t, "timed out waiting for surviving waiter")
			}
			assert.Equal(t, int32(1), harness.bindCount.Load(), "cancellation must not restart or cancel the shared bind")
		})
	})

	t.Run("cancellation selected before preparation leaves confirmed peer unprepared", func(t *testing.T) {
		harness := newPrepareHarness(t, true)
		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("caller selected cancellation") //nolint:err113 // Test-local cause.
		result := make(chan error, 1)
		go func() { result <- harness.conn.PreparePeer(ctx, harness.peer) }()

		require.Eventually(t, func() bool {
			return harness.bindCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)
		cancel(cause)
		require.ErrorIs(t, <-result, cause)

		harness.bindGate.release()
		bound, ok := harness.conn.bindingMgr.findByAddr(harness.peer)
		require.True(t, ok)
		require.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)

		_, err := harness.conn.WriteTo([]byte("still unprepared"), harness.peer)
		require.ErrorIs(t, err, ErrNotPrepared)
		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Equal(t, int32(1), harness.bindCount.Load(), "the second waiter observes existing readiness")
	})

	t.Run("cancellation wakes waiter during in-flight permission transaction", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			harness.permGate = newPrepareGate(t)

			waiterA := harness.startWaiter(context.Background())
			requirePrepareWaiting(t, waiterA, 1)
			require.Equal(t, int32(1), harness.permCount.Load())

			ctxB, cancelB := context.WithCancelCause(context.Background())
			defer cancelB(nil)
			waiterB := harness.startWaiter(ctxB)
			requirePrepareWaiting(t, waiterB, 1)

			cause := errors.New("waiter B gave up") //nolint:err113 // test-local cause
			cancelB(cause)
			select {
			case err := <-waiterB.result:
				require.ErrorIs(t, err, cause,
					"waiter must be cancelable while the permission transaction is in flight")
			case <-time.After(2 * time.Second):
				assert.Fail(t, "canceled waiter did not wake during in-flight permission transaction")
			}

			requirePrepareWaiting(t, waiterA, 1)
			harness.permGate.release()
			select {
			case err := <-waiterA.result:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				assert.Fail(t, "timed out waiting for first caller")
			}
			assert.Equal(t, int32(1), harness.permCount.Load(), "permission transactions should coalesce")
		})
	})

	t.Run("permission refresh failure fails writes, never Send indication", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))

		// Simulate the permission-refresh timer firing against a server that
		// now rejects the refresh.
		harness.failPerms.Store(true)
		harness.conn.onRefreshTimers(timerIDRefreshPerms)

		writesBefore := harness.writeCount()
		_, err := harness.conn.WriteTo([]byte("data"), harness.peer)
		require.ErrorIs(t, err, ErrPermissionRefreshFailed)
		assert.Equal(t, writesBefore, harness.writeCount(),
			"failed write for a prepared peer must not emit anything (no Send indication fallback)")

		assert.ErrorIs(t, harness.conn.PreparePeer(context.Background(), harness.peer), ErrPermissionRefreshFailed,
			"readiness must be terminal after permission refresh failure")
	})

	t.Run("permission refresh success keeps prepared binding usable", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
		assert.Equal(t, int32(1), harness.permCount.Load())

		harness.conn.onRefreshTimers(timerIDRefreshPerms)
		assert.Equal(t, int32(2), harness.permCount.Load(),
			"the consolidated receiver must refresh the existing permission")

		n, err := harness.conn.WriteTo([]byte("still ready"), harness.peer)
		require.NoError(t, err)
		assert.Equal(t, len("still ready"), n)
		assert.True(t, proto.IsChannelData(harness.lastWrite()))
	})

	t.Run("bind failure surfaces to preparing caller", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		// First permission succeeds, but every ChannelBind transaction fails.
		mock := harness.script
		inner := mock.performTransaction
		mock.performTransaction = func(msg *stun.Message) (*stun.Message, error) {
			if msg.Type.Method == stun.MethodChannelBind {
				harness.bindCount.Add(1)

				return nil, errFake
			}

			return inner(msg)
		}

		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		require.ErrorIs(t, err, errChannelBindTransactionFailed)
		assert.False(t, harness.conn.isClosed())

		mock.performTransaction = inner
		require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer),
			"a fresh transaction failure is attempt-local and a later caller may retry")
		assert.Equal(t, int32(2), harness.bindCount.Load())
	})

	t.Run("joined bind failure is attempt-local for every waiter", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			harness := newPrepareHarness(t, false)
			require.NoError(t, harness.conn.awaitPermission(context.Background(), harness.peer))
			gate := newPrepareGate(t)
			mock := harness.script
			inner := mock.performTransaction
			mock.performTransaction = func(msg *stun.Message) (*stun.Message, error) {
				if msg.Type.Method == stun.MethodChannelBind {
					harness.bindCount.Add(1)
					<-gate.done

					return nil, errFake
				}

				return inner(msg)
			}

			waiters := []*prepareWaiter{
				harness.startWaiter(context.Background()),
				harness.startWaiter(context.Background()),
			}
			for _, waiter := range waiters {
				requirePrepareWaiting(t, waiter, 1)
			}
			require.Equal(t, int32(1), harness.bindCount.Load())
			gate.release()

			for _, waiter := range waiters {
				require.ErrorIs(t, <-waiter.result, errChannelBindTransactionFailed)
			}
			assert.Equal(t, int32(1), harness.bindCount.Load())

			mock.performTransaction = inner
			require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
			assert.Equal(t, int32(2), harness.bindCount.Load())
		})
	})

	t.Run("server bind rejection surfaces typed TURN error", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		mock := harness.script
		inner := mock.performTransaction
		mock.performTransaction = func(msg *stun.Message) (*stun.Message, error) {
			if msg.Type.Method == stun.MethodChannelBind {
				harness.bindCount.Add(1)

				return stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
					stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Forbidden")},
				), nil
			}

			return inner(msg)
		}

		err := harness.conn.PreparePeer(context.Background(), harness.peer)
		var turnErr *stun.TurnError
		if assert.ErrorAs(t, err, &turnErr) {
			assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
		}
		require.ErrorIs(t, err, errCannotBindChannel)
		assert.False(t, harness.conn.isClosed())
	})

	t.Run("close joins in-flight bind workers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			harness := newPrepareHarness(t, true)
			require.NoError(t, harness.conn.awaitPermission(context.Background(), harness.peer))

			waiter := harness.startWaiter(context.Background())
			requirePrepareWaiting(t, waiter, 1)
			require.Equal(t, int32(1), harness.bindCount.Load())

			closeResult := make(chan error, 1)
			go func() { closeResult <- harness.conn.Close() }()

			// The waiter unblocks promptly; Close must keep waiting for the worker.
			select {
			case err := <-waiter.result:
				require.ErrorIs(t, err, net.ErrClosed)
			case <-time.After(2 * time.Second):
				assert.Fail(t, "PreparePeer waiter did not unblock on close")
			}
			synctest.Wait()
			select {
			case <-closeResult:
				assert.Fail(t, "Close returned while a bind worker was still in flight")
			default:
			}

			harness.bindGate.release()
			select {
			case err := <-closeResult:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				assert.Fail(t, "Close did not return after the bind worker finished")
			}
			bound, ok := harness.conn.bindingMgr.findByAddr(harness.peer)
			require.True(t, ok)
			final, readinessErr := bound.preparationAccess(time.Now())
			assert.False(t, final, "an attempt completing after Allocation close creates no readiness outcome")
			assert.NoError(t, readinessErr)
		})
	})

	t.Run("attempt in flight during self-seal records the terminal cause", func(t *testing.T) {
		harness := newPrepareHarness(t, false)
		harness.permGate = newPrepareGate(t)
		harness.staleNonce.Store(true)

		// A permission attempt is mid-transaction when the allocation seals
		// itself. The stale-nonce reply sends the worker around its retry loop,
		// where it observes the seal; the result it records for any waiter that
		// joined the attempt must carry the recorded terminal cause, not bare
		// net.ErrClosed.
		perm := harness.conn.permMap.getOrCreate(harness.peer)
		attempt, fresh := perm.beginOrJoin()
		require.True(t, fresh)
		require.True(t, harness.conn.startPermissionAttempt(perm, harness.peer))
		assert.Eventually(t, func() bool {
			return harness.permCount.Load() == 1
		}, 5*time.Second, 10*time.Millisecond)

		harness.conn.startClose(errFake)
		harness.permGate.release()
		select {
		case <-attempt.done:
		case <-time.After(5 * time.Second):
			assert.Fail(t, "permission attempt did not finish after the seal")
		}

		permitted, err := perm.readiness()
		assert.False(t, permitted)
		require.ErrorIs(t, err, net.ErrClosed)
		require.ErrorIs(t, err, errFake,
			"an in-flight attempt finishing after the seal must record the terminal cause")
		assert.Equal(t, []netip.AddrPort{harness.peer}, harness.conn.permMap.addrs(),
			"seal precedence retains permission membership")
	})

	t.Run("re-entry after binding expiry is terminal", func(t *testing.T) {
		harness := newPrepareHarness(t, false)

		bound, ok := harness.conn.bindingMgr.getOrCreate(harness.peer)
		require.True(t, ok)
		confirmedAt := time.Now().Add(-channelBindingLifetime)
		confirmBindingAt(t, bound, confirmedAt)
		final, err := bound.preparationAccess(confirmedAt)
		require.True(t, final)
		require.NoError(t, err)

		err = harness.conn.PreparePeer(context.Background(), harness.peer)
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
				require.NoError(t, harness.conn.PreparePeer(context.Background(), harness.peer))
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
				bound, ok := harness.conn.bindingMgr.getOrCreate(harness.peer)
				require.True(t, ok)
				confirmedAt := time.Now().Add(-channelBindingLifetime)
				confirmBindingAt(t, bound, confirmedAt)
				final, err := bound.preparationAccess(confirmedAt)
				require.True(t, final)
				require.NoError(t, err)
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
				require.NoError(t, err)
				assert.Equal(t, len("payload"), n)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
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
