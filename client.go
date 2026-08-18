// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package turn provides GridSwarm's owned UDP TURN client, centered on Client.Allocate and Allocation.PreparePeer.
package turn

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/pion/stun/v3"
	"github.com/the-sarge/turn/v5/internal/client"
	"github.com/the-sarge/turn/v5/internal/proto"
)

const (
	defaultRTO        = 200 * time.Millisecond
	maxDataBufferSize = math.MaxUint16 // Message size limit for Chromium
)

//              interval [msec]
// 0: 0 ms      +500
// 1: 500 ms	+1000
// 2: 1500 ms   +2000
// 3: 3500 ms   +4000
// 4: 7500 ms   +8000
// 5: 15500 ms  +16000
// 6: 31500 ms  +32000
// -: 63500 ms  failed

// ClientConfig is a bag of config parameters for Client.
type ClientConfig struct {
	// Server is the TURN server's transport address. It must already be
	// canonical: a unicast IPv4 or IPv6 literal (IPv4-mapped IPv6 is
	// rejected; use the IPv4 literal), no zone, nonzero port. The client
	// performs no name resolution.
	Server   netip.AddrPort
	Username string
	Password string //nolint:gosec // runtime credential, not hardcoded.
	RTO      time.Duration
	Conn     net.PacketConn // Caller-owned socket; the caller runs the read pump.

	// PermissionTimeout sets the refresh interval of permissions. Defaults to 2 minutes.
	PermissionRefreshInterval time.Duration

	bindingRefreshInterval time.Duration
	bindingCheckInterval   time.Duration
}

// Client is a TURN client bound to one server and one caller-owned socket.
type Client struct {
	conn       net.PacketConn // Read-only
	server     netip.AddrPort // Read-only; canonical, the inbound admission comparand
	serverAddr net.Addr       // Read-only; server as the transaction destination on conn

	username     stun.Username               // Read-only
	password     string                      // Read-only
	realm        stun.Realm                  // Read-only
	integrity    stun.MessageIntegrity       // Read-only
	transactions *client.TransactionRegistry // Thread-safe
	relayedConn  *client.UDPConn             // Protected by mutex ***
	allocTryLock client.TryLock              // Thread-safe
	mutex        sync.RWMutex                // Thread-safe

	// REQUESTED-ADDRESS-FAMILY attribute for allocations (RFC 6156)
	requestedAddressFamily proto.RequestedAddressFamily

	permissionRefreshInterval time.Duration
	bindingRefreshInterval    time.Duration
	bindingCheckInterval      time.Duration
}

// inferAddressFamilyFromConn attempts to determine the address
// family (IPv4 or IPv6) from a PacketConn's local address.
// Returns an error if the address type is not IP-based.
func inferAddressFamilyFromConn(
	conn net.PacketConn,
) (proto.RequestedAddressFamily, error) {
	addr := conn.LocalAddr()

	switch a := addr.(type) {
	case *net.UDPAddr:
		if a.IP.To4() != nil {
			return proto.RequestedFamilyIPv4, nil
		}

		return proto.RequestedFamilyIPv6, nil
	default:
		return 0, fmt.Errorf("cannot infer address family from %T", addr) //nolint:err113
	}
}

// getRequestedAddressFamily determines the address family to use
// for TURN allocations. It follows this priority:
//  1. Try to infer from the PacketConn's local address
//  2. Fall back to IPv4 default per RFC 6156
func getRequestedAddressFamily(conn net.PacketConn) proto.RequestedAddressFamily {
	if inferred, err := inferAddressFamilyFromConn(conn); err == nil {
		return inferred
	}

	// Default to IPv4 per RFC 6156
	return proto.RequestedFamilyIPv4
}

// appendRequestedAddressFamily adds REQUESTED-ADDRESS-FAMILY to the provided
// setters slice. The attribute is only included when IPv6 is desired.
func appendRequestedAddressFamily(
	setters []stun.Setter,
	requestedFamily proto.RequestedAddressFamily,
) []stun.Setter {
	// Only include the attribute when IPv6 is explicitly requested.
	// This indirectly implied by the specification:
	// If the REQUESTED-ADDRESS-FAMILY attribute is absent, the server MUST
	// allocate an IPv4-relayed transport address for the TURN client.
	// https://www.rfc-editor.org/rfc/rfc6156#section-4.2
	if requestedFamily == proto.RequestedFamilyIPv6 {
		return append(setters, requestedFamily)
	}

	return setters
}

