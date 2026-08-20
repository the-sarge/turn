// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net/netip"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionAttemptLifecycle(t *testing.T) {
	t.Run("first caller starts and second caller joins one attempt", func(t *testing.T) {
		perm := &permission{}

		attempt, fresh := perm.beginOrJoin()
		require.NotNil(t, attempt)
		assert.True(t, fresh)

		joined, fresh := perm.beginOrJoin()
		assert.Equal(t, attempt, joined)
		assert.False(t, fresh)
	})

	tests := []struct {
		name          string
		result        error
		wantPermitted bool
		wantErr       error
	}{
		{
			name:          "success marks the permission permitted",
			wantPermitted: true,
		},
		{
			name:    "failure records the attempt error",
			result:  errFake,
			wantErr: errFake,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perm := &permission{}
			attempt, fresh := perm.beginOrJoin()
			require.True(t, fresh)

			perm.resolve(tt.result)
			select {
			case <-attempt.done:
			default:
				assert.Fail(t, "resolve did not wake attempt waiters")
			}

			permitted, err := perm.readiness()
			assert.Equal(t, tt.wantPermitted, permitted)
			assert.ErrorIs(t, err, tt.wantErr)
			if tt.wantPermitted {
				attempt, fresh = perm.beginOrJoin()
				assert.Nil(t, attempt, "a permitted permission does not start another attempt")
				assert.False(t, fresh)
			}
		})
	}

	t.Run("a new attempt clears a previously resolved failure", func(t *testing.T) {
		perm := &permission{}
		_, fresh := perm.beginOrJoin()
		require.True(t, fresh)
		perm.resolve(errFake)

		attempt, fresh := perm.beginOrJoin()
		require.True(t, fresh)
		permitted, err := perm.readiness()
		assert.False(t, permitted)
		assert.NoError(t, err)

		perm.resolve(nil)
		<-attempt.done
		permitted, err = perm.readiness()
		assert.True(t, permitted)
		assert.NoError(t, err)
	})

	t.Run("joined attempt result remains stable when a stale caller starts another attempt", func(t *testing.T) {
		perm := &permission{}
		attemptA, fresh := perm.beginOrJoin()
		require.True(t, fresh)
		joinedA, fresh := perm.beginOrJoin()
		assert.Equal(t, attemptA, joinedA)
		assert.False(t, fresh)

		perm.resolve(errFake)
		attemptB, fresh := perm.beginOrJoin()
		require.True(t, fresh)
		assert.ErrorIs(t, joinedA.result(), errFake)

		perm.resolve(nil)
		<-attemptB.done
	})
}

func TestPermissionMap(t *testing.T) {
	t.Run("Identity, membership, and deletion", func(t *testing.T) {
		pm := newPermissionMap()
		assert.NotNil(t, pm)
		assert.NotNil(t, pm.permMap)

		addr1 := netip.MustParseAddrPort("1.2.3.4:5000")
		addr2 := netip.MustParseAddrPort("5.6.7.8:8888")
		perm1 := pm.getOrCreate(addr1)
		perm2 := pm.getOrCreate(addr2)
		assert.NotSame(t, perm1, perm2)

		addrs := pm.addrs()
		assert.Len(t, addrs, 2)
		sort.Slice(addrs, func(i, j int) bool {
			return strings.Compare(addrs[i].String(), addrs[j].String()) < 0
		})
		assert.Equal(t, addr1, addrs[0])
		assert.Equal(t, addr2, addrs[1])

		pm.delete(addr1)
		assert.Equal(t, []netip.AddrPort{addr2}, pm.addrs())
		pm.delete(addr2)
		assert.Empty(t, pm.addrs())
	})

	t.Run("Permissions are per peer IP", func(t *testing.T) {
		pm := newPermissionMap()

		perm := pm.getOrCreate(netip.MustParseAddrPort("1.2.3.4:5000"))
		samePeerOtherPort := pm.getOrCreate(netip.MustParseAddrPort("1.2.3.4:6000"))
		assert.Equal(t, perm, samePeerOtherPort, "peers differing only by port share one permission")
		assert.Equal(t, []netip.AddrPort{netip.MustParseAddrPort("1.2.3.4:5000")}, pm.addrs())
	})

	t.Run("failed permission is absent before waiters wake", func(t *testing.T) {
		pm := newPermissionMap()
		addr := netip.MustParseAddrPort("1.2.3.4:5000")
		perm := pm.getOrCreate(addr)
		attempt, fresh := perm.beginOrJoin()
		require.True(t, fresh)

		pm.delete(addr)
		perm.resolve(errFake)
		<-attempt.done

		assert.Empty(t, pm.addrs())
		assert.NotSame(t, perm, pm.getOrCreate(addr))
	})
}
