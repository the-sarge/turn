// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAwaitWriteWaitsForRecording(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int32
	}{
		{name: "paused first write", n: 1},
		{name: "blocked second write", n: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn := newObserverConn()
				conn.blockFrom = tc.n
				to := testServerNetAddr()
				if tc.n == 2 {
					_, err := conn.WriteTo([]byte("first"), to)
					require.NoError(t, err)
				}

				payload := []byte("requested datagram")
				writeDone := make(chan struct{})
				go func() {
					defer close(writeDone)
					n, err := conn.WriteTo(payload, to)
					assert.NoError(t, err)
					assert.Equal(t, len(payload), n)
				}()
				<-conn.blocked // Admission has happened; recording cannot happen yet.

				// Fake time advances only while goroutines are blocked. The waiter
				// must survive several polling ticks before recording is released.
				go func() {
					time.Sleep(50 * time.Millisecond)
					close(conn.gate)
				}()
				defer func() { <-writeDone }()

				raw := awaitWrite(t, conn, tc.n)
				assert.Equal(t, []byte("requested datagram"), raw)
				assert.Equal(t, to.String(), conn.destination(int(tc.n-1)))
				<-writeDone
				payload[0] = 'X'
				assert.Equal(t, []byte("requested datagram"), conn.write(int(tc.n-1)),
					"recording owns its bytes after WriteTo returns")
				if len(raw) > 0 {
					raw[0] = 'Y'
					assert.Equal(t, []byte("requested datagram"), conn.write(int(tc.n-1)),
						"readers receive a copy of recorded bytes")
				}
			})
		})
	}
}

func TestObserverRequestFiltersWaitForRecording(t *testing.T) {
	for _, helper := range []string{"request after", "new Refresh"} {
		t.Run(helper, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn := newObserverConn()
				conn.blockFrom = 4
				to := testServerNetAddr()
				allocate := stun.MustBuild(stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest))
				excluded := stun.MustBuild(stun.TransactionID, stun.NewType(stun.MethodRefresh, stun.ClassRequest))
				wanted := stun.MustBuild(stun.TransactionID, stun.NewType(stun.MethodRefresh, stun.ClassRequest))
				for _, msg := range []*stun.Message{allocate, excluded, excluded} {
					_, err := conn.WriteTo(msg.Raw, to)
					require.NoError(t, err)
				}

				writeDone := make(chan struct{})
				go func() {
					defer close(writeDone)
					_, err := conn.WriteTo(wanted.Raw, to)
					assert.NoError(t, err)
				}()
				<-conn.blocked
				go func() {
					time.Sleep(50 * time.Millisecond)
					close(conn.gate)
				}()
				defer func() { <-writeDone }()

				var raw []byte
				if helper == "request after" {
					raw = awaitRequestAfter(t, conn, 1, excluded.TransactionID)
				} else {
					raw = awaitNewRefresh(t, conn, map[[stun.TransactionIDSize]byte]struct{}{
						excluded.TransactionID: {},
					})
				}
				assert.Equal(t, wanted.Raw, raw)
				assert.Equal(t, to.String(), conn.destination(3))
			})
		})
	}
}
