# Inbound Allocation Delivery Implementation Plan

**Date:** 2026-08-19
**Status:** Accepted; not yet implemented
**Track:** 2 of 3 in the 2026-08-19 architecture deepening program
**Depends on:** Nothing — parallel-safe with the other tracks
**Related:** [Program index](2026-08-19-architecture-deepening-program.md), [Prepared-only writes ADR](2026-08-15-prepared-only-writes.md), [Allocation lifecycle plan](2026-08-17-allocation-lifecycle-plan.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Independently audited against `122019cf5a040bcb7a8ba9002bd3a82e9ad947cf` on 2026-08-19; T2.S1 passed after delivery/seal linearization, ChannelData result semantics, no-live ownership, copy scope, queued-before-seal observability, deterministic evidence, and synchronization cost were closed

## Goal

Make internal Allocation own the complete semantic delivery of decoded peer data: channel lookup, peer association, payload ownership, queue admission, full-queue drop, and post-seal disposition. Root `Client` keeps external source admission and TURN wire interpretation but stops composing Allocation-owned lookup with Allocation-owned queueing.

## Current Shape (verified 2026-08-19 at `122019cf5a040bcb7a8ba9002bd3a82e9ad947cf`)

`Client.HandleInbound` admits only the configured canonical server, demultiplexes STUN and ChannelData, and returns protocol errors only for admitted malformed or unexpected input (`client.go:363-391`). For Data indications, root decodes and canonicalizes the peer, extracts payload, finds the live Allocation, and calls its queue operation (`client.go:393-445`). For ChannelData, root decodes the frame, finds the live Allocation, asks it to resolve the channel number, constructs the unknown-channel error, and then calls the separate queue operation with the resolved peer (`client.go:447-468`).

`UDPConn.HandleInbound` only copies a peer-labeled payload into a nonblocking queue, while `FindAddrByChannelNumber` only delegates to `bindingManager` (`internal/client/udp_conn.go:634-658`). `UDPConn` already owns both the binding manager and the read queue (`internal/client/udp_conn.go:43-80`), and public `Allocation.ReadFrom` is the consumer-facing delivery seam with documented full-queue drop and close behavior (`allocation.go:48-56`; `internal/client/udp_conn.go:152-168`).

The normal seal path clears root's live Allocation pointer before lifetime-zero release (`internal/client/udp_conn.go:513-541`), so later inbound data is normally discarded in root. A race can retain a previously loaded `UDPConn` pointer and enqueue after its `closeCh` is closed because the current queue method does not synchronize delivery with the seal transition. The accepted grill resolves that ambiguity: delivery and seal have one linearization order; a delivery linearized after seal is silently handled without channel lookup or queueing, while a delivery linearized before seal may enqueue. Seal does not drain, reorder, or close `readCh`; the unchanged `ReadFrom` select may therefore choose either an already queued datagram or the closed error when both arms are ready after seal.

The prepared-only ADR explicitly keeps inbound Data indications valid for permitted-but-unbound peers, and current ChannelData lookup accepts any assigned binding rather than requiring a successful `PreparePeer` (`docs/adr/2026-08-15-prepared-only-writes.md`; `internal/client/binding.go:174-180`). This slice preserves that asymmetry.

## Decision

Root `Client` retains server-source admission, STUN/ChannelData classification and decoding, transaction completion, and Data-indication peer canonicalization. Once it has a decoded payload plus either a canonical peer or a channel number, it delegates semantic delivery to the live Allocation.

`UDPConn` exposes two package-crossing delivery operations with the concrete semantics of `HandleDataIndication(data, peer)` and `HandleChannelData(data, channel) handled bool`; exact private names may vary, but no tagged union is introduced. The ChannelData result means only that the datagram was handled or silently discarded: it returns false only for a live Allocation with an unknown channel, allowing root to preserve the existing `errChannelBindNotFound` identity and formatting. A live known channel returns true even when the full queue drops its payload. A sealed Allocation returns true for both known and unknown channels so root emits no post-seal error.

`UDPConn` adds one private delivery read/write guard. Each delivery holds its read side across the live check plus any channel lookup, delivery-owned payload copy, and nonblocking queue admission. `startCloseLocked`, while already holding `closeMutex`, holds the delivery guard's write side only across the `closeCh` transition, then releases it before aborting transactions, notifying deallocation, or emitting lifetime zero. The lock order is `closeMutex` then delivery guard for seal; delivery never takes `closeMutex`, and no delivery guard spans socket or transaction I/O. This produces two dispositions: delivery linearized before seal may lookup/copy/enqueue; delivery linearized after seal performs no additional delivery-owned payload allocation or copy, lookup, error, or queue write. Root may already have copied wire bytes for decoding before invoking a stale Allocation pointer.

While live, both operations copy the payload before returning so caller read-buffer mutation cannot alter queued data, and they use the existing nonblocking bounded queue. Channel lookup continues to accept assigned-but-not-prepared bindings. Seal does not drain, reorder, or close the queue. `ReadFrom` remains the unchanged select between `readCh` and `closeCh`, so no guarantee is made that a pre-seal queued item will be observed after seal.

**Rejected alternative (do not do this):** Move server-source admission, wire demultiplexing, STUN/ChannelData decoding, transaction completion, or canonical address handling into `internal/client`. Those are accepted root responsibilities and would enlarge the seam.

**Rejected alternative (do not do this):** Use one tagged inbound union. The two real decoded forms have distinct valid identities; a tag would add invalid combinations and caller-side construction without hiding more behavior.

**Rejected alternative (do not do this):** Require a prepared binding for inbound ChannelData or remove Data indications. Prepared-only is an outbound invariant; current inbound asymmetry is deliberate.

**Non-goals:** No parser, canonicalization, public `ReadFrom`, queue-size, copy-count optimization, drain, close-selection, binding identity, prepared-only write, error-text, general packet-path performance, or protocol-codec change.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T2.S1 | New | Decoded Data indications and ChannelData are delivered through Allocation-owned semantics | None | Removes outward channel lookup plus caller-composed lookup/queue delivery in the same slice |

## Implementation Slices

### Slice T2.S1 — Route decoded peer data through Allocation

**What it delivers:** One PR replacing the root `FindAddrByChannelNumber` then `HandleInbound` composition with two Allocation-owned delivery operations, consolidating live/sealed disposition, channel lookup, peer association, copy, and queue admission behind `UDPConn`, preserving root errors and wire responsibilities, and pinning the accepted post-seal silent-drop rule.

**Existing-work disposition:** New slice. There are no open issues, PRs, or implementation branches to retain or rebaseline as of 2026-08-19.

**Blocked by:** None.

**Single owner after merge:** Root `Client` owns external source admission, decoding, canonicalization, transaction completion, and the no-live-Allocation silent discard. Once root loads a nonnil Allocation pointer, `UDPConn` owns decoded peer-data delivery, delivery/seal linearization, and seal disposition. `bindingManager` remains the sole channel-number-to-peer identity owner. The bounded read queue remains private to `UDPConn`.

**Authority completeness:** No persisted fact becomes authoritative. The same slice covers both decoded inbound producers, the only channel lookup consumer outside binding maintenance, queue construction, live/sealed validation, and public `ReadFrom` consumption. The old generic lookup-plus-queue composition is removed.

**Transitional-seam budget:** None at merge. The old outward channel lookup and generic queue method may coexist only while the branch migrates both root callers; neither remains in the intended PR.

**Blast radius:** Data-indication and ChannelData delivery, unknown-channel error reachability, payload lifetime, queue-full behavior, seal races, and focused tests across root/internal packages. The delivery guard adds one bounded read-side synchronization operation per delivery and one write-side acquisition around seal's `closeCh` transition; it may nest with `bindingManager` lookup in delivery order but never with socket/transaction I/O or a reverse `closeMutex` acquisition. Source admission, malformed input, transaction completion, canonical peer labels, assigned-binding identity, short-buffer behavior, public interfaces, bytes, and nonblocking queue admission are preserved. Intended clarification: a root caller holding a stale Allocation pointer after seal now silently handles the datagram before lookup rather than possibly enqueueing or reporting unknown channel. No benchmark or general performance guarantee is part of the slice. Untraced effects are not accepted: changing pre-seal queued-data behavior, adding prepared admission, reversing lock order, holding the delivery guard across I/O, or moving wire ownership stops the slice.

**Artifact classification:** Allocation-owned live delivery and the seal guard are shipped behavior and required lifecycle enforcement. Root-to-`ReadFrom` tests, queue saturation tests, payload ownership tests, and race tests are verification aids. Plans, issues, review receipts, and tasks are process or traceability metadata. No verification aid becomes a maintained product deliverable.

**Representation contract:** Supported domain = datagrams admitted from the configured server and successfully decoded by existing `pion/stun` or `internal/proto` code into either Data-indication payload plus canonical `netip.AddrPort`, or ChannelData payload plus a 16-bit channel number; root no-live discard; and, when root has loaded a nonnil Allocation pointer, live/sealed linearization, known/unknown assigned channel, and queue available/full. Root parsers and canonicalizers own wire and peer representations; root Client owns the no-live disposition; the delivery guard owns live/sealed ordering; `bindingManager` owns channel association; `UDPConn` owns copied queued payloads. Guarantee = universal over this finite semantic table, not over malformed wire syntax or arbitrary peer spellings. Terminating evidence = one focused case per behaviorally distinct row, controlled tests of both delivery/seal orders, payload-copy and queue-full cases, structural preservation of unchanged `ReadFrom`/no-drain behavior, existing parser/admission tests, `go test -race ./...`, `task preflight`, same-head hosted CI, and one review plus at most one replacement.

**Contract closure:** Not triggered. The delivery table is finite, has one delivery owner after the slice, and ordinary focused tests reasonably cover live/sealed, known/unknown, and queue states.

**Evidence budget:** Data indication delivered with canonical peer and immutable copied payload; known ChannelData delivered via internal lookup; assigned-but-unprepared channel still delivered; live unknown channel returns the existing root error and no queue entry; no-live Allocation silently discards in root; full queue drops either live form without blocking and a known channel still reports handled. Controlled delivery-guard tests exercise both orders: delivery wins and queues before seal; seal wins and Data indication plus known/unknown ChannelData all silently discard, with sealed unknown producing no root error. A structural assertion confirms seal does not remove a pre-seal queued item, but no test asserts which ready `ReadFrom` select arm wins after seal. Short destination buffer preserves `io.ErrShortBuffer`; wrong-server, malformed STUN/ChannelData, unsupported indication, and transaction completion remain covered by existing root tests. `go test -race` is the data-race gate rather than evidence of semantic order. No mutation, fuzzing, repeated timing loop, or parser matrix. Termination = listed cases and gates, one fresh review, at most one replacement, and no `stop-for-decision` finding.

**TDD and preservation evidence:** First add public-seam tests entering through `Client.HandleInbound` and observing `Allocation.ReadFrom` for both decoded forms and live unknown channel. Add controlled internal tests around the delivery guard for both linearization orders, plus direct queue saturation and payload-ownership cases where root cannot drive the condition deterministically. Preserve root parser/source tests unchanged wherever possible. Do not use scheduling sleeps or assert which post-seal `ReadFrom` arm wins.

**Dispatch context budget:** This slice contract; `client.go:363-468`; `allocation.go:48-56`; `internal/client/udp_conn.go:43-80,152-168,513-541,634-658`; `internal/client/binding.go:165-180`; root `client_test.go`, Allocation tests, and focused internal UDPConn tests; plus the prepared-only and Allocation lifecycle ADRs. No transaction implementation, binding readiness redesign, or historical review transcript is required. The change is two root call sites, two internal operations, and focused tests and fits one fresh context.

**Slice decision audit:** Strongest further split = introduce Allocation delivery operations first and add sealed-drop behavior second. Rejected because the new owner must define its lifecycle disposition to be coherent, and retaining race-dependent delivery would leave the semantic operation incomplete. Strongest merge = combine with channel-binding readiness because both touch bindings. Rejected because delivery only consumes the existing channel bijection and has separate ownership/evidence; either can merge green alone. No blocker is required.

**Stop conditions:** The change requires moving parsing or canonicalization into Allocation; preserving current behavior requires prepared-only inbound admission; deterministic sealed delivery requires draining/reordering the queue or redesigning `ReadFrom`; the delivery guard must be held across socket/transaction I/O or needs a reverse lock order; the live unknown-channel error cannot be preserved without exposing lookup mechanics; or packet-copy optimization becomes necessary to complete the interface move.

## Acceptance Criteria

- [ ] Every admitted, successfully decoded Data indication and ChannelData item in the supported finite domain for which root loaded a nonnil Allocation delegates semantic delivery to `UDPConn`; root owns no-live discard, and all representation owners, the universal domain, and terminating evidence are defined in T2.S1.
- [ ] Root `Client` no longer composes outward channel lookup with queue delivery, while it retains source admission, wire decoding, canonicalization, transaction completion, and existing unknown-channel error construction.
- [ ] Live known channels and Data indications preserve peer labels, copy ownership, queue-full drop, short-buffer behavior, and assigned-but-unprepared inbound acceptance; only live unknown ChannelData returns the existing root error.
- [ ] The delivery guard linearizes decoded delivery with seal: delivery ordered before seal may enqueue, while Data indication and known/unknown ChannelData ordered after seal are silently handled without additional delivery-owned copy, lookup, or queueing.
- [ ] Seal does not drain, reorder, or close `readCh`; the unchanged `ReadFrom` select makes no guarantee which ready arm wins for pre-seal queued data after seal.
- [ ] Public API, wire parsing, prepared-only outbound writes, binding identity, and packet-path performance policy remain unchanged.

## Validation Gates

Run focused root-to-`ReadFrom` delivery tests, unknown/no-live/malformed/source-admission tests, internal queue saturation and seal-race tests, existing Allocation read/close tests, `go test ./...`, `go test -race ./...`, and `task preflight`. Keep the PR draft through bounded review and exact-head local certification, mark it ready afterward, and require the latest post-ready `ci` run's `ci-required` job to succeed on the exact live head before squash merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice`, composed with any future repository-specific overlays. Keep wire facts in root, decoded delivery on `UDPConn`, channel identity in `bindingManager`, and lifecycle seal on `UDPConn`. Stop rather than widening into parsing, queue redesign, prepared-peer policy, or packet optimization.