// NewClient returns a new Client instance bound to config.Conn and
// config.Server. Server is validated once here and becomes the single
// comparand for inbound admission and the destination of every transaction.
func NewClient(config *ClientConfig) (*Client, error) {
	if config.Conn == nil {
		return nil, errNilConn
	}

	server, ok := canonicalAddrPort(config.Server, canonicalStrict)
	if !ok {
		return nil, errInvalidServer
	}

	rto := defaultRTO
	if config.RTO > 0 {
		rto = config.RTO
	}

	turnClient := &Client{
		conn:                      config.Conn,
		server:                    server,
		serverAddr:                net.UDPAddrFromAddrPort(server),
		username:                  stun.NewUsername(config.Username),
		password:                  config.Password,
		requestedAddressFamily:    getRequestedAddressFamily(config.Conn),
		permissionRefreshInterval: config.PermissionRefreshInterval,
		bindingRefreshInterval:    config.bindingRefreshInterval,
		bindingCheckInterval:      config.bindingCheckInterval,
	}
	turnClient.transactions = client.NewTransactionRegistry(turnClient.writeTo, rto)

	return turnClient, nil
}

// writeTo sends data to the specified destination using the base socket.
func (c *Client) writeTo(data []byte, to net.Addr) (int, error) {
	return c.conn.WriteTo(data, to)
}

// Close closes this client: every pending transaction is closed and its
// waiter woken with an error wrapping net.ErrClosed. Close is idempotent and
// never touches the caller-owned socket.
func (c *Client) Close() {
	c.transactions.AbortCurrent()
}

func (c *Client) sendAllocateRequest(ctx context.Context, protocol proto.Protocol) ( //nolint:cyclop
	relayed proto.RelayedAddress,
	lifetime proto.Lifetime,
	nonce stun.Nonce,
	err error,
) {
	allocationSetters := []stun.Setter{
		stun.TransactionID,
		stun.NewType(stun.MethodAllocate, stun.ClassRequest),
		proto.RequestedTransport{Protocol: protocol},
	}

	allocationSetters = appendRequestedAddressFamily(allocationSetters, c.requestedAddressFamily)

	// FINGERPRINT must be the last attribute per RFC 5389
	allocationSetters = append(allocationSetters, stun.Fingerprint)

	msg, err := stun.Build(allocationSetters...)
	if err != nil {
		return relayed, lifetime, nonce, err
	}

	trRes, err := c.performAllocateTransaction(ctx, msg)
	if err != nil {
		return relayed, lifetime, nonce, err
	}

	res := trRes.Msg

	// Anonymous allocate failed, trying to authenticate.
	if err = nonce.GetFrom(res); err != nil {
		return relayed, lifetime, nonce, err
	}
	if err = c.realm.GetFrom(res); err != nil {
		return relayed, lifetime, nonce, err
	}
	c.realm = append([]byte(nil), c.realm...)
	c.integrity = stun.NewLongTermIntegrity(
		c.username.String(), c.realm.String(), c.password,
	)
	// Trying to authorize.
	allocationSetters = []stun.Setter{
		stun.TransactionID,
		stun.NewType(stun.MethodAllocate, stun.ClassRequest),
		proto.RequestedTransport{Protocol: protocol},
		&c.username,
		&c.realm,
		&nonce,
		&c.integrity,
	}

	allocationSetters = appendRequestedAddressFamily(allocationSetters, c.requestedAddressFamily)

	// FINGERPRINT must be the last attribute per RFC 5389
	allocationSetters = append(allocationSetters, stun.Fingerprint)

	msg, err = stun.Build(allocationSetters...)
	if err != nil {
		return relayed, lifetime, nonce, err
	}

	trRes, err = c.performAllocateTransaction(ctx, msg)
	if err != nil {
		return relayed, lifetime, nonce, err
	}
	res = trRes.Msg

	if res.Type.Class == stun.ClassErrorResponse {
		var code stun.ErrorCodeAttribute
		if err = code.GetFrom(res); err == nil {
			turnError := &stun.TurnError{
				StunMessageType: res.Type,
				ErrorCodeAttr:   code,
			}

			return relayed, lifetime, nonce, turnError
		}

		return relayed, lifetime, nonce, fmt.Errorf("%s", res.Type) //nolint:err113
	}

	// Getting relayed addresses from response.
	if err := relayed.GetFrom(res); err != nil {
		return relayed, lifetime, nonce, err
	}

	// Getting lifetime from response
	if err := lifetime.GetFrom(res); err != nil {
		return relayed, lifetime, nonce, err
	}

	return relayed, lifetime, nonce, nil
}

