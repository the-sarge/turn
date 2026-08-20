// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

var errFake = errors.New("fake error") //nolint:err113 // Shared test-local failure sentinel.

// testConnScript is the single internal test adapter for UDPConn's package
// crossings. Method values read these fields at call time, so tests may
// rescript an already-built connection without writing its private fields.
type testConnScript struct {
	writeTo            func(data []byte) (int, error)
	performTransaction func(msg *stun.Message) (*stun.Message, error)
	startTransaction   func(msg *stun.Message) error
	onDeallocated      func()

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

func (s *testConnScript) PerformTransaction(msg *stun.Message) (*stun.Message, error) {
	if s.performTransaction != nil {
		return s.performTransaction(msg)
	}

	return nil, errFake
}

func (s *testConnScript) StartTransaction(msg *stun.Message) error {
	if s.startTransaction != nil {
		return s.startTransaction(msg)
	}

	return nil
}

func (s *testConnScript) OnDeallocated() {
	if s.onDeallocated != nil {
		s.onDeallocated()
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
		StartTransaction:   script.StartTransaction,
		OnDeallocated:      script.OnDeallocated,
		Username:           stun.NewUsername("user"),
		Realm:              stun.NewRealm("realm"),
		Integrity:          stun.NewShortTermIntegrity("pass"),
		Nonce:              stun.NewNonce("nonce"),
		Lifetime:           time.Hour,
	}
}

// newTestConn builds an unactivated UDPConn through the same invariant-owning
// constructor as production. Tests that exercise timers call Activate explicitly.
func newTestConn(t *testing.T, script *testConnScript) *UDPConn {
	t.Helper()

	return newUDPConn(testAllocationConfig(script), func() {})
}
