// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package turn provides GridSwarm's owned UDP TURN client, centered on Client.Allocate and Client.PrepareUDPPeer.
package turn

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/the-sarge/turn/v5/internal/client"
	"github.com/the-sarge/turn/v5/internal/proto"
)

const (
	defaultRTO        = 200 * time.Millisecond
	maxRtxCount       = 7              // Total 7 requests (Rc)
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
	Server        netip.AddrPort
	Username      string
	Password      string //nolint:gosec // runtime credential, not hardcoded.
	RTO           time.Duration
	Conn          net.PacketConn // Listening socket (net.PacketConn)
	LoggerFactory logging.LoggerFactory

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

	username      stun.Username          // Read-only
	password      string                 // Read-only
	realm         stun.Realm             // Read-only
	integrity     stun.MessageIntegrity  // Read-only
	trMap         *client.TransactionMap // Thread-safe
	rto           time.Duration          // Read-only
	relayedConn   *client.UDPConn        // Protected by mutex ***
	allocTryLock  client.TryLock         // Thread-safe
	listenTryLock client.TryLock         // Thread-safe
	mutex         sync.RWMutex           // Thread-safe
	mutexTrMap    sync.Mutex             // Thread-safe
	log           logging.LeveledLogger  // Read-only

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
func getRequestedAddressFamily(
	log logging.LeveledLogger,
	conn net.PacketConn,
) proto.RequestedAddressFamily {
	// Try to infer from the PacketConn
	if inferred, err := inferAddressFamilyFromConn(conn); err == nil {
		log.Debugf("Inferred address family %v from connection", inferred)

		return inferred
	}

	log.Debugf("Could not infer address family, defaulting to IPv4")

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
	loggerFactory := config.LoggerFactory
	if loggerFactory == nil {
		loggerFactory = logging.NewDefaultLoggerFactory()
	}

	log := loggerFactory.NewLogger("turnc")

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

	// Determine the requested address family (RFC 6156)
	requestedAddressFamily := getRequestedAddressFamily(log, config.Conn)

	client := &Client{
		conn:                      config.Conn,
		server:                    server,
		serverAddr:                net.UDPAddrFromAddrPort(server),
		username:                  stun.NewUsername(config.Username),
		password:                  config.Password,
		trMap:                     client.NewTransactionMap(),
		rto:                       rto,
		log:                       log,
		requestedAddressFamily:    requestedAddressFamily,
		permissionRefreshInterval: config.PermissionRefreshInterval,
		bindingRefreshInterval:    config.bindingRefreshInterval,
		bindingCheckInterval:      config.bindingCheckInterval,
	}

	return client, nil
}

// writeTo sends data to the specified destination using the base socket.
func (c *Client) writeTo(data []byte, to net.Addr) (int, error) {
	return c.conn.WriteTo(data, to)
}

// Listen will have this client start listening on the conn provided via the config.
// This is optional. If not used, you will need to call HandleInbound method
// to supply incoming data, instead.
func (c *Client) Listen() error {
	if err := c.listenTryLock.Lock(); err != nil {
		return fmt.Errorf("%w: %s", errAlreadyListening, err.Error())
	}

	go func() {
		buf := make([]byte, maxDataBufferSize)
		for {
			n, from, err := c.conn.ReadFrom(buf)
			if err != nil {
				c.log.Debugf("Failed to read: %s. Exiting loop", err)

				break
			}

			err = c.HandleInbound(buf[:n], from)
			if err != nil {
				c.log.Debugf("Failed to handle inbound message: %s. Exiting loop", err)

				break
			}
		}

		c.listenTryLock.Unlock()
	}()

	return nil
}

// Close closes this client.
func (c *Client) Close() {
	c.mutexTrMap.Lock()
	defer c.mutexTrMap.Unlock()

	c.trMap.CloseAndDeleteAll()
}

// abortPendingTransactionsTo closes every pending transaction addressed to
// the given destination, waking their waiters with an error.
func (c *Client) abortPendingTransactionsTo(to net.Addr) {
	c.mutexTrMap.Lock()
	defer c.mutexTrMap.Unlock()

	c.trMap.CloseAndDeleteAllTo(to)
}

func (c *Client) sendAllocateRequest(protocol proto.Protocol) ( //nolint:cyclop
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

	trRes, err := c.performTransaction(msg, c.serverAddr, false)
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

	trRes, err = c.performTransaction(msg, c.serverAddr, false)
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

// Allocate sends a TURN allocation request to the given transport address.
func (c *Client) Allocate() (net.PacketConn, error) {
	if err := c.allocTryLock.Lock(); err != nil {
		return nil, fmt.Errorf("%w: %s", errOneAllocateOnly, err.Error())
	}
	defer c.allocTryLock.Unlock()

	relayedConn := c.relayedUDPConn()
	if relayedConn != nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyAllocated, relayedConn.LocalAddr().String())
	}

	relayed, lifetime, nonce, err := c.sendAllocateRequest(proto.ProtoUDP)
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
		Log:                       c.log,
		PermissionRefreshInterval: c.permissionRefreshInterval,
		BindingRefreshInterval:    c.bindingRefreshInterval,
		BindingCheckInterval:      c.bindingCheckInterval,
		AbortTransactions: func() {
			c.abortPendingTransactionsTo(c.serverAddr)
		},
	})
	c.setRelayedUDPConn(relayedConn)

	return relayedConn, nil
}

