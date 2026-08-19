// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBindingReadinessBeginsAndRetriesFreshAttempt(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := &binding{}

	token1, class, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
	require.True(t, started)
	assert.Equal(t, bindingAttemptFresh, class)
	final, err := bound.preparationAccess(t0)
	assert.False(t, final)
	assert.NoError(t, err)
	assert.ErrorIs(t, bound.writeAccess(t0), ErrNotPrepared)

	_, _, started = bound.beginAttempt(t0, defaultBindingRefreshInterval)
	assert.False(t, started, "one readiness generation may be active at a time")

	assert.True(t, bound.resolveAttempt(token1, bindingAttemptUncertain, nil, t0))
	token2, class, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
	require.True(t, started)
	assert.NotEqual(t, token1, token2)
	assert.Equal(t, bindingAttemptFresh, class)

	assert.False(t, bound.resolveAttempt(token1, bindingAttemptConfirmed, nil, t0.Add(time.Minute)),
		"a stale completion must not overwrite a newer attempt")
}

func TestBindingReadinessRefreshThresholdAndPreservedConfirmation(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := &binding{}
	token, _, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
	require.True(t, started)
	require.True(t, bound.resolveAttempt(token, bindingAttemptConfirmed, nil, t0))

	for _, now := range []time.Time{
		t0.Add(defaultBindingRefreshInterval - time.Nanosecond),
		t0.Add(defaultBindingRefreshInterval),
	} {
		_, _, started = bound.beginAttempt(now, defaultBindingRefreshInterval)
		assert.False(t, started, "refresh eligibility is strictly after the interval")
	}

	refreshToken, class, started := bound.beginAttempt(
		t0.Add(defaultBindingRefreshInterval+time.Nanosecond),
		defaultBindingRefreshInterval,
	)
	require.True(t, started)
	assert.Equal(t, bindingAttemptPreviouslyConfirmed, class)
	require.True(t, bound.resolveAttempt(
		refreshToken,
		bindingAttemptPreserveConfirmation,
		nil,
		t0.Add(defaultBindingRefreshInterval+time.Minute),
	))

	final, err := bound.preparationAccess(t0.Add(channelBindingLifetime))
	assert.True(t, final)
	assert.ErrorIs(t, err, ErrChannelBindingExpired,
		"preserving a prior confirmation must not advance its age")
}

func TestBindingReadinessPreparationAndWriteAccess(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := &binding{}

	assert.ErrorIs(t, bound.writeAccess(t0), ErrNotPrepared)
	token, _, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
	require.True(t, started)
	require.True(t, bound.resolveAttempt(token, bindingAttemptConfirmed, nil, t0))
	assert.ErrorIs(t, bound.writeAccess(t0), ErrNotPrepared,
		"server confirmation alone must not establish prepared history")

	final, err := bound.preparationAccess(t0.Add(channelBindingLifetime - time.Nanosecond))
	assert.True(t, final)
	require.NoError(t, err)
	require.NoError(t, bound.writeAccess(t0.Add(channelBindingLifetime-time.Nanosecond)))

	assert.ErrorIs(t, bound.writeAccess(t0.Add(channelBindingLifetime)), ErrChannelBindingExpired)
	assert.ErrorIs(t, bound.writeAccess(t0.Add(channelBindingLifetime+time.Second)), ErrChannelBindingExpired,
		"expiry must retain its first durable cause")
	assert.False(t, bound.resolveAttempt(token, bindingAttemptConfirmed, nil, t0.Add(time.Hour)),
		"late completion must not resurrect terminal readiness")
}

func TestBindingReadinessPreparedPermissionLossOrdering(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	permissionCause := fmt.Errorf("%w: permission transaction", ErrPermissionRefreshFailed)
	otherCause := fmt.Errorf("%w: later failure", ErrPermissionRefreshFailed)

	t.Run("loss before preparation does not terminalize", func(t *testing.T) {
		bound := confirmedBinding(t, t0)
		assert.False(t, bound.failPrepared(permissionCause))
		final, err := bound.preparationAccess(t0)
		assert.True(t, final)
		assert.NoError(t, err)
	})

	t.Run("preparation before loss records the first cause", func(t *testing.T) {
		bound := confirmedBinding(t, t0)
		final, err := bound.preparationAccess(t0)
		require.True(t, final)
		require.NoError(t, err)

		assert.True(t, bound.failPrepared(permissionCause))
		assert.False(t, bound.failPrepared(otherCause))
		assert.ErrorIs(t, bound.writeAccess(t0), permissionCause)
		assert.NotErrorIs(t, bound.writeAccess(t0), otherCause)
	})
}

