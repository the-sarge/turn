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

	// PermissionRefreshInterval sets the refresh cadence for permissions. Zero
	// selects the two-minute default. Explicit values must be positive and
	// strictly less than five minutes; other values are rejected by NewClient.
	// Accepted values do not guarantee refresh before expiry under operational delay.
	PermissionRefreshInterval time.Duration

	bindingRefreshInterval time.Duration
	bindingCheckInterval   time.Duration
}

// Client is a TURN client bound to one server and one caller-owned socket.
type Client struct {
	conn       net.PacketConn // Read-only
	server     netip.AddrPort // Read-only; canonical, the inbound admission comparand
	serverAddr net.Addr       // Read-only; socket-facing form of server

	username     stun.Username               // Read-only
	password     string                      // Read-only
	transactions *client.TransactionRegistry // Thread-safe
	relayedConn  *client.UDPConn             // Protected by mutex ***
	allocating   bool                        // Protected by mutex
	mutex        sync.RWMutex                // Thread-safe

	// REQUESTED-ADDRESS-FAMILY attribute for allocations (RFC 6156)
	requestedAddressFamily proto.RequestedAddressFamily

	permissionRefreshInterval time.Duration
	bindingRefreshInterval    time.Duration
	bindingCheckInterval      time.Duration
}

// requestedAddressFamily infers the family from a UDP local address and falls
// back to the RFC 6156 IPv4 default for other address types.
func requestedAddressFamily(conn net.PacketConn) proto.RequestedAddressFamily {
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		if addr.IP.To4() != nil {
			return proto.RequestedFamilyIPv4
		}

		return proto.RequestedFamilyIPv6
	}

	return proto.RequestedFamilyIPv4
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
	if config.PermissionRefreshInterval < 0 || config.PermissionRefreshInterval >= 5*time.Minute {
		return nil, errInvalidPermissionRefreshInterval
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
		requestedAddressFamily:    requestedAddressFamily(config.Conn),
		permissionRefreshInterval: config.PermissionRefreshInterval,
		bindingRefreshInterval:    config.bindingRefreshInterval,
		bindingCheckInterval:      config.bindingCheckInterval,
	}
	turnClient.transactions = client.NewTransactionRegistry(turnClient.sendToServer, rto)

	return turnClient, nil
}

// sendToServer sends data to the configured server using the base socket.
func (c *Client) sendToServer(data []byte) (int, error) {
	return c.conn.WriteTo(data, c.serverAddr)
}

// Close closes this client: every pending transaction is closed and its
// waiter woken with an error wrapping net.ErrClosed. Close is idempotent and
// never touches the caller-owned socket.
func (c *Client) Close() {
	c.transactions.AbortCurrent()
}

type allocateCredentials struct {
	realm     stun.Realm
	nonce     stun.Nonce
	integrity stun.MessageIntegrity
}

type allocateExchange struct {
	relayed   proto.RelayedAddress
	lifetime  proto.Lifetime
	realm     stun.Realm
	nonce     stun.Nonce
	integrity stun.MessageIntegrity
}

func (c *Client) buildAllocateRequest(
	protocol proto.Protocol,
	credentials *allocateCredentials,
) (*stun.Message, error) {
	allocationSetters := []stun.Setter{
		stun.TransactionID,
		stun.NewType(stun.MethodAllocate, stun.ClassRequest),
		proto.RequestedTransport{Protocol: protocol},
	}
	if credentials != nil {
		allocationSetters = append(allocationSetters,
			&c.username,
			&credentials.realm,
			&credentials.nonce,
			&credentials.integrity,
		)
	}

	if c.requestedAddressFamily == proto.RequestedFamilyIPv6 {
		allocationSetters = append(allocationSetters, c.requestedAddressFamily)
	}

	// FINGERPRINT must be the last attribute per RFC 5389
	allocationSetters = append(allocationSetters, stun.Fingerprint)

	return stun.Build(allocationSetters...)
}

