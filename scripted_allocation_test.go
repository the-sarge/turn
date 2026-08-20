// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
	"github.com/the-sarge/turn/v5/internal/client"
)

type scriptedAllocationScript struct {
	performTransaction func(msg *stun.Message) (*stun.Message, error)
	startTransaction   func(msg *stun.Message) error
	onDeallocated      func()
	onChannelBind      func(msg *stun.Message)
	permissionCount    atomic.Int32
	bindingCount       atomic.Int32

	writes struct {
		sync.Mutex
		data [][]byte
	}
}

func (s *scriptedAllocationScript) writeTo(data []byte) (int, error) {
	s.writes.Lock()
	s.writes.data = append(s.writes.data, append([]byte(nil), data...))
	s.writes.Unlock()

	return len(data), nil
}

func (s *scriptedAllocationScript) transact(
	msg *stun.Message,
) (*stun.Message, error) {
	if s.performTransaction != nil {
		return s.performTransaction(msg)
	}

	switch msg.Type.Method {
	case stun.MethodCreatePermission:
		s.permissionCount.Add(1)

		return stun.MustBuild(
			stun.NewType(stun.MethodCreatePermission, stun.ClassSuccessResponse),
		), nil
	case stun.MethodChannelBind:
		s.bindingCount.Add(1)
		if s.onChannelBind != nil {
			s.onChannelBind(msg)
		}

		return stun.MustBuild(
			stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse),
		), nil
	default:
		return new(stun.Message), nil
	}
}

func (s *scriptedAllocationScript) start(msg *stun.Message) error {
	if s.startTransaction != nil {
		return s.startTransaction(msg)
	}

	return nil
}

func (s *scriptedAllocationScript) deallocated() {
	if s.onDeallocated != nil {
		s.onDeallocated()
	}
}

func (s *scriptedAllocationScript) lastWrite() []byte {
	s.writes.Lock()
	defer s.writes.Unlock()

	if len(s.writes.data) == 0 {
		return nil
	}

	return s.writes.data[len(s.writes.data)-1]
}

func newScriptedAllocation(
	t *testing.T, script *scriptedAllocationScript,
) (*Allocation, *client.UDPConn) {
	t.Helper()

	relayed := netip.MustParseAddrPort("127.0.0.1:54321")
	conn := client.NewUDPConn(&client.AllocationConfig{
		WriteTo:            script.writeTo,
		PerformTransaction: script.transact,
		StartTransaction:   script.start,
		OnDeallocated:      script.deallocated,
		Username:           stun.NewUsername("user"),
		Realm:              stun.NewRealm("realm"),
		Integrity:          stun.NewShortTermIntegrity("pass"),
		Nonce:              stun.NewNonce("nonce"),
		Lifetime:           time.Hour,
	}, func() {})
	require.NoError(t, conn.Activate(func() {}))
	t.Cleanup(func() { _ = conn.Close() })

	return newAllocation(conn, relayed), conn
}