// PrepareUDPPeer creates a permission for peer on the client's UDP allocation
// and waits until the TURN server confirms a channel binding for it. After it
// returns nil, writes to peer use ChannelData (or fail) for the lifetime of
// the allocation; they never fall back to Send indications. Concurrent calls
// for the same peer share one permission and one bind; canceling ctx wakes
// only that caller and leaves the shared work running.
func (c *Client) PrepareUDPPeer(ctx context.Context, peer net.Addr) error {
	conn := c.relayedUDPConn()
	if conn == nil {
		return errUDPAllocationNotFound
	}

	return conn.PreparePeer(ctx, peer)
}

// performTransaction performs STUN transaction.
func (c *Client) performTransaction(msg *stun.Message, to net.Addr, ignoreResult bool) (client.TransactionResult,
	error,
) {
	trKey := b64.StdEncoding.EncodeToString(msg.TransactionID[:])

	raw := make([]byte, len(msg.Raw))
	copy(raw, msg.Raw)

	tr := client.NewTransaction(&client.TransactionConfig{
		Key:          trKey,
		Raw:          raw,
		To:           to,
		Interval:     c.rto,
		IgnoreResult: ignoreResult,
	})

	c.trMap.Insert(trKey, tr)

	c.log.Tracef("Start %s transaction %s to %s", msg.Type, trKey, tr.To)
	_, err := c.conn.WriteTo(tr.Raw, to)
	if err != nil {
		return client.TransactionResult{}, err
	}

	tr.StartRtxTimer(c.onRtxTimeout)

	// If ignoreResult is true, get the transaction going and return immediately
	if ignoreResult {
		return client.TransactionResult{}, nil
	}

	res := tr.WaitForResult()
	if res.Err != nil {
		return res, res.Err
	}

	return res, nil
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
// ChannelData on an unbound channel, or a datagram that is neither STUN nor
// ChannelData); the caller may discard it.
func (c *Client) HandleInbound(data []byte, from net.Addr) error {
	source, ok := canonicalSourceAddr(from)
	if !ok || source != c.server {
		return nil
	}

	switch {
	case stun.IsMessage(data):
		return c.handleSTUNMessage(data, from)
	case proto.IsChannelData(data):
		return c.handleChannelData(data)
	default:
		return errUnexpectedServerDatagram
	}
}

func (c *Client) handleSTUNMessage(data []byte, from net.Addr) error { //nolint:cyclop
	raw := make([]byte, len(data))
	copy(raw, data)

	msg := &stun.Message{Raw: raw}
	if err := msg.Decode(); err != nil {
		return fmt.Errorf("%w: %s", errFailedToDecodeSTUN, err.Error())
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
			from = &net.UDPAddr{
				IP:   peerAddr.IP,
				Port: peerAddr.Port,
			}

			var data proto.Data
			if err := data.GetFrom(msg); err != nil {
				return err
			}

			c.log.Tracef("Data indication received from %s", from)

			relayedConn := c.relayedUDPConn()
			if relayedConn == nil {
				c.log.Debug("No relayed conn allocated")

				return nil // Silently discard
			}
			relayedConn.HandleInbound(data, from)
		default:
			c.log.Debug("Received unsupported STUN method")
		}

		return nil
	}

	// This is a STUN response message (transactional)
	// The type is either:
	// - stun.ClassSuccessResponse
	// - stun.ClassErrorResponse

	trKey := b64.StdEncoding.EncodeToString(msg.TransactionID[:])

	c.mutexTrMap.Lock()
	tr, ok := c.trMap.Find(trKey)
	if !ok {
		c.mutexTrMap.Unlock()
		// Silently discard
		c.log.Debugf("No transaction for %s", msg)

		return nil
	}

	// End the transaction
	tr.StopRtxTimer()
	c.trMap.Delete(trKey)
	c.mutexTrMap.Unlock()

	if !tr.WriteResult(client.TransactionResult{
		Msg:     msg,
		From:    from,
		Retries: tr.Retries(),
	}) {
		c.log.Debugf("No listener for %s", msg)
	}

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
		c.log.Debug("No relayed conn allocated")

		return nil // Silently discard
	}

	addr, ok := relayedConn.FindAddrByChannelNumber(uint16(chData.Number))
	if !ok {
		return fmt.Errorf("%w: %d", errChannelBindNotFound, int(chData.Number))
	}

	c.log.Tracef("Channel data received from %s (ch=%d)", addr.String(), int(chData.Number))

	relayedConn.HandleInbound(chData.Data, addr)

	return nil
}

func (c *Client) onRtxTimeout(trKey string, nRtx int) {
	c.mutexTrMap.Lock()
	defer c.mutexTrMap.Unlock()

	tr, ok := c.trMap.Find(trKey)
	if !ok {
		return // Already gone
	}

	if nRtx == maxRtxCount {
		// All retransmissions failed
		c.trMap.Delete(trKey)
		if !tr.WriteResult(client.TransactionResult{
			Err: fmt.Errorf("%w %s", errAllRetransmissionsFailed, trKey),
		}) {
			c.log.Debug("No listener for transaction")
		}

		return
	}

	c.log.Tracef("Retransmitting transaction %s to %s (nRtx=%d)",
		trKey, tr.To, nRtx)
	_, err := c.conn.WriteTo(tr.Raw, tr.To)
	if err != nil {
		c.trMap.Delete(trKey)
		if !tr.WriteResult(client.TransactionResult{
			Err: fmt.Errorf("%w %s", errFailedToRetransmitTransaction, trKey),
		}) {
			c.log.Debug("No listener for transaction")
		}

		return
	}
	tr.StartRtxTimer(c.onRtxTimeout)
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
