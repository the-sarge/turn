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
	bindingMgr             *bindingManager   // Thread-safe
	checkBindingsTimer     *PeriodicTimer    // Thread-safe
	readCh                 chan *inboundData // Thread-safe
	closeCh                chan struct{}     // Thread-safe
	closeMutex             sync.Mutex        // Thread-safe; also gates workerWG.Add vs close
	workerWG               sync.WaitGroup    // Joins bind/permission workers on Close
	bindingRefreshInterval time.Duration     // Read-only
	allocation
}

// NewUDPConn creates a new instance of UDPConn.
func NewUDPConn(config *AllocationConfig) *UDPConn {
	conn := &UDPConn{
		bindingMgr:             newBindingManager(),
		readCh:                 make(chan *inboundData, maxReadQueueSize),
		closeCh:                make(chan struct{}),
		bindingRefreshInterval: defaultBindingRefreshInterval,
		allocation: allocation{
			clientHooks: clientHooks{
				writeTo:            config.WriteTo,
				performTransaction: config.PerformTransaction,
				onDeallocated:      config.OnDeallocated,
			},
			relayedAddr:       config.RelayedAddr,
			serverAddr:        config.ServerAddr,
			permMap:           newPermissionMap(),
			username:          config.Username,
			realm:             config.Realm,
			integrity:         config.Integrity,
			_nonce:            config.Nonce,
			_lifetime:         config.Lifetime,
			log:               config.Log,
			abortTransactions: config.AbortTransactions,
		},
	}

	if config.BindingRefreshInterval != 0 {
		conn.bindingRefreshInterval = config.BindingRefreshInterval
	}
	conn.onPermRefreshFailure = conn.failPreparedBindings

	conn.log.Debugf("Initial lifetime: %d seconds", int(conn.lifetime().Seconds()))

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

	if conn.refreshAllocTimer.Start() {
		conn.log.Debugf("Started refresh allocation timer")
	}
	if conn.refreshPermsTimer.Start() {
		conn.log.Debugf("Started refresh permission timer")
	}
	if conn.checkBindingsTimer.Start() {
		conn.log.Debugf("Started check bindings timer")
	}

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
		return 0, netip.AddrPort{}, &net.OpError{
			Op:   "read",
			Net:  c.LocalAddr().Network(),
			Addr: c.LocalAddr(),
			Err:  errClosed,
		}
	}
}

