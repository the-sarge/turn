// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"net/netip"
	"sync"
	"time"
)

// Channel number:
//
//	0x4000 through 0x7FFF: These values are the allowed channel
//	numbers (16,384 possible values).
const (
	minChannelNumber   uint16 = 0x4000
	maxChannelNumber   uint16 = 0x7fff
	maxChannelBindings        = int(maxChannelNumber-minChannelNumber) + 1
)

type bindingState int32

const (
	bindingStateIdle bindingState = iota
	bindingStateRequest
	bindingStateUnknown
	bindingStateReadyUnknown
	bindingStateReady
	bindingStateRefresh
	bindingStateFailed
)

type bindingAttemptToken uint64

type bindingAttemptClass uint8

const (
	bindingAttemptFresh bindingAttemptClass = iota
	bindingAttemptPreviouslyConfirmed
)

type bindingAttemptOutcome uint8

const (
	bindingAttemptConfirmed bindingAttemptOutcome = iota
	bindingAttemptUncertain
	bindingAttemptPreserveConfirmation
	bindingAttemptPermanentFailure
)

type binding struct {
	number         uint16          // Read-only
	st             bindingState    // Protected by readinessMutex
	addr           netip.AddrPort  // Read-only
	muBind         sync.Mutex      // Thread-safe, for ChannelBind ops
	attempt        *bindingAttempt // Protected by muBind; non-nil while a bind attempt is in flight
	confirmedAt    time.Time       // Protected by readinessMutex
	terminalCause  error           // Protected by readinessMutex
	terminal       bool            // Protected by readinessMutex
	prepared       bool            // Protected by readinessMutex; peer promised ChannelData-only writes
	generation     bindingAttemptToken
	activeToken    bindingAttemptToken
	activeClass    bindingAttemptClass
	readinessMutex sync.Mutex
}

// beginAttempt claims one readiness generation when an initial or refresh
// attempt is eligible. Callers supply the logical decision time; readiness
// never reads a clock itself.
func (b *binding) beginAttempt(now time.Time, refreshAfter time.Duration) (
	bindingAttemptToken,
	bindingAttemptClass,
	bool,
) {
	b.readinessMutex.Lock()
	defer b.readinessMutex.Unlock()

	if b.terminal || b.activeToken != 0 {
		return 0, bindingAttemptFresh, false
	}

	state := b.st
	var class bindingAttemptClass
	switch {
	case state == bindingStateIdle || state == bindingStateUnknown:
		class = bindingAttemptFresh
		b.st = bindingStateRequest
	case state == bindingStateReadyUnknown:
		class = bindingAttemptPreviouslyConfirmed
		b.st = bindingStateRefresh
	case state == bindingStateReady && now.Sub(b.confirmedAt) > refreshAfter:
		class = bindingAttemptPreviouslyConfirmed
		b.st = bindingStateRefresh
	default:
		return 0, bindingAttemptFresh, false
	}

	b.generation++
	if b.generation == 0 {
		b.generation++
	}
	b.activeToken = b.generation
	b.activeClass = class

	return b.activeToken, class, true
}

// resolveAttempt applies one semantic result to the generation identified by
// token. Duplicate, stale, late, and post-terminal resolutions are ignored.
func (b *binding) resolveAttempt(
	token bindingAttemptToken,
	outcome bindingAttemptOutcome,
	cause error,
	now time.Time,
) bool {
	b.readinessMutex.Lock()
	defer b.readinessMutex.Unlock()

	if b.terminal || token == 0 || token != b.activeToken {
		return false
	}

	if outcome > bindingAttemptPermanentFailure {
		return false
	}
	if !b.applyAttemptOutcomeLocked(outcome, cause, now) {
		return false
	}
	b.activeToken = 0

	return true
}

func (b *binding) applyAttemptOutcomeLocked(
	outcome bindingAttemptOutcome,
	cause error,
	now time.Time,
) bool {
	switch outcome {
	case bindingAttemptConfirmed:
		b.confirmedAt = now
		b.st = bindingStateReady
	case bindingAttemptUncertain:
		if b.activeClass == bindingAttemptPreviouslyConfirmed {
			b.st = bindingStateReadyUnknown
		} else {
			b.st = bindingStateUnknown
		}
	case bindingAttemptPreserveConfirmation:
		if b.activeClass != bindingAttemptPreviouslyConfirmed {
			return false
		}
		b.st = bindingStateReady
	case bindingAttemptPermanentFailure:
		b.terminal = true
		b.terminalCause = cause
		b.st = bindingStateFailed
	default:
		return false
	}

	return true
}

