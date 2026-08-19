# TURN client

This context describes the language of GridSwarm's owned UDP TURN client library: the relay state a TURN server holds on the client's behalf, and the client-side facts the library keeps about that state. It is a glossary only; decisions live in `docs/adr/`.

## Language

### Server-side relay state

**Allocation**:
The relay state associated with one client-server five-tuple. It owns a relay transport address and the permissions and channel bindings created beneath it.
_Avoid_: Session

**Five-tuple**:
The client transport address, server transport address, and transport protocol that together identify an allocation.
_Avoid_: Connection ID

**Relay transport address**:
The server transport address allocated for communication between a client and its peers.
_Avoid_: Relay address when the transport distinction matters

**Permission**:
An allocation-scoped authorization to communicate with a peer IP address, independent of the peer port.
_Avoid_: Peer connection

**Channel binding**:
An allocation-scoped association between one channel number and one peer transport address. A channel binding also creates or refreshes the required permission.
_Avoid_: Permission

### Messages

**Inbound TURN message**:
A complete message received from the TURN server, represented as either a STUN-formatted message or ChannelData.
_Avoid_: Request, packet

**TURN request**:
A STUN-formatted request asking a TURN server to perform a TURN method. Indications and ChannelData are not TURN requests.
_Avoid_: Inbound TURN message when the distinction matters

### Client-side facts

**Prepared peer**:
A peer for which the client's allocation holds a confirmed permission and channel binding, so that every later write to it is ChannelData or fails for the allocation's lifetime.
_Avoid_: Bound peer, ready peer

**Terminal cause**:
The first error that sealed an allocation, whether the caller closed it or the allocation sealed itself; every later operation reports it.
_Avoid_: Close reason

**Attempt**:
One in-flight CreatePermission exchange for a permission (per peer IP) or ChannelBind exchange for a channel binding (per peer transport address), shared by every concurrent PreparePeer caller for it until it resolves.
_Avoid_: Request (that is the TURN message), retry

**Readiness**:
The client's own record of whether a permission or channel binding can be relied on for a peer; channel-binding readiness is a time-based state, permission readiness is the outcome of its attempt.
_Avoid_: Status, state

**Release**:
The lifetime-zero Refresh that ends an allocation on the server; emitted exactly once per allocation.
_Avoid_: Teardown
