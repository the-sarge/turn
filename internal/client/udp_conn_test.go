// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"errors"
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
	mock := &mockClient{}
	var conn *UDPConn

	require.Panics(t, func() {
		conn = NewUDPConn(&AllocationConfig{
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
		}, nil)
	})
	if conn != nil {
		_ = conn.Close()
	}
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

func TestRefreshAllocationPreservesRequestAndRetriesStaleNonce(t *testing.T) {
	const lifetime = 10 * time.Minute
	username := stun.NewUsername("user")
	realm := stun.NewRealm("realm")
	integrity := stun.NewShortTermIntegrity("pass")
	oldNonce := stun.NewNonce("old-nonce")
	freshNonce := stun.NewNonce("fresh-nonce")
	serverAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478}

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
			mock := &mockClient{
				performTransaction: func(msg *stun.Message, _ net.Addr, dontWait bool) (TransactionResult, error) {
					calls++
					assert.False(t, dontWait)
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
						return TransactionResult{Msg: stun.MustBuild(
							stun.NewType(stun.MethodRefresh, stun.ClassErrorResponse),
							stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")},
							freshNonce,
						)}, nil
					}

					return TransactionResult{Msg: stun.MustBuild(
						stun.NewType(stun.MethodRefresh, stun.ClassSuccessResponse),
						proto.Lifetime{Duration: lifetime},
					)}, nil
				},
			}
			conn := UDPConn{
				serverAddr: serverAddr,
				username:   username,
				realm:      realm,
				integrity:  integrity,
				_nonce:     oldNonce,
				_lifetime:  lifetime,
			}
			mock.configure(&conn)

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
	serverAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478}
	peerA := netip.MustParseAddrPort("192.0.2.1:5000")
	peerB := netip.MustParseAddrPort("192.0.2.2:6000")
	bindingMgr := newBindingManager()
	bound := requireBinding(t, bindingMgr, peerA)

	mock := &mockClient{
		performTransaction: func(msg *stun.Message, _ net.Addr, dontWait bool) (TransactionResult, error) {
			assert.False(t, dontWait)
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

				return TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
				)}, nil
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

				return TransactionResult{Msg: stun.MustBuild(
					stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
				)}, nil
			default:
				return TransactionResult{}, errFake
			}
		},
	}
	conn := UDPConn{
		serverAddr: serverAddr,
		username:   username,
		realm:      realm,
		integrity:  integrity,
		_nonce:     nonce,
		bindingMgr: bindingMgr,
	}
	mock.configure(&conn)

	require.NoError(t, conn.CreatePermissions(peerA, peerB))
	require.NoError(t, conn.bind(bound))
}

