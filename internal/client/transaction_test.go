// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransactionRegistryInitialSendFailureRollsBack(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	response := stun.MustBuild(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
	)
	sendErr := errors.New("initial send failed") //nolint:err113 // test-local failure
	var sends atomic.Int32
	registry := NewTransactionRegistry(func([]byte) (int, error) {
		if sends.Add(1) == 1 {
			return 0, sendErr
		}

		return len(request.Raw), nil
	}, time.Hour)

	_, err := registry.Perform(request)
	require.ErrorIs(t, err, sendErr)

	resultCh := make(chan *stun.Message, 1)
	go func() {
		result, _ := registry.Perform(request)
		resultCh <- result
	}()
	require.Eventually(t, func() bool { return sends.Load() == 2 }, time.Second, time.Millisecond)
	registry.Complete(response)

	select {
	case result := <-resultCh:
		assert.Same(t, response, result)
	case <-time.After(time.Second):
		assert.Fail(t, "replacement transaction did not complete")
	}
}

func TestTransactionRegistryRejectsDuplicateLiveID(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	response := stun.MustBuild(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
	)
	var sends atomic.Int32
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		sends.Add(1)

		return len(raw), nil
	}, time.Hour)

	firstResult := make(chan *stun.Message, 1)
	go func() {
		result, _ := registry.Perform(request)
		firstResult <- result
	}()
	require.Eventually(t, func() bool { return sends.Load() == 1 }, time.Second, time.Millisecond)

	_, err := registry.Perform(request)
	require.ErrorIs(t, err, errTransactionAlreadyExists)
	assert.Equal(t, int32(1), sends.Load(), "a duplicate must not reach the socket")

	registry.Complete(response)
	select {
	case result := <-firstResult:
		assert.Same(t, response, result, "the original owner must remain live")
	case <-time.After(time.Second):
		assert.Fail(t, "original transaction did not complete")
	}
}

func TestTransactionRegistryAbortWinsBlockedInitialSend(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		close(sendStarted)
		<-releaseSend

		return len(raw), nil
	}, time.Millisecond)

	resultCh := make(chan error, 1)
	go func() {
		_, err := registry.Perform(request)
		resultCh <- err
	}()
	<-sendStarted
	registry.AbortCurrent()
	close(releaseSend)

	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(time.Second):
		assert.Fail(t, "aborted transaction did not wake")
	}
}

func TestTransactionRegistryCancellationClaimsLiveWait(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	response := stun.MustBuild(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
	)
	sent := make(chan struct{})
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		close(sent)

		return len(raw), nil
	}, time.Hour)
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("caller canceled") //nolint:err113 // test-local cause

	resultCh := make(chan error, 1)
	go func() {
		_, err := registry.PerformWithContext(ctx, request)
		resultCh <- err
	}()
	<-sent
	cancel(cause)

	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, cause)
	case <-time.After(time.Second):
		assert.Fail(t, "canceled transaction did not wake")
	}

	registry.Complete(response)
}

func TestTransactionRegistryExhaustionSendsSevenByteIdenticalRequests(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	expected := append([]byte(nil), request.Raw...)
	initialSent := make(chan struct{})
	var writes atomic.Int32
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		assert.True(t, bytes.Equal(expected, raw), "every retry must preserve the supplied bytes")
		if writes.Add(1) == 1 {
			close(initialSent)
		}

		return len(raw), nil
	}, 5*time.Millisecond)

	resultCh := make(chan error, 1)
	go func() {
		_, err := registry.Perform(request)
		resultCh <- err
	}()
	<-initialSent
	request.Raw[0] ^= 0xff

	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, ErrTransactionTimeout)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "transaction did not exhaust its retry budget")
	}
	assert.Equal(t, int32(7), writes.Load())
}

func TestTransactionRegistryResponseWinsBlockedInitialSend(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	response := stun.MustBuild(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
	)
	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	var sends atomic.Int32
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		sends.Add(1)
		close(sendStarted)
		<-releaseSend

		return len(raw), nil
	}, time.Millisecond)

	resultCh := make(chan *stun.Message, 1)
	go func() {
		result, _ := registry.Perform(request)
		resultCh <- result
	}()
	<-sendStarted
	registry.Complete(response)
	close(releaseSend)

	select {
	case result := <-resultCh:
		assert.Same(t, response, result)
	case <-time.After(time.Second):
		assert.Fail(t, "response winner did not wake the blocked begin caller")
	}
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, int32(1), sends.Load(), "a claimed initial send must not arm a timer")
}

func TestTransactionRegistryInitialSendErrorSurvivesAbort(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	sendErr := errors.New("blocked initial send failed") //nolint:err113 // test-local failure
	registry := NewTransactionRegistry(func([]byte) (int, error) {
		close(sendStarted)
		<-releaseSend

		return 0, sendErr
	}, time.Millisecond)

	resultCh := make(chan error, 1)
	go func() {
		_, err := registry.Perform(request)
		resultCh <- err
	}()
	<-sendStarted
	registry.AbortCurrent()
	close(releaseSend)

	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, sendErr)
	case <-time.After(time.Second):
		assert.Fail(t, "blocked send failure did not return")
	}
}

