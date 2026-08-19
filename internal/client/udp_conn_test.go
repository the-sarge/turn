// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/proto"
)

// timeoutError simulates a transaction failure whose Timeout() is true, like
// an exhausted retransmission budget.
type timeoutError struct {
	msg string
}

func newTimeoutError(msg string) error {
	return &timeoutError{
		msg: msg,
	}
}

func (e *timeoutError) Error() string {
	return e.msg
}

func requireBinding(t *testing.T, mgr *bindingManager, peer netip.AddrPort) *binding {
	t.Helper()

	bound, ok := mgr.getOrCreate(peer)
	require.True(t, ok)

	return bound
}

func (e *timeoutError) Timeout() bool {
	return true
}

func TestNewUDPConnRejectsMissingAbortBeforeStartingWork(t *testing.T) {
	mock := &testConnScript{}
	var conn *UDPConn

	require.Panics(t, func() {
		conn = NewUDPConn(testAllocationConfig(mock), nil)
	})
	if conn != nil {
		_ = conn.Close()
	}
}

func TestNewUDPConnBuildsUnstartedAndClosesOnce(t *testing.T) {
	var releases atomic.Int32
	mock := &testConnScript{
		startTransaction: func(msg *stun.Message) error {
			if msg.Type.Method == stun.MethodRefresh {
				releases.Add(1)
			}

			return nil
		},
	}
	conn := newTestConn(t, mock)

	assert.False(t, conn.refreshAllocTimer.IsRunning())
	assert.False(t, conn.refreshPermsTimer.IsRunning())
	assert.False(t, conn.checkBindingsTimer.IsRunning())
	require.NoError(t, conn.Close())
	assert.Equal(t, int32(1), releases.Load())
	assert.False(t, conn.refreshAllocTimer.IsRunning())
	assert.False(t, conn.refreshPermsTimer.IsRunning())
	assert.False(t, conn.checkBindingsTimer.IsRunning())
}

func TestNewUDPConnStartsTimers(t *testing.T) {
	mock := &testConnScript{
		performTransaction: func(*stun.Message) (*stun.Message, error) {
			return new(stun.Message), nil
		},
	}
	conn := NewUDPConn(testAllocationConfig(mock), func() {})
	t.Cleanup(func() { _ = conn.Close() })

	assert.True(t, conn.refreshAllocTimer.IsRunning())
	assert.True(t, conn.refreshPermsTimer.IsRunning())
	assert.True(t, conn.checkBindingsTimer.IsRunning())
}

func TestUDPConnHandleDataIndicationOwnsQueuedPayload(t *testing.T) {
	conn := UDPConn{
		readCh:  make(chan *inboundData, 1),
		closeCh: make(chan struct{}),
	}
	peer := netip.MustParseAddrPort("192.0.2.10:5000")
	payload := []byte("hello")

	conn.HandleDataIndication(payload, peer)
	payload[0] = 'j'

	buf := make([]byte, len(payload))
	n, from, err := conn.ReadFrom(buf)
	require.NoError(t, err)
	assert.Equal(t, len(payload), n)
	assert.Equal(t, peer, from)
	assert.Equal(t, []byte("hello"), buf)
}

func TestUDPConnHandleDataIndicationSilentlyDiscardsAfterSeal(t *testing.T) {
	conn := UDPConn{
		readCh:  make(chan *inboundData, 1),
		closeCh: make(chan struct{}),
	}
	close(conn.closeCh)

	conn.HandleDataIndication([]byte("late"), netip.MustParseAddrPort("192.0.2.10:5000"))

	assert.Empty(t, conn.readCh)
}

