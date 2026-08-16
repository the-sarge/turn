// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net/netip"
	"sync"
	"sync/atomic"
)

type permState int32

const (
	permStateIdle permState = iota
	permStatePermitted
)

type permission struct {
	addr        netip.AddrPort
	st          permState     // Thread-safe (atomic op)
	attemptDone chan struct{} // Protected by attemptMutex; non-nil while a create attempt is in flight
	attemptErr  error         // Protected by attemptMutex; result of the last create attempt

	// attemptMutex guards the attempt bookkeeping only. It is never held
	// across a transaction, unlike mutex, which createPermission holds for
	// the duration of the CreatePermission transaction; waiters joining an
	// attempt must not block behind that transaction.
	attemptMutex sync.Mutex   // Thread-safe
	mutex        sync.RWMutex // Thread-safe
}

func (p *permission) setState(state permState) {
	atomic.StoreInt32((*int32)(&p.st), int32(state))
}

func (p *permission) state() permState {
	return permState(atomic.LoadInt32((*int32)(&p.st)))
}

// Thread-safe permission map. TURN permissions are per peer IP, so the map is
// keyed by the canonical netip.Addr alone: peers that differ only by port
// share one permission, exactly as the previous IP-string fingerprint key did.
type permissionMap struct {
	permMap map[netip.Addr]*permission
	mutex   sync.RWMutex
}

func (m *permissionMap) insert(addr netip.AddrPort, p *permission) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	p.addr = addr
	m.permMap[addr.Addr()] = p

	return true
}

// getOrCreate returns the existing permission for addr, or creates one, so
// that concurrent callers for the same peer share a single permission.
func (m *permissionMap) getOrCreate(addr netip.AddrPort) *permission {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	key := addr.Addr()
	if p, ok := m.permMap[key]; ok {
		return p
	}

	p := &permission{addr: addr}
	m.permMap[key] = p

	return p
}

func (m *permissionMap) find(addr netip.AddrPort) (*permission, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	p, ok := m.permMap[addr.Addr()]

	return p, ok
}

func (m *permissionMap) delete(addr netip.AddrPort) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	delete(m.permMap, addr.Addr())
}

func (m *permissionMap) addrs() []netip.AddrPort {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	addrs := []netip.AddrPort{}
	for _, p := range m.permMap {
		addrs = append(addrs, p.addr)
	}

	return addrs
}

func newPermissionMap() *permissionMap {
	return &permissionMap{
		permMap: map[netip.Addr]*permission{},
	}
}
