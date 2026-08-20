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

// bindingAttempt is UDPConn-owned coordination for one ChannelBind generation.
// Readiness owns only the token and durable result of resolving that generation.
type bindingAttempt struct {
	done   chan struct{}
	token  bindingAttemptToken
	class  bindingAttemptClass
	result error
}

// UDPConn is one live UDP relay allocation. Peer addresses cross its methods
// as canonical netip.AddrPort values; the root package owns canonicalization
// and validation, so every peer reaching this type is already canonical.
type UDPConn struct {
	// Package-crossing operations are immutable production/mock adapters. They
	// do not own or mutate Allocation lifecycle state.
	writeTo            func(data []byte) (int, error)
	performTransaction func(msg *stun.Message) (*stun.Message, error)
	startTransaction   func(msg *stun.Message) error
	onDeallocated      func()
	abortTransactions  func()

	permMap   *permissionMap
	integrity stun.MessageIntegrity // Read-only
	username  stun.Username         // Read-only
	realm     stun.Realm            // Read-only
	_nonce    stun.Nonce            // Protected by mutex
	_lifetime time.Duration         // Protected by mutex

	refreshAllocTimer      *PeriodicTimer
	refreshPermsTimer      *PeriodicTimer
	mutex                  sync.RWMutex // Protects nonce and lifetime
	bindingMgr             *bindingManager
	checkBindingsTimer     *PeriodicTimer
	readCh                 chan *inboundData
	closeCh                chan struct{}
	deliveryMutex          sync.RWMutex   // Linearizes decoded delivery with the closeCh transition
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
	conn := newUDPConn(config, abortTransactions)
	conn.start()

	return conn
}

