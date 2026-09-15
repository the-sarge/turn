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
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/internal/client"
)

// permissionWaitContext signals after PreparePeer captures its permission
// attempt and evaluates the cancellation operand of its wait. The silent
// server cannot complete that attempt before this test closes the allocation.
type permissionWaitContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func (ctx *permissionWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.joined) })

	return ctx.Context.Done()
}

func awaitPermissionJoin(t *testing.T, ctx *permissionWaitContext) {
	t.Helper()
	select {
	case <-ctx.joined:
	case <-time.After(time.Second):
		t.Fatal("PreparePeer did not join the permission attempt")
	}
}

// newSilentServerAllocation builds a UDP allocation whose transactions go to a
// server that never responds, driving the real transaction/retransmission
// machinery. Its abort adapter mirrors the wiring Allocate performs.
func newSilentServerAllocation(t *testing.T) (*client.UDPConn, <-chan string, net.PacketConn) {
	t.Helper()

	var listenConfig net.ListenConfig
	serverSock, err := listenConfig.ListenPacket(context.Background(), "udp4", "127.0.0.1:0") // Never responds
	require.NoError(t, err)
	clientSock, err := listenConfig.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	require.NoError(t, err)

	cl, err := NewClient(&ClientConfig{
		Conn:     clientSock,
		Server:   netip.MustParseAddrPort(serverSock.LocalAddr().String()),
		Username: testUsername,
		Password: testPassword,
		RTO:      25 * time.Millisecond,
	})
	require.NoError(t, err)
	startTestPump(t, cl, clientSock)
	closeOrder := make(chan string, 3)

	config := &client.AllocationConfig{
		WriteTo:            cl.sendToServer,
		PerformTransaction: cl.transactions.Perform,
		StartTransaction: func(msg *stun.Message) error {
			if msg.Type.Method == stun.MethodRefresh {
				closeOrder <- "release"
			}

			return cl.transactions.Start(msg)
		},
		OnDeallocated: func() {
			closeOrder <- "deallocated"
			cl.onDeallocated()
		},
		Username:  stun.NewUsername(testUsername),
		Realm:     stun.NewRealm("realm"),
		Integrity: stun.NewShortTermIntegrity(testPassword),
		Nonce:     stun.NewNonce("nonce"),
		Lifetime:  time.Hour,
	}

	conn := client.NewUDPConn(config, func() {
		cl.transactions.AbortCurrent()
		closeOrder <- "abort"
	})
	require.NoError(t, conn.Activate(func() {}))
	t.Cleanup(func() {
		_ = conn.Close()
		cl.Close()
		_ = clientSock.Close()
		_ = serverSock.Close()
	})

	return conn, closeOrder, serverSock
}

func TestCloseInterruptsTransactionWaits(t *testing.T) {
	peer := netip.MustParseAddrPort("127.0.0.1:1234")

	t.Run("with abort Close returns promptly and cancellation stays waiter-local", func(t *testing.T) {
		conn, closeOrder, serverSock := newSilentServerAllocation(t)

		ctxA := &permissionWaitContext{Context: context.Background(), joined: make(chan struct{})}
		resultA := make(chan error, 1)
		go func() { resultA <- conn.PreparePeer(ctxA, peer) }()

		ctxB, cancelB := context.WithCancelCause(context.Background())
		defer cancelB(nil)
		observedB := &permissionWaitContext{Context: ctxB, joined: make(chan struct{})}
		resultB := make(chan error, 1)
		go func() { resultB <- conn.PreparePeer(observedB, peer) }()

		awaitPermissionJoin(t, ctxA)
		awaitPermissionJoin(t, observedB)
		require.NoError(t, serverSock.SetReadDeadline(time.Now().Add(time.Second)))
		packet := make([]byte, 2048)
		n, _, err := serverSock.ReadFrom(packet)
		require.NoError(t, err)
		request := &stun.Message{Raw: packet[:n]}
		require.NoError(t, request.Decode())
		require.Equal(t, stun.NewType(stun.MethodCreatePermission, stun.ClassRequest), request.Type)

		// Canceling one waiter must not abort the shared transaction work.
		cause := errors.New("waiter B gave up") //nolint:err113 // test-local cause
		cancelB(cause)
		select {
		case err := <-resultB:
			require.ErrorIs(t, err, cause)
		case <-time.After(time.Second):
			assert.Fail(t, "canceled waiter did not wake promptly")
		}
		select {
		case err := <-resultA:
			assert.Failf(t, "surviving waiter finished early", "err: %v", err)
		default:
		}

		require.Empty(t, closeOrder, "waiter cancellation must not abort or deallocate shared work")

		start := time.Now()
		require.NoError(t, conn.Close())
		elapsed := time.Since(start)
		t.Logf("Close took %v with abort", elapsed)
		assert.Less(t, elapsed, time.Second,
			"with abort, Close must not wait out the retransmission budget")
		// All event producers run synchronously inside Close. Fail before
		// receiving if an event is missing; there is nothing left to wait for.
		require.Len(t, closeOrder, 3, "Close must emit all lifecycle events before returning")
		assert.Equal(t, []string{"abort", "deallocated", "release"},
			[]string{<-closeOrder, <-closeOrder, <-closeOrder},
			"the real transaction adapter must abort the old live set before the release transaction starts")

		select {
		case err := <-resultA:
			require.Error(t, err)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "surviving waiter did not unblock on close")
		}
	})
}
