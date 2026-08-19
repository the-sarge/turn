// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

// testConnScript is the single internal test adapter for UDPConn's package
// crossings. Method values read these fields at call time, so tests may
// rescript an already-built connection without writing its private fields.
type testConnScript struct {
	writeTo            func(data []byte) (int, error)
	performTransaction func(msg *stun.Message, dontWait bool) (TransactionResult, error)
	onDeallocated      func(relayedAddr net.Addr)

	writes struct {
		sync.Mutex
		data [][]byte
	}
}

func (s *testConnScript) WriteTo(data []byte) (int, error) {
	s.writes.Lock()
	s.writes.data = append(s.writes.data, append([]byte(nil), data...))
	s.writes.Unlock()

	if s.writeTo != nil {
		return s.writeTo(data)
	}

	return len(data), nil
}

func (s *testConnScript) PerformTransaction(msg *stun.Message, dontWait bool) (TransactionResult, error) {
	if s.performTransaction != nil {
		return s.performTransaction(msg, dontWait)
	}

	return TransactionResult{}, errFake
}

func (s *testConnScript) OnDeallocated(relayedAddr net.Addr) {
	if s.onDeallocated != nil {
		s.onDeallocated(relayedAddr)
	}
}

func (s *testConnScript) writeCount() int {
	s.writes.Lock()
	defer s.writes.Unlock()

	return len(s.writes.data)
}

func (s *testConnScript) lastWrite() []byte {
	s.writes.Lock()
	defer s.writes.Unlock()

	if len(s.writes.data) == 0 {
		return nil
	}

	return s.writes.data[len(s.writes.data)-1]
}

func testAllocationConfig(script *testConnScript) *AllocationConfig {
	return &AllocationConfig{
		WriteTo:            script.WriteTo,
		PerformTransaction: script.PerformTransaction,
		OnDeallocated:      script.OnDeallocated,
		RelayedAddr:        &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321},
		Username:           stun.NewUsername("user"),
		Realm:              stun.NewRealm("realm"),
		Integrity:          stun.NewShortTermIntegrity("pass"),
		Nonce:              stun.NewNonce("nonce"),
		Lifetime:           time.Hour,
	}
}

// newTestConn builds an unstarted UDPConn through the same invariant-owning
// constructor as production. Tests that exercise timers call start explicitly.
func newTestConn(t *testing.T, script *testConnScript) *UDPConn {
	t.Helper()

	return newUDPConn(testAllocationConfig(script), func() {})
}
