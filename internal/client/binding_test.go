// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		require.NotContains(t, bindingsByNumber, bound.number, "channel number reused for %v", peer)
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
		require.NotContains(t, seen, bound.number, "channel number reused for %v", bound.addr)
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