func TestUDPConnHandleChannelDataDeliversAssignedUnpreparedBinding(t *testing.T) {
	peer := netip.MustParseAddrPort("192.0.2.20:6000")
	bindingMgr := newBindingManager()
	bound := requireBinding(t, bindingMgr, peer)
	conn := UDPConn{
		bindingMgr: bindingMgr,
		readCh:     make(chan *inboundData, 1),
		closeCh:    make(chan struct{}),
	}
	payload := []byte("bound")

	handled := conn.HandleChannelData(payload, bound.number)
	payload[0] = 'x'

	assert.True(t, handled)
	assert.ErrorIs(t, bound.writeAccess(time.Now()), ErrNotPrepared,
		"inbound ChannelData must not establish prepared write access")
	buf := make([]byte, len(payload))
	n, from, err := conn.ReadFrom(buf)
	require.NoError(t, err)
	assert.Equal(t, len(payload), n)
	assert.Equal(t, peer, from)
	assert.Equal(t, []byte("bound"), buf)
}

func newInboundDeliveryConn(t *testing.T) *UDPConn {
	t.Helper()

	return newTestConn(t, &testConnScript{
		performTransaction: func(*stun.Message) (*stun.Message, error) {
			return nil, nil
		},
	})
}

func TestUDPConnDecodedDeliveryQueueAndSealDispositions(t *testing.T) {
	peer := netip.MustParseAddrPort("192.0.2.30:7000")
	conn := newInboundDeliveryConn(t)
	bound := requireBinding(t, conn.bindingMgr, peer)

	conn.HandleDataIndication([]byte("before seal"), peer)
	require.Len(t, conn.readCh, 1)
	require.NoError(t, conn.Close())
	assert.Len(t, conn.readCh, 1, "seal must preserve data queued before its linearization point")

	conn.HandleDataIndication([]byte("late indication"), peer)
	assert.True(t, conn.HandleChannelData([]byte("late known channel"), bound.number))
	assert.True(t, conn.HandleChannelData([]byte("late unknown channel"), bound.number+1))
	assert.Len(t, conn.readCh, 1, "delivery after seal must not queue data")
}

func TestUDPConnDecodedDeliveryDropsWhenQueueIsFull(t *testing.T) {
	peer := netip.MustParseAddrPort("192.0.2.40:8000")
	conn := newInboundDeliveryConn(t)
	bound := requireBinding(t, conn.bindingMgr, peer)
	for range cap(conn.readCh) {
		conn.readCh <- &inboundData{data: []byte("existing"), from: peer}
	}

	conn.HandleDataIndication([]byte("dropped indication"), peer)
	assert.True(t, conn.HandleChannelData([]byte("dropped known channel"), bound.number),
		"a known channel remains handled when its payload is dropped")
	assert.False(t, conn.HandleChannelData([]byte("unknown channel"), bound.number+1))
	assert.Len(t, conn.readCh, cap(conn.readCh))
}

func TestUDPConnDecodedDeliveryPreservesShortBufferError(t *testing.T) {
	conn := newInboundDeliveryConn(t)
	conn.HandleDataIndication([]byte("payload"), netip.MustParseAddrPort("192.0.2.50:9000"))

	n, from, err := conn.ReadFrom(make([]byte, 3))

	assert.ErrorIs(t, err, io.ErrShortBuffer)
	assert.Zero(t, n)
	assert.Equal(t, netip.AddrPort{}, from)
}

func assertRequestShape(
	t *testing.T,
	actual *stun.Message,
	wantAttrs []stun.AttrType,
	setters ...stun.Setter,
) {
	t.Helper()

	gotAttrs := make([]stun.AttrType, 0, len(actual.Attributes))
	for _, attr := range actual.Attributes {
		gotAttrs = append(gotAttrs, attr.Type)
	}
	assert.Equal(t, wantAttrs, gotAttrs, "TURN request attribute order")

	expectedSetters := make([]stun.Setter, 0, len(setters)+1)
	expectedSetters = append(expectedSetters, stun.NewTransactionIDSetter(actual.TransactionID))
	expectedSetters = append(expectedSetters, setters...)
	expected := stun.MustBuild(expectedSetters...)
	assert.Equal(t, expected.Raw, actual.Raw, "TURN request normalized to the generated transaction ID")
}

