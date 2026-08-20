// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPeriodicTimer(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		timerID := 3
		var nCbs atomic.Uint64
		rt := NewPeriodicTimer(timerID, func(id int) {
			nCbs.Add(1)
			assert.Equal(t, timerID, id)
		}, 50*time.Millisecond)

		assert.False(t, rt.IsRunning(), "should not be running yet")

		ok := rt.Start()
		assert.True(t, ok, "should be true")
		assert.True(t, rt.IsRunning(), "should be running")

		time.Sleep(100 * time.Millisecond)

		ok = rt.Start()
		assert.False(t, ok, "start again is noop")

		time.Sleep(120 * time.Millisecond)
		rt.Stop()
		assert.False(t, rt.IsRunning(), "should not be running")
		assert.Equal(
			t,
			uint64(4),
			nCbs.Load(),
			"should be called 4 times (actual: %d)",
			nCbs.Load(),
		)
	})

	t.Run("stop inside handler", func(t *testing.T) {
		timerID := 4
		stopped := make(chan struct{})
		var rt *PeriodicTimer
		rt = NewPeriodicTimer(timerID, func(id int) {
			assert.Equal(t, timerID, id)
			rt.Stop()
			close(stopped)
		}, 20*time.Millisecond)

		assert.False(t, rt.IsRunning(), "should not be running yet")

		ok := rt.Start()
		assert.True(t, ok, "should be true")
		assert.True(t, rt.IsRunning(), "should be running")

		// Wait for the handler's Stop instead of calibrating a sleep against
		// the timer interval, which is racy on slow runners.
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for handler to stop the timer")
		}
		assert.False(t, rt.IsRunning(), "should not be running")
	})
}
