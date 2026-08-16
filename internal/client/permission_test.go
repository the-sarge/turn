// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net/netip"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPermission(t *testing.T) {
	t.Run("Getter and setter", func(t *testing.T) {
		perm := &permission{}

		assert.Equal(t, permStateIdle, perm.state())
		perm.setState(permStatePermitted)
		assert.Equal(t, permStatePermitted, perm.state())
	})
}

func TestPermissionMap(t *testing.T) {
	t.Run("Basic operations", func(t *testing.T) {
		pm := newPermissionMap()
		assert.NotNil(t, pm)
		assert.NotNil(t, pm.permMap)

		perm1 := &permission{st: permStateIdle}
		perm2 := &permission{st: permStatePermitted}
		addr1 := netip.MustParseAddrPort("1.2.3.4:5000")
		addr2 := netip.MustParseAddrPort("5.6.7.8:8888")
		assert.True(t, pm.insert(addr1, perm1))
		assert.Equal(t, 1, len(pm.permMap))
		assert.True(t, pm.insert(addr2, perm2))
		assert.Equal(t, 2, len(pm.permMap))

		perms, ok := pm.find(addr1)
		assert.True(t, ok)
		assert.Equal(t, perm1, perms)
		assert.Equal(t, permStateIdle, perms.st)

		perms, ok = pm.find(addr2)
		assert.True(t, ok)
		assert.Equal(t, perm2, perms)
		assert.Equal(t, permStatePermitted, perms.st)

		addrs := pm.addrs()
		assert.Equal(t, 2, len(addrs))
		sort.Slice(addrs, func(i, j int) bool {
			return strings.Compare(addrs[i].String(), addrs[j].String()) < 0
		})
		assert.Equal(t, addr1, addrs[0])
		assert.Equal(t, addr2, addrs[1])

		pm.delete(addr1)
		assert.Equal(t, 1, len(pm.permMap))
		pm.delete(addr2)
		assert.Equal(t, 0, len(pm.permMap))
	})

	t.Run("Permissions are per peer IP", func(t *testing.T) {
		pm := newPermissionMap()

		perm := pm.getOrCreate(netip.MustParseAddrPort("1.2.3.4:5000"))
		samePeerOtherPort := pm.getOrCreate(netip.MustParseAddrPort("1.2.3.4:6000"))
		assert.Equal(t, perm, samePeerOtherPort, "peers differing only by port share one permission")
		assert.Equal(t, 1, len(pm.permMap))

		found, ok := pm.find(netip.MustParseAddrPort("1.2.3.4:7000"))
		assert.True(t, ok)
		assert.Equal(t, perm, found)
	})
}