func TestUDPConnReleaseUsesStartTransactionAndPreservesRequest(t *testing.T) {
	startCalls := 0
	config := testAllocationConfig(&testConnScript{
		performTransaction: func(*stun.Message) (*stun.Message, error) {
			require.Fail(t, "release used the waited transaction crossing")

			return nil, errFake
		},
	})
	config.StartTransaction = func(msg *stun.Message) error {
		startCalls++
		assertRequestShape(t, msg, []stun.AttrType{
			stun.AttrLifetime,
			stun.AttrUsername,
			stun.AttrRealm,
			stun.AttrNonce,
			stun.AttrMessageIntegrity,
			stun.AttrFingerprint,
		},
			stun.NewType(stun.MethodRefresh, stun.ClassRequest),
			proto.Lifetime{Duration: 0},
			config.Username,
			config.Realm,
			config.Nonce,
			config.Integrity,
			stun.Fingerprint,
		)

		return nil
	}
	conn := newUDPConn(config, func() {})

	require.NoError(t, conn.Close())
	assert.Equal(t, 1, startCalls)
}

func TestRefreshAllocationPreservesRequestAndRetriesStaleNonce(t *testing.T) {
	const lifetime = time.Hour
	username := stun.NewUsername("user")
	realm := stun.NewRealm("realm")
	integrity := stun.NewShortTermIntegrity("pass")
	oldNonce := stun.NewNonce("nonce")
	freshNonce := stun.NewNonce("fresh-nonce")

	for _, tt := range []struct {
		name       string
		staleFirst bool
		wantCalls  int
	}{
		{name: "ordinary success", wantCalls: 1},
		{name: "438 then success", staleFirst: true, wantCalls: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			script := &testConnScript{
				performTransaction: func(msg *stun.Message) (*stun.Message, error) {
					calls++
					nonce := oldNonce
					if calls > 1 {
						nonce = freshNonce
					}
					assertRequestShape(t, msg, []stun.AttrType{
						stun.AttrLifetime,
						stun.AttrUsername,
						stun.AttrRealm,
						stun.AttrNonce,
						stun.AttrMessageIntegrity,
						stun.AttrFingerprint,
					},
						stun.NewType(stun.MethodRefresh, stun.ClassRequest),
						proto.Lifetime{Duration: lifetime},
						username,
						realm,
						nonce,
						integrity,
						stun.Fingerprint,
					)
					if tt.staleFirst && calls == 1 {
						return stun.MustBuild(
							stun.NewType(stun.MethodRefresh, stun.ClassErrorResponse),
							stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")},
							freshNonce,
						), nil
					}

					return stun.MustBuild(
						stun.NewType(stun.MethodRefresh, stun.ClassSuccessResponse),
						proto.Lifetime{Duration: lifetime},
					), nil
				},
			}
			conn := newTestConn(t, script)

			conn.refreshAllocationWithRetries()

			assert.Equal(t, tt.wantCalls, calls)
			assert.Equal(t, lifetime, conn.lifetime())
			if tt.staleFirst {
				assert.Equal(t, freshNonce, conn.nonce())
			} else {
				assert.Equal(t, oldNonce, conn.nonce())
			}
		})
	}
}