func TestBindingReadinessPreviouslyConfirmedAttemptKeepsAccess(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := confirmedBinding(t, t0)
	final, err := bound.preparationAccess(t0)
	require.True(t, final)
	require.NoError(t, err)

	token, class, started := bound.beginAttempt(
		t0.Add(defaultBindingRefreshInterval+time.Nanosecond),
		defaultBindingRefreshInterval,
	)
	require.True(t, started)
	assert.Equal(t, bindingAttemptPreviouslyConfirmed, class)
	require.NoError(t, bound.writeAccess(t0.Add(defaultBindingRefreshInterval+time.Second)),
		"a previously confirmed refresh in flight remains usable")

	require.True(t, bound.resolveAttempt(token, bindingAttemptUncertain, nil, t0.Add(6*time.Minute)))
	require.NoError(t, bound.writeAccess(t0.Add(6*time.Minute)),
		"previously confirmed uncertainty preserves usable history")
	_, class, started = bound.beginAttempt(t0.Add(6*time.Minute), defaultBindingRefreshInterval)
	require.True(t, started, "uncertain confirmed readiness is immediately refresh eligible")
	assert.Equal(t, bindingAttemptPreviouslyConfirmed, class)
}

func TestBindingReadinessPermanentFailureIsDurable(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := &binding{}
	token, _, started := bound.beginAttempt(t0, defaultBindingRefreshInterval)
	require.True(t, started)
	cause := fmt.Errorf("permanent: %w", ErrChannelBindFailed)

	require.True(t, bound.resolveAttempt(token, bindingAttemptPermanentFailure, cause, t0))
	final, err := bound.preparationAccess(t0)
	assert.True(t, final)
	assert.ErrorIs(t, err, cause)
	_, _, started = bound.beginAttempt(t0.Add(time.Minute), defaultBindingRefreshInterval)
	assert.False(t, started)
	assert.False(t, bound.resolveAttempt(token, bindingAttemptConfirmed, nil, t0.Add(time.Minute)))
}

func TestBindingReadinessPermanentOutcomeOrdersWithAccess(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := confirmedBinding(t, t0)
	final, err := bound.preparationAccess(t0)
	require.True(t, final)
	require.NoError(t, err)
	token, _, started := bound.beginAttempt(
		t0.Add(defaultBindingRefreshInterval+time.Nanosecond),
		defaultBindingRefreshInterval,
	)
	require.True(t, started)

	require.NoError(t, bound.writeAccess(t0.Add(defaultBindingRefreshInterval+time.Second)),
		"access linearized before the permanent outcome may observe prior usability")
	cause := fmt.Errorf("permanent refresh: %w", ErrChannelBindFailed)
	require.True(t, bound.resolveAttempt(token, bindingAttemptPermanentFailure, cause, t0.Add(6*time.Minute)))
	assert.ErrorIs(t, bound.writeAccess(t0.Add(6*time.Minute)), cause,
		"access linearized after the permanent outcome observes the durable cause")
}

func TestBindingReadinessExpiryRejectsInFlightCompletion(t *testing.T) {
	t0 := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	bound := confirmedBinding(t, t0)
	final, err := bound.preparationAccess(t0)
	require.True(t, final)
	require.NoError(t, err)

	token, class, started := bound.beginAttempt(
		t0.Add(defaultBindingRefreshInterval+time.Nanosecond),
		defaultBindingRefreshInterval,
	)
	require.True(t, started)
	assert.Equal(t, bindingAttemptPreviouslyConfirmed, class)
	assert.ErrorIs(t, bound.writeAccess(t0.Add(channelBindingLifetime)), ErrChannelBindingExpired)
	assert.False(t, bound.resolveAttempt(token, bindingAttemptConfirmed, nil, t0.Add(channelBindingLifetime)),
		"an in-flight success must not resurrect terminal expiry")
	assert.ErrorIs(t, bound.writeAccess(t0.Add(channelBindingLifetime)), ErrChannelBindingExpired)
}

func confirmedBinding(t *testing.T, confirmedAt time.Time) *binding {
	t.Helper()

	bound := &binding{}
	confirmBindingAt(t, bound, confirmedAt)

	return bound
}

func confirmBindingAt(t *testing.T, bound *binding, confirmedAt time.Time) {
	t.Helper()

	token, _, started := bound.beginAttempt(confirmedAt, defaultBindingRefreshInterval)
	require.True(t, started)
	require.True(t, bound.resolveAttempt(token, bindingAttemptConfirmed, nil, confirmedAt))
}