func (a *allocation) createPermission(perm *permission, addr netip.AddrPort) error {
	perm.mutex.Lock()
	defer perm.mutex.Unlock()

	if perm.state() == permStateIdle {
		// Punch a hole! (this would block a bit..)
		if err := a.CreatePermissions(addr); err != nil {
			a.permMap.delete(addr)

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
		return errClosed
	}

	if err := c.awaitPermission(ctx, peer); err != nil {
		return err
	}

	return c.awaitBinding(ctx, c.bindingMgr.getOrCreate(peer))
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
			return errClosed
		}

		select {
		case <-done:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.closeCh:
			return errClosed
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
				err = errClosed

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
				return errClosed
			}

			return errChannelBindFailed
		}

		select {
		case <-done:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.closeCh:
			return errClosed
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
			bound.terminalize(errChannelBindingExpired)

			return true, errChannelBindingExpired
		}
		bound.prepared.Store(true)

		return true, nil
	}
	if bound.state() == bindingStateFailed {
		if err := bound.bindErr(); err != nil {
			return true, err
		}

		return true, errChannelBindFailed
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
// prepared, losing its permission must fail writes rather than fall back to
// Send indications.
func (c *UDPConn) failPreparedBindings(err error) {
	for _, bound := range c.bindingMgr.all() {
		if bound.prepared.Load() {
			bound.terminalize(fmt.Errorf("%w: %w", errPermissionRefreshFailed, err))
		}
	}
}

// WriteTo writes a packet with payload to the canonical peer addr. Writes to
// a prepared peer use its channel binding or fail; writes to an unprepared
// peer may fall back to Send indications while a binding is established.
func (c *UDPConn) WriteTo(payload []byte, addr netip.AddrPort) (int, error) { //nolint:gocognit,cyclop
	var err error
	if c.isClosed() {
		return 0, &net.OpError{
			Op:   "write",
			Net:  c.LocalAddr().Network(),
			Addr: c.LocalAddr(),
			Err:  errClosed,
		}
	}

	// Check if we have a permission for the destination IP addr
	perm, ok := c.permMap.find(addr)
	if !ok {
		perm = &permission{}
		c.permMap.insert(addr, perm)
	}

	for range maxRetryAttempts {
		// c.createPermission() would block, per destination IP (, or perm),
		// until the perm state becomes "requested". Purpose of this is to
		// guarantee the order of packets (within the same perm).
		// Note that CreatePermission transaction may not be complete before
		// all the data transmission. This is done assuming that the request
		// will be most likely successful and we can tolerate some loss of
		// UDP packet (or reorder), inorder to minimize the latency in most cases.
		if err = c.createPermission(perm, addr); !errors.Is(err, errTryAgain) {
			break
		}
	}
	if err != nil {
		return 0, err
	}

	// Bind channel
	bound, ok := c.bindingMgr.findByAddr(addr)
	if !ok {
		bound = c.bindingMgr.create(addr)
	}

	// A prepared peer promised ChannelData-only writes: fail instead of ever
	// falling back to Send indications.
	if bound.prepared.Load() {
		if bound.ok() && time.Since(bound.refreshedAt()) >= channelBindingLifetime {
			bound.terminalize(errChannelBindingExpired)
		}
		if !bound.ok() {
			if bindErr := bound.bindErr(); bindErr != nil {
				return 0, bindErr
			}

			return 0, errChannelBindFailed
		}
	}

	//nolint:nestif
	if !bound.ok() {
		// Try to establish an initial binding with the server.
		// Writes still occur via indications meanwhile.
		c.maybeBind(bound)

		// Send data using SendIndication
		peerAddr := peerAddress(addr)
		var msg *stun.Message
		msg, err = stun.Build(
			stun.TransactionID,
			stun.NewType(stun.MethodSend, stun.ClassIndication),
			proto.Data(payload),
			peerAddr,
			stun.Fingerprint,
		)
		if err != nil {
			return 0, err
		}

		if _, err = c.writeTo(msg.Raw, c.serverAddr); err != nil {
			return 0, err
		}

		return len(payload), nil
	}

	// Binding is ready beyond this point, so send over it.
	_, err = c.sendChannelData(payload, bound.number)
	if err != nil {
		return 0, err
	}

	return len(payload), nil
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
// Close returns only after allocation-owned goroutines (refresh timers and
// bind/permission workers) have finished. It never closes or sets deadlines on
// the caller-owned base socket, so a worker blocked on that socket is joined
// only once the caller unblocks its I/O.
func (c *UDPConn) Close() error {
	first, err := c.startClose()

	c.refreshAllocTimer.StopAndWait()
	c.refreshPermsTimer.StopAndWait()
	c.checkBindingsTimer.StopAndWait()
	c.workerWG.Wait()

	if !first {
		return errAlreadyClosed
	}

	return err
}

// startClose makes the allocation refuse new work and emits the deallocate
// refresh. It performs no joins, so allocation-owned workers may call it safely.
func (c *UDPConn) startClose() (bool, error) {
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

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
	if c.abortTransactions != nil {
		c.abortTransactions()
	}

	c.onDeallocated(c.relayedAddr)

	return true, c.refreshAllocation(0, true /* dontWait=true */)
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
func (a *allocation) CreatePermissions(addrs ...netip.AddrPort) error {
	setters := []stun.Setter{
		stun.TransactionID,
		stun.NewType(stun.MethodCreatePermission, stun.ClassRequest),
	}

	for _, addr := range addrs {
		setters = append(setters, peerAddress(addr))
	}

	setters = append(setters,
		a.username,
		a.realm,
		a.nonce(),
		a.integrity,
		stun.Fingerprint)

	msg, err := stun.Build(setters...)
	if err != nil {
		return err
	}

	trRes, err := a.performTransaction(msg, a.serverAddr, false)
	if err != nil {
		return err
	}

	res := trRes.Msg

	if res.Type.Class == stun.ClassErrorResponse {
		var code stun.ErrorCodeAttribute
		if err = code.GetFrom(res); err == nil {
			if code.Code == stun.CodeStaleNonce {
				a.setNonceFromMsg(res)

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
		c.log.Warnf("Receive buffer full")
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
			return errClosed
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

	c.log.Warnf("Failed to bind channel %d: %s", bound.number, err)
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
		c.closeAfterChannelBindBadRequest(bound)
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
	c.log.Warnf(
		"ChannelBind returned 400 for saved binding %s on channel %d; keeping binding ready",
		bound.addr,
		bound.number,
	)
	bound.setState(bindingStateReady)

	return true
}

func bindingStateWasReady(state bindingState) bool {
	return state == bindingStateReady || state == bindingStateReadyUnknown
}

func (c *UDPConn) closeAfterChannelBindBadRequest(bound *binding) {
	c.log.Warnf(
		"ChannelBind rejected with 400 for %s on channel %d; closing TURN allocation",
		bound.addr,
		bound.number,
	)

	// startClose, not Close: this runs on a Pion-owned bind worker, which must
	// not join itself. The caller's Close still joins every worker.
	if _, err := c.startClose(); err != nil {
		c.log.Warnf("Failed to close TURN allocation after ChannelBind 400: %s", err)
	}
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

	c.log.Debugf("Channel binding successful: %s %d", bound.addr, bound.number)

	// Success.
	return nil
}

func (c *UDPConn) handleChannelBindErrorResponse(res *stun.Message) error {
	var code stun.ErrorCodeAttribute
	if err := code.GetFrom(res); err != nil {
		return fmt.Errorf("%w: unexpected response type %s", errCannotBindChannel, res.Type) // nolint:err113
	}

	switch code.Code {
	case stun.CodeStaleNonce:
		c.setNonceFromMsg(res)

		return errTryAgain
	case stun.CodeBadRequest:
		return fmt.Errorf("%w: %w: received error %d", errCannotBindChannel, errChannelBindBadRequest, code.Code)
	default:
		return fmt.Errorf("%w: received error %d", errCannotBindChannel, code.Code) // nolint:err113
	}
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
