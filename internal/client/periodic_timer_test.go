// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeriodicTimer(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		timerID := 3
		var nCbs atomic.Uint64
		callbacks := make(chan int, 1)
		rt := NewPeriodicTimer(timerID, func(id int) {
			nCbs.Add(1)
			select {
			case callbacks <- id:
			default:
			}
		}, 50*time.Millisecond)
		t.Cleanup(rt.StopAndWait)

		assert.False(t, rt.IsRunning(), "should not be running yet")
		require.True(t, rt.Start())
		assert.True(t, rt.IsRunning())
		assert.False(t, rt.Start(), "start again is noop")

		for range 2 {
			select {
			case id := <-callbacks:
				assert.Equal(t, timerID, id)
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for timer callback")
			}
		}
		rt.StopAndWait()
		assert.False(t, rt.IsRunning())
		assert.GreaterOrEqual(t, nCbs.Load(), uint64(2))
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

		t.Cleanup(rt.StopAndWait)
		assert.False(t, rt.IsRunning(), "should not be running yet")

		ok := rt.Start()
		assert.True(t, ok, "should be true")

		// Wait for the handler's Stop instead of calibrating a sleep against
		// the timer interval, which is racy on slow runners.
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			assert.Fail(t, "timed out waiting for handler to stop the timer")
		}
		rt.StopAndWait()
		assert.False(t, rt.IsRunning(), "should not be running")
	})

	t.Run("stop waits for in-flight handler", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			entered := make(chan struct{})
			gate := newPrepareGate(t)
			rt := NewPeriodicTimer(5, func(int) {
				close(entered)
				<-gate.done
			}, time.Millisecond)
			// Release must precede the join, including failed assertions.
			t.Cleanup(func() {
				gate.release()
				rt.StopAndWait()
			})
			require.True(t, rt.Start())
			<-entered
			stopped := make(chan struct{})
			go func() {
				rt.StopAndWait()
				close(stopped)
			}()
			synctest.Wait()
			assert.False(t, rt.IsRunning())
			select {
			case <-stopped:
				t.Fatal("StopAndWait returned before the handler finished")
			default:
			}
			gate.release()
			<-stopped
		})
	})
}
