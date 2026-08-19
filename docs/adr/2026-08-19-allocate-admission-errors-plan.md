# Allocate Admission and Public Error Vocabulary Implementation Plan

**Date:** 2026-08-19
**Status:** T2.S1 implemented by PR #100; T2.S2 pending
**Track:** 2 of 4 in the 2026-08-19 seam deepening program
**Depends on:** Nothing — parallel-safe; T1.S1 is recommended first to reduce test churn but is not a blocker
**Related:** [Program index](2026-08-19-seam-deepening-program.md), [Allocation lifecycle plan](2026-08-17-allocation-lifecycle-plan.md), [Modernize kept API plan](2026-08-15-modernize-kept-api-plan.md), [Allocation construction timing validity plan](2026-08-19-allocation-construction-timing-validity-plan.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Self-grilled and independently slice-audited against `dad68d4868efc8b041114f7a4efdeaa3b229edfb` on 2026-08-19 (see the program index for the audit receipt)

## Goal

Give "at most one live or in-flight Allocation per Client" one guard and one public error, make the relayed address cross the Client→`UDPConn` seam in its canonical spelling or not at all, and make the public error vocabulary have one owner for identity, prose, and prefix. Root `Client` gets deeper (the admission rule is one place, tested through the public interface); `UDPConn` loses an unvalidated address and an unused callback parameter; callers learn one error set.

## Current Shape (verified 2026-08-19 at `dad68d4868efc8b041114f7a4efdeaa3b229edfb`)

`Client.Allocate` enforces the single-allocation invariant twice: `allocTryLock.Lock()` (`client.go:295-298`) returns `errOneAllocateOnly` wrapping the unexported internal `errDoubleLock` for a concurrent in-flight Allocate, then `relayedUDPConn() != nil` (`client.go:300-303`) returns exported `ErrAlreadyAllocated` formatted with `relayedConn.LocalAddr().String()`. Which error a caller sees depends on the race; neither `errOneAllocateOnly` nor `errDoubleLock` is matchable by a caller; both branches are uncovered. `TryLock` (`internal/client/trylock.go`, 28 lines, plus `trylock_test.go`, 66 lines) wraps one compare-and-swap and has exactly one caller.

The server-reported relayed address crosses the seam three ways: `proto.RelayedAddress` from the response (`client.go:305`), an unvalidated `*net.UDPAddr` placed in `AllocationConfig.RelayedAddr` and stored as `UDPConn.relayedAddr` (`client.go:310-319`, `internal/client/udp_conn.go:63`), and the canonical `netip.AddrPort` computed afterwards (`client.go:331`) that `Allocation.RelayedAddr()` returns. The middle form has two consumers: `onDeallocated(c.relayedAddr)` (`internal/client/udp_conn.go:514`), whose root receiver ignores its parameter (`client.go:364`), and `UDPConn.LocalAddr()` (`internal/client/udp_conn.go:548`), whose only caller formats the `ErrAlreadyAllocated` message. `setRelayedUDPConn(relayedConn)` publishes the pointer to the inbound path (`client.go:329`) before `canonicalWireAddr` validates the address (`client.go:331`), so a datagram in that window is enqueued into a conn the caller will never receive.

Error vocabulary is split across the seam: `errNilContext` exists in both packages with different text (`errors.go:67`, `internal/client/errors.go:51`); `Client.Allocate` checks nil ctx with root's (`client.go:289-291`), `UDPConn.PreparePeer` with internal's (`internal/client/udp_conn.go:202-204`), and root `Allocation.PreparePeer` does not check (`allocation.go:39-46`), so the internal sentinel escapes the public interface. `errFake` is a test-only sentinel in the production vocabulary (`internal/client/errors.go:39`). The six re-exported sentinels carry duplicated doc comments that have diverged (`errors.go:20-45` versus `internal/client/errors.go:14-36`). `errChannelBindNotFound`, `errFailedToDecodeSTUN`, `errUnexpectedSTUNRequestMessage`, and `errOneAllocateOnly` lack the `turn:` prefix every other root sentinel carries (`errors.go:68-72`), and all reach callers.

## Decision

**One admission rule.** `Client` keeps an `allocating` flag under its existing mutex. `Allocate` performs one guarded check-and-claim: if a live Allocation is published or another Allocate is in flight, return `ErrAlreadyAllocated`; otherwise claim. The claim is released in the same critical section that publishes the new `UDPConn`, or on any failure path. `TryLock`, `trylock_test.go`, `errOneAllocateOnly`, and `errDoubleLock` are deleted. `ErrAlreadyAllocated`'s message no longer embeds the relayed address (identity unchanged; callers match with `errors.Is`).

**One relayed-address spelling.** `AllocationConfig.RelayedAddr`, `UDPConn.relayedAddr`, and `UDPConn.LocalAddr()` are deleted; `OnDeallocated` becomes `func()`. The canonical `netip.AddrPort` computed at Allocate remains the only spelling and continues to live on root `Allocation`. The construct-then-close rule for an invalid relayed address is preserved and tightened: Allocate constructs the `UDPConn`, validates, and on failure closes the unpublished conn (the lifetime-zero release is still emitted; Refresh carries no relayed address; STUN responses route through the registry, not the published pointer); on success it publishes the pointer and releases the claim in one critical section. A datagram arriving before publication is discarded exactly as when no Allocation is live.

**One error vocabulary owner.** Root owns public error identity, prose, and the `turn:` prefix; `internal/client` owns only the identities it raises and re-exports. The nil-context programmer error is checked once, at the public ingress: `Allocation.PreparePeer` gains the check with root's `errNilContext`, performed first — mirroring `Allocate` — so the (nil context, invalid peer) double programmer error now returns `errNilContext` rather than `ErrInvalidPeer` (accepted); `UDPConn.PreparePeer` drops its check and `internal/client` drops its `errNilContext` (the same single-ingress reasoning T4.S1 applied to cadence validation). `errFake` moves to a test file. Internal sentinel doc comments become one-line pointers to the root prose. The unprefixed sentinels *defined in root `errors.go`* gain the `turn:` prefix (text change, identity unchanged); the six sentinels re-exported from `internal/client` keep their internal message text (non-goal: their text is an identity owned internally, root owns only their prose). Sentinels stay unexported where they are unexported today; no public error is added.

**Rejected alternative (do not do this):** Keep two guards "for defense in depth." Two guards with two errors is the defect; one claim under one lock covers in-flight and live.

**Rejected alternative (do not do this):** Replace `TryLock` with `sync.Mutex.TryLock` and keep the second guard. That preserves two error modes.

**Rejected alternative (do not do this):** Keep `RelayedAddr` on `AllocationConfig` as `netip.AddrPort` "for symmetry." No `UDPConn` behavior reads it; carrying an unused fact across the seam is the leakage being removed. If a future consumer needs it, it is a one-field addition with a reader.

**Rejected alternative (do not do this):** Export `ErrNilContext` or make nil context panic. Programmer-error sentinels stay unexported; panicking is a behavior change no consumer asked for.

**Rejected alternative (do not do this):** Keep the internal nil-ctx check "as defense." T4.S1 established that duplicate enforcement at one public ingress splits ownership; the internal method is reached only through root and internal tests.

**Non-goals:** No change to Client close semantics (abort-current, non-terminal), Allocation lifecycle ownership, release emission, wire bytes, public API shape beyond error text, `HandleInbound`'s error contract, or `turntest`. No new exported error. No validation in `NewUDPConn`. No change to the message text of the six re-exported sentinels.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T2.S1 | Complete via PR #100 | One admission rule and error for the single live Allocation; relayed address crosses the seam only as canonical `netip.AddrPort`; `TryLock`, `LocalAddr`, `RelayedAddr`, `OnDeallocated` parameter gone; publish-after-validate | None | Second guard; unvalidated relayed-address spelling |
| T2.S2 | New | One owner for public error identity, prose, and prefix; nil-context checked once at the public ingress; `errFake` out of production vocabulary | None | Duplicate `errNilContext`; diverged prose |

## Implementation Slices

### Slice T2.S1 — One admission rule for the single live Allocation

**What it delivers:** One PR replacing `TryLock` + pointer check with one guarded claim returning `ErrAlreadyAllocated` for in-flight and live cases, deleting `TryLock`/`trylock_test.go`/`errOneAllocateOnly`/`errDoubleLock`, deleting `AllocationConfig.RelayedAddr`/`UDPConn.relayedAddr`/`UDPConn.LocalAddr`, changing `OnDeallocated` to `func()`, publishing the Allocation only after canonical validation, and adding public-interface admission tests.

**Implementation:** PR #100 is this slice's one product PR.

**Existing-work disposition:** New slice.

**Blocked by:** None. T1.S1 first is recommended (its helper is where `RelayedAddr` would otherwise be deleted from ten literals) but the slice is independently implementable.

**Single owner after merge:** Root `Client.Allocate`, under the Client mutex, is the only owner of the in-flight claim and of publishing/clearing the live Allocation pointer (clearing via `onDeallocated`, which `UDPConn.startCloseLocked` invokes exactly as today). Root `Allocation` is the only holder of the relayed address. `UDPConn` owns no address.

**Authority completeness:** No persisted fact. The claim is constructed, released on every failure path (context cancellation, Allocate transaction failure, invalid relayed address after the doomed conn's Close, Client close during Allocate), and consumed by the only destructive consumer (a second Allocate) within the slice.

**Transitional-seam budget:** None at merge. No second guard, no `LocalAddr`, no address parameter on `OnDeallocated`, no `RelayedAddr` field may remain.

**Blast radius:** `client.go:58-79,285-366`, `internal/client/allocation.go:19-35`, `internal/client/udp_conn.go:56-63,95-110,514,548`, `internal/client/trylock*.go`, `errors.go`, `internal/client/errors.go`, and every test that sets `RelayedAddr` or `OnDeallocated`. Concurrency: one new boolean under the existing Client mutex; no lock held across I/O (the claim is taken, the mutex released, the Allocate transaction performed, the mutex retaken to publish/clear). Ordering: construct → validate → (invalid: Close unpublished conn; valid: publish and release claim). Interfaces: `ErrAlreadyAllocated` text changes (identity unchanged); `AllocationConfig` loses a field and a callback parameter (internal). Failure modes: an invalid relayed address still emits exactly one lifetime-zero release; `HandleInbound` during the doomed conn's Close discards (no live Allocation) instead of enqueuing into an unreceivable conn; Client close during Allocate still wins with the closed error and releases the claim. Performance/security/dependencies: none. Untraced effects are not accepted: any path that leaves the claim held, any second release, or any HandleInbound delivery to an unpublished conn stops the slice.

**Artifact classification:** The admission guard, publish-after-validate, and the seam narrowing are shipped behavior and required lifecycle enforcement. The admission tests are verification aids. Plans/issues are process metadata.

**Representation contract:** Supported domain = the finite set of Allocate admission paths on one Client: first Allocate; concurrent second Allocate while the first is in flight; Allocate while an Allocation is live; Allocate after the live Allocation closed (caller Close and self-seal); Allocate that fails before construction; Allocate with an invalid relayed address; Client close during Allocate. Representation owner = `Client.Allocate` under the Client mutex. Guarantee = universal over that finite set. Terminating evidence = the matrix below, `go test -race ./...`, `task preflight`, hosted CI, one review plus at most one replacement.

**Contract closure:** Triggered — a second live Allocation or an unreleased claim is a broken lifecycle obligation (server resource or a permanently un-Allocatable Client), and admission is reached through independent paths (concurrent callers, live pointer, invalid-address close, Client close) that one focused test cannot cover.

| Semantic class | Expected disposition | Enforcement owner | Evidence | Guard mutation when required | Status |
|---|---|---|---|---|---|
| First Allocate on an idle Client | Claim, allocate, publish, release claim | `Client.Allocate` | Existing Allocate tests | n/a | Covered |
| Second Allocate while the first is in flight | `ErrAlreadyAllocated`, no network output | `Client.Allocate` | Deterministic concurrent test (observer conn gates the first request) | Delete the in-flight branch and observe the test fail | Covered |
| Allocate while an Allocation is live | `ErrAlreadyAllocated`, no network output | `Client.Allocate` | Focused public test | Covered by the same mutation | Covered |
| Allocate after caller `Close` of the live Allocation | Succeeds (claim and pointer were cleared) | `Client.Allocate` + `onDeallocated` | Sequential re-Allocate test | n/a | Covered |
| Allocate after the live Allocation self-sealed | Succeeds | `Client.Allocate` + `onDeallocated` | By composition: the internal refresh-failure suite proves self-seal invokes `onDeallocated` (`udp_conn.go:514`, the same call caller Close makes), and the root re-Allocate-after-Close test proves Allocate succeeds after `onDeallocated`; no root self-seal timing cell | n/a | Covered |
| Allocate fails before construction (transaction error, cancellation) | Claim released; a later Allocate succeeds | `Client.Allocate` | Existing cancellation tests plus one re-Allocate assertion | n/a | Covered |
| Invalid relayed address | Construct, do not publish, Close (one lifetime-zero release), `ErrInvalidRelayedAddress`, claim released; inbound during that window discards | `Client.Allocate` | Existing `TestAllocateRejectsInvalidRelayedAddress` extended with no-publication assertion | n/a | Covered |
| Client `Close` during Allocate | Closed error wins; claim released; Client remains usable for a later Allocate | `Client.Allocate` + registry abort | Existing close-versus-cancel test plus one re-Allocate assertion | n/a | Covered |

**Evidence budget:** The matrix rows are the complete budget: one deterministic concurrent-Allocate test, one live-Allocation rejection, two re-Allocate-after-end cases, one claim-release-after-failure assertion, one invalid-relayed no-publication assertion, one close-during-Allocate re-Allocate assertion; existing Allocate/lifecycle/refresh-failure suites; `go test -race ./...`. One guard mutation: delete the in-flight branch of the claim and observe the concurrent test fail. Negative evidence: no `TryLock`, `errOneAllocateOnly`, `errDoubleLock`, `LocalAddr`, or `RelayedAddr` symbol. No repetition counts, platforms, or timing cells.

**TDD and preservation evidence:** Write the concurrent-Allocate and live-rejection tests first against the public interface (both branches are uncovered today); then replace the guards; then write the no-publication assertion and move publication after validation; then delete the seam fields and parameter; then delete `TryLock`. Preservation gates: `TestAllocateRejectsInvalidRelayedAddress` (one release), `TestAllocateCancelVsClientClose`, Allocation lifecycle tests, `HandleInbound` discard-without-live-Allocation tests.

**Dispatch context budget:** This slice contract; `client.go:58-79,285-366`; `internal/client/allocation.go:16-35`; `internal/client/udp_conn.go:55-110,486-530,548-550`; `internal/client/trylock.go`; `errors.go`; `allocate_ctx_test.go`, `allocation_test.go:194-229`; the Allocation lifecycle plan. Fits one fresh context: one guard, one ordering move, three deletions, bounded tests.

**Slice decision audit:** Strongest further split = admission rule in one PR, relayed-address seam narrowing in another. Rejected: the error message is the only consumer of `LocalAddr`, and the publish-after-validate move is part of the same admission ordering; splitting leaves `LocalAddr` alive for one PR solely to format a string. Strongest merge = fold T2.S2. Rejected: T2.S2 touches `PreparePeer` and public error text with no admission semantics; separate PRs keep each preservation argument narrow. No blocking edge is needed.

**Stop conditions:** The Client mutex must be held across the Allocate transaction or any I/O (the claim itself is held across the transaction by design; the lock is not); the construct-then-close release cannot be preserved without publishing; a path is found where the claim is not released; `UDPConn` turns out to read the relayed address; or Client close semantics would need to change.

### Slice T2.S2 — One owner for the public error vocabulary

**What it delivers:** One PR adding the nil-context check to `Allocation.PreparePeer` with root's sentinel, removing the internal check and sentinel, moving `errFake` to a test file, reducing internal sentinel comments to pointers at the root prose, and adding the `turn:` prefix to the remaining unprefixed root sentinels.

**Existing-work disposition:** New slice.

**Blocked by:** None. T2.S1 first is recommended (it deletes `errOneAllocateOnly`, one of the unprefixed sentinels) but not required: if T2.S2 lands first it prefixes a sentinel T2.S1 later deletes.

**Single owner after merge:** Root `errors.go` owns public error prose and prefix; root public entry points own programmer-error checks; `internal/client/errors.go` owns only identities.

**Authority completeness:** No persisted fact. Both public nil-context ingresses (`Allocate`, `Allocation.PreparePeer`) are covered; the internal method's only callers are root and internal tests, which pass real contexts.

**Transitional-seam budget:** None at merge. No second `errNilContext`, no `errFake` in production code, no duplicated prose.

**Blast radius:** `errors.go`, `internal/client/errors.go`, `allocation.go:39-46`, `internal/client/udp_conn.go:202-204`, tests that reference the internal sentinel or `errFake`. Interfaces: error text of the root-defined unprefixed sentinels gains a prefix (identity unchanged; the sole consumer matches by identity per the README's typed-values statement); the six re-exported sentinels keep their text. Behavior: `Allocation.PreparePeer(nil, …)` returns root's sentinel instead of internal's; `UDPConn.PreparePeer(nil, …)` is no longer defended (internal, unreachable from the public surface). No concurrency, wire, performance, or dependency effect. Untraced effects are not accepted: any consumer found matching on error text stops the slice.

**Artifact classification:** Public error text and the nil-context check are shipped behavior. Prose and comments are process/traceability metadata. Tests are verification aids.

**Representation contract:** Supported domain = the sentinels defined in root `errors.go` (not the six re-exports) and the two public nil-context ingresses. Owner = root `errors.go` and the two public methods. Guarantee = universal over that finite set. Terminating evidence = one nil-context test per public ingress, a grep that every root-defined sentinel carries the prefix, `go test ./...`, `task preflight`, hosted CI, one review.

**Contract closure:** Not triggered — text and a single programmer-error check; focused tests suffice.

**Evidence budget:** Two focused tests (nil ctx to `Allocate` and to `Allocation.PreparePeer` return root's sentinel); existing suites green; negative grep for internal `errNilContext` and production `errFake`. No mutation, platforms, or repetitions.

**TDD and preservation evidence:** Write the `Allocation.PreparePeer` nil-context test first; add the check; remove the internal check/sentinel; move `errFake`; edit prose and prefixes. Preservation: every `errors.Is` assertion in the suites is unchanged.

**Dispatch context budget:** This slice contract; `errors.go`; `internal/client/errors.go`; `allocation.go`; `internal/client/udp_conn.go:201-205`; the README's typed-values statement. Fits trivially in one context.

**Slice decision audit:** Strongest split = none sensible. Strongest merge = into T2.S1; rejected above. No blocking edge.

**Stop conditions:** A consumer is found to depend on error text; or the nil-context decision would need a new exported error or a panic.

## Acceptance Criteria

- [x] A concurrent second Allocate, or an Allocate while an Allocation is live, returns `ErrAlreadyAllocated` with zero network output, and an Allocate after the live Allocation ends (caller Close or self-seal) succeeds. Domain: the T2.S1 matrix; owner: `Client.Allocate`; guarantee: universal over that finite set; evidence: the matrix plus one guard mutation.
- [x] `TryLock`, `errOneAllocateOnly`, `errDoubleLock`, `UDPConn.LocalAddr`, `AllocationConfig.RelayedAddr`, and the `OnDeallocated` address parameter do not exist; the relayed address lives only on root `Allocation` as canonical `netip.AddrPort`.
- [x] An invalid relayed address still yields exactly one lifetime-zero release and `ErrInvalidRelayedAddress`, and the doomed Allocation is never published to the inbound path.
- [ ] `Allocate(nil, …)` and `Allocation.PreparePeer(nil, …)` return root's `errNilContext` (checked first); no internal `errNilContext` or production `errFake` exists; every sentinel defined in root `errors.go` has a message starting with `turn:` (the six re-exports keep their text); sentinel identities are unchanged. Domain: root-defined sentinels and the two public ingresses; owner: root `errors.go`; guarantee: universal over that finite set; evidence: two ingress tests and one grep.
- [ ] Client close semantics, Allocation lifecycle ownership, wire bytes, and public API shape (beyond error text) are unchanged.

## Validation Gates

Per slice: the focused tests in its evidence budget, `go test ./...`, `go test -race ./...`, and `task preflight`; draft through review and certification, ready, post-ready `ci-required` success on the exact head, squash merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice`; this repository has no overlay. Keep admission on `Client.Allocate`, the relayed address on root `Allocation`, lifecycle on `UDPConn`, and error prose at root. Stop rather than holding the Client mutex across I/O, adding an exported error, or changing Client close semantics.
