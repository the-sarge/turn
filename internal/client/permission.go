// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net/netip"
	"sync"
)

type permission struct {
	addr      netip.AddrPort
	mu        sync.Mutex
	permitted bool
	attempt   *permissionAttempt
	result    error
}

// permissionAttempt owns one generation's immutable completion result. A read
// after done closes observes the result published by resolve.
type permissionAttempt struct {
	done chan struct{}
	err  error
}

func (a *permissionAttempt) result() error {
	return a.err
}

// beginOrJoin returns the current attempt handle. The caller that receives
// fresh=true owns running and resolving the attempt. A nil handle means the
// permission became ready before this caller could start work.
func (p *permission) beginOrJoin() (attempt *permissionAttempt, fresh bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.permitted {
		return nil, false
	}
	if p.attempt != nil {
		return p.attempt, false
	}

	p.attempt = &permissionAttempt{done: make(chan struct{})}
	p.result = nil

	return p.attempt, true
}

// resolve publishes one attempt result before waking every joined caller.
func (p *permission) resolve(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.attempt == nil {
		return
	}
	attempt := p.attempt
	p.attempt = nil
	p.result = err
	p.permitted = err == nil
	attempt.err = err
	close(attempt.done)
}

// readiness reports the durable permitted fact and the last attempt result.
func (p *permission) readiness() (permitted bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.permitted, p.result
}

// Thread-safe permission map. TURN permissions are per peer IP, so the map is
// keyed by the canonical netip.Addr alone: peers that differ only by port
// share one permission, exactly as the previous IP-string fingerprint key did.
type permissionMap struct {
	permMap map[netip.Addr]*permission
	mutex   sync.RWMutex
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
