// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

// observerConn is a caller-owned socket standing in for the silent-server
// pattern: writes reach a server that never responds on its own, driving the
// real transaction/retransmission machinery deterministically. It records
// outbound datagrams and counts the deadline and close calls that the fork
// must never make on a caller-owned socket. An optional gate blocks the Nth
// and later writes, modeling a retransmit write stuck in caller-socket I/O.
type observerConn struct {
	deadlineCalls  atomic.Int32
	closeCalls     atomic.Int32
	admittedWrites atomic.Int32 // Write entry, before gating or recording.
	localAddr      net.Addr
	blockFrom      int32         // 1-based write ordinal at which writes block; 0 = never
	gate           chan struct{} // Closed to release blocked writes
	blocked        chan struct{} // Signaled once a write is blocked on the gate

	mu           sync.Mutex
	writes       [][]byte
	destinations []string
}

func newObserverConn() *observerConn {
	return &observerConn{
		localAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555},
		gate:      make(chan struct{}),
		blocked:   make(chan struct{}, 8),
	}
}

func (o *observerConn) WriteTo(p []byte, to net.Addr) (int, error) {
	n := o.admittedWrites.Add(1)
	if o.blockFrom > 0 && n >= o.blockFrom {
		o.blocked <- struct{}{}
		<-o.gate
	}
	o.mu.Lock()
	o.writes = append(o.writes, append([]byte(nil), p...))
	o.destinations = append(o.destinations, to.String())
	o.mu.Unlock()

	return len(p), nil
}

// recordedCount observes entries only after both bytes and destination are stored.
// Entries are append-only, so an observed index remains available to readers.
func (o *observerConn) recordedCount() int32 {
	o.mu.Lock()
	defer o.mu.Unlock()

	return int32(len(o.writes)) //nolint:gosec // Test recordings are bounded well below int32 capacity.
}

func (o *observerConn) destination(i int) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.destinations) {
		return ""
	}

	return o.destinations[i]
}

func (o *observerConn) write(i int) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.writes) {
		return nil
	}

	return append([]byte(nil), o.writes[i]...)
}

func (o *observerConn) ReadFrom([]byte) (int, net.Addr, error) {
	select {} // The tests feed inbound datagrams through HandleInbound directly.
}

func (o *observerConn) Close() error {
	o.closeCalls.Add(1)

	return nil
}

func (o *observerConn) LocalAddr() net.Addr {
	return o.localAddr
}

func (o *observerConn) SetDeadline(time.Time) error {
	o.deadlineCalls.Add(1)

	return nil
}

func (o *observerConn) SetReadDeadline(time.Time) error {
	o.deadlineCalls.Add(1)

	return nil
}

func (o *observerConn) SetWriteDeadline(time.Time) error {
	o.deadlineCalls.Add(1)

	return nil
}

func testServerAddrPort() netip.AddrPort {
	return netip.MustParseAddrPort("127.0.0.1:3478")
}

func newObservedClient(t *testing.T, conn *observerConn) *Client {
	t.Helper()

	cl, err := NewClient(&ClientConfig{
		Conn:     conn,
		Server:   testServerAddrPort(),
		Username: testUsername,
		Password: testPassword,
		RTO:      25 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)

	return cl
}

func testServerNetAddr() net.Addr {
	return net.UDPAddrFromAddrPort(testServerAddrPort())
}

// transactionID extracts the transaction ID from a recorded request datagram.
func transactionID(t *testing.T, raw []byte) [stun.TransactionIDSize]byte {
	t.Helper()

	msg := &stun.Message{Raw: append([]byte(nil), raw...)}
	require.NoError(t, msg.Decode())

	return msg.TransactionID
}

// unauthorizedResponse builds the 401 challenge for the recorded
// unauthenticated Allocate request, carrying the nonce and realm the client
// needs for its authenticated attempt.
func unauthorizedResponse(t *testing.T, req []byte) []byte {
	t.Helper()

	nonce := stun.NewNonce("test-nonce")
	realm := stun.NewRealm("test-realm")
	msg, err := stun.Build(
		stun.NewTransactionIDSetter(transactionID(t, req)),
		stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse),
		stun.ErrorCodeAttribute{Code: stun.CodeUnauthorized, Reason: []byte("Unauthorized")},
		&nonce,
		&realm,
	)
	require.NoError(t, err)

	return msg.Raw
}

// awaitWrite waits until the observer has recorded at least n writes and
// returns the raw datagram at index n-1.
func awaitWrite(t *testing.T, conn *observerConn, n int32) []byte {
	t.Helper()

	require.Eventually(t, func() bool {
		return conn.recordedCount() >= n
	}, 5*time.Second, 5*time.Millisecond, "request %d never left the socket", n)

	return conn.write(int(n - 1))
}

// awaitRequestAfter waits for a request recorded at or after write index from
// whose transaction ID differs from excludeID (skipping retransmits of the
// excluded request), returning its raw datagram.
func awaitRequestAfter(t *testing.T, conn *observerConn, from int32, excludeID [stun.TransactionIDSize]byte) []byte {
	t.Helper()

	var raw []byte
	require.Eventually(t, func() bool {
		count := conn.recordedCount()
		for i := from; i < count; i++ {
			candidate := conn.write(int(i))
			if candidate == nil {
				continue
			}
			if transactionID(t, candidate) != excludeID {
				raw = candidate

				return true
			}
		}

		return false
	}, 5*time.Second, 5*time.Millisecond, "expected request never left the socket")

	return raw
}

type allocateResult struct {
	alloc *Allocation
	err   error
}

func startObservedAllocate(cl *Client, ctx context.Context) <-chan allocateResult {
	result := make(chan allocateResult, 1)
	go func() {
		alloc, err := cl.Allocate(ctx)
		result <- allocateResult{alloc: alloc, err: err}
	}()

	return result
}

func awaitNewRefresh(
	t *testing.T,
	conn *observerConn,
	excluded map[[stun.TransactionIDSize]byte]struct{},
) []byte {
	t.Helper()

	var raw []byte
	require.Eventually(t, func() bool {
		for i := range conn.recordedCount() {
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
