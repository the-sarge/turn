# Server-Bound Outbound Transport Implementation Plan

**Date:** 2026-08-19
**Status:** Accepted; not yet implemented
**Track:** 1 of 3 in the 2026-08-19 architecture deepening program
**Depends on:** Nothing — safe to start first
**Related:** [Program index](2026-08-19-architecture-deepening-program.md), [Transaction registry plan](2026-08-17-transaction-registry-plan.md), [Allocation lifecycle plan](2026-08-17-allocation-lifecycle-plan.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Independently audited against `122019cf5a040bcb7a8ba9002bd3a82e9ad947cf` on 2026-08-19; T1.S1 passed after its exact interface, construction boundary, characterization strategy, source-domain representation, performance ceiling, and evidence budget were closed

## Goal

Make the configured TURN server a fact owned once by root `Client`, so internal transaction and Allocation callers can send only to that server and no longer carry or choose a destination. Deepen the existing Client/transaction-registry/Allocation seam rather than introducing another transport module.

## Current Shape (verified 2026-08-19 at `122019cf5a040bcb7a8ba9002bd3a82e9ad947cf`)

`NewClient` validates one canonical server, stores both its canonical `netip.AddrPort` and socket-facing `net.Addr`, and constructs the transaction registry with an unbound `writeTo(data, to)` callback (`client.go:55-65`, `client.go:129-165`). The public Client is explicitly bound to one server, and inbound admission compares every datagram against that one canonical source (`client.go:363-391`).

Destination authority nevertheless crosses every outbound internal seam. Each transaction entry stores `to net.Addr`; `Perform`, `Start`, `PerformWithContext`, `begin`, and retry send paths accept or reuse it (`internal/client/transaction.go:28-47`, `internal/client/transaction.go:55-118`, `internal/client/transaction.go:133-182`). Root transaction delegation accepts a destination, and Allocate's context-aware path supplies `c.serverAddr` (`client.go:338-355`).

Allocation construction passes both an arbitrary-destination raw writer, an arbitrary-destination transaction function, and `ServerAddr` (`client.go:309-323`; `internal/client/allocation.go:16-35`; `internal/client/udp_conn.go:46-55`, `internal/client/udp_conn.go:83-108`). Refresh, CreatePermission, ChannelBind, and ChannelData then pass that stored address back into those functions (`internal/client/allocation.go:47-63`; `internal/client/udp_conn.go:580-605`, `internal/client/udp_conn.go:777-796`, `internal/client/udp_conn.go:835-842`). No production path targets a second destination.

The accepted transaction-registry ADR already defines the one-server domain and rejects destination-scoped abort semantics, while preserving registration-before-send, rollback, retry ownership, caller-owned socket authority, nonterminal Client close, and Allocation abort-before-release (`docs/adr/2026-08-17-transaction-registry-plan.md`; `docs/adr/2026-08-17-allocation-lifecycle-plan.md`).

## Decision

Root `Client` remains the sole owner of the canonical configured server and adapts the caller-owned socket into one `sendToServer([]byte) (int, error)` function. The existing transaction registry captures that function at construction through `NewTransactionRegistry(sendToServer, rto)`. Its transaction entries retain copied bytes, timing, retry, and waiter state but no destination. `TransactionRegistry.Perform(msg)`, `Start(msg)`, and `PerformWithContext(ctx, msg)` retain their existing waited, fire-and-forget, and cancelable semantics without a destination parameter. T1.S1 introduces no new transaction adapter interface or transport wrapper.

`Client.performTransaction` becomes destination-free. `AllocationConfig.WriteTo` becomes `func([]byte) (int, error)`, `AllocationConfig.PerformTransaction` becomes `func(*stun.Message, bool) (TransactionResult, error)`, `AllocationConfig.ServerAddr` is removed, and `UDPConn` stores no server address. Allocate's context-aware wait and inbound transaction completion remain root/registry behavior and do not move into the Allocation seam. The existing required positional abort-current capability remains explicit because it participates in Allocation seal ordering rather than ordinary outbound transport.

All exact request builders, response policies, retry cadence, registry one-winner claims, initial-send rollback, lifetime-zero fire-and-forget semantics, and ChannelData encoding remain in their existing owners. The root adapter performs one `PacketConn.WriteTo` to the configured server for every send. The caller-owned socket remains outside internal authority.

The same slice performs only constructor signature changes forced by removing destination authority. It preserves the existing mandatory abort-current guard and adds no new nil-capability validation or invalid-construction behavior. It must not introduce a builder, factory, options layer, nested field-grouping exercise, or separate construction abstraction. Allocation construction is re-audited after merge using the program checkpoint.

**Rejected alternative (do not do this):** Add a generic `Transport`, `ServerIO`, or packet-writer module. There is one socket and one server; another named wrapper would be a pass-through seam rather than a deep owner.

**Rejected alternative (do not do this):** Preserve optional destination parameters for hypothetical multi-server use. A second destination is outside the accepted Client domain and would retain authority no supported caller needs.

**Rejected alternative (do not do this):** Fold request construction, credentials, nonce retry, response parsing, inbound completion, or Allocation lifecycle into the bound sender. That recreates the rejected generic authenticated-exchange or splits accepted owners.

**Non-goals:** No public API, wire-byte, setter-order, retry-policy, timer, socket-ownership, Client-close, server-canonicalization, Allocation-lifecycle, error-chain, package-layout, test-harness, or performance change.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T1.S1 | New | Every production outbound send is structurally bound to the configured TURN server | None | Removes destination parameters and storage from the registry and Allocation seam in the same slice |

## Implementation Slices

### Slice T1.S1 — Remove destination authority from outbound internals

**What it delivers:** One PR that binds the registry sender to root `Client.serverAddr`, removes destination fields and parameters from transaction operations, removes `ServerAddr` and arbitrary-destination callbacks from Allocation construction and `UDPConn`, adapts Refresh/CreatePermission/ChannelBind/release to the bound transaction capability, adapts ChannelData to the bound raw-send capability, and preserves every current outbound behavior through the real Client and Allocation seams.

**Existing-work disposition:** New slice. There are no open issues, PRs, or implementation branches to retain or rebaseline as of 2026-08-19.

**Blocked by:** None.

**Single owner after merge:** Root `Client` is the sole owner of the configured server identity and socket-address adaptation. `TransactionRegistry` remains the sole owner of transaction membership, copied bytes, retries, and terminal claims. `UDPConn` remains the sole Allocation lifecycle owner. The external caller remains the socket owner.

**Authority completeness:** No persisted fact becomes authoritative. For the in-memory server fact, `NewClient` performs construction and validation, the root bound sender is the only outbound address consumer, and `HandleInbound` remains the only source-admission consumer. Destination-bearing generic mutation paths are removed in the same slice.

**Transitional-seam budget:** None at merge. A branch may temporarily add bound operations while migrating callers, but no destination-bearing registry operation, transaction-entry field, Allocation callback, or `UDPConn.serverAddr` may remain in the intended PR.

**Blast radius:** All outbound Allocate, Refresh, CreatePermission, ChannelBind, lifetime-zero release, retry, and ChannelData writes; registry and Allocation constructor signatures; test adapters that currently observe or supply arbitrary destinations. Concurrency, ordering, error chains, public interfaces, wire representations, and caller socket authority are preserved. The implementation adds no network write, retry, goroutine, timer, payload copy, per-send address conversion, or protocol encoding step; no benchmark or general performance guarantee is part of this slice. The accepted trade-off is removal of unsupported per-request destination choice. Untraced effects are not accepted: a production network write outside the root bound sender, a second supported destination, a changed release/retry order, or any added per-send work named above stops the slice.

**Artifact classification:** Bound outbound behavior and the absence of destination authority are shipped behavior and required structural enforcement. Focused observer, request-shape, registry race, and lifecycle tests are verification aids. The plans, issues, review receipts, and task mirror are process or traceability metadata. No verification aid becomes a maintained product deliverable.

**Representation contract:** Supported domain = the non-test production send graph rooted in `Client` and bounded at the implementation head to `client.go`, `internal/client/transaction.go`, `internal/client/allocation.go`, and `internal/client/udp_conn.go`: anonymous/authenticated Allocate, retries, Refresh including lifetime zero, CreatePermission, ChannelBind, and ChannelData. `NewClient` plus the existing canonical-address validator own the configured server representation; `pion/stun` and `internal/proto` own message/frame bytes; the root bound sender owns socket-address adaptation; the compiled current source and bounded call-site census own the structural representation of destination-free signatures and absence of new generic modules. Guarantee = universal over every datagram instance emitted through that finite current graph because every actual socket write funnels through the one bound sender, not over future or hypothetical transports or destinations. Terminating evidence = a recorded static census of that graph, compiler-enforced destination-free signatures, the reviewed diff showing no forbidden module or operation, one representative live observation per outbound method class, existing byte-shape and transaction/lifecycle suites, `go test -race ./...`, `task preflight`, same-head hosted CI, and one review plus at most one replacement.

**Contract closure:** Not triggered. Destination misrouting would be material, but the supported send-site set and one adapter are finite and reasonably covered by structural signatures plus ordinary focused tests; no semantic matrix beyond the listed method classes is required.

**Evidence budget:** One authenticated Allocate flow observes both anonymous and authenticated sends; one representative regular Refresh, lifetime-zero release, CreatePermission, ChannelBind, and ChannelData case observes the configured server; one generic registry retry case proves retries reuse the captured sender and copied bytes. Existing wrong-source admission proves inbound authority remains separate. Preserve initial-send rollback, duplicate-ID rejection, response/abort/late-write ownership, cancellation, Client-close nonterminality, invalid-relayed construct-then-release, exact request/ChannelData bytes, and payload-length reporting. One recorded static census of the bounded production send graph confirms no destination-bearing outbound seam remains. Do not create a TURN-method-by-retry cross-product. No mutation, fuzzing, timing repetition, new platform matrix, or broad harness is required. Termination = the listed cases and gates, one fresh review, at most one replacement, and no `stop-for-decision` finding.

**TDD and preservation evidence:** First add or identify green destination-sensitive characterization tests through the real Client and Allocation paths. Preserve them while removing destination parameters; compilation plus the bounded production-source census is the negative evidence that unsupported destination authority is absent. Migrate registry tests to a bound recording sender while retaining all concurrency cases. Adapt existing request-shape and close tests without weakening assertions. Run exact-byte comparisons before and after the seam change.

**Dispatch context budget:** This slice contract; `client.go:55-65,129-165,282-355`; `internal/client/transaction.go`; `internal/client/allocation.go:16-100`; `internal/client/udp_conn.go:43-149,580-605,777-846`; existing registry tests, request-shape tests, Allocate cancellation tests, close-latency tests, and invalid-relayed tests; plus the 2026-08-17 transaction and Allocation lifecycle plans. No historical review transcript or other track plan is required. The change spans one root adapter, one registry, one Allocation construction seam, and their focused tests and fits one fresh context.

**Slice decision audit:** Strongest further split = bind the transaction registry first and Allocation sends second. Rejected because the first PR would leave two destination-authority models and would not establish the universal one-server invariant; the second is required for coherent completion. Strongest merge = include the broader Allocation-construction checkpoint. Rejected because the remaining constructor facts may be intrinsic and no validity defect has been established. No blocking edge is necessary because this slice does not depend on inbound delivery or binding readiness.

**Stop conditions:** A supported production caller genuinely targets a second destination; binding the sender would change emitted bytes, retry count, timeout, error precedence, or abort/release ordering; internal code would need the caller-owned socket rather than a send capability; required construction cleanup grows into a builder/factory; or the design begins to own authentication or response policy.

## Acceptance Criteria

- [ ] Every outbound datagram in the supported retained UDP Client domain targets the one canonical server configured at `NewClient`; representation owner, universal domain, and finite evidence are defined in T1.S1.
- [ ] No transaction entry or internal Allocation operation in the bounded compiled production send graph stores, accepts, or selects a destination; root `Client` alone adapts the server to the caller-owned socket. The compiled source/diff and recorded census own this universal structural guarantee.
- [ ] Allocate, Refresh, CreatePermission, ChannelBind, lifetime-zero release, retries, and ChannelData preserve exact bytes, ordering, results, and error behavior.
- [ ] Transaction one-winner concurrency, Client nonterminal close, Allocation abort-before-notification-before-release, invalid-relayed cleanup, and caller socket ownership remain unchanged.
- [ ] No generic transport, authenticated exchange, builder, clock, or broader constructor module appears in the reviewed production diff; this is a universal structural guarantee over the bounded current source graph owned by the compiled source/diff and recorded census.

## Validation Gates

Run focused configured-server observer tests, all transaction registry tests, request-shape tests, Allocate cancellation and invalid-relayed tests, Allocation close/refresh-failure tests, `go test ./...`, `go test -race ./...`, and `task preflight`. Keep the PR draft through bounded review and exact-head local certification, mark it ready afterward, and require the latest post-ready `ci` run's `ci-required` job to succeed on the exact live head before squash merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice`, composed with any future repository-specific overlays. Keep server identity in root, transactions in the registry, TURN method policy in existing callers, and Allocation lifecycle on `UDPConn`. Stop rather than restoring destination authority or widening into construction, authentication, timing, or socket work.