// Allocate requests a UDP relay allocation from the configured server and
// returns it as an Allocation. The server-reported relayed address is
// canonicalized once here and becomes authoritative for RelayedAddr; if it
// cannot be canonicalized, the allocation is released with a lifetime-0
// Refresh and Allocate returns ErrInvalidRelayedAddress.
//
// Canceling ctx wakes only this caller: Allocate returns context.Cause(ctx)
// promptly without touching the caller-owned socket. A cancellation that
// lands after the request left the socket may orphan a server-side
// allocation until its lifetime expires, and a retried Allocate on the same
// Conn may then receive 437 Allocation Mismatch; use a fresh socket per
// Allocate attempt. If the client is closed while Allocate waits, the closed
// error (wrapping net.ErrClosed) takes precedence over cancellation.
func (c *Client) Allocate(ctx context.Context) (*Allocation, error) {
	if ctx == nil {
		return nil, errNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	if err := c.allocTryLock.Lock(); err != nil {
		return nil, fmt.Errorf("%w: %w", errOneAllocateOnly, err)
	}
	defer c.allocTryLock.Unlock()

	relayedConn := c.relayedUDPConn()
	if relayedConn != nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyAllocated, relayedConn.LocalAddr().String())
	}

	relayed, lifetime, nonce, err := c.sendAllocateRequest(ctx, proto.ProtoUDP)
	if err != nil {
		return nil, err
	}

	relayedAddr := &net.UDPAddr{
		IP:   relayed.IP,
		Port: relayed.Port,
	}

	relayedConn = client.NewUDPConn(&client.AllocationConfig{
		WriteTo:                   c.writeTo,
		PerformTransaction:        c.performTransaction,
		OnDeallocated:             c.onDeallocated,
		RelayedAddr:               relayedAddr,
		ServerAddr:                c.serverAddr,
		Realm:                     c.realm,
		Username:                  c.username,
		Integrity:                 c.integrity,
		Nonce:                     nonce,
		Lifetime:                  lifetime.Duration,
		PermissionRefreshInterval: c.permissionRefreshInterval,
		BindingRefreshInterval:    c.bindingRefreshInterval,
		BindingCheckInterval:      c.bindingCheckInterval,
	}, c.transactions.AbortCurrent)
	c.setRelayedUDPConn(relayedConn)

	canonicalRelayed, ok := canonicalWireAddr(relayed.IP, relayed.Port)
	if !ok {
		// Release the server-side allocation (lifetime-0 Refresh) and clear
		// the client's pointer via OnDeallocated before rejecting.
		_ = relayedConn.Close()

		return nil, fmt.Errorf("%w: %s", ErrInvalidRelayedAddress, relayedAddr)
	}

	return newAllocation(relayedConn, canonicalRelayed), nil
}

// performTransaction performs STUN transaction.
func (c *Client) performTransaction(msg *stun.Message, to net.Addr, ignoreResult bool) (client.TransactionResult,
	error,
) {
	if ignoreResult {
		return client.TransactionResult{}, c.transactions.Start(msg, to)
	}

	return c.transactions.Perform(msg, to)
}

