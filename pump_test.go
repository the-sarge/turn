// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
)

// startTestPump runs the read pump the deleted Client.Listen used to provide:
// it reads datagrams from conn and feeds them to cl.HandleInbound until a
// read fails (normally because the test closed the socket). Production
// consumers own their read pump by contract; the fork's own tests use this
// helper. Cleanup closes the socket and joins the pump. The returned stop
// performs the same cleanup and may be called by the parent for earlier teardown.
func startTestPump(t *testing.T, cl *Client, conn net.PacketConn) func() {
	t.Helper()

	done := make(chan struct{})
	var pumpErr error
	go func() {
		defer close(done)
		buf := make([]byte, maxDataBufferSize)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					pumpErr = fmt.Errorf("test pump read: %w", err)
				}
				return
			}
			if err := cl.HandleInbound(buf[:n], from); err != nil {
				pumpErr = fmt.Errorf("test pump inbound: %w", err)
				return
			}
		}
	}()
	stop := sync.OnceFunc(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("test pump close: %v", err)
		}
		<-done
		if pumpErr != nil {
			t.Error(pumpErr)
		}
	})
	t.Cleanup(stop)
	return stop
}

func TestTestPumpCleanupJoinsBlockedRead(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	exited := make(chan struct{})
	observed := &pumpObservedConn{PacketConn: conn, exited: exited}
	t.Run("owner", func(t *testing.T) {
		// No datagrams arrive, so the client is never consulted.
		startTestPump(t, nil, observed)
	})
	select {
	case <-exited:
	default:
		t.Fatal("test cleanup returned before the pump reader exited")
	}
}

type pumpObservedConn struct {
	net.PacketConn
	exited chan struct{}
}

func (c *pumpObservedConn) ReadFrom(p []byte) (int, net.Addr, error) {
	defer close(c.exited)
	return c.PacketConn.ReadFrom(p)
}
