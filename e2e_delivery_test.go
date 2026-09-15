// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSendPacketsContainsDeliveryFailure(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	readerDone := make(chan struct{})
	started := time.Now()
	err = sendPackets(ctx, "dropped delivery", func([]byte) error { return nil }, func(p []byte) (int, error) {
		defer close(readerDone)
		n, _, readErr := conn.ReadFrom(p)
		return n, readErr
	}, func() { _ = conn.Close() })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "dropped delivery")
	require.ErrorContains(t, err, "0/25")
	require.Less(t, time.Since(started), time.Second)
	select {
	case <-readerDone:
	default:
		t.Fatal("delivery returned before its blocked reader exited")
	}
}

func TestSendPacketsReportsReaderFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wantErr := errors.New("reader failed") //nolint:err113 // Test-local injected I/O failure.
	reads := 0
	err := sendPackets(ctx, "reader failure", func([]byte) error { return nil }, func(p []byte) (int, error) {
		reads++
		if reads == 1 {
			return copy(p, []byte{0xDE, 0xAD, 0xBE, 0xEF}), nil
		}
		return 0, wantErr
	}, func() { t.Error("finished reader should not need interruption") })
	require.ErrorIs(t, err, wantErr)
	require.ErrorContains(t, err, "reader failure: received 1/25: read:")
}

func TestSendPacketsReportsWriterFailureAndJoinsReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wantErr := errors.New("writer failed") //nolint:err113 // Test-local injected I/O failure.
	interrupted := make(chan struct{})
	readerDone := make(chan struct{})
	err := sendPackets(ctx, "writer failure", func([]byte) error { return wantErr }, func([]byte) (int, error) {
		defer close(readerDone)
		<-interrupted
		return 0, net.ErrClosed
	}, func() { close(interrupted) })
	require.ErrorIs(t, err, wantErr)
	require.ErrorContains(t, err, "writer failure: received 0/25: write:")
	select {
	case <-readerDone:
	default:
		t.Fatal("writer failure returned before the reader exited")
	}
}

func TestSendPacketsReportsPayloadMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := sendPackets(ctx, "bad payload", func([]byte) error { return nil }, func(p []byte) (int, error) {
		return copy(p, []byte{0xBA, 0xAD}), nil
	}, func() { t.Error("finished reader should not need interruption") })
	require.ErrorContains(t, err, "bad payload: received 0/25: payload: got baad, want deadbeef")
}

func TestClientE2EDeliveryFailure(t *testing.T) {
	for _, direction := range []string{"allocation to peer", "peer to allocation"} {
		t.Run(direction, func(t *testing.T) {
			allocation, clientConn, peerConn := newClientE2E(t)
			readDone := make(chan struct{})
			read := func(p []byte) (int, error) {
				defer close(readDone)
				if direction == "allocation to peer" {
					n, _, err := peerConn.ReadFrom(p)
					return n, err
				}
				n, _, err := allocation.ReadFrom(p)
				return n, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			// A successful write that deliberately sends nothing models lost delivery.
			err := sendPackets(ctx, direction, func([]byte) error { return nil }, read, func() {
				closeE2EResource(t, clientConn)
				closeE2EResource(t, peerConn)
				closeE2EResource(t, allocation)
			})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.ErrorContains(t, err, direction+": received 0/25")
			require.Less(t, time.Since(started), time.Second)
			select {
			case <-readDone:
			default:
				t.Fatal("delivery returned without joining its reader")
			}
		})
	}
}