func TestUDPConn(t *testing.T) { // nolint:maintidx,cyclop,gocyclo
	makeConn := func(client *mockClient, bm *bindingManager) *UDPConn {
		conn := UDPConn{
			bindingMgr:             bm,
			bindingRefreshInterval: defaultBindingRefreshInterval,
		}
		client.configure(&conn)

		return &conn
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
		tests := []struct {
			name          string
			initialState  bindingState
			interimState  bindingState
			finalState    bindingState
			pastInterval  bool
			shouldSucceed bool
		}{
			{"idle -> request -> ready", bindingStateIdle, bindingStateRequest, bindingStateReady, false, true},
			{"idle -> request -> failed", bindingStateIdle, bindingStateRequest, bindingStateFailed, false, false},
			{"unknown -> request -> ready", bindingStateUnknown, bindingStateRequest, bindingStateReady, false, true},
			{"ready unknown -> refresh -> ready", bindingStateReadyUnknown, bindingStateRefresh, bindingStateReady, false, true},
			{"ready (stale) -> refresh -> ready", bindingStateReady, bindingStateRefresh, bindingStateReady, true, true},
			{"ready (stale) -> refresh -> failed", bindingStateReady, bindingStateRefresh, bindingStateFailed, true, false},

			// Noop cases:
			{"ready (noop)", bindingStateReady, bindingStateReady, bindingStateReady, false, true},
			{"request (noop)", bindingStateRequest, bindingStateRequest, bindingStateRequest, false, true},
			{"refresh (noop)", bindingStateRefresh, bindingStateRefresh, bindingStateRefresh, false, true},
			{"failed (noop)", bindingStateFailed, bindingStateFailed, bindingStateFailed, false, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				unblock := make(chan struct{})

				bm := newBindingManager()
				bound := requireBinding(t, bm, netip.MustParseAddrPort("127.0.0.1:1234"))
				conn := makeConn(&mockClient{
					performTransaction: func(msg *stun.Message, addr net.Addr, dontWait bool) (TransactionResult, error) {
						<-unblock
						if tt.shouldSucceed {
							return TransactionResult{Msg: new(stun.Message)}, nil
						}

						return TransactionResult{Msg: staleNonceMsg()}, nil
					},
				}, bm)

				bound.setState(tt.initialState)
				if tt.pastInterval {
					bound.setRefreshedAt(time.Now().Add(-(defaultBindingRefreshInterval + 1*time.Minute)))
				}

				conn.maybeBind(bound)
				assert.Equal(t, tt.interimState, bound.state())

				// Release barrier so inner bind() can move forward.
				close(unblock)

				assert.Eventually(t, func() bool {
					return bound.state() == tt.finalState
				}, 5*time.Second, 10*time.Millisecond)
			})
		}
	})

	t.Run("bind()", func(t *testing.T) {
		tests := []struct {
			name                 string
			transactionFn        func(*stun.Message, net.Addr, bool) (TransactionResult, error)
			expectErr            error
			expectErrContains    string
			expectBadRequest     bool
			expectTurnErrorCode  stun.ErrorCode
			expectBindingDeleted bool
			expectNonceChanged   bool
		}{
			{
				name: "PerformTransaction returns error",
				transactionFn: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
					return TransactionResult{}, errFake
				},
				expectErr:            errFake,
				expectBindingDeleted: false,
			},
			{
				name: "ErrorResponse with CodeStaleNonce triggers nonce update",
				transactionFn: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
					return TransactionResult{Msg: staleNonceMsg()}, nil
				},
				expectErr:          errTryAgain,
				expectNonceChanged: true,
			},
			{
				name: "ErrorResponse with error code returns cannot bind channel error",
				transactionFn: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
					res := stun.MustBuild(
						stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeForbidden, Reason: []byte("Forbidden")},
					)

					return TransactionResult{Msg: res}, nil
				},
				expectErr:           errCannotBindChannel,
				expectErrContains:   "received error",
				expectTurnErrorCode: stun.CodeForbidden,
			},
			{
				name: "ErrorResponse with CodeBadRequest is detectable",
				transactionFn: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
					res := stun.MustBuild(
						stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
						stun.ErrorCodeAttribute{Code: stun.CodeBadRequest, Reason: []byte("Bad Request")},
					)

					return TransactionResult{Msg: res}, nil
				},
				expectErr:           errCannotBindChannel,
				expectErrContains:   "received error",
				expectBadRequest:    true,
				expectTurnErrorCode: stun.CodeBadRequest,
			},
			{
				name: "ErrorResponse without error code returns unexpected response type error",
				transactionFn: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
					res := stun.MustBuild(
						stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
					)

					return TransactionResult{Msg: res}, nil
				},
				expectErr:         errCannotBindChannel,
				expectErrContains: "unexpected response type",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				bm := newBindingManager()
				bound := requireBinding(t, bm, netip.MustParseAddrPort("127.0.0.1:1234"))
				conn := makeConn(&mockClient{performTransaction: tt.transactionFn}, bm)

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
					assert.Empty(t, bm.chanMap)
					assert.Empty(t, bm.addrMap)
				} else {
					// Binding should remain so we don't re-bind the same peer with a different channel number
					// after a lost/failed ChannelBind transaction.
					assert.NotEmpty(t, bm.chanMap)
					assert.NotEmpty(t, bm.addrMap)
					b2, ok := bm.findByAddr(bound.addr)
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
		bm := newBindingManager()
		bound := requireBinding(t, bm, netip.MustParseAddrPort("127.0.0.1:1234"))
		var attempts atomic.Int32
		conn := makeConn(&mockClient{
			performTransaction: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
				attempts.Add(1)

				return TransactionResult{Msg: staleNonceMsg()}, nil
			},
		}, bm)

		err := conn.bindChannel(bound, bindingStateIdle)
		assert.ErrorIs(t, err, errTryAgain)
		assert.Equal(t, int32(maxRetryAttempts), attempts.Load())
		assert.Equal(t, bindingStateFailed, bound.state())
		var turnErr *stun.TurnError
		assert.False(t, errors.As(err, &turnErr), "438 retry exhaustion must not become a typed TURN error")
	})

	t.Run("maybeBind() retries unknown binding after transaction failure", func(t *testing.T) {
		var failed atomic.Bool

		bm := newBindingManager()
		bound := requireBinding(t, bm, netip.MustParseAddrPort("127.0.0.1:1234"))
		originalCh := bound.number
		conn := makeConn(&mockClient{
			performTransaction: func(msg *stun.Message, addr net.Addr, dontWait bool) (TransactionResult, error) {
				if failed.CompareAndSwap(false, true) {
					return TransactionResult{}, errFake
				}

				return TransactionResult{Msg: new(stun.Message)}, nil
			},
		}, bm)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return bound.state() == bindingStateUnknown
		}, 5*time.Second, 10*time.Millisecond)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return bound.state() == bindingStateReady
		}, 5*time.Second, 10*time.Millisecond)

		b2, ok := bm.findByAddr(bound.addr)
		assert.True(t, ok)
		assert.Equal(t, originalCh, b2.number)
	})

	t.Run("ChannelBind 400 closes allocation", func(t *testing.T) {
		relayedAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}
		peerAddr := netip.MustParseAddrPort("127.0.0.1:50000")
		serverAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478}

		deallocatedCh := make(chan net.Addr, 1)
		refreshLifetimeCh := make(chan time.Duration, 1)
		refreshDontWaitCh := make(chan bool, 1)
		refreshErrCh := make(chan error, 1)

		client := &mockClient{
			performTransaction: func(msg *stun.Message, _ net.Addr, dontWait bool) (TransactionResult, error) {
				switch msg.Type.Method {
				case stun.MethodChannelBind:
					return TransactionResult{
						Msg: stun.MustBuild(
							stun.NewType(stun.MethodChannelBind, stun.ClassErrorResponse),
							stun.ErrorCodeAttribute{Code: stun.CodeBadRequest, Reason: []byte("Bad Request")},
						),
					}, nil
				case stun.MethodRefresh:
					var lifetime proto.Lifetime
					if err := lifetime.GetFrom(msg); err != nil {
						refreshErrCh <- err
					} else {
						refreshLifetimeCh <- lifetime.Duration
						refreshDontWaitCh <- dontWait
					}

					return TransactionResult{}, nil
				default:
					return TransactionResult{}, errFake
				}
			},
			onDeallocated: func(addr net.Addr) {
				deallocatedCh <- addr
			},
		}

		conn := NewUDPConn(&AllocationConfig{
			WriteTo:            client.WriteTo,
			PerformTransaction: client.PerformTransaction,
			OnDeallocated:      client.OnDeallocated,
			RelayedAddr:        relayedAddr,
			ServerAddr:         serverAddr,
			Username:           stun.NewUsername("user"),
			Realm:              stun.NewRealm("realm"),
			Integrity:          stun.NewShortTermIntegrity("pass"),
			Nonce:              stun.NewNonce("nonce"),
			Lifetime:           time.Hour,
		}, func() {})
		defer func() { _ = conn.Close() }()

		bound := requireBinding(t, conn.bindingMgr, peerAddr)
		conn.maybeBind(bound)

		assert.Eventually(t, func() bool {
			return bound.state() == bindingStateFailed && conn.isClosed()
		}, 5*time.Second, 10*time.Millisecond)

		select {
		case err := <-refreshErrCh:
			assert.NoError(t, err)
		default:
		}
		select {
		case deallocatedAddr := <-deallocatedCh:
			assert.Equal(t, relayedAddr, deallocatedAddr)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for deallocation callback")
		}

		select {
		case lifetime := <-refreshLifetimeCh:
			assert.Equal(t, time.Duration(0), lifetime)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for refresh deallocation")
		}

		select {
		case dontWait := <-refreshDontWaitCh:
			assert.True(t, dontWait)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for refresh dontWait flag")
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
		relayedAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}
		peerAddr := netip.MustParseAddrPort("127.0.0.1:1234")
		serverAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478}

		var channelBindAttempts atomic.Int32
		deallocatedCh := make(chan net.Addr, 1)

		client := &mockClient{
			performTransaction: func(msg *stun.Message, addr net.Addr, dontWait bool) (TransactionResult, error) {
				switch msg.Type.Method {
				case stun.MethodChannelBind:
					if channelBindAttempts.Add(1) == 1 {
						return TransactionResult{}, errFake
					}

					return TransactionResult{Msg: badRequestMsg()}, nil
				case stun.MethodRefresh:
					return TransactionResult{}, nil
				default:
					return TransactionResult{}, errFake
				}
			},
			onDeallocated: func(addr net.Addr) {
				deallocatedCh <- addr
			},
		}
		conn := NewUDPConn(&AllocationConfig{
			WriteTo:            client.WriteTo,
			PerformTransaction: client.PerformTransaction,
			OnDeallocated:      client.OnDeallocated,
			RelayedAddr:        relayedAddr,
			ServerAddr:         serverAddr,
			Username:           stun.NewUsername("user"),
			Realm:              stun.NewRealm("realm"),
			Integrity:          stun.NewShortTermIntegrity("pass"),
			Nonce:              stun.NewNonce("nonce"),
			Lifetime:           time.Hour,
		}, func() {})
		defer func() { _ = conn.Close() }()

		bound := requireBinding(t, conn.bindingMgr, peerAddr)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return bound.state() == bindingStateUnknown
		}, 5*time.Second, 10*time.Millisecond)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return bound.state() == bindingStateFailed && conn.isClosed()
		}, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, int32(2), channelBindAttempts.Load())

		select {
		case deallocatedAddr := <-deallocatedCh:
			assert.Equal(t, relayedAddr, deallocatedAddr)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for deallocation callback")
		}

		_, err := conn.WriteTo([]byte("still closed"), peerAddr)
		var turnErr *stun.TurnError
		require.ErrorAs(t, err, &turnErr)
		assert.Equal(t, stun.CodeBadRequest, turnErr.ErrorCodeAttr.Code)
	})

	t.Run("ChannelBind 400 after lost ready refresh keeps saved binding", func(t *testing.T) {
		relayedAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}
		peerAddr := netip.MustParseAddrPort("127.0.0.1:1234")
		serverAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478}

		var channelBindAttempts atomic.Int32

		client := &mockClient{
			performTransaction: func(msg *stun.Message, addr net.Addr, dontWait bool) (TransactionResult, error) {
				switch msg.Type.Method {
				case stun.MethodChannelBind:
					if channelBindAttempts.Add(1) == 1 {
						return TransactionResult{}, newTimeoutError("channel bind timeout")
					}

					return TransactionResult{Msg: badRequestMsg()}, nil
				case stun.MethodRefresh:
					return TransactionResult{}, nil
				default:
					return TransactionResult{}, errFake
				}
			},
		}
		conn := NewUDPConn(&AllocationConfig{
			WriteTo:            client.WriteTo,
			PerformTransaction: client.PerformTransaction,
			OnDeallocated:      client.OnDeallocated,
			RelayedAddr:        relayedAddr,
			ServerAddr:         serverAddr,
			Username:           stun.NewUsername("user"),
			Realm:              stun.NewRealm("realm"),
			Integrity:          stun.NewShortTermIntegrity("pass"),
			Nonce:              stun.NewNonce("nonce"),
			Lifetime:           time.Hour,
		}, func() {})
		defer func() { _ = conn.Close() }()

		bound := requireBinding(t, conn.bindingMgr, peerAddr)
		staleRefreshedAt := time.Now().Add(-(defaultBindingRefreshInterval + time.Minute))
		bound.setState(bindingStateReady)
		bound.setRefreshedAt(staleRefreshedAt)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return bound.state() == bindingStateReadyUnknown
		}, 5*time.Second, 10*time.Millisecond)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return channelBindAttempts.Load() == 2 && bound.state() == bindingStateReady
		}, 5*time.Second, 10*time.Millisecond)
		assert.True(t, bound.refreshedAt().Equal(staleRefreshedAt))
		assert.False(t, conn.isClosed())
	})

	t.Run("ChannelBind 400 refresh keeps saved binding", func(t *testing.T) {
		bm := newBindingManager()
		bound := requireBinding(t, bm, netip.MustParseAddrPort("127.0.0.1:1234"))
		staleRefreshedAt := time.Now().Add(-(defaultBindingRefreshInterval + time.Minute))
		var channelBindAttempts atomic.Int32
		bound.setState(bindingStateReady)
		bound.setRefreshedAt(staleRefreshedAt)
		conn := makeConn(&mockClient{
			performTransaction: func(msg *stun.Message, addr net.Addr, dontWait bool) (TransactionResult, error) {
				channelBindAttempts.Add(1)

				return TransactionResult{Msg: badRequestMsg()}, nil
			},
		}, bm)

		conn.maybeBind(bound)
		assert.Eventually(t, func() bool {
			return channelBindAttempts.Load() == 1 && bound.state() == bindingStateReady
		}, 5*time.Second, 10*time.Millisecond)
		assert.True(t, bound.refreshedAt().Equal(staleRefreshedAt))
		startState, ok := conn.startBinding(bound)
		assert.True(t, ok, "recovered binding should remain eligible for refresh")
		assert.Equal(t, bindingStateReady, startState)
		assert.Equal(t, bindingStateRefresh, bound.state())
		assert.False(t, conn.isClosed())
	})

	t.Run("WriteTo()", func(t *testing.T) {
		client := &mockClient{
			performTransaction: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
				return TransactionResult{}, errFake
			},
			writeTo: func(data []byte, _ net.Addr) (int, error) {
				return len(data), nil
			},
		}

		addr := netip.MustParseAddrPort("127.0.0.1:1234")

		pm := newPermissionMap()
		assert.True(t, pm.insert(addr, &permission{
			st: permStatePermitted,
		}))

		bm := newBindingManager()
		binding := requireBinding(t, bm, addr)
		binding.setState(bindingStateReady)
		binding.setRefreshedAt(time.Now())
		binding.prepared.Store(true)

		conn := UDPConn{
			permMap:    pm,
			bindingMgr: bm,
		}
		client.configure(&conn)

		buf := []byte("Hello")
		n, err := conn.WriteTo(buf, addr)
		assert.NoError(t, err)
		assert.Equal(t, len(buf), n, "WriteTo reports the payload length, not the ChannelData frame length")
	})

	t.Run("ChannelBind transaction failure retains channel number", func(t *testing.T) {
		addr := netip.MustParseAddrPort("127.0.0.1:9999")
		serverAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 3478}

		pm := newPermissionMap()
		assert.True(t, pm.insert(addr, &permission{st: permStatePermitted}))

		bm := newBindingManager()
		bound := requireBinding(t, bm, addr)
		originalCh := bound.number

		client := &mockClient{
			performTransaction: func(*stun.Message, net.Addr, bool) (TransactionResult, error) {
				return TransactionResult{}, errFake
			},
			writeTo: func(data []byte, _ net.Addr) (int, error) {
				return len(data), nil
			},
		}

		conn := UDPConn{
			serverAddr: serverAddr,
			permMap:    pm,
			username:   stun.NewUsername("user"),
			realm:      stun.NewRealm("realm"),
			integrity:  stun.NewShortTermIntegrity("pass"),
			_nonce:     stun.NewNonce("nonce"),
			bindingMgr: bm,
		}
		client.configure(&conn)

		// A failed bind attempt should not remove the binding: the same peer keeps
		// its channel number, and a write (which fails, unprepared) does not
		// disturb it.
		err := conn.bind(bound)
		assert.ErrorIs(t, err, errFake)

		_, err = conn.WriteTo([]byte("hi"), addr)
		assert.ErrorIs(t, err, ErrNotPrepared)

		b2, ok := bm.findByAddr(addr)
		assert.True(t, ok)
		assert.Equal(t, originalCh, b2.number)
	})
}

