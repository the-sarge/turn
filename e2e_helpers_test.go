// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/turntest"
)

const e2ePacketCount = 25

// sendPackets owns its reader and reports failures to its caller. write must
// have a deadline no later than ctx's deadline; interrupt must unblock read.
// These obligations are supplied by the test-owned sockets/allocation, not by
// production APIs. Successful delivery leaves the endpoints open for reuse.
func sendPackets(ctx context.Context, direction string, write func([]byte) error,
	read func([]byte) (int, error), interrupt func(),
) error {
	expected := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	type deliveryResult struct {
		count int
		err   error
	}
	result := make(chan deliveryResult, 1)
	go func() {
		outcome := deliveryResult{}
		defer func() { result <- outcome }()
		buf := make([]byte, len(expected)+1)
		for outcome.count < e2ePacketCount {
			n, err := read(buf)
			if err != nil {
				outcome.err = fmt.Errorf("read: %w", err)
				return
			}
			if !bytes.Equal(expected, buf[:n]) {
				outcome.err = fmt.Errorf("payload: got %x, want %x", buf[:n], expected)
				return
			}
			outcome.count++
		}
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			interrupt()
			outcome := <-result
			return fmt.Errorf("%s: received %d/%d: %w", direction, outcome.count, e2ePacketCount, ctx.Err())
		case outcome := <-result:
			if outcome.err != nil {
				return fmt.Errorf("%s: received %d/%d: %w", direction, outcome.count, e2ePacketCount, outcome.err)
			}
			return nil
		case <-ticker.C:
			if err := write(expected); err != nil {
				interrupt()
				outcome := <-result
				return fmt.Errorf("%s: received %d/%d: write: %w", direction, outcome.count, e2ePacketCount, err)
			}
		}
	}
}

// newClientE2E registers ownership at acquisition, including paths that fail
// before peer preparation. The pump stays alive until allocation cleanup joins.
func newClientE2E(t *testing.T) (*Allocation, net.PacketConn, net.PacketConn) {
	t.Helper()
	server, err := turntest.New(turntest.Options{
		Realm:              "pion.ly",
		Username:           testUsername,
		Password:           testPassword,
		AllocationLifetime: time.Second,
		PermissionTimeout:  100 * time.Millisecond,
		ChannelBindTimeout: 100 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeE2EResource(t, server) })
	clientConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { closeE2EResource(t, clientConn) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	require.NoError(t, clientConn.SetWriteDeadline(deadline))
	cl, err := NewClient(&ClientConfig{
		Conn:                      clientConn,
		Server:                    server.Addr(),
		Username:                  testUsername,
		Password:                  testPassword,
		PermissionRefreshInterval: 50 * time.Millisecond,
		bindingRefreshInterval:    50 * time.Millisecond,
		bindingCheckInterval:      50 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)
	startTestPump(t, cl, clientConn)
	allocation, err := cl.Allocate(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Release may write, even when the test has already exhausted its budget.
		// Preserve the graceful release on success, with a bounded write deadline.
		if err := clientConn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("set cleanup write deadline: %v", err)
		}
		closeE2EResource(t, allocation)
	})
	peerConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { closeE2EResource(t, peerConn) })
	peer := netip.MustParseAddrPort(peerConn.LocalAddr().String())
	require.NoError(t, allocation.PreparePeer(ctx, peer))
	return allocation, clientConn, peerConn
}

func closeE2EResource(t *testing.T, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("E2E cleanup: %v", err)
	}
}
