# The client writes to prepared peers only; it never emits Send indications

Pion's UDP client lets `WriteTo` reach an unprepared peer by creating a permission on the fly and sending via a Send indication until a channel binding is ready. Our one consumer forbids any write before `PreparePeer` succeeds and depends on every emitted datagram being ChannelData (its `/3` relay profile sizes packets against the four-byte ChannelData header). We decided (M1, Slice 4) to delete the Send-indication write path rather than guard it: `WriteTo` requires a prepared, confirmed binding and otherwise fails with zero network output, so the lifetime ChannelData-only invariant is structural — no code in the client can build a Send indication — instead of being one flag check that every future edit could bypass.

## Consequences

- Permissions and bindings are created only by `PreparePeer`; permission refresh covers exactly the prepared peers.
- Data indications are still accepted inbound (a permitted-but-unbound peer may legitimately produce them); the asymmetry is deliberate.
- Re-adding pre-preparation writes would be a re-implementation, not a flag flip; a consumer that needs them is not this library's consumer.
