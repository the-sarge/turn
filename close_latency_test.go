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
	"github.com/the-sarge/turn/v5/internal/client"
)

// newSilentServerAllocation builds a UDP allocation whose transactions go to a
// server that never responds, driving the real transaction/retransmission
// machinery. Its abort adapter mirrors the wiring Allocate performs.
func newSilentServerAllocation(t *testing.T) (*client.UDPConn, <-chan string) {
	t.Helper()

	var listenConfig net.ListenConfig
	serverSock, err := listenConfig.ListenPacket(context.Background(), "udp4", "127.0.0.1:0") // Never responds
	require.NoError(t, err)
	clientSock, err := listenConfig.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	require.NoError(t, err)

	cl, err := NewClient(&ClientConfig{
		Conn:     clientSock,
		Server:   netip.MustParseAddrPort(serverSock.LocalAddr().String()),
		Username: "user",
		Password: "secret",
		RTO:      25 * time.Millisecond,
	})
	require.NoError(t, err)
	startTestPump(t, cl, clientSock)
	closeOrder := make(chan string, 3)

	config := &client.AllocationConfig{
		WriteTo: cl.sendToServer,
		PerformTransaction: func(msg *stun.Message, dontWait bool) (client.TransactionResult, error) {
			if msg.Type.Method == stun.MethodRefresh && dontWait {
				closeOrder <- "release"
			}

			return cl.performTransaction(msg, dontWait)
		},
		OnDeallocated: func(addr net.Addr) {
			closeOrder <- "deallocated"
			cl.onDeallocated(addr)
		},
		RelayedAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321},
		Username:    stun.NewUsername("user"),
		Realm:       stun.NewRealm("realm"),
		Integrity:   stun.NewShortTermIntegrity("secret"),
		Nonce:       stun.NewNonce("nonce"),
		Lifetime:    time.Hour,
	}

	conn := client.NewUDPConn(config, func() {
		cl.transactions.AbortCurrent()
		closeOrder <- "abort"
	})
	t.Cleanup(func() {
		_ = conn.Close()
		cl.Close()
		_ = clientSock.Close()
		_ = serverSock.Close()
	})

	return conn, closeOrder
}

func TestCloseInterruptsTransactionWaits(t *testing.T) {
	peer := netip.MustParseAddrPort("127.0.0.1:1234")

	t.Run("with abort Close returns promptly and cancellation stays waiter-local", func(t *testing.T) {
		conn, closeOrder := newSilentServerAllocation(t)

		resultA := make(chan error, 1)
		go func() { resultA <- conn.PreparePeer(context.Background(), peer) }()

		ctxB, cancelB := context.WithCancelCause(context.Background())
		defer cancelB(nil)
		resultB := make(chan error, 1)
		go func() { resultB <- conn.PreparePeer(ctxB, peer) }()

		time.Sleep(150 * time.Millisecond) // Let the CreatePermission transaction get in flight

		// Canceling one waiter must not abort the shared transaction work.
		cause := errors.New("waiter B gave up") //nolint:err113 // test-local cause
		cancelB(cause)
		select {
		case err := <-resultB:
			assert.ErrorIs(t, err, cause)
		case <-time.After(time.Second):
			assert.Fail(t, "canceled waiter did not wake promptly")
		}
		select {
		case err := <-resultA:
			assert.Failf(t, "surviving waiter finished early", "err: %v", err)
		case <-time.After(200 * time.Millisecond):
		}

		start := time.Now()
		assert.NoError(t, conn.Close())
		elapsed := time.Since(start)
		t.Logf("Close took %v with abort", elapsed)
		assert.Less(t, elapsed, time.Second,
			"with abort, Close must not wait out the retransmission budget")
		assert.Equal(t, []string{"abort", "deallocated", "release"},
			[]string{<-closeOrder, <-closeOrder, <-closeOrder},
			"the real transaction adapter must abort the old live set before the release transaction starts")

		select {
		case err := <-resultA:
			assert.Error(t, err)
		case <-time.After(5 * time.Second):
			assert.Fail(t, "surviving waiter did not unblock on close")
		}
	})
}
