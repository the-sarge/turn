// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"errors"
	"fmt"
	"net"
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
}

// setNonceFromMsg updates the nonce from a 438 response carrying one. A 438
// without a NONCE leaves the stale nonce standing, so the retry loop exhausts
// and the failure reaches the caller's disposition as a value.
func (c *UDPConn) setNonceFromMsg(msg *stun.Message) {
	var nonce stun.Nonce
	if err := nonce.GetFrom(msg); err == nil {
		c.setNonce(nonce)
	}
}

func (c *UDPConn) refreshAllocation(lifetime time.Duration, dontWait bool) error {
	msg, err := stun.Build(
		stun.TransactionID,
		stun.NewType(stun.MethodRefresh, stun.ClassRequest),
		proto.Lifetime{Duration: lifetime},
		c.username,
		c.realm,
		c.nonce(),
		c.integrity,
		stun.Fingerprint,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", errFailedToBuildRefreshRequest, err)
	}

	trRes, err := c.performTransaction(msg, c.serverAddr, dontWait)
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
				c.setNonceFromMsg(res)

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

	c.setLifetime(updatedLifetime.Duration)

	return nil
}

func (c *UDPConn) refreshPermissions() error {
	addrs := c.permMap.addrs()
	if len(addrs) == 0 {
		return nil
	}
	if err := c.CreatePermissions(addrs...); err != nil {
		return err
	}

	return nil
}

func (c *UDPConn) onRefreshTimers(id int) {
	switch id {
	case timerIDRefreshAlloc:
		c.refreshAllocationWithRetries()
	case timerIDRefreshPerms:
		c.refreshPermissionsWithRetries()
	}
}

func (c *UDPConn) refreshAllocationWithRetries() {
	var err error
	lifetime := c.lifetime()
	// Limit the max retries on errTryAgain to 3
	// when stale nonce returns, sencond retry should succeed
	for range maxRetryAttempts {
		err = c.refreshAllocation(lifetime, false)
		if !errors.Is(err, errTryAgain) {
			break
		}
	}
	if err != nil {
		// The periodic schedule at lifetime/2 offers no effective retry
		// before server expiry: a standing failure is permanent. This worker
		// seals directly but never joins itself; caller Close remains the join.
		c.startClose(fmt.Errorf("%w: %w", ErrAllocationRefreshFailed, err))
	}
}

func (c *UDPConn) refreshPermissionsWithRetries() {
	var err error
	for range maxRetryAttempts {
		err = c.refreshPermissions()
		if !errors.Is(err, errTryAgain) {
			break
		}
	}
	if err != nil {
		// Permission failure terminalizes prepared bindings without sealing the
		// Allocation; there is no upward lifecycle callback or fallback emitter.
		c.failPreparedBindings(err)
	}
}

func (c *UDPConn) nonce() stun.Nonce {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c._nonce
}

func (c *UDPConn) setNonce(nonce stun.Nonce) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c._nonce = nonce
}

func (c *UDPConn) lifetime() time.Duration {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c._lifetime
}

func (c *UDPConn) setLifetime(lifetime time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c._lifetime = lifetime
}