// newUDPConn builds a UDPConn with every construction invariant established
// but no timer goroutines started.
func newUDPConn(config *AllocationConfig, abortTransactions func()) *UDPConn {
	if abortTransactions == nil {
		panic("client: missing abort capability") //nolint:forbidigo // Programmer-invalid internal construction.
	}

	conn := &UDPConn{
		writeTo:                config.WriteTo,
		performTransaction:     config.PerformTransaction,
		startTransaction:       config.StartTransaction,
		onDeallocated:          config.OnDeallocated,
		abortTransactions:      abortTransactions,
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

	return conn
}

// start arms every timer owned by the allocation.
func (c *UDPConn) start() {
	c.refreshAllocTimer.Start()
	c.refreshPermsTimer.Start()
	c.checkBindingsTimer.Start()
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

func (c *UDPConn) createPermission(addr netip.AddrPort) error {
	return c.CreatePermissions(addr)
}

// PreparePeer creates a permission for the canonical peer and waits until the
// TURN server confirms a channel binding for it. After it returns nil, writes
// to peer use ChannelData (or fail) for the lifetime of the allocation; they
// never fall back to Send indications. Concurrent callers for the same peer
// share one permission and one bind attempt; canceling ctx wakes only that
// caller (with its cause) and leaves the shared work running.
func (c *UDPConn) PreparePeer(ctx context.Context, peer netip.AddrPort) error {
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
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if c.isClosed() {
			return c.closedErr()
		}

		return ErrChannelBindFailed
	}

	return c.awaitBinding(ctx, bound)
}

// awaitPermission blocks until a permission for peer is installed, the shared
// create attempt fails, or ctx is canceled.
func (c *UDPConn) awaitPermission(ctx context.Context, peer netip.AddrPort) error {
	for {
		perm := c.permMap.getOrCreate(peer)
		permitted, _ := perm.readiness()
		if permitted {
			return nil
		}

		attempt, fresh := perm.beginOrJoin()
		if attempt == nil {
			continue
		}
		if fresh {
			c.runPermissionAttempt(perm, peer)
		}

		select {
		case <-attempt.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.closeCh:
			return c.closedErr()
		}

		permitted, err := c.permissionAttemptResult(perm, attempt)
		if err != nil {
			return err
		}
		if permitted {
			return nil
		}
		// The attempt we joined predates our loop iteration; re-evaluate.
	}
}

// permissionAttemptResult preserves the exact attempt generation a caller
// joined before consulting readiness established by any later attempt.
func (*UDPConn) permissionAttemptResult(
	perm *permission,
	attempt *permissionAttempt,
) (permitted bool, err error) {
	if err = attempt.result(); err != nil {
		return false, err
	}

	return perm.readiness()
}

func (c *UDPConn) runPermissionAttempt(perm *permission, peer netip.AddrPort) {
	if !c.startPermissionAttempt(perm, peer) {
		perm.resolve(c.closedErr())
	}
}

// startPermissionAttempt registers and starts the CreatePermission worker for
// a fresh permission-owned attempt. It returns false once the allocation is
// closing; the caller must resolve the attempt so joined waiters wake.
func (c *UDPConn) startPermissionAttempt(perm *permission, peer netip.AddrPort) bool {
	if !c.addWorker() {
		return false
	}

	go func() {
		defer c.workerWG.Done()
		var err error
		deleteOnFailure := false
		for range maxRetryAttempts {
			if c.isClosed() {
				// Seal precedence: a waiter that joins this attempt gets the
				// recorded terminal cause, exactly like an operation started
				// after the seal.
				err = c.closedErr()
				deleteOnFailure = false

				break
			}
			err = c.createPermission(peer)
			deleteOnFailure = err != nil
			if !errors.Is(err, errTryAgain) {
				break
			}
		}
		if deleteOnFailure {
			c.permMap.delete(peer)
		}
		perm.resolve(err)
	}()

	return true
}

// awaitBinding blocks until the server confirms the channel binding, the
// binding fails, or ctx is canceled.
func (c *UDPConn) awaitBinding(ctx context.Context, bound *binding) error { //nolint:cyclop
	for {
		if final, err := bound.preparationAccess(time.Now()); final {
			return err
		}

		bound.muBind.Lock()
		attempt := bound.attempt
		if attempt == nil {
			attempt = c.startBindAttemptLocked(bound, time.Now())
		}
		bound.muBind.Unlock()

		if attempt == nil {
			// No attempt is needed (state already decisive) or none can start (closing).
			if final, err := bound.preparationAccess(time.Now()); final {
				return err
			}
			if c.isClosed() {
				return c.closedErr()
			}

			return ErrChannelBindFailed
		}

		select {
		case <-attempt.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.closeCh:
			return c.closedErr()
		}

		if final, err := bound.preparationAccess(time.Now()); final {
			return err
		}
		// The joined attempt ended without confirming; surface its error rather
		// than retrying forever on the caller's behalf.
		if attempt.result != nil {
			return attempt.result
		}
	}
}

// startBindAttemptLocked starts a tracked bind attempt if the binding state
// calls for one. It requires bound.muBind to be held and returns the channel
// that closes when the attempt completes, or nil if no attempt was started.
func (c *UDPConn) startBindAttemptLocked(bound *binding, now time.Time) *bindingAttempt {
	if !c.addWorker() {
		return nil
	}
	token, class, started := bound.beginAttempt(now, c.bindingRefreshInterval)
	if !started {
		c.workerWG.Done()

		return nil
	}

	attempt := &bindingAttempt{
		done:  make(chan struct{}),
		token: token,
		class: class,
	}
	bound.attempt = attempt
	go func() {
		defer c.workerWG.Done()
		err := c.bindChannel(bound, token, class)
		bound.muBind.Lock()
		attempt.result = err
		if bound.attempt == attempt {
			bound.attempt = nil
		}
		bound.muBind.Unlock()
		close(attempt.done)
	}()

	return attempt
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
	cause := fmt.Errorf("%w: %w", ErrPermissionRefreshFailed, err)
	for _, bound := range c.bindingMgr.all() {
		bound.failPrepared(cause)
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
	if !ok {
		return 0, ErrNotPrepared
	}

	if err := bound.writeAccess(time.Now()); err != nil {
		return 0, err
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

	c.deliveryMutex.Lock()
	select {
	case <-c.closeCh:
		c.deliveryMutex.Unlock()

		return false, nil
	default:
		close(c.closeCh)
	}
	c.deliveryMutex.Unlock()

	// Wake workers blocked on in-flight transaction waits so Close does not
	// wait out the retransmission budget against an unresponsive server.
	c.abortTransactions()

	c.onDeallocated()

	emitErr := c.emitRelease()
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
	defer c.closeMutex.Unlock()

	return c.closedErrLocked()
}

func (c *UDPConn) closedErrLocked() error {
	cause := c.terminalCause
	if cause != nil {
		return fmt.Errorf("%w: %w", net.ErrClosed, cause)
	}

	return net.ErrClosed
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

	res, err := c.performTransaction(msg)
	if err != nil {
		return err
	}

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

// enqueueInbound copies one relayed datagram into the bounded read queue. The
// caller holds deliveryMutex for reading and has already established that the
// allocation is live.
func (c *UDPConn) enqueueInbound(data []byte, from netip.AddrPort) {
	copied := make([]byte, len(data))
	copy(copied, data)

	select {
	case c.readCh <- &inboundData{data: copied, from: from}:
	default:
		// The receive queue is full: the datagram is dropped, matching UDP
		// semantics. Documented on Allocation.ReadFrom.
	}
}

// HandleDataIndication delivers one decoded Data indication from peer.
func (c *UDPConn) HandleDataIndication(data []byte, peer netip.AddrPort) {
	c.deliveryMutex.RLock()
	defer c.deliveryMutex.RUnlock()
	if c.isClosed() {
		return
	}

	c.enqueueInbound(data, peer)
}

// HandleChannelData delivers one decoded ChannelData payload. It reports
// false only when the live allocation has no binding for channel.
func (c *UDPConn) HandleChannelData(data []byte, channel uint16) bool {
	c.deliveryMutex.RLock()
	defer c.deliveryMutex.RUnlock()
	if c.isClosed() {
		return true
	}

	bound, ok := c.bindingMgr.findByNumber(channel)
	if !ok {
		return false
	}

	c.enqueueInbound(data, bound.addr)

	return true
}

func (c *UDPConn) maybeBind(bound *binding) {
	// Block only callers with the same binding until
	// the binding transaction has been started
	bound.muBind.Lock()
	defer bound.muBind.Unlock()

	if bound.attempt == nil {
		// Establish binding with the server if the state machine allows it.
		c.startBindAttemptLocked(bound, time.Now())
	}
}

// bindChannel performs one ChannelBind attempt. It returns nil when the
// binding was confirmed or recovered, and the attempt's error otherwise.
func (c *UDPConn) bindChannel(
	bound *binding,
	token bindingAttemptToken,
	class bindingAttemptClass,
) error {
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
		return c.resolveBindError(bound, token, class, err)
	}

	_, completionErr := c.completeBindingAttempt(bound, token, bindingAttemptConfirmed, nil, time.Now())

	return completionErr
}

// completeBindingAttempt orders readiness publication with Allocation seal.
// The close lock is never held across TURN I/O; the only nested order is
// closeMutex then one binding's readiness lock.
func (c *UDPConn) completeBindingAttempt(
	bound *binding,
	token bindingAttemptToken,
	outcome bindingAttemptOutcome,
	cause error,
	now time.Time,
) (bool, error) {
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

	if c.isClosed() {
		return false, c.closedErrLocked()
	}

	return bound.resolveAttempt(token, outcome, cause, now), nil
}

func (c *UDPConn) resolveBindError(
	bound *binding,
	token bindingAttemptToken,
	class bindingAttemptClass,
	err error,
) error {
	if errors.Is(err, errChannelBindBadRequest) && class == bindingAttemptPreviouslyConfirmed {
		_, completionErr := c.completeBindingAttempt(
			bound,
			token,
			bindingAttemptPreserveConfirmation,
			nil,
			time.Now(),
		)

		return completionErr
	}
	if errors.Is(err, errChannelBindTransactionFailed) {
		applied, completionErr := c.completeBindingAttempt(
			bound,
			token,
			bindingAttemptUncertain,
			nil,
			time.Now(),
		)
		if completionErr != nil {
			return completionErr
		}
		if !applied {
			return nil
		}

		return err
	}

	applied, completionErr := c.completeBindingAttempt(
		bound,
		token,
		bindingAttemptPermanentFailure,
		err,
		time.Now(),
	)
	if completionErr != nil {
		return completionErr
	}
	if applied && errors.Is(err, errChannelBindBadRequest) {
		c.closeAfterChannelBindBadRequest(err)
	}

	return nil
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

	res, err := c.performTransaction(msg)
	if err != nil {
		return fmt.Errorf("%w: %w", errChannelBindTransactionFailed, err)
	}
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
	_, err := c.writeTo(chData.Raw)
	if err != nil {
		return 0, err
	}

	return len(data), nil
}