func TestTransactionRegistryClaimDuringBlockedRetryPreventsRearm(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claim  func(*TransactionRegistry, *stun.Message)
		assert func(*testing.T, *stun.Message, error, *stun.Message)
	}{
		{
			name: "response",
			claim: func(registry *TransactionRegistry, response *stun.Message) {
				registry.Complete(response)
			},
			assert: func(t *testing.T, result *stun.Message, err error, response *stun.Message) {
				t.Helper()
				require.NoError(t, err)
				assert.Same(t, response, result)
			},
		},
		{
			name: "abort",
			claim: func(registry *TransactionRegistry, _ *stun.Message) {
				registry.AbortCurrent()
			},
			assert: func(t *testing.T, _ *stun.Message, err error, _ *stun.Message) {
				t.Helper()
				assert.ErrorIs(t, err, net.ErrClosed)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
			response := stun.MustBuild(
				stun.NewTransactionIDSetter(request.TransactionID),
				stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
			)
			retryStarted := make(chan struct{})
			releaseRetry := make(chan struct{})
			var sends atomic.Int32
			registry := NewTransactionRegistry(func(raw []byte) (int, error) {
				if sends.Add(1) == 2 {
					close(retryStarted)
					<-releaseRetry
				}

				return len(raw), nil
			}, time.Millisecond)

			type outcome struct {
				result *stun.Message
				err    error
			}
			outcomeCh := make(chan outcome, 1)
			go func() {
				result, err := registry.Perform(request)
				outcomeCh <- outcome{result: result, err: err}
			}()
			<-retryStarted
			tc.claim(registry, response)
			close(releaseRetry)

			select {
			case got := <-outcomeCh:
				tc.assert(t, got.result, got.err, response)
			case <-time.After(time.Second):
				assert.Fail(t, "claim did not wake the waiter")
			}
			time.Sleep(10 * time.Millisecond)
			assert.Equal(t, int32(2), sends.Load(), "a lost retry must not re-arm")
		})
	}
}

func TestTransactionRegistryWaitedRetryFailureRetires(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	response := stun.MustBuild(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
	)
	retryErr := errors.New("retry failed") //nolint:err113 // test-local failure
	var sends atomic.Int32
	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	registry := NewTransactionRegistry(func(raw []byte) (int, error) {
		sendNumber := sends.Add(1)
		if sendNumber == 2 {
			return 0, retryErr
		}
		if sendNumber == 3 {
			close(replacementStarted)
			<-releaseReplacement
		}

		return len(raw), nil
	}, time.Millisecond)

	_, err := registry.Perform(request)
	require.ErrorIs(t, err, retryErr)
	assert.Equal(t, int32(2), sends.Load())

	resultCh := make(chan *stun.Message, 1)
	go func() {
		result, _ := registry.Perform(request)
		resultCh <- result
	}()
	<-replacementStarted
	registry.Complete(response)
	close(releaseReplacement)
	select {
	case result := <-resultCh:
		assert.Same(t, response, result)
	case <-time.After(time.Second):
		assert.Fail(t, "replacement after retry failure did not complete")
	}
}

func TestTransactionRegistryFireAndForgetRetiresOnEveryTerminalPath(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		response := stun.MustBuild(
			stun.NewTransactionIDSetter(request.TransactionID),
			stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
		)
		var sends atomic.Int32
		registry := NewTransactionRegistry(func(raw []byte) (int, error) {
			sends.Add(1)

			return len(raw), nil
		}, time.Hour)

		err := registry.Start(request)
		require.NoError(t, err)
		registry.Complete(response)
		err = registry.Start(request)
		require.NoError(t, err, "the response must retire fire-and-forget ownership")
		assert.Equal(t, int32(2), sends.Load())
		registry.AbortCurrent()
	})

	t.Run("retransmit failure", func(t *testing.T) {
		request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		retryErr := errors.New("retry failed") //nolint:err113 // test-local failure
		var sends atomic.Int32
		registry := NewTransactionRegistry(func(raw []byte) (int, error) {
			if sends.Add(1) == 2 {
				return 0, retryErr
			}

			return len(raw), nil
		}, time.Millisecond)

		err := registry.Start(request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			performErr := registry.Start(request)

			return performErr == nil
		}, time.Second, time.Millisecond, "a failed retry must retire fire-and-forget ownership")
		registry.AbortCurrent()
	})

	t.Run("exhaustion", func(t *testing.T) {
		request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		var sends atomic.Int32
		registry := NewTransactionRegistry(func(raw []byte) (int, error) {
			sends.Add(1)

			return len(raw), nil
		}, time.Millisecond)

		err := registry.Start(request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			performErr := registry.Start(request)

			return performErr == nil
		}, time.Second, time.Millisecond, "exhaustion must retire fire-and-forget ownership")
		assert.Equal(t, int32(8), sends.Load())
		registry.AbortCurrent()
	})
}
