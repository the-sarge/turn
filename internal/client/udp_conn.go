// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package client implements the API for a TURN client
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/pion/stun/v3"
	"github.com/the-sarge/turn/v5/internal/proto"
)

const (
	maxReadQueueSize              = 1024
	defaultPermRefreshInterval    = 120 * time.Second
	defaultBindingRefreshInterval = 5 * time.Minute
	defaultBindingCheckInterval   = 30 * time.Second
	channelBindingLifetime        = 10 * time.Minute
	maxRetryAttempts              = 3
)

const (
	timerIDRefreshAlloc int = iota
	timerIDRefreshPerms
	timerIDCheckBindings
)

// inboundData is one relayed datagram. from is the canonical peer label,
// stored at creation so ReadFrom returns it without per-packet conversion.
type inboundData struct {
	data []byte
	from netip.AddrPort
}

// UDPConn is one live UDP relay allocation. Peer addresses cross its methods
// as canonical netip.AddrPort values; the root package owns canonicalization
// and validation, so every peer reaching this type is already canonical.
type UDPConn struct {
	// Package-crossing operations are immutable production/mock adapters. They
	// do not own or mutate Allocation lifecycle state.
	writeTo            func(data []byte, to net.Addr) (int, error)
	performTransaction func(msg *stun.Message, to net.Addr, dontWait bool) (TransactionResult, error)
	onDeallocated      func(relayedAddr net.Addr)
	abortTransactions  func()

	relayedAddr net.Addr // Read-only
	serverAddr  net.Addr // Read-only
	permMap     *permissionMap
	integrity   stun.MessageIntegrity // Read-only
	username    stun.Username         // Read-only
	realm       stun.Realm            // Read-only
	_nonce      stun.Nonce            // Protected by mutex
	_lifetime   time.Duration         // Protected by mutex

	refreshAllocTimer      *PeriodicTimer
	refreshPermsTimer      *PeriodicTimer
	mutex                  sync.RWMutex // Protects nonce and lifetime
	bindingMgr             *bindingManager
	checkBindingsTimer     *PeriodicTimer
	readCh                 chan *inboundData
	closeCh                chan struct{}
	closeMutex             sync.Mutex     // Also gates workerWG.Add vs close
	workerWG               sync.WaitGroup // Joins bind/permission workers on Close
	bindingRefreshInterval time.Duration  // Read-only

	// terminalCause is the recorded cause of a self-seal (refresh failure or
	// ChannelBind 400), set exactly once inside startClose's guarded arm.
	// Protected by closeMutex; nil when the caller performed the seal.
	terminalCause error
	// callerClosed records that the caller's Close has run, so a repeated
	// caller Close returns net.ErrClosed. Protected by closeMutex.
	callerClosed bool
}

// NewUDPConn creates a new instance of UDPConn. abortTransactions is a
// required capability: every allocation must be able to wake its pending
// transaction waits before deallocation and lifetime-zero release.
func NewUDPConn(config *AllocationConfig, abortTransactions func()) *UDPConn {
	if abortTransactions == nil {
		panic("client: missing abort capability") //nolint:forbidigo // Programmer-invalid internal construction.
	}

	conn := &UDPConn{
		writeTo:                config.WriteTo,
		performTransaction:     config.PerformTransaction,
		onDeallocated:          config.OnDeallocated,
		abortTransactions:      abortTransactions,
		relayedAddr:            config.RelayedAddr,
		serverAddr:             config.ServerAddr,
		permMap:                newPermissionMap(),
		integrity:              config.Integrity,
		username:               config.Username,
		realm:                  config.Realm,
		_nonce:                 config.Nonce,
		_lifetime:              config.Lifetime,
		bindingMgr:             newBindingManager(),
		readCh:                 make(chan *inboundData, maxReadQueueSize),
		closeCh:                make(chan struct{}),
		bindingRefreshInterval: defaultBindingRefreshInterval,
	}

	if config.BindingRefreshInterval != 0 {
		conn.bindingRefreshInterval = config.BindingRefreshInterval
	}
	conn.refreshAllocTimer = NewPeriodicTimer(
		timerIDRefreshAlloc,
		conn.onRefreshTimers,
		conn.lifetime()/2,
	)

	permRefreshInterval := defaultPermRefreshInterval
	if config.PermissionRefreshInterval != 0 {
		permRefreshInterval = config.PermissionRefreshInterval
	}

	conn.refreshPermsTimer = NewPeriodicTimer(
		timerIDRefreshPerms,
		conn.onRefreshTimers,
		permRefreshInterval,
	)

	bindingCheckInterval := defaultBindingCheckInterval
	if config.BindingCheckInterval != 0 {
		bindingCheckInterval = config.BindingCheckInterval
	}

	conn.checkBindingsTimer = NewPeriodicTimer(
		timerIDCheckBindings,
		func(timerID int) {
			for _, bound := range conn.bindingMgr.all() {
				conn.maybeBind(bound)
			}
		},
		bindingCheckInterval,
	)

	conn.refreshAllocTimer.Start()
	conn.refreshPermsTimer.Start()
	conn.checkBindingsTimer.Start()

	return conn
}

