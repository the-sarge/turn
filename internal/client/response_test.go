// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/internal/proto"
)

func TestTransactionRegistryRejectsForgedResponses(t *testing.T) {
	key := stun.NewLongTermIntegrity("user", "realm", "password")
	for _, method := range []stun.Method{
		stun.MethodAllocate, stun.MethodRefresh, stun.MethodCreatePermission, stun.MethodChannelBind,
	} {
		t.Run(method.String(), func(t *testing.T) {
			for _, kind := range []string{"wrong method", "request", "indication", "unsigned", "bad integrity", "short integrity"} {
				t.Run(kind, func(t *testing.T) {
					request := stun.MustBuild(stun.TransactionID, stun.NewType(method, stun.ClassRequest), key)
					registry := NewTransactionRegistry(func(raw []byte) (int, error) { return len(raw), nil }, time.Hour)
					t.Cleanup(registry.AbortCurrent)
					entry, err := registry.begin(request, key, make(chan transactionResult, 1))
					require.NoError(t, err)

					responseType := stun.NewType(method, stun.ClassSuccessResponse)
					switch kind {
					case "wrong method":
						responseType.Method = stun.MethodBinding
					case "request":
						responseType.Class = stun.ClassRequest
					case "indication":
						responseType.Class = stun.ClassIndication
					}
					forged := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), responseType)
					switch kind {
					case "unsigned":
					case "short integrity":
						forged.Add(stun.AttrMessageIntegrity, []byte{0})
					case "bad integrity":
						require.NoError(t, stun.NewShortTermIntegrity("wrong key").AddTo(forged))
					default:
						require.NoError(t, key.AddTo(forged))
					}
					registry.Complete(forged)
					require.Same(t, entry, registry.live[request.TransactionID], "forgery must leave the legitimate transaction live")
					assert.Empty(t, entry.resultCh, "forgery must not publish a result")

					valid := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
						stun.NewType(method, stun.ClassSuccessResponse), key, stun.Fingerprint)
					registry.Complete(valid)
					assert.Same(t, valid, (<-entry.resultCh).msg)
					assert.Empty(t, registry.live)
				})
			}
		})
	}
}

func TestTransactionRegistryAllocateChallenge(t *testing.T) {
	for _, kind := range []string{"wrong method", "success", "wrong code", "missing realm", "empty realm", "missing nonce", "empty nonce"} {
		t.Run(kind, func(t *testing.T) {
			request := stun.MustBuild(stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest))
			registry := NewTransactionRegistry(func(raw []byte) (int, error) { return len(raw), nil }, time.Hour)
			t.Cleanup(registry.AbortCurrent)
			entry, err := registry.begin(request, nil, make(chan transactionResult, 1))
			require.NoError(t, err)
			responseType := stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse)
			code := stun.CodeUnauthorized
			if kind == "wrong method" {
				responseType.Method = stun.MethodRefresh
			}
			if kind == "success" {
				responseType.Class = stun.ClassSuccessResponse
			}
			if kind == "wrong code" {
				code = stun.CodeStaleNonce
			}
			forged := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), responseType,
				stun.ErrorCodeAttribute{Code: code})
			if kind != "missing realm" {
				realm := "realm"
				if kind == "empty realm" {
					realm = ""
				}
				require.NoError(t, stun.NewRealm(realm).AddTo(forged))
			}
			if kind != "missing nonce" {
				nonce := "nonce"
				if kind == "empty nonce" {
					nonce = ""
				}
				require.NoError(t, stun.NewNonce(nonce).AddTo(forged))
			}
			registry.Complete(forged)
			require.Same(t, entry, registry.live[request.TransactionID])
			valid := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
				stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse),
				stun.ErrorCodeAttribute{Code: stun.CodeUnauthorized}, stun.NewRealm("realm"), stun.NewNonce("nonce"))
			registry.Complete(valid)
			assert.Same(t, valid, (<-entry.resultCh).msg)
		})
	}
}

