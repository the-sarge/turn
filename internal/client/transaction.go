// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/pion/stun/v3"
)

const (
	maxRtxInterval time.Duration = 1600 * time.Millisecond
	maxRtxCount                  = 7 // Initial request plus six retries.
)

type transactionResult struct {
	msg *stun.Message
	err error
}

type transactionRegistryEntry struct {
	id       [stun.TransactionIDSize]byte
	raw      []byte
	interval time.Duration
	nRtx     int
	timer    *time.Timer
	resultCh chan transactionResult
}

// TransactionRegistry owns the live transaction set and its terminal claims.
type TransactionRegistry struct {
	send  func([]byte) (int, error)
	rto   time.Duration
	mutex sync.Mutex
	live  map[[stun.TransactionIDSize]byte]*transactionRegistryEntry
}

// NewTransactionRegistry creates a registry that can only send on the caller-owned socket.
func NewTransactionRegistry(send func([]byte) (int, error), rto time.Duration) *TransactionRegistry {
	return &TransactionRegistry{
		send: send,
		rto:  rto,
		live: make(map[[stun.TransactionIDSize]byte]*transactionRegistryEntry),
	}
}

// Perform registers, initially sends, and waits for one transaction.
func (r *TransactionRegistry) Perform(msg *stun.Message) (*stun.Message, error) {
	entry, err := r.begin(msg, make(chan transactionResult, 1))
	if err != nil {
		return nil, err
	}

	return waitForTransaction(entry)
}

// Start registers and initially sends one fire-and-forget transaction.
func (r *TransactionRegistry) Start(msg *stun.Message) error {
	_, err := r.begin(msg, nil)

	return err
}

// PerformWithContext performs one transaction whose private wait may be canceled.
func (r *TransactionRegistry) PerformWithContext(
	ctx context.Context,
	msg *stun.Message,
) (*stun.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	entry, err := r.begin(msg, make(chan transactionResult, 1))
	if err != nil {
		return nil, err
	}

	select {
	case result, ok := <-entry.resultCh:
		return finishResult(result, ok)
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

		return nil, context.Cause(ctx)
	}
	r.mutex.Unlock()

	return waitForTransaction(entry)
}

func (r *TransactionRegistry) begin(
	msg *stun.Message,
	resultCh chan transactionResult,
) (*transactionRegistryEntry, error) {
	entry := &transactionRegistryEntry{
		id:       msg.TransactionID,
		raw:      append([]byte(nil), msg.Raw...),
		interval: r.rto,
		resultCh: resultCh,
	}

	r.mutex.Lock()
	if _, exists := r.live[msg.TransactionID]; exists {
		r.mutex.Unlock()

		return nil, fmt.Errorf("%w: %s", errTransactionAlreadyExists, transactionKey(msg.TransactionID))
	}
	r.live[msg.TransactionID] = entry
	r.mutex.Unlock()

	if _, err := r.send(entry.raw); err != nil {
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
		publishResult(entry, transactionResult{
			err: fmt.Errorf("%w: transaction %s", ErrTransactionTimeout, transactionKey(entry.id)),
		})

		return
	}
	r.mutex.Unlock()

	_, err := r.send(entry.raw)

	r.mutex.Lock()
	if r.live[entry.id] != entry {
		r.mutex.Unlock()

		return
	}
	if err != nil {
		delete(r.live, entry.id)
		r.mutex.Unlock()
		publishResult(entry, transactionResult{
			err: fmt.Errorf("%w: transaction %s: %w", errFailedToRetransmitTransaction, transactionKey(entry.id), err),
		})

		return
	}
	r.armTimerLocked(entry)
	r.mutex.Unlock()
}

func transactionKey(id [stun.TransactionIDSize]byte) string {
	return b64.StdEncoding.EncodeToString(id[:])
}

func publishResult(entry *transactionRegistryEntry, result transactionResult) {
	if entry.resultCh != nil {
		entry.resultCh <- result
	}
}

func waitForTransaction(entry *transactionRegistryEntry) (*stun.Message, error) {
	result, ok := <-entry.resultCh

	return finishResult(result, ok)
}

func finishResult(result transactionResult, ok bool) (*stun.Message, error) {
	if !ok {
		result.err = errTransactionClosed
	}

	return result.msg, result.err
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
		publishResult(entry, transactionResult{msg: msg})
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