func TestCreatePermissions(t *testing.T) {
	t.Run("CreatePermissions success", func(t *testing.T) {
		called := false
		client := &mockClient{
			performTransaction: func(msg *stun.Message, addr net.Addr, _ bool) (TransactionResult, error) {
				called = true
				// Simulate a successful response
				res := stun.New()
				res.Type = stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse)

				return TransactionResult{Msg: res}, nil
			},
		}
		conn := &UDPConn{
			serverAddr: &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 3478},
			username:   stun.NewUsername("user"),
			realm:      stun.NewRealm("realm"),
			integrity:  stun.NewShortTermIntegrity("pass"),
			_nonce:     stun.NewNonce("nonce"),
		}
		client.configure(conn)
		addr := netip.MustParseAddrPort("5.6.7.8:12345")
		err := conn.CreatePermissions(addr)
		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("CreatePermissions error", func(t *testing.T) {
		client := &mockClient{
			performTransaction: func(msg *stun.Message, addr net.Addr, _ bool) (TransactionResult, error) {
				res := stun.New()
				res.Type = stun.NewType(stun.MethodCreatePermission, stun.ClassErrorResponse)
				code := stun.ErrorCodeAttribute{
					Code:   stun.CodeForbidden,
					Reason: []byte("Forbidden"),
				}
				_ = code.AddTo(res)

				return TransactionResult{Msg: res}, nil
			},
		}
		conn := &UDPConn{
			serverAddr: &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 3478},
			username:   stun.NewUsername("user"),
			realm:      stun.NewRealm("realm"),
			integrity:  stun.NewShortTermIntegrity("pass"),
			_nonce:     stun.NewNonce("nonce"),
		}
		client.configure(conn)
		addr := netip.MustParseAddrPort("5.6.7.8:12345")
		err := conn.CreatePermissions(addr)
		var turnErr *stun.TurnError
		assert.Error(t, err)
		assert.True(t, errors.As(err, &turnErr), "should return a TurnError")
		assert.Equal(t, stun.CodeForbidden, turnErr.ErrorCodeAttr.Code)
	})
}
