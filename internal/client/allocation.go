// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/stun/v3"
	"github.com/the-sarge/turn/v5/internal/proto"
)

// AllocationConfig is a set of configuration params used by NewUDPConn.
type AllocationConfig struct {
	// WriteTo sends data to the given destination on the client's base socket.
	WriteTo func(data []byte, to net.Addr) (int, error)
	// PerformTransaction runs a STUN transaction against the given destination.
	PerformTransaction func(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error)
	// OnDeallocated is called once de-allocation of the relayed address is complete.
	OnDeallocated func(relayedAddr net.Addr)

	RelayedAddr               net.Addr
	ServerAddr                net.Addr
	Integrity                 stun.MessageIntegrity
	Nonce                     stun.Nonce
	Username                  stun.Username
	Realm                     stun.Realm
	Lifetime                  time.Duration
	PermissionRefreshInterval time.Duration
	BindingRefreshInterval    time.Duration
	BindingCheckInterval      time.Duration

	// AbortTransactions, when set, interrupts the allocation's pending
	// transaction waits. Called once by UDPConn when it starts closing, so
	// Close does not wait out the retransmission budget of in-flight
	// transactions against an unresponsive server.
	AbortTransactions func()
}

type allocation struct {
	clientHooks                             // Read-only
	relayedAddr       net.Addr              // Read-only
	serverAddr        net.Addr              // Read-only
	permMap           *permissionMap        // Thread-safe
	integrity         stun.MessageIntegrity // Read-only
	username          stun.Username         // Read-only
	realm             stun.Realm            // Read-only
	_nonce            stun.Nonce            // Needs mutex x
	_lifetime         time.Duration         // Needs mutex x
	refreshAllocTimer *PeriodicTimer        // Thread-safe
	refreshPermsTimer *PeriodicTimer        // Thread-safe
	mutex             sync.RWMutex          // Thread-safe

	// onPermRefreshFailure, when set, observes a permission refresh that kept
	// failing after retries. Read-only after construction.
	onPermRefreshFailure func(error)

	// onAllocRefreshFailure, when set, observes a permanent allocation-refresh
	// failure: one exhausted refresh transaction, one well-formed non-438
	// error response, or stale-nonce retry exhaustion. Read-only after
	// construction.
	onAllocRefreshFailure func(error)

	// abortTransactions, when set, interrupts the allocation's pending
	// transaction waits. Read-only after construction.
	abortTransactions func()
}

// setNonceFromMsg updates the nonce from a 438 response carrying one. A 438
// without a NONCE leaves the stale nonce standing, so the retry loop exhausts
// and the failure reaches the caller's disposition as a value.
func (a *allocation) setNonceFromMsg(msg *stun.Message) {
	var nonce stun.Nonce
	if err := nonce.GetFrom(msg); err == nil {
		a.setNonce(nonce)
	}
}

func (a *allocation) refreshAllocation(lifetime time.Duration, dontWait bool) error {
	msg, err := stun.Build(
		stun.TransactionID,
		stun.NewType(stun.MethodRefresh, stun.ClassRequest),
		proto.Lifetime{Duration: lifetime},
		a.username,
		a.realm,
		a.nonce(),
		a.integrity,
		stun.Fingerprint,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", errFailedToBuildRefreshRequest, err)
	}

	trRes, err := a.performTransaction(msg, a.serverAddr, dontWait)
	if err != nil {
		return fmt.Errorf("%w: %w", errFailedToRefreshAllocation, err)
	}

	if dontWait {
		return nil
	}

	res := trRes.Msg
	if res.Type.Class == stun.ClassErrorResponse {
		var code stun.ErrorCodeAttribute
		if err = code.GetFrom(res); err == nil {
			if code.Code == stun.CodeStaleNonce {
				a.setNonceFromMsg(res)

				return errTryAgain
			}

			// A well-formed non-438 error response is a permanent refresh
			// rejection: surface it as a typed value.
			return &stun.TurnError{
				StunMessageType: res.Type,
				ErrorCodeAttr:   code,
			}
		}

		return fmt.Errorf("%s", res.Type) //nolint:err113
	}

	// Getting lifetime from response
	var updatedLifetime proto.Lifetime
	if err := updatedLifetime.GetFrom(res); err != nil {
		return fmt.Errorf("%w: %w", errFailedToGetLifetime, err)
	}

	a.setLifetime(updatedLifetime.Duration)

	return nil
}

func (a *allocation) refreshPermissions() error {
	addrs := a.permMap.addrs()
	if len(addrs) == 0 {
		return nil
	}
	if err := a.CreatePermissions(addrs...); err != nil {
		return err
	}

	return nil
}

func (a *allocation) onRefreshTimers(id int) {
	switch id {
	case timerIDRefreshAlloc:
		a.refreshAllocationWithRetries()
	case timerIDRefreshPerms:
		a.refreshPermissionsWithRetries()
	}
}

func (a *allocation) refreshAllocationWithRetries() {
	var err error
	lifetime := a.lifetime()
	// Limit the max retries on errTryAgain to 3
	// when stale nonce returns, sencond retry should succeed
	for range maxRetryAttempts {
		err = a.refreshAllocation(lifetime, false)
		if !errors.Is(err, errTryAgain) {
			break
		}
	}
	if err != nil && a.onAllocRefreshFailure != nil {
		// The periodic schedule at lifetime/2 offers no effective retry
		// before server expiry: a standing failure is permanent.
		a.onAllocRefreshFailure(err)
	}
}

func (a *allocation) refreshPermissionsWithRetries() {
	var err error
	for range maxRetryAttempts {
		err = a.refreshPermissions()
		if !errors.Is(err, errTryAgain) {
			break
		}
	}
	if err != nil && a.onPermRefreshFailure != nil {
		a.onPermRefreshFailure(err)
	}
}

func (a *allocation) nonce() stun.Nonce {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	return a._nonce
}

func (a *allocation) setNonce(nonce stun.Nonce) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	a._nonce = nonce
}

func (a *allocation) lifetime() time.Duration {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	return a._lifetime
}

func (a *allocation) setLifetime(lifetime time.Duration) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	a._lifetime = lifetime
}