// preparationAccess observes readiness for one PreparePeer waiter. It marks
// prepared history only when that waiter observes confirmed, unexpired
// readiness. The bool reports whether readiness is decisive for the caller.
func (b *binding) preparationAccess(now time.Time) (bool, error) {
	b.readinessMutex.Lock()
	defer b.readinessMutex.Unlock()

	if b.terminal {
		return true, b.failureCauseLocked()
	}
	if !b.usableLocked() {
		return false, nil
	}
	if err := b.expireLocked(now); err != nil {
		return true, err
	}

	b.prepared = true

	return true, nil
}

// writeAccess enforces prepared-only, currently usable ChannelData writes.
// It performs no I/O and terminalizes an expired confirmed binding once.
func (b *binding) writeAccess(now time.Time) error {
	b.readinessMutex.Lock()
	defer b.readinessMutex.Unlock()

	if !b.prepared {
		return ErrNotPrepared
	}
	if b.terminal {
		return b.failureCauseLocked()
	}
	if !b.usableLocked() {
		return ErrChannelBindFailed
	}

	return b.expireLocked(now)
}

// failPrepared applies tokenless Permission-refresh loss. Preparation and
// permission loss serialize on the same readiness lock.
func (b *binding) failPrepared(cause error) bool {
	b.readinessMutex.Lock()
	defer b.readinessMutex.Unlock()

	if b.terminal || !b.prepared {
		return false
	}
	b.terminalizeLocked(cause)

	return true
}

func (b *binding) usableLocked() bool {
	return b.st == bindingStateReady || b.st == bindingStateRefresh || b.st == bindingStateReadyUnknown
}

func (b *binding) expireLocked(now time.Time) error {
	if now.Sub(b.confirmedAt) < channelBindingLifetime {
		return nil
	}
	b.terminalizeLocked(ErrChannelBindingExpired)

	return ErrChannelBindingExpired
}

func (b *binding) failureCauseLocked() error {
	if b.terminalCause != nil {
		return b.terminalCause
	}

	return ErrChannelBindFailed
}

func (b *binding) terminalizeLocked(cause error) {
	if b.terminal {
		return
	}
	b.terminal = true
	b.terminalCause = cause
	b.st = bindingStateFailed
}

// Thread-safe binding map.
type bindingManager struct {
	chanMap map[uint16]*binding
	addrMap map[netip.AddrPort]*binding
	next    uint16
	mutex   sync.RWMutex
}

func newBindingManager() *bindingManager {
	return &bindingManager{
		chanMap: map[uint16]*binding{},
		addrMap: map[netip.AddrPort]*binding{},
		next:    minChannelNumber,
	}
}

func (mgr *bindingManager) assignChannelNumber() uint16 {
	n := mgr.next
	if mgr.next == maxChannelNumber {
		mgr.next = minChannelNumber
	} else {
		mgr.next++
	}

	return n
}

// getOrCreate returns the existing binding for addr, or creates one while a
// channel number remains available. Concurrent callers for the same peer share
// a single channel number. A false result leaves both maps unchanged.
func (mgr *bindingManager) getOrCreate(addr netip.AddrPort) (*binding, bool) {
	mgr.mutex.Lock()
	defer mgr.mutex.Unlock()

	if b, ok := mgr.addrMap[addr]; ok {
		return b, true
	}
	if len(mgr.addrMap) >= maxChannelBindings {
		return nil, false
	}

	b := &binding{
		number: mgr.assignChannelNumber(),
		addr:   addr,
	}

	mgr.chanMap[b.number] = b
	mgr.addrMap[b.addr] = b

	return b, true
}

func (mgr *bindingManager) findByAddr(addr netip.AddrPort) (*binding, bool) {
	mgr.mutex.RLock()
	defer mgr.mutex.RUnlock()

	b, ok := mgr.addrMap[addr]

	return b, ok
}

func (mgr *bindingManager) findByNumber(number uint16) (*binding, bool) {
	mgr.mutex.RLock()
	defer mgr.mutex.RUnlock()

	b, ok := mgr.chanMap[number]

	return b, ok
}

func (mgr *bindingManager) all() []*binding {
	mgr.mutex.RLock()
	defer mgr.mutex.RUnlock()

	list := make([]*binding, 0, len(mgr.chanMap))
	for _, b := range mgr.chanMap {
		list = append(list, b)
	}

	return list
}