// ReadFrom reads one relayed datagram, copying the payload into p. It returns
// the number of bytes copied and the canonical source peer, blocking until a
// datagram arrives or the allocation closes.
func (c *UDPConn) ReadFrom(p []byte) (int, netip.AddrPort, error) {
	select {
	case ibData := <-c.readCh:
		n := copy(p, ibData.data)
		if n < len(ibData.data) {
			return 0, netip.AddrPort{}, io.ErrShortBuffer
		}

		return n, ibData.from, nil

	case <-c.closeCh:
		return 0, netip.AddrPort{}, c.closedErr()
	}
}

func (c *UDPConn) createPermission(perm *permission, addr netip.AddrPort) error {
	perm.mutex.Lock()
	defer perm.mutex.Unlock()

	if perm.state() == permStateIdle {
		// Punch a hole! (this would block a bit..)
		if err := c.CreatePermissions(addr); err != nil {
			c.permMap.delete(addr)

			return err
		}
		perm.setState(permStatePermitted)
	}

	return nil
}

// PreparePeer creates a permission for the canonical peer and waits until the
// TURN server confirms a channel binding for it. After it returns nil, writes
// to peer use ChannelData (or fail) for the lifetime of the allocation; they
// never fall back to Send indications. Concurrent callers for the same peer
// share one permission and one bind attempt; canceling ctx wakes only that
// caller (with its cause) and leaves the shared work running.
func (c *UDPConn) PreparePeer(ctx context.Context, peer netip.AddrPort) error {
	if ctx == nil {
		return errNilContext
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if c.isClosed() {
		return c.closedErr()
	}

	if err := c.awaitPermission(ctx, peer); err != nil {
		return err
	}

	bound, ok := c.bindingMgr.getOrCreate(peer)
	if !ok {
		return ErrChannelBindFailed
	}

	return c.awaitBinding(ctx, bound)
}

// awaitPermission blocks until a permission for peer is installed, the shared
// create attempt fails, or ctx is canceled.
func (c *UDPConn) awaitPermission(ctx context.Context, peer netip.AddrPort) error {
	for {
		perm := c.permMap.getOrCreate(peer)
		if perm.state() == permStatePermitted {
			return nil
		}

		done := c.ensurePermissionAttempt(perm, peer)
		if done == nil {
			return c.closedErr()
		}

		select {
		case <-done:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.closeCh:
			return c.closedErr()
		}

		if perm.state() == permStatePermitted {
			return nil
		}
		perm.attemptMutex.Lock()
		err := perm.attemptErr
		perm.attemptMutex.Unlock()
		if err != nil {
			return err
		}
		// The attempt we joined predates our loop iteration; re-evaluate.
	}
}

// ensurePermissionAttempt returns a channel that closes when the in-flight
// CreatePermission attempt (existing or newly started) completes. It returns
// nil once the allocation is closing.
func (c *UDPConn) ensurePermissionAttempt(perm *permission, peer netip.AddrPort) chan struct{} {
	perm.attemptMutex.Lock()
	defer perm.attemptMutex.Unlock()

	if perm.attemptDone != nil {
		return perm.attemptDone
	}
	if !c.addWorker() {
		return nil
	}

	done := make(chan struct{})
	perm.attemptDone = done
	go func() {
		defer c.workerWG.Done()
		var err error
		for range maxRetryAttempts {
			if c.isClosed() {
				// Seal precedence: a waiter that joins this attempt gets the
				// recorded terminal cause, exactly like an operation started
				// after the seal.
				err = c.closedErr()

				break
			}
			if err = c.createPermission(perm, peer); !errors.Is(err, errTryAgain) {
				break
			}
		}
		perm.attemptMutex.Lock()
		perm.attemptDone = nil
		perm.attemptErr = err
		perm.attemptMutex.Unlock()
		close(done)
	}()

	return done
}

// awaitBinding blocks until the server confirms the channel binding, the
// binding fails, or ctx is canceled.
func (c *UDPConn) awaitBinding(ctx context.Context, bound *binding) error { //nolint:cyclop
	for {
		if final, err := bindingResult(bound); final {
			return err
		}

		bound.muBind.Lock()
		done := bound.attemptDone
		if done == nil {
			done = c.startBindAttemptLocked(bound)
		}
		bound.muBind.Unlock()

		if done == nil {
			// No attempt is needed (state already decisive) or none can start (closing).
			if final, err := bindingResult(bound); final {
				return err
			}
			if c.isClosed() {
				return c.closedErr()
			}

			return ErrChannelBindFailed
		}

		select {
		case <-done:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.closeCh:
			return c.closedErr()
		}

		if final, err := bindingResult(bound); final {
			return err
		}
		// The joined attempt ended without confirming; surface its error rather
		// than retrying forever on the caller's behalf.
		if err := bound.bindErr(); err != nil {
			return err
		}
	}
}

// bindingResult reports whether the binding reached a decisive state for a
// preparing caller: (true, nil) once the server has confirmed the channel
// mapping, (true, err) once the binding failed or its confirmation expired.
func bindingResult(bound *binding) (bool, error) {
	if bound.ok() {
		if time.Since(bound.refreshedAt()) >= channelBindingLifetime {
			bound.terminalize(ErrChannelBindingExpired)

			return true, ErrChannelBindingExpired
		}
		bound.prepared.Store(true)

		return true, nil
	}
	if bound.state() == bindingStateFailed {
		if err := bound.bindErr(); err != nil {
			return true, err
		}

		return true, ErrChannelBindFailed
	}

	return false, nil
}

// startBindAttemptLocked starts a tracked bind attempt if the binding state
// calls for one. It requires bound.muBind to be held and returns the channel
// that closes when the attempt completes, or nil if no attempt was started.
func (c *UDPConn) startBindAttemptLocked(bound *binding) chan struct{} {
	if !c.addWorker() {
		return nil
	}
	startState, ok := c.startBinding(bound)
	if !ok {
		c.workerWG.Done()

		return nil
	}

	done := make(chan struct{})
	bound.attemptDone = done
	go func() {
		defer c.workerWG.Done()
		err := c.bindChannel(bound, startState)
		bound.setBindErr(err)
		bound.muBind.Lock()
		bound.attemptDone = nil
		bound.muBind.Unlock()
		close(done)
	}()

	return done
}

// addWorker registers an allocation-owned goroutine with the close join.
// It returns false once the allocation has begun closing.
func (c *UDPConn) addWorker() bool {
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

	if c.isClosed() {
		return false
	}
	c.workerWG.Add(1)

	return true
}

// failPreparedBindings terminalizes every prepared binding: once a peer is
// prepared, losing its permission must fail its writes with the recorded
// cause; there is no other write path to fall back to.
func (c *UDPConn) failPreparedBindings(err error) {
	for _, bound := range c.bindingMgr.all() {
		if bound.prepared.Load() {
			bound.terminalize(fmt.Errorf("%w: %w", ErrPermissionRefreshFailed, err))
		}
	}
}

// WriteTo writes payload to the canonical peer addr as ChannelData over the
// peer's prepared channel binding. It is the single guard of the prepared-only
// write invariant: a peer that PreparePeer has not confirmed on this
// allocation gets ErrNotPrepared with zero network output; a prepared binding
// that has since expired or failed returns its recorded cause with zero
// network output. WriteTo never creates a permission, starts a bind, or emits
// anything other than ChannelData — no Send-indication constructor exists in
// this client.
func (c *UDPConn) WriteTo(payload []byte, addr netip.AddrPort) (int, error) {
	if c.isClosed() {
		return 0, c.closedErr()
	}

	bound, ok := c.bindingMgr.findByAddr(addr)
	if !ok || !bound.prepared.Load() {
		return 0, ErrNotPrepared
	}

	if bound.ok() && time.Since(bound.refreshedAt()) >= channelBindingLifetime {
		bound.terminalize(ErrChannelBindingExpired)
	}
	if !bound.ok() {
		if bindErr := bound.bindErr(); bindErr != nil {
			return 0, bindErr
		}

		return 0, ErrChannelBindFailed
	}

	return c.sendChannelData(payload, bound.number)
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
// Close returns only after allocation-owned goroutines (refresh timers and
// bind/permission workers) have finished. It never closes or sets deadlines on
// the caller-owned base socket, so a worker blocked on that socket is joined
// only once the caller unblocks its I/O.
//
// Close always joins, then returns: the lifetime-0 emission error (or nil)
// when this caller performed the seal; the recorded terminal cause when the
// allocation had already sealed itself (refresh failure or ChannelBind 400);
// net.ErrClosed on a repeated caller Close.
func (c *UDPConn) Close() error {
	// The caller-duplicate decision and the seal-ownership decision happen in
	// one closeMutex hold, so concurrent caller Closes cannot disagree about
	// which of them performed the seal: exactly one caller observes the
	// emission result and every duplicate returns net.ErrClosed.
	c.closeMutex.Lock()
	repeat := c.callerClosed
	c.callerClosed = true
	first, emitErr := c.startCloseLocked(nil)
	c.closeMutex.Unlock()

	c.refreshAllocTimer.StopAndWait()
	c.refreshPermsTimer.StopAndWait()
	c.checkBindingsTimer.StopAndWait()
	c.workerWG.Wait()

	if first {
		return emitErr
	}
	if repeat {
		return net.ErrClosed
	}

	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

	return c.terminalCause
}

// startClose makes the allocation refuse new work and emits the deallocate
// refresh. It performs no joins, so allocation-owned workers may call it
// safely; a worker seal passes its cause, the caller's Close passes nil.
//
// The closeCh select is the double-emission guard: a self-seal racing a
// caller Close yields exactly one lifetime-0 emission and one recorded
// terminal cause, and a seal attempt after the allocation is already sealed
// (such as an in-flight refresh aborted by Close) records nothing.
func (c *UDPConn) startClose(cause error) {
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

	_, _ = c.startCloseLocked(cause)
}

// startCloseLocked is startClose's body; it requires closeMutex to be held.
func (c *UDPConn) startCloseLocked(cause error) (bool, error) {
	c.refreshAllocTimer.Stop()
	c.refreshPermsTimer.Stop()
	c.checkBindingsTimer.Stop()

	select {
	case <-c.closeCh:
		return false, nil
	default:
		close(c.closeCh)
	}

	// Wake workers blocked on in-flight transaction waits so Close does not
	// wait out the retransmission budget against an unresponsive server.
	c.abortTransactions()

	c.onDeallocated(c.relayedAddr)

	emitErr := c.refreshAllocation(0, true /* dontWait=true */)
	if cause != nil {
		// Self-seal: record the terminal cause; a failed lifetime-0 emission
		// is joined into it so the caller's Close can still observe it.
		c.terminalCause = cause
		if emitErr != nil {
			c.terminalCause = errors.Join(cause, emitErr)
		}
	}

	return true, emitErr
}

// closedErr is the operation error for a sealed allocation: net.ErrClosed,
// wrapped with the recorded terminal cause when the allocation sealed itself.
func (c *UDPConn) closedErr() error {
	c.closeMutex.Lock()
	cause := c.terminalCause
	c.closeMutex.Unlock()

	if cause != nil {
		return fmt.Errorf("%w: %w", net.ErrClosed, cause)
	}

	return net.ErrClosed
}

// LocalAddr returns the local network address.
func (c *UDPConn) LocalAddr() net.Addr {
	return c.relayedAddr
}

func (c *UDPConn) isClosed() bool {
	select {
	case <-c.closeCh:
		return true
	default:
		return false
	}
}

// peerAddress converts a canonical peer to its wire attribute form.
func peerAddress(addr netip.AddrPort) proto.PeerAddress {
	return proto.PeerAddress{
		IP:   net.IP(addr.Addr().AsSlice()),
		Port: int(addr.Port()),
	}
}

// CreatePermissions Issues a CreatePermission request for the supplied addresses
// as described in https://datatracker.ietf.org/doc/html/rfc5766#section-9
func (c *UDPConn) CreatePermissions(addrs ...netip.AddrPort) error {
	setters := []stun.Setter{
		stun.TransactionID,
		stun.NewType(stun.MethodCreatePermission, stun.ClassRequest),
	}

	for _, addr := range addrs {
		setters = append(setters, peerAddress(addr))
	}

	setters = append(setters,
		c.username,
		c.realm,
		c.nonce(),
		c.integrity,
		stun.Fingerprint)

	msg, err := stun.Build(setters...)
	if err != nil {
		return err
	}

	trRes, err := c.performTransaction(msg, c.serverAddr, false)
	if err != nil {
		return err
	}

	res := trRes.Msg

	if res.Type.Class == stun.ClassErrorResponse {
		var code stun.ErrorCodeAttribute
		if err = code.GetFrom(res); err == nil {
			if code.Code == stun.CodeStaleNonce {
				c.setNonceFromMsg(res)

				return errTryAgain
			}

			turnError := &stun.TurnError{
				StunMessageType: res.Type,
				ErrorCodeAttr:   code,
			}

			return turnError
		}

		return fmt.Errorf("%s", res.Type) //nolint // dynamic errors
	}

	return nil
}

// HandleInbound passes one relayed datagram to the allocation. from is the
// canonical source peer label, stored as-is and returned by ReadFrom.
func (c *UDPConn) HandleInbound(data []byte, from netip.AddrPort) {
	// Copy data
	copied := make([]byte, len(data))
	copy(copied, data)

	select {
	case c.readCh <- &inboundData{data: copied, from: from}:
	default:
		// The receive queue is full: the datagram is dropped, matching UDP
		// semantics. Documented on Allocation.ReadFrom.
	}
}

// FindAddrByChannelNumber returns the canonical peer address associated with
// the channel number on this UDPConn.
func (c *UDPConn) FindAddrByChannelNumber(chNum uint16) (netip.AddrPort, bool) {
	b, ok := c.bindingMgr.findByNumber(chNum)
	if !ok {
		return netip.AddrPort{}, false
	}

	return b.addr, true
}

func (c *UDPConn) maybeBind(bound *binding) {
	// Block only callers with the same binding until
	// the binding transaction has been started
	bound.muBind.Lock()
	defer bound.muBind.Unlock()

	if bound.attemptDone == nil {
		// Establish binding with the server if the state machine allows it.
		c.startBindAttemptLocked(bound)
	}
}

func (c *UDPConn) startBinding(bound *binding) (bindingState, bool) {
	startState := bound.state()
	switch {
	case startState == bindingStateIdle || startState == bindingStateUnknown:
		bound.setState(bindingStateRequest)
	case startState == bindingStateReadyUnknown:
		bound.setState(bindingStateRefresh)
	case startState == bindingStateReady && time.Since(bound.refreshedAt()) > c.bindingRefreshInterval:
		bound.setState(bindingStateRefresh)
	default:
		return startState, false
	}

	return startState, true
}

// bindChannel performs one ChannelBind attempt. It returns nil when the
// binding was confirmed or recovered, and the attempt's error otherwise.
func (c *UDPConn) bindChannel(bound *binding, startState bindingState) error {
	var err error
	for range maxRetryAttempts {
		if c.isClosed() {
			// Seal precedence: the binding result a waiter joins carries the
			// recorded terminal cause, exactly like an operation started after
			// the seal.
			return c.closedErr()
		}
		if err = c.bind(bound); !errors.Is(err, errTryAgain) {
			break
		}
	}
	if err != nil {
		if c.isClosed() {
			// Closing: the binding state no longer matters, and an aborted
			// transaction must not count as a bind failure.
			return err
		}
		if c.handleBindChannelError(bound, startState, err) {
			return nil
		}

		return err
	}

	bound.setRefreshedAt(time.Now())
	bound.setState(bindingStateReady)

	return nil
}

// handleBindChannelError reports whether the binding recovered (kept usable).
func (c *UDPConn) handleBindChannelError(bound *binding, startState bindingState, err error) bool {
	if c.recoverChannelBindBadRequest(bound, startState, err) {
		return true
	}

	if errors.Is(err, errChannelBindTransactionFailed) {
		if bindingStateWasReady(startState) {
			bound.setState(bindingStateReadyUnknown)
		} else {
			bound.setState(bindingStateUnknown)
		}

		return false
	}

	bound.setState(bindingStateFailed)
	if errors.Is(err, errChannelBindBadRequest) {
		c.closeAfterChannelBindBadRequest(err)
	}

	return false
}

func (c *UDPConn) recoverChannelBindBadRequest(bound *binding, startState bindingState, err error) bool {
	if !errors.Is(err, errChannelBindBadRequest) {
		return false
	}
	if !bindingStateWasReady(startState) {
		return false
	}

	// If this binding was previously confirmed, a refresh transaction failure or
	// unexpected 400 does not prove that the saved channel mapping is wrong. The
	// server may still have the old binding, and switching channels would be
	// worse because it can trigger "same peer with different channel number" (like what we get from Coturn).
	// This Keep the saved mapping usable and retry refresh later.
	bound.setState(bindingStateReady)

	return true
}

func bindingStateWasReady(state bindingState) bool {
	return state == bindingStateReady || state == bindingStateReadyUnknown
}

// closeAfterChannelBindBadRequest terminalizes the whole allocation after a
// ChannelBind 400 on a fresh binding: startClose, not Close, because this
// runs on an allocation-owned bind worker, which must not join itself. The
// caller's Close still joins every worker and observes the recorded cause; a
// failed lifetime-0 emission is joined into that cause by startClose.
func (c *UDPConn) closeAfterChannelBindBadRequest(bindErr error) {
	c.startClose(fmt.Errorf("%w: %w", ErrChannelBindFailed, bindErr))
}

func (c *UDPConn) bind(bound *binding) error {
	setters := []stun.Setter{
		stun.TransactionID,
		stun.NewType(stun.MethodChannelBind, stun.ClassRequest),
		peerAddress(bound.addr),
		proto.ChannelNumber(bound.number),
		c.username,
		c.realm,
		c.nonce(),
		c.integrity,
		stun.Fingerprint,
	}

	msg, err := stun.Build(setters...)
	if err != nil {
		return err
	}

	trRes, err := c.performTransaction(msg, c.serverAddr, false)
	if err != nil {
		return fmt.Errorf("%w: %w", errChannelBindTransactionFailed, err)
	}

	res := trRes.Msg
	if res.Type.Class == stun.ClassErrorResponse {
		return c.handleChannelBindErrorResponse(res)
	}

	// Success.
	return nil
}

func (c *UDPConn) handleChannelBindErrorResponse(res *stun.Message) error {
	var code stun.ErrorCodeAttribute
	if err := code.GetFrom(res); err != nil {
		return fmt.Errorf("%w: unexpected response type %s", errCannotBindChannel, res.Type) // nolint:err113
	}

	if code.Code == stun.CodeStaleNonce {
		c.setNonceFromMsg(res)

		return errTryAgain
	}

	turnError := &stun.TurnError{
		StunMessageType: res.Type,
		ErrorCodeAttr:   code,
	}
	if code.Code == stun.CodeBadRequest {
		return fmt.Errorf(
			"%w: %w: received error %d: %w",
			errCannotBindChannel, errChannelBindBadRequest, code.Code, turnError,
		)
	}

	return fmt.Errorf("%w: received error %d: %w", errCannotBindChannel, code.Code, turnError) // nolint:err113
}

func (c *UDPConn) sendChannelData(data []byte, chNum uint16) (int, error) {
	chData := &proto.ChannelData{
		Data:   data,
		Number: proto.ChannelNumber(chNum),
	}
	chData.Encode()
	_, err := c.writeTo(chData.Raw, c.serverAddr)
	if err != nil {
		return 0, err
	}

	return len(data), nil
}
