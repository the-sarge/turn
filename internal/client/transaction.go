// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/stun/v3"
)

const (
	maxRtxInterval time.Duration = 1600 * time.Millisecond
	maxRtxCount                  = 7 // Initial request plus six retries.
)

// TransactionResult is a bag of result values of a transaction.
type TransactionResult struct {
	Msg *stun.Message
	Err error
}

type transactionRegistryEntry struct {
	id       [stun.TransactionIDSize]byte
	raw      []byte
	to       net.Addr
	interval time.Duration
	nRtx     int
	timer    *time.Timer
	resultCh chan TransactionResult
}

// TransactionRegistry owns the live transaction set and its terminal claims.
type TransactionRegistry struct {
	send  func([]byte, net.Addr) (int, error)
	rto   time.Duration
	mutex sync.Mutex
	live  map[[stun.TransactionIDSize]byte]*transactionRegistryEntry
}

// NewTransactionRegistry creates a registry that can only send on the caller-owned socket.
func NewTransactionRegistry(send func([]byte, net.Addr) (int, error), rto time.Duration) *TransactionRegistry {
	return &TransactionRegistry{
		send: send,
		rto:  rto,
		live: make(map[[stun.TransactionIDSize]byte]*transactionRegistryEntry),
	}
}

// Perform registers, initially sends, and waits for one transaction.
func (r *TransactionRegistry) Perform(msg *stun.Message, to net.Addr) (TransactionResult, error) {
	entry, err := r.begin(msg, to, false)
	if err != nil {
		return TransactionResult{}, err
	}

	return waitForTransaction(entry)
}

// Start registers and initially sends one fire-and-forget transaction.
func (r *TransactionRegistry) Start(msg *stun.Message, to net.Addr) error {
	_, err := r.begin(msg, to, true)

	return err
}

// PerformWithContext performs one transaction whose private wait may be canceled.
func (r *TransactionRegistry) PerformWithContext(
	ctx context.Context,
	msg *stun.Message,
	to net.Addr,
) (TransactionResult, error) {
	if err := ctx.Err(); err != nil {
		return TransactionResult{}, context.Cause(ctx)
	}

	entry, err := r.begin(msg, to, false)
	if err != nil {
		return TransactionResult{}, err
	}

	select {
	case result, ok := <-entry.resultCh:
		return finishTransactionResult(result, ok)
	case <-ctx.Done():
	}

	r.mutex.Lock()
	if r.live[msg.TransactionID] == entry {
		delete(r.live, msg.TransactionID)
		if entry.timer != nil {
			entry.timer.Stop()
		}
		r.mutex.Unlock()
		close(entry.resultCh)

		return TransactionResult{}, context.Cause(ctx)
	}
	r.mutex.Unlock()

	return waitForTransaction(entry)
}

func (r *TransactionRegistry) begin(
	msg *stun.Message,
	to net.Addr,
	ignoreResult bool,
) (*transactionRegistryEntry, error) {
	entry := &transactionRegistryEntry{
		id:       msg.TransactionID,
		raw:      append([]byte(nil), msg.Raw...),
		to:       to,
		interval: r.rto,
	}
	if !ignoreResult {
		entry.resultCh = make(chan TransactionResult, 1)
	}

	r.mutex.Lock()
	if _, exists := r.live[msg.TransactionID]; exists {
		r.mutex.Unlock()

		return nil, fmt.Errorf("%w: %s", errTransactionAlreadyExists, transactionKey(msg.TransactionID))
	}
	r.live[msg.TransactionID] = entry
	r.mutex.Unlock()

	if _, err := r.send(entry.raw, entry.to); err != nil {
		r.mutex.Lock()
		if r.live[entry.id] == entry {
			delete(r.live, entry.id)
		}
		r.mutex.Unlock()

		return nil, err
	}

	r.mutex.Lock()
	if r.live[entry.id] == entry {
		r.armTimerLocked(entry)
	}
	r.mutex.Unlock()

	return entry, nil
}

func (r *TransactionRegistry) armTimerLocked(entry *transactionRegistryEntry) {
	entry.timer = time.AfterFunc(entry.interval, func() {
		r.retry(entry)
	})
}

func (r *TransactionRegistry) retry(entry *transactionRegistryEntry) {
	r.mutex.Lock()
	if r.live[entry.id] != entry {
		r.mutex.Unlock()

		return
	}

	entry.nRtx++
	entry.interval *= 2
	if entry.interval > maxRtxInterval {
		entry.interval = maxRtxInterval
	}
	if entry.nRtx == maxRtxCount {
		delete(r.live, entry.id)
		r.mutex.Unlock()
		publishTransactionResult(entry, TransactionResult{
			Err: fmt.Errorf("%w: transaction %s", ErrTransactionTimeout, transactionKey(entry.id)),
		})

		return
	}
	r.mutex.Unlock()

	_, err := r.send(entry.raw, entry.to)

	r.mutex.Lock()
	if r.live[entry.id] != entry {
		r.mutex.Unlock()

		return
	}
	if err != nil {
		delete(r.live, entry.id)
		r.mutex.Unlock()
		publishTransactionResult(entry, TransactionResult{
			Err: fmt.Errorf("%w: transaction %s: %w", errFailedToRetransmitTransaction, transactionKey(entry.id), err),
		})

		return
	}
	r.armTimerLocked(entry)
	r.mutex.Unlock()
}

func transactionKey(id [stun.TransactionIDSize]byte) string {
	return b64.StdEncoding.EncodeToString(id[:])
}

func publishTransactionResult(entry *transactionRegistryEntry, result TransactionResult) {
	if entry.resultCh != nil {
		entry.resultCh <- result
	}
}

func waitForTransaction(entry *transactionRegistryEntry) (TransactionResult, error) {
	result, ok := <-entry.resultCh

	return finishTransactionResult(result, ok)
}

func finishTransactionResult(result TransactionResult, ok bool) (TransactionResult, error) {
	if !ok {
		result.Err = errTransactionClosed
	}

	return result, result.Err
}

// Complete claims a matching response and publishes it to a waiting caller.
func (r *TransactionRegistry) Complete(msg *stun.Message) {
	r.mutex.Lock()
	entry, ok := r.live[msg.TransactionID]
	if ok {
		delete(r.live, msg.TransactionID)
		if entry.timer != nil {
			entry.timer.Stop()
		}
	}
	r.mutex.Unlock()
	if ok {
		publishTransactionResult(entry, TransactionResult{Msg: msg})
	}
}

// AbortCurrent atomically claims and closes every transaction currently live.
func (r *TransactionRegistry) AbortCurrent() {
	r.mutex.Lock()
	claimed := make([]*transactionRegistryEntry, 0, len(r.live))
	for id, entry := range r.live {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		claimed = append(claimed, entry)
		delete(r.live, id)
	}
	r.mutex.Unlock()

	for _, entry := range claimed {
		if entry.resultCh != nil {
			close(entry.resultCh)
		}
	}
}
