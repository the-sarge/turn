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
	// ErrTransactionTimeout is re-exported by the root package, which owns its public prose.
	ErrTransactionTimeout = errors.New("transaction timeout: all retransmissions failed")
	// ErrAllocationRefreshFailed is re-exported by the root package, which owns its public prose.
	ErrAllocationRefreshFailed = errors.New("allocation refresh failed")
	// ErrPermissionRefreshFailed is re-exported by the root package, which owns its public prose.
	ErrPermissionRefreshFailed = errors.New("permission refresh failed")
	// ErrChannelBindFailed is re-exported by the root package, which owns its public prose.
	ErrChannelBindFailed = errors.New("channel bind failed")
	// ErrChannelBindingExpired is re-exported by the root package, which owns its public prose.
	ErrChannelBindingExpired = errors.New("confirmed channel binding expired")
	// ErrNotPrepared is re-exported by the root package, which owns its public prose.
	ErrNotPrepared = errors.New("peer not prepared: no confirmed channel binding")
)

var (
	errTryAgain                      = errors.New("try again")
	errTransactionClosed             = fmt.Errorf("transaction closed: %w", net.ErrClosed)
	errTransactionAlreadyExists      = errors.New("transaction ID is already live")
	errFailedToRetransmitTransaction = errors.New("turn: failed to retransmit transaction")
	errFailedToBuildRefreshRequest   = errors.New("failed to build refresh request")
	errFailedToRefreshAllocation     = errors.New("failed to refresh allocation")
	errFailedToGetLifetime           = errors.New("failed to get lifetime from refresh response")
	errZeroRemainingLifetime         = errors.New("turn: allocation has zero remaining lifetime")
	errCannotBindChannel             = errors.New("cannot bind channel")
	errChannelBindBadRequest         = errors.New("channel bind bad request")
	errChannelBindTransactionFailed  = errors.New("channel bind transaction failed")
)