func (c *Client) sendAllocateRequest(ctx context.Context, protocol proto.Protocol) ( //nolint:cyclop
	exchange allocateExchange,
	err error,
) {
	msg, err := c.buildAllocateRequest(protocol, nil)
	if err != nil {
		return exchange, err
	}

	res, err := c.performAllocateTransaction(ctx, msg)
	if err != nil {
		return exchange, err
	}

	// Anonymous allocate failed, trying to authenticate.
	credentials := allocateCredentials{}
	if err = credentials.nonce.GetFrom(res); err != nil {
		return exchange, err
	}
	if err = credentials.realm.GetFrom(res); err != nil {
		return exchange, err
	}
	credentials.realm = append([]byte(nil), credentials.realm...)
	credentials.integrity = stun.NewLongTermIntegrity(
		c.username.String(), credentials.realm.String(), c.password,
	)

	msg, err = c.buildAllocateRequest(protocol, &credentials)
	if err != nil {
		return exchange, err
	}

	res, err = c.performAllocateTransaction(ctx, msg)
	if err != nil {
		return exchange, err
	}

	if res.Type.Class == stun.ClassErrorResponse {
		var code stun.ErrorCodeAttribute
		if err = code.GetFrom(res); err == nil {
			turnError := &stun.TurnError{
				StunMessageType: res.Type,
				ErrorCodeAttr:   code,
			}

			return exchange, turnError
		}

		return exchange, fmt.Errorf("%s", res.Type) //nolint:err113
	}

	// Getting relayed addresses from response.
	if err := exchange.relayed.GetFrom(res); err != nil {
		return exchange, err
	}

	// Getting lifetime from response
	if err := exchange.lifetime.GetFrom(res); err != nil {
		return exchange, err
	}

	exchange.realm = credentials.realm
	exchange.nonce = credentials.nonce
	exchange.integrity = credentials.integrity

	return exchange, nil
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
	if !c.claimAllocation() {
		return nil, ErrAlreadyAllocated
	}
	claimHeld := true
	defer func() {
		if claimHeld {
			c.releaseAllocationClaim()
		}
	}()

	exchange, err := c.sendAllocateRequest(ctx, proto.ProtoUDP)
	if err != nil {
		return nil, err
	}

	relayedConn := client.NewUDPConn(&client.AllocationConfig{
		WriteTo:                   c.sendToServer,
		PerformTransaction:        c.transactions.Perform,
		StartTransaction:          c.transactions.Start,
		OnDeallocated:             c.onDeallocated,
		Realm:                     exchange.realm,
		Username:                  c.username,
		Integrity:                 exchange.integrity,
		Nonce:                     exchange.nonce,
		Lifetime:                  exchange.lifetime.Duration,
		PermissionRefreshInterval: c.permissionRefreshInterval,
		BindingRefreshInterval:    c.bindingRefreshInterval,
		BindingCheckInterval:      c.bindingCheckInterval,
	}, c.transactions.AbortCurrent)

	canonicalRelayed, ok := canonicalWireAddr(exchange.relayed.IP, exchange.relayed.Port)
	if !ok {
		// Release the server-side allocation (lifetime-0 Refresh) without
		// publishing the doomed connection to the inbound path.
		_ = relayedConn.Close()

		return nil, fmt.Errorf("%w: %s", ErrInvalidRelayedAddress, exchange.relayed)
	}

	c.publishRelayedUDPConn(relayedConn)
	claimHeld = false

	return newAllocation(relayedConn, canonicalRelayed), nil
}

// performAllocateTransaction delegates Allocate's private cancelable wait to
// the transaction registry; cancellation does not cancel shared peer work.
func (c *Client) performAllocateTransaction(ctx context.Context, msg *stun.Message) (
	*stun.Message, error,
) {
	return c.transactions.PerformWithContext(ctx, msg)
}

// onDeallocated is called when de-allocation of relay address has been complete.
// (Called by UDPConn).
func (c *Client) onDeallocated() {
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
			relayedConn.HandleDataIndication(data, source)
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

	if !relayedConn.HandleChannelData(chData.Data, uint16(chData.Number)) {
		return fmt.Errorf("%w: %d", errChannelBindNotFound, int(chData.Number))
	}

	return nil
}

func (c *Client) setRelayedUDPConn(conn *client.UDPConn) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.relayedConn = conn
}

func (c *Client) claimAllocation() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.allocating || c.relayedConn != nil {
		return false
	}
	c.allocating = true

	return true
}

func (c *Client) releaseAllocationClaim() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.allocating = false
}

func (c *Client) publishRelayedUDPConn(conn *client.UDPConn) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.relayedConn = conn
	c.allocating = false
}

func (c *Client) relayedUDPConn() *client.UDPConn {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.relayedConn
}