func TestPermissionAndBindingRequestShapes(t *testing.T) {
	username := stun.NewUsername("user")
	realm := stun.NewRealm("realm")
	integrity := stun.NewShortTermIntegrity("pass")
	nonce := stun.NewNonce("nonce")
	peerA := netip.MustParseAddrPort("192.0.2.1:5000")
	peerB := netip.MustParseAddrPort("192.0.2.2:6000")
	var bound *binding
	script := &testConnScript{
		performTransaction: func(msg *stun.Message) (*stun.Message, error) {
			switch msg.Type.Method {
			case stun.MethodCreatePermission:
				assertRequestShape(t, msg, []stun.AttrType{
					stun.AttrXORPeerAddress,
					stun.AttrXORPeerAddress,
					stun.AttrUsername,
					stun.AttrRealm,
					stun.AttrNonce,
					stun.AttrMessageIntegrity,
					stun.AttrFingerprint,
				},
					stun.NewType(stun.MethodCreatePermission, stun.ClassRequest),
					peerAddress(peerA),
					peerAddress(peerB),
					username,
					realm,
					nonce,
					integrity,
					stun.Fingerprint,
				)

				return stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
				), nil
			case stun.MethodChannelBind:
				assertRequestShape(t, msg, []stun.AttrType{
					stun.AttrXORPeerAddress,
					stun.AttrChannelNumber,
					stun.AttrUsername,
					stun.AttrRealm,
					stun.AttrNonce,
					stun.AttrMessageIntegrity,
					stun.AttrFingerprint,
				},
					stun.NewType(stun.MethodChannelBind, stun.ClassRequest),
					peerAddress(peerA),
					proto.ChannelNumber(bound.number),
					username,
					realm,
					nonce,
					integrity,
					stun.Fingerprint,
				)

				return stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
				), nil
			default:
				return nil, errFake
			}
		},
	}
	conn := newTestConn(t, script)
	bound = requireBinding(t, conn.bindingMgr, peerA)

	require.NoError(t, conn.CreatePermissions(peerA, peerB))
	require.NoError(t, conn.bind(bound))
}