func TestTransactionRegistryStaleNonceRequiresAuthentication(t *testing.T) {
	key := stun.NewLongTermIntegrity("user", "realm", "password")
	for _, method := range []stun.Method{
		stun.MethodAllocate, stun.MethodRefresh, stun.MethodCreatePermission, stun.MethodChannelBind,
	} {
		t.Run(method.String(), func(t *testing.T) {
			for _, kind := range []string{"unsigned", "bad integrity", "wrong realm", "missing realm", "missing nonce", "empty nonce", "nonce after integrity"} {
				t.Run(kind, func(t *testing.T) {
					request := stun.MustBuild(stun.TransactionID, stun.NewType(method, stun.ClassRequest),
						stun.NewRealm("realm"), stun.NewNonce("old nonce"), key)
					registry := NewTransactionRegistry(func(raw []byte) (int, error) { return len(raw), nil }, time.Hour)
					t.Cleanup(registry.AbortCurrent)
					entry, err := registry.begin(request, key, make(chan transactionResult, 1))
					require.NoError(t, err)
					forged := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
						stun.NewType(method, stun.ClassErrorResponse), stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce})
					if kind != "missing realm" {
						realm := "realm"
						if kind == "wrong realm" {
							realm = "attacker realm"
						}
						require.NoError(t, stun.NewRealm(realm).AddTo(forged))
					}
					if kind == "nonce after integrity" {
						require.NoError(t, key.AddTo(forged))
					}
					if kind != "missing nonce" {
						nonce := "attacker nonce"
						if kind == "empty nonce" {
							nonce = ""
						}
						require.NoError(t, stun.NewNonce(nonce).AddTo(forged))
					}
					switch kind {
					case "unsigned", "nonce after integrity":
					case "bad integrity":
						require.NoError(t, stun.NewShortTermIntegrity("wrong key").AddTo(forged))
					default:
						require.NoError(t, key.AddTo(forged))
					}
					registry.Complete(forged)
					require.Same(t, entry, registry.live[request.TransactionID], "invalid 438 must leave the transaction live")
					valid := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
						stun.NewType(method, stun.ClassErrorResponse), stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce},
						stun.NewRealm("realm"), stun.NewNonce("new nonce"), key)
					registry.Complete(valid)
					assert.Same(t, valid, (<-entry.resultCh).msg)
				})
			}
		})
	}
}

func TestTransactionRegistryHidesUnauthenticatedAttributes(t *testing.T) {
	key := transactionTestIntegrity()
	request := stun.MustBuild(stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest), key)
	registry := NewTransactionRegistry(func(raw []byte) (int, error) { return len(raw), nil }, time.Hour)
	t.Cleanup(registry.AbortCurrent)
	entry, err := registry.begin(request, key, make(chan transactionResult, 1))
	require.NoError(t, err)
	response := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse), key,
		proto.RelayedAddress{IP: net.IPv4(192, 0, 2, 1), Port: 40000}, proto.Lifetime{Duration: time.Hour})
	registry.Complete(response)
	result := <-entry.resultCh
	assert.False(t, result.msg.Contains(stun.AttrXORRelayedAddress))
	assert.False(t, result.msg.Contains(stun.AttrLifetime))
}

func TestTransactionRegistryCopiesResponseKey(t *testing.T) {
	key := transactionTestIntegrity()
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest, key)
	response := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse), key)
	registry := NewTransactionRegistry(func(raw []byte) (int, error) { return len(raw), nil }, time.Hour)
	t.Cleanup(registry.AbortCurrent)
	entry, err := registry.begin(request, key, make(chan transactionResult, 1))
	require.NoError(t, err)
	key[0] ^= 0xff
	registry.Complete(response)
	require.Empty(t, registry.live, "changing the caller's key must not change the registered verifier")
	assert.Same(t, response, (<-entry.resultCh).msg)
}

func TestTransactionRegistryInvalidResponseKeepsRetransmitting(t *testing.T) {
	key := transactionTestIntegrity()
	request := stun.MustBuild(stun.TransactionID, stun.NewType(stun.MethodRefresh, stun.ClassRequest), key)
	sent := make(chan struct{}, maxRtxCount)
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		sent <- struct{}{}

		return len(raw), nil
	}, time.Millisecond)
	t.Cleanup(registry.AbortCurrent)
	require.NoError(t, registry.Start(request, key))
	<-sent
	registry.Complete(stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodRefresh, stun.ClassSuccessResponse)))
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("invalid response stopped retransmission")
	}
	registry.Complete(stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodRefresh, stun.ClassSuccessResponse), key))
	require.NoError(t, registry.Start(request, key), "a valid response must retire fire-and-forget work")
}

func TestTransactionRegistryRequiresIntegrityKey(t *testing.T) {
	for _, method := range []stun.Method{
		stun.MethodAllocate, stun.MethodRefresh, stun.MethodCreatePermission, stun.MethodChannelBind,
	} {
		t.Run(method.String(), func(t *testing.T) {
			request := stun.MustBuild(stun.TransactionID, stun.NewType(method, stun.ClassRequest), transactionTestIntegrity())
			registry := NewTransactionRegistry(func([]byte) (int, error) {
				t.Fatal("a request without a response verification key must not be sent")

				return 0, nil
			}, time.Hour)
			_, err := registry.Perform(request, nil)
			require.ErrorIs(t, err, errResponseIntegrityRequired)
			assert.Empty(t, registry.live)
		})
	}
}
