// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
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

func requireZeroLifetimeRefresh(t *testing.T, raw []byte) [stun.TransactionIDSize]byte {
	t.Helper()

	msg := &stun.Message{Raw: append([]byte(nil), raw...)}
	require.NoError(t, msg.Decode())
	require.Equal(t, stun.MethodRefresh, msg.Type.Method)

	var lifetime proto.Lifetime
	require.NoError(t, lifetime.GetFrom(msg))
	require.Zero(t, lifetime.Duration)

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

func TestCharacterizeZeroLifetimeRefreshSuccess(t *testing.T) {
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
	require.NoError(t, cl.HandleInbound(
		allocateSuccessResponseWithLifetime(t, authenticated, 0),
		testServerNetAddr(),
	))

	var allocation *Allocation
	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.allocation)
		allocation = result.allocation
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Allocate did not return after the zero-lifetime success response")
	}
	require.Same(t, allocation.conn, cl.relayedUDPConn(), "zero-lifetime Allocation remains published")

	seenRefreshes := make(map[[stun.TransactionIDSize]byte]struct{})
	firstRefresh := awaitNewRefresh(t, conn, seenRefreshes)
	firstRefreshID := requireZeroLifetimeRefresh(t, firstRefresh)
	seenRefreshes[firstRefreshID] = struct{}{}
	require.NoError(t, cl.HandleInbound(
		refreshSuccessResponseWithLifetime(t, firstRefresh, 0),
		testServerNetAddr(),
	))

	secondRefresh := awaitNewRefresh(t, conn, seenRefreshes)
	secondRefreshID := requireZeroLifetimeRefresh(t, secondRefresh)
	seenRefreshes[secondRefreshID] = struct{}{}

	_, err = cl.Allocate(context.Background())
	require.ErrorIs(t, err, ErrAlreadyAllocated, "published zero-lifetime Allocation blocks later admission")
	require.Same(t, allocation.conn, cl.relayedUDPConn(), "successful zero-lifetime Refresh does not self-seal")

	require.NoError(t, allocation.Close(), "caller remains the seal owner")
	require.Nil(t, cl.relayedUDPConn(), "caller Close clears the published Allocation")

	release := awaitNewRefresh(t, conn, seenRefreshes)
	releaseID := requireZeroLifetimeRefresh(t, release)
	seenRefreshes[releaseID] = struct{}{}
	cl.Close()

	uniqueRefreshes := make(map[[stun.TransactionIDSize]byte]struct{})
	for i := int32(0); i < conn.writeCount.Load(); i++ {
		raw := conn.write(int(i))
		msg := &stun.Message{Raw: raw}
		require.NoError(t, msg.Decode())
		if msg.Type.Method != stun.MethodRefresh {
			continue
		}

		var lifetime proto.Lifetime
		require.NoError(t, lifetime.GetFrom(msg))
		assert.Zero(t, lifetime.Duration)
		uniqueRefreshes[msg.TransactionID] = struct{}{}
	}
	require.GreaterOrEqual(t, len(uniqueRefreshes), 3)
	t.Logf("observed %d distinct lifetime-zero Refresh transactions", len(uniqueRefreshes))
}