func TestUDPConn(t *testing.T) { // nolint:maintidx,cyclop,gocyclo
	makeConn := func(script *testConnScript) *UDPConn {
		return newTestConn(t, script)
	}

	staleNonceMsg := func() *stun.Message {
		return stun.MustBuild(
			stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
			stun.CodeStaleNonce,
			stun.NewNonce("new-nonce-123"),
		)
	}

	badRequestMsg := func() *stun.Message {
		return stun.MustBuild(
			stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
			stun.ErrorCodeAttribute{Code: stun.CodeBadRequest, Reason: []byte("Bad Request")},
		)
	}

	t.Run("maybeBind()", func(t *testing.T) {
		t.Run("fresh success becomes preparable", func(t *testing.T) {
			conn := makeConn(&testConnScript{
				performTransaction: func(*stun.Message) (*stun.Message, error) {
					return new(stun.Message), nil
				},
			})
			bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))

			conn.maybeBind(bound)
			assert.Eventually(t, func() bool {
				bound.muBind.Lock()
				defer bound.muBind.Unlock()

				return bound.attempt == nil
			}, 5*time.Second, 10*time.Millisecond)
			final, err := bound.preparationAccess(time.Now())
			assert.True(t, final)
			assert.NoError(t, err)
		})

		t.Run("recent confirmation does not refresh", func(t *testing.T) {
			var attempts atomic.Int32
			conn := makeConn(&testConnScript{
				performTransaction: func(*stun.Message) (*stun.Message, error) {
					attempts.Add(1)

					return new(stun.Message), nil
				},
			})
			bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))
			confirmBindingAt(t, bound, time.Now())

			conn.maybeBind(bound)
			assert.Equal(t, int32(0), attempts.Load())
		})

		t.Run("stale confirmation refreshes", func(t *testing.T) {
			var attempts atomic.Int32
			conn := makeConn(&testConnScript{
				performTransaction: func(*stun.Message) (*stun.Message, error) {
					attempts.Add(1)

					return new(stun.Message), nil
				},
			})
			bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))
			confirmBindingAt(t, bound, time.Now().Add(-defaultBindingRefreshInterval-time.Minute))

			conn.maybeBind(bound)
			assert.Eventually(t, func() bool { return attempts.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
		})
	})

	t.Run("bind()", func(t *testing.T) {
		tests := []struct {
			name                 string
			transactionFn        func(*stun.Message) (*stun.Message, error)
			expectErr            error
			expectErrContains    string
			expectBadRequest     bool
			expectTurnErrorCode  stun.ErrorCode
			expectBindingDeleted bool
			expectNonceChanged   bool
		}{
			{
				name: "PerformTransaction returns error",
				transactionFn: func(*stun.Message) (*stun.Message, error) {
					return nil, errFake
				},
				expectErr:            errFake,
				expectBindingDeleted: false,
			},
			{
				name: "ErrorResponse with CodeStaleNonce triggers nonce update",
				transactionFn: func(*stun.Message) (*stun.Message, error) {
					return staleNonceMsg(), nil
				},
				expectErr:          errTryAgain,
				expectNonceChanged: true,
			},
			{
				name: "ErrorResponse with error code returns cannot bind channel error",
				transactionFn: func(*stun.Message) (*stun.Message, error) {
					res := stun.MustBuild(
						stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Forbidden")},
					)

					return res, nil
				},
				expectErr:           errCannotBindChannel,
				expectErrContains:   "received error",
				expectTurnErrorCode: stun.CodeForbidden,
			},
			{
				name: "ErrorResponse with CodeBadRequest is detectable",
				transactionFn: func(*stun.Message) (*stun.Message, error) {
					res := stun.MustBuild(
						stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeBadRequest, Reason: []byte("Bad Request")},
					)

					return res, nil
				},
				expectErr:           errCannotBindChannel,
				expectErrContains:   "received error",
				expectBadRequest:    true,
				expectTurnErrorCode: stun.CodeBadRequest,
			},
			{
				name: "ErrorResponse without error code returns unexpected response type error",
				transactionFn: func(*stun.Message) (*stun.Message, error) {
					res := stun.MustBuild(
						stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
					)

					return res, nil
				},
				expectErr:         errCannotBindChannel,
				expectErrContains: "unexpected response type",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				conn := makeConn(&testConnScript{performTransaction: tt.transactionFn})
				bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))

				nonceT0 := conn.nonce()

				err := conn.bind(bound)
				if tt.expectErr == nil {
					assert.NoError(t, err)
				} else {
					assert.ErrorIs(t, err, tt.expectErr)
				}
				if tt.expectErrContains != "" {
					assert.ErrorContains(t, err, tt.expectErrContains)
				}
				assert.Equal(t, tt.expectBadRequest, errors.Is(err, errChannelBindBadRequest))
				var turnErr *stun.TurnError
				if tt.expectTurnErrorCode != 0 {
					require.ErrorAs(t, err, &turnErr)
					assert.Equal(t, tt.expectTurnErrorCode, turnErr.ErrorCodeAttr.Code)
				} else {
					assert.False(t, errors.As(err, &turnErr), "response class must remain untyped")
				}

				if tt.expectBindingDeleted {
					assert.Empty(t, conn.bindingMgr.chanMap)
					assert.Empty(t, conn.bindingMgr.addrMap)
				} else {
					// Binding should remain so we don't re-bind the same peer with a different channel number
					// after a lost/failed ChannelBind transaction.
					assert.NotEmpty(t, conn.bindingMgr.chanMap)
					assert.NotEmpty(t, conn.bindingMgr.addrMap)
					b2, ok := conn.bindingMgr.findByAddr(bound.addr)
					assert.True(t, ok)
					assert.Equal(t, bound.number, b2.number)
				}

				nonceT1 := conn.nonce()
				if tt.expectNonceChanged {
					assert.NotEqual(t, nonceT0, nonceT1, "should change")
					assert.NotEmpty(t, nonceT1, "should be non-empty")
				} else {
					assert.Equal(t, nonceT0, nonceT1, "should remain unchanged")
				}
			})
		}
	})

	t.Run("bindChannel exhausts stale nonce retries without a typed TURN error", func(t *testing.T) {
		var attempts atomic.Int32
		conn := makeConn(&testConnScript{
			performTransaction: func(*stun.Message) (*stun.Message, error) {
				attempts.Add(1)

				return staleNonceMsg(), nil
			},
		})
		bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))

		token, class, started := bound.beginAttempt(time.Now(), defaultBindingRefreshInterval)
		require.True(t, started)
		err := conn.bindChannel(bound, token, class)
		assert.NoError(t, err, "the permanent exhausted-retry cause lives only in readiness")
		assert.Equal(t, int32(maxRetryAttempts), attempts.Load())
		final, readinessErr := bound.preparationAccess(time.Now())
		assert.True(t, final)
		assert.ErrorIs(t, readinessErr, errTryAgain)
		var turnErr *stun.TurnError
		assert.False(t, errors.As(err, &turnErr), "438 retry exhaustion must not become a typed TURN error")
	})

	t.Run("maybeBind() retries unknown binding after transaction failure", func(t *testing.T) {
		var failed atomic.Bool

		conn := makeConn(&testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				if failed.CompareAndSwap(false, true) {
					return nil, errFake
				}

				return new(stun.Message), nil
			},
		})
		bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))
		originalCh := bound.number

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)
		final, err := bound.preparationAccess(time.Now())
		assert.False(t, final)
		assert.NoError(t, err)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			final, err = bound.preparationAccess(time.Now())

			return final && err == nil
		}, 5*time.Second, 10*time.Millisecond)

		b2, ok := conn.bindingMgr.findByAddr(bound.addr)
		assert.True(t, ok)
		assert.Equal(t, originalCh, b2.number)
	})

	t.Run("ChannelBind 400 closes allocation", func(t *testing.T) {
		peerAddr := netip.MustParseAddrPort("127.0.0.1:50000")
		deallocatedCh := make(chan struct{}, 1)
		refreshLifetimeCh := make(chan time.Duration, 1)
		refreshErrCh := make(chan error, 1)

		script := &testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				switch msg.Type.Method {
				case stun.MethodChannelBind:
					return stun.MustBuild(
							stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
							stun.ErrorCodeAttribute{Code: stun.CodeBadRequest, Reason: []byte("Bad Request")},
						),
						nil
				case stun.MethodRefresh:
					return nil, errFake
				default:
					return nil, errFake
				}
			},
			startTransaction: func(msg *stun.Message) error {
				if msg.Type.Method == stun.MethodRefresh {
					var lifetime proto.Lifetime
					if err := lifetime.GetFrom(msg); err != nil {
						refreshErrCh <- err
					} else {
						refreshLifetimeCh <- lifetime.Duration
					}
				}

				return nil
			},
			onDeallocated: func() {
				deallocatedCh <- struct{}{}
			},
		}

		conn := newTestConn(t, script)

		bound := requireBinding(t, conn.bindingMgr, peerAddr)
		conn.maybeBind(bound)

		assert.Eventually(t, func() bool {
			return conn.isClosed()
		}, 5*time.Second, 10*time.Millisecond)

		select {
		case err := <-refreshErrCh:
			assert.NoError(t, err)
		default:
		}
		select {
		case <-deallocatedCh:
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for deallocation callback")
		}

		select {
		case lifetime := <-refreshLifetimeCh:
			assert.Equal(t, time.Duration(0), lifetime)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for refresh deallocation")
		}

		_, err := conn.WriteTo([]byte("still closed"), peerAddr)
		assert.ErrorIs(t, err, net.ErrClosed)
		var turnErr *stun.TurnError
		require.ErrorAs(t, err, &turnErr)
		assert.Equal(t, stun.CodeBadRequest, turnErr.ErrorCodeAttr.Code)

		closeErr := conn.Close()
		require.ErrorAs(t, closeErr, &turnErr)
		assert.Equal(t, stun.CodeBadRequest, turnErr.ErrorCodeAttr.Code)
	})

	t.Run("ChannelBind 400 after unknown binding closes allocation", func(t *testing.T) {
		peerAddr := netip.MustParseAddrPort("127.0.0.1:1234")
		var channelBindAttempts atomic.Int32
		deallocatedCh := make(chan struct{}, 1)

		script := &testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				switch msg.Type.Method {
				case stun.MethodChannelBind:
					if channelBindAttempts.Add(1) == 1 {
						return nil, errFake
					}

					return badRequestMsg(), nil
				default:
					return nil, errFake
				}
			},
			onDeallocated: func() {
				deallocatedCh <- struct{}{}
			},
		}
		conn := newTestConn(t, script)

		bound := requireBinding(t, conn.bindingMgr, peerAddr)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return conn.isClosed()
		}, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, int32(2), channelBindAttempts.Load())

		select {
		case <-deallocatedCh:
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for deallocation callback")
		}

		_, err := conn.WriteTo([]byte("still closed"), peerAddr)
		var turnErr *stun.TurnError
		require.ErrorAs(t, err, &turnErr)
		assert.Equal(t, stun.CodeBadRequest, turnErr.ErrorCodeAttr.Code)
	})

	t.Run("ChannelBind 400 after lost ready refresh keeps saved binding", func(t *testing.T) {
		peerAddr := netip.MustParseAddrPort("127.0.0.1:1234")
		var channelBindAttempts atomic.Int32

		script := &testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				switch msg.Type.Method {
				case stun.MethodChannelBind:
					if channelBindAttempts.Add(1) == 1 {
						return nil, newTimeoutError("channel bind timeout")
					}

					return badRequestMsg(), nil
				default:
					return nil, errFake
				}
			},
		}
		conn := newTestConn(t, script)

		bound := requireBinding(t, conn.bindingMgr, peerAddr)
		staleRefreshedAt := time.Now().Add(-(defaultBindingRefreshInterval + time.Minute))
		confirmBindingAt(t, bound, staleRefreshedAt)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)
		final, err := bound.preparationAccess(time.Now())
		require.True(t, final)
		require.NoError(t, err)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return channelBindAttempts.Load() == 2 && bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)
		_, err = bound.preparationAccess(staleRefreshedAt.Add(channelBindingLifetime))
		assert.ErrorIs(t, err, ErrChannelBindingExpired,
			"recovered 400 must not advance confirmation time")
		assert.False(t, conn.isClosed())
	})

	t.Run("ChannelBind 400 refresh keeps saved binding", func(t *testing.T) {
		staleRefreshedAt := time.Now().Add(-(defaultBindingRefreshInterval + time.Minute))
		var channelBindAttempts atomic.Int32
		conn := makeConn(&testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				channelBindAttempts.Add(1)

				return badRequestMsg(), nil
			},
		})
		bound := requireBinding(t, conn.bindingMgr, netip.MustParseAddrPort("127.0.0.1:1234"))
		confirmBindingAt(t, bound, staleRefreshedAt)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			bound.muBind.Lock()
			defer bound.muBind.Unlock()

			return channelBindAttempts.Load() == 1 && bound.attempt == nil
		}, 5*time.Second, 10*time.Millisecond)
		conn.maybeBind(bound)
		assert.Eventually(t, func() bool { return channelBindAttempts.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
		assert.False(t, conn.isClosed())
	})

	t.Run("WriteTo()", func(t *testing.T) {
		script := &testConnScript{
			performTransaction: func(*stun.Message) (*stun.Message, error) {
				return nil, errFake
			},
			writeTo: func(data []byte) (int, error) {
				return len(data), nil
			},
		}

		addr := netip.MustParseAddrPort("127.0.0.1:1234")

		conn := newTestConn(t, script)
		assert.True(t, conn.permMap.insert(addr, &permission{
			st: permStatePermitted,
		}))

		binding := requireBinding(t, conn.bindingMgr, addr)
		confirmBindingAt(t, binding, time.Now())
		final, err := binding.preparationAccess(time.Now())
		require.True(t, final)
		require.NoError(t, err)

		buf := []byte("Hello")
		n, err := conn.WriteTo(buf, addr)
		assert.NoError(t, err)
		assert.Equal(t, len(buf), n, "WriteTo reports the payload length, not the ChannelData frame length")
	})

	t.Run("ChannelBind transaction failure retains channel number", func(t *testing.T) {
		addr := netip.MustParseAddrPort("127.0.0.1:9999")
		script := &testConnScript{
			performTransaction: func(*stun.Message) (*stun.Message, error) {
				return nil, errFake
			},
			writeTo: func(data []byte) (int, error) {
				return len(data), nil
			},
		}
		conn := newTestConn(t, script)
		assert.True(t, conn.permMap.insert(addr, &permission{st: permStatePermitted}))
		bound := requireBinding(t, conn.bindingMgr, addr)
		originalCh := bound.number

		// A failed bind attempt should not remove the binding: the same peer keeps
		// its channel number, and a write (which fails, unprepared) does not
		// disturb it.
		err := conn.bind(bound)
		assert.ErrorIs(t, err, errFake)

		_, err = conn.WriteTo([]byte("hi"), addr)
		assert.ErrorIs(t, err, ErrNotPrepared)

		b2, ok := conn.bindingMgr.findByAddr(addr)
		assert.True(t, ok)
		assert.Equal(t, originalCh, b2.number)
	})
}