func TestBindingManagerCapacity(t *testing.T) {
	const wantCapacity = 16_384

	assert.Equal(t, wantCapacity, int(maxChannelNumber-minChannelNumber)+1)

	bm := newBindingManager()
	peers := make([]netip.AddrPort, wantCapacity)
	bindingsByNumber := make(map[uint16]*binding, wantCapacity)
	peerAddr := netip.MustParseAddr("192.0.2.1")
	for i := range wantCapacity {
		peer := netip.AddrPortFrom(peerAddr, uint16(i+1)) //nolint:gosec // Bounded by channel capacity.
		peers[i] = peer

		bound, ok := bm.getOrCreate(peer)
		require.True(t, ok)
		require.NotNil(t, bound)
		assert.GreaterOrEqual(t, bound.number, minChannelNumber)
		assert.LessOrEqual(t, bound.number, maxChannelNumber)
		_, reused := bindingsByNumber[bound.number]
		require.False(t, reused, "channel number %#x reused for %v", bound.number, peer)
		bindingsByNumber[bound.number] = bound

		byAddr, found := bm.findByAddr(peer)
		require.True(t, found)
		assert.Same(t, bound, byAddr)
		byNumber, found := bm.findByNumber(bound.number)
		require.True(t, found)
		assert.Same(t, bound, byNumber)
	}

	assert.Len(t, bm.all(), wantCapacity)
	assert.Len(t, bm.addrMap, wantCapacity)
	assert.Len(t, bindingsByNumber, wantCapacity)

	existing, ok := bm.getOrCreate(peers[0])
	require.True(t, ok)
	assert.Same(t, bindingsByNumber[minChannelNumber], existing)

	exhaustedPeer := netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), wantCapacity+1)
	exhausted, ok := bm.getOrCreate(exhaustedPeer)
	assert.False(t, ok)
	assert.Nil(t, exhausted)
	assert.Len(t, bm.all(), wantCapacity)
	assert.Len(t, bm.addrMap, wantCapacity)
	assert.Len(t, bindingsByNumber, wantCapacity)
	_, found := bm.findByAddr(exhaustedPeer)
	assert.False(t, found)
}

func TestBindingManagerConcurrentFinalSlot(t *testing.T) {
	bm := newBindingManager()
	peerAddr := netip.MustParseAddr("192.0.2.1")
	for i := range maxChannelBindings - 1 {
		peer := netip.AddrPortFrom(peerAddr, uint16(i+1)) //nolint:gosec // Bounded by channel capacity.
		_, ok := bm.getOrCreate(peer)
		require.True(t, ok)
	}

	peers := []netip.AddrPort{
		netip.MustParseAddrPort("192.0.2.2:1"),
		netip.MustParseAddrPort("192.0.2.2:2"),
	}
	type result struct {
		bound *binding
		ok    bool
	}
	results := make(chan result, len(peers))
	start := make(chan struct{})
	var callers sync.WaitGroup
	for _, peer := range peers {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			bound, ok := bm.getOrCreate(peer)
			results <- result{bound: bound, ok: ok}
		}()
	}
	close(start)
	callers.Wait()
	close(results)

	successes := 0
	exhaustions := 0
	for got := range results {
		if got.ok {
			successes++
			require.NotNil(t, got.bound)
		} else {
			exhaustions++
			assert.Nil(t, got.bound)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, exhaustions)

	bindings := bm.all()
	require.Len(t, bindings, maxChannelBindings)
	seen := make(map[uint16]netip.AddrPort, maxChannelBindings)
	for _, bound := range bindings {
		_, reused := seen[bound.number]
		require.False(t, reused, "channel number %#x reused for %v", bound.number, bound.addr)
		seen[bound.number] = bound.addr

		byAddr, found := bm.findByAddr(bound.addr)
		require.True(t, found)
		assert.Same(t, bound, byAddr)
		byNumber, found := bm.findByNumber(bound.number)
		require.True(t, found)
		assert.Same(t, bound, byNumber)
	}
}

func TestBindingManager(t *testing.T) {
	t.Run("lookup and iteration", func(t *testing.T) {
		count := 100
		bm := newBindingManager()
		for i := range count {
			addr := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", 10000+i))
			b0, created := bm.getOrCreate(addr)
			require.True(t, created)
			b1, ok := bm.findByAddr(addr)
			assert.True(t, ok, "should succeed")
			b2, ok := bm.findByNumber(b0.number)
			assert.True(t, ok, "should succeed")

			assert.Equal(t, b0, b1, "should match")
			assert.Equal(t, b0, b2, "should match")
		}

		all := bm.all()
		for _, b := range all {
			found, ok := bm.findByNumber(b.number)
			assert.True(t, ok, "should exist")
			assert.Equal(t, b, found, "should match")
		}
		assert.Equal(t, count, len(all), "should match")
	})

	t.Run("missing lookup", func(t *testing.T) {
		addr := netip.MustParseAddrPort("127.0.0.1:7777")
		m := newBindingManager()
		_, ok := m.findByAddr(addr)
		assert.False(t, ok, "should fail")
		_, ok = m.findByNumber(uint16(5555))
		assert.False(t, ok, "should fail")
	})
}
