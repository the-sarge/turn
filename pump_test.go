// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"net"
	"testing"
)

// startTestPump runs the read pump the deleted Client.Listen used to provide:
// it reads datagrams from conn and feeds them to cl.HandleInbound until a
// read fails (normally because the test closed the socket). Production
// consumers own their read pump by contract; the fork's own tests use this
// helper.
func startTestPump(t *testing.T, cl *Client, conn net.PacketConn) {
	t.Helper()

	go func() {
		buf := make([]byte, maxDataBufferSize)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if err := cl.HandleInbound(buf[:n], from); err != nil {
				return
			}
		}
	}()
}