func TestUDPConnBindingCompletionOrdersWithClose(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := &binding{}
	token, _, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
	require.True(t, started)
	conn := &UDPConn{closeCh: make(chan struct{})}
	sealCause := errors.New("self-seal won") //nolint:err113 // Test-local cause.

	conn.closeMutex.Lock()
	result := make(chan struct {
		applied bool
		err     error
	}, 1)
	go func() {
		applied, err := conn.completeBindingAttempt(bound, token, bindingAttemptConfirmed, nil, t0)
		result <- struct {
			applied bool
			err     error
		}{applied: applied, err: err}
	}()
	conn.terminalCause = sealCause
	close(conn.closeCh)
	conn.closeMutex.Unlock()

	got := <-result
	assert.False(t, got.applied)
	assert.ErrorIs(t, got.err, net.ErrClosed)
	assert.ErrorIs(t, got.err, sealCause)
	final, err := bound.preparationAccess(t0)
	assert.False(t, final, "close-winning completion must not create readiness")
	assert.NoError(t, err)
}

func TestUDPConnBindingAttemptResultOwnership(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)

	t.Run("permanent cause lives only in readiness", func(t *testing.T) {
		bound := &binding{}
		token, class, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
		require.True(t, started)
		conn := &UDPConn{closeCh: make(chan struct{})}
		cause := fmt.Errorf("permanent: %w", errCannotBindChannel)

		attemptResult := conn.resolveBindError(bound, token, class, cause)
		assert.NoError(t, attemptResult, "attempt coordination must not duplicate a durable cause")
		final, readinessErr := bound.preparationAccess(t0)
		assert.True(t, final)
		assert.ErrorIs(t, readinessErr, cause)
	})

	t.Run("uncertainty remains attempt-local", func(t *testing.T) {
		bound := &binding{}
		token, class, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
		require.True(t, started)
		conn := &UDPConn{closeCh: make(chan struct{})}
		cause := fmt.Errorf("%w: %w", errChannelBindTransactionFailed, errFake)

		attemptResult := conn.resolveBindError(bound, token, class, cause)
		assert.ErrorIs(t, attemptResult, cause)
		final, readinessErr := bound.preparationAccess(t0)
		assert.False(t, final)
		assert.NoError(t, readinessErr, "transient attempt results must not become durable readiness causes")
	})
}

func TestCreatePermissions(t *testing.T) {
	t.Run("CreatePermissions success", func(t *testing.T) {
		called := false
		script := &testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				called = true
				// Simulate a successful response
				res := stun.New()
				res.Type = stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse)

				return res, nil
			},
		}
		conn := newTestConn(t, script)
		addr := netip.MustParseAddrPort("5.6.7.8:12345")
		err := conn.CreatePermissions(addr)
		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("CreatePermissions error", func(t *testing.T) {
		script := &testConnScript{
			performTransaction: func(msg *stun.Message) (*stun.Message, error) {
				res := stun.New()
				res.Type = stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse)
				code := stun.ErrorCodeAttribute{
					Code:   stun.CodeForbidden,
					Reason: []byte("Forbidden"),
				}
				_ = code.AddTo(res)

				return res, nil
			},
		}
		conn := newTestConn(t, script)
		addr := netip.MustParseAddrPort("5.6.7.8:12345")
		err := conn.CreatePermissions(addr)
		var turnErr *stun.TurnError
		assert.Error(t, err)
		assert.True(t, errors.As(err, &turnErr), "should return a TurnError")
		assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
	})
}
