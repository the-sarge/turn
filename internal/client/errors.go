// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"errors"
	"fmt"
	"net"
)

// Exported sentinels: the failure classes a consumer can act on. The root
// package re-exports them.
var (
	// ErrTransactionTimeout reports a STUN transaction whose retransmissions
	// were all exhausted without a server response.
	ErrTransactionTimeout = errors.New("transaction timeout: all retransmissions failed")
	// ErrAllocationRefreshFailed reports a permanent allocation-refresh
	// failure. The allocation seals itself with this cause: pending waiters
	// wake, and every subsequent operation returns net.ErrClosed wrapped with
	// it.
	ErrAllocationRefreshFailed = errors.New("allocation refresh failed")
	// ErrPermissionRefreshFailed reports a permission refresh that kept
	// failing after retries; prepared bindings for the allocation terminalize
	// with it.
	ErrPermissionRefreshFailed = errors.New("permission refresh failed")
	// ErrChannelBindFailed reports a channel binding that could not be
	// established or has failed.
	ErrChannelBindFailed = errors.New("channel bind failed")
	// ErrChannelBindingExpired reports a confirmed channel binding whose
	// server-side lifetime has expired.
	ErrChannelBindingExpired = errors.New("confirmed channel binding expired")
	// ErrNotPrepared reports a write to a peer that has no prepared,
	// confirmed channel binding: nothing was sent.
	ErrNotPrepared = errors.New("peer not prepared: no confirmed channel binding")
)

var (
	errFake                                = errors.New("fake error")
	errTryAgain                            = errors.New("try again")
	errDoubleLock                          = errors.New("try-lock is already locked")
	errTransactionClosed                   = fmt.Errorf("transaction closed: %w", net.ErrClosed)
	errWaitForResultOnNonResultTransaction = errors.New("WaitForResult called on non-result transaction")
	errFailedToBuildRefreshRequest         = errors.New("failed to build refresh request")
	errFailedToRefreshAllocation           = errors.New("failed to refresh allocation")
	errFailedToGetLifetime                 = errors.New("failed to get lifetime from refresh response")
	errCannotBindChannel                   = errors.New("cannot bind channel")
	errChannelBindBadRequest               = errors.New("channel bind bad request")
	errChannelBindTransactionFailed        = errors.New("channel bind transaction failed")
	errNilContext                          = errors.New("context must not be nil")
)