// performAllocateTransaction delegates Allocate's private cancelable wait to
// the transaction registry; cancellation does not cancel shared peer work.
func (c *Client) performAllocateTransaction(ctx context.Context, msg *stun.Message) (
	client.TransactionResult, error,
) {
	return c.transactions.PerformWithContext(ctx, msg, c.serverAddr)
}

// onDeallocated is called when de-allocation of relay address has been complete.
// (Called by UDPConn).
func (c *Client) onDeallocated(net.Addr) {
	c.setRelayedUDPConn(nil)
}

// HandleInbound handles one datagram read from the caller's socket.
//
// Only datagrams whose canonical source is the configured Server are
// admitted: any other source (a different address or port, or a non-UDP
// net.Addr) is ignored with a nil error and no delivery. This is defense in
// depth under the caller's own source guard, not a replacement for it.
//
// A datagram from the Server is de-multiplexed by type: STUN responses
// complete their pending transaction, Data indications and ChannelData are
// delivered to the allocation. An error is returned only for malformed or
// unexpected protocol input from the Server (a parse failure, a STUN request,
// a Data indication whose peer address cannot be canonicalized
// (ErrInvalidPeer), ChannelData on an unbound channel, or a datagram that is
// neither STUN nor ChannelData); the caller may discard it.
func (c *Client) HandleInbound(data []byte, from net.Addr) error {
	source, ok := canonicalSourceAddr(from)
	if !ok || source != c.server {
		return nil
	}

	switch {
	case stun.IsMessage(data):
		return c.handleSTUNMessage(data)
	case proto.IsChannelData(data):
		return c.handleChannelData(data)
	default:
		return errUnexpectedServerDatagram
	}
}

func (c *Client) handleSTUNMessage(data []byte) error { //nolint:cyclop
	raw := make([]byte, len(data))
	copy(raw, data)

	msg := &stun.Message{Raw: raw}
	if err := msg.Decode(); err != nil {
		return fmt.Errorf("%w: %w", errFailedToDecodeSTUN, err)
	}

	if msg.Type.Class == stun.ClassRequest {
		return fmt.Errorf("%w : %s", errUnexpectedSTUNRequestMessage, msg.String())
	}

	if msg.Type.Class == stun.ClassIndication { // nolint:nestif
		switch msg.Type.Method {
		case stun.MethodData:
			var peerAddr proto.PeerAddress
			if err := peerAddr.GetFrom(msg); err != nil {
				return err
			}
			// The canonical peer label is created here, once per datagram, so
			// ReadFrom returns it without per-packet conversion.
			source, ok := canonicalWireAddr(peerAddr.IP, peerAddr.Port)
			if !ok {
				return fmt.Errorf("%w: data indication from %s", ErrInvalidPeer, peerAddr)
			}

			var data proto.Data
			if err := data.GetFrom(msg); err != nil {
				return err
			}

			relayedConn := c.relayedUDPConn()
			if relayedConn == nil {
				return nil // Silently discard
			}
			relayedConn.HandleInbound(data, source)
		default:
			// Unsupported indication methods are silently discarded.
		}

		return nil
	}

	// This is a STUN response message (transactional)
	// The type is either:
	// - stun.ClassSuccessResponse
	// - stun.ClassErrorResponse

	c.transactions.Complete(msg)

	return nil
}

func (c *Client) handleChannelData(data []byte) error {
	chData := &proto.ChannelData{
		Raw: make([]byte, len(data)),
	}
	copy(chData.Raw, data)
	if err := chData.Decode(); err != nil {
		return err
	}

	relayedConn := c.relayedUDPConn()
	if relayedConn == nil {
		return nil // Silently discard
	}

	addr, ok := relayedConn.FindAddrByChannelNumber(uint16(chData.Number))
	if !ok {
		return fmt.Errorf("%w: %d", errChannelBindNotFound, int(chData.Number))
	}

	relayedConn.HandleInbound(chData.Data, addr)

	return nil
}

func (c *Client) setRelayedUDPConn(conn *client.UDPConn) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.relayedConn = conn
}

func (c *Client) relayedUDPConn() *client.UDPConn {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.relayedConn
}
