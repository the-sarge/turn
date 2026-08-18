# TURN Consistency and Bounded-State Implementation Plan

**Date:** 2026-08-17
**Status:** Accepted; T3.S1 implemented by PR #53; T3.S2 remains on the frontier
**Track:** 3 of 3 in the 2026-08-17 architecture deepening program
**Depends on:** Nothing; both slices are independent of Tracks 1 and 2 and of each other
**Related:** [Program index](2026-08-17-architecture-deepening-program.md), [Modernize the kept API plan](2026-08-15-modernize-kept-api-plan.md), [Prepared-only writes ADR](2026-08-15-prepared-only-writes.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Independently audited against `e251b6b` on 2026-08-17; both slices passed after typed-error recovery/exhaustion and concurrent final-channel capacity contracts were closed; T3.S1 implemented by PR #53

## Goal

Ship the two narrow correctness/consistency outcomes that survived grilling without inventing the rejected authenticated-exchange or prepared-peer modules: preserve typed TURN server errors through ChannelBind's existing policy, and make the finite channel-number space fail explicitly rather than silently overwriting a live binding. Contract the binding manager's dead test-only mutation surface while establishing the capacity owner.

## Current Shape (verified 2026-08-17 at `e251b6b`)

Allocate, Refresh, and CreatePermission convert a well-formed non-438 TURN error response to `*stun.TurnError` (`client.go:192-283`; `internal/client/allocation.go:107-125`; `internal/client/udp_conn.go:588-607`). ChannelBind instead formats most codes into untyped errors; 400 is wrapped with `errChannelBindBadRequest`, other codes only with `errCannotBindChannel`, and 438 updates nonce and returns the retry sentinel (`internal/client/udp_conn.go:756-803`). This violates M1's accepted failure-as-values direction for a supported TURN request, but the four request paths have different challenge, retry, success, and Allocation-lifecycle policies.

Channel numbers are the inclusive range `0x4000` through `0x7fff`, which contains 16,384 values; the source comment says 16,383 (`internal/client/binding.go:13-20`). `bindingManager.assignChannelNumber` wraps to the minimum after the maximum, and `getOrCreate` then overwrites `chanMap[number]` while retaining the old peer in `addrMap` (`internal/client/binding.go:112-164`). On the 16,385th distinct binding, inbound channel lookup can therefore resolve to a different peer than an older prepared binding. No production path deletes bindings during an Allocation, so silent reuse is not a lease/reclamation policy.

The binding manager also exposes helpers used only by internal tests: `create`, `deleteByAddr`, `deleteByNumber`, and `size`; `binding.mgr` is assigned but never read (`internal/client/binding.go:34-45,139-141,184-219`; usage census at this commit). Those helpers encourage tests to validate a generic map rather than the live get-or-create, lookup, iteration, preparation, and write behavior.

## Decision

Keep authenticated request construction and method-specific policy explicit. For ChannelBind only, the classifier-created error for every well-formed non-438 server response carries a `*stun.TurnError` in its error chain. Code 400 additionally remains recognizable as `errChannelBindBadRequest`: a previously confirmed binding intentionally consumes that error and recovers with nil, while a fresh binding exposes the typed error through its binding and Allocation terminal chains. Code 438 remains the nonce-update/retry signal and is never converted to `*stun.TurnError` in this slice, including when the existing retry owner exhausts its attempts. Malformed error responses preserve the existing unexpected-response failure class.

Make `bindingManager` the sole channel-number capacity owner. Existing peers always receive their existing binding. A new distinct peer receives one of the 16,384 channel numbers exactly once. On a live Allocation whose permission phase succeeds, when all numbers are assigned, another distinct peer fails without overwriting either map and without starting a ChannelBind request; `PreparePeer` returns an error satisfying public `ErrChannelBindFailed`. Permission rejection, cancellation, or Allocation closure may terminate earlier with their existing outcomes. A permission may already have been created because the accepted operation order is permission then binding; changing that order or reclaiming bindings is outside this slice.

Delete `binding.mgr` and the dead test-only `create`, delete, and size methods in the capacity slice; adapt tests to the same live get-or-create/lookup/all seam used by production or to public `PreparePeer`/`WriteTo` behavior. Do not delete live state behavior or broad test scenarios. The per-IP Permission and per-transport-address channel binding remain distinct domain facts.

**Rejected alternative (do not do this):** Build a generic authenticated-exchange module now. It would need request setters, success parsers, retry switches, and lifecycle callbacks for four materially different policies; repetition alone does not justify that shallow interface. Re-audit after Tracks 1 and 2, and extract only if a small policy-free core remains.

**Rejected alternative (do not do this):** Extract a prepared-peer orchestration object. `Allocation`/`UDPConn` already provide the deep public module; a second owner would require a wide internal interface spanning preparation, write admission, refresh, inbound lookup, seal, and worker join.

**Rejected alternative (do not do this):** Reclaim or reuse channel numbers. No binding expiry/reclamation lifecycle is accepted, and reuse can violate the server's channel/peer association. Explicit exhaustion is the safe bounded behavior.

**Non-goals:** No authenticated Allocate 438 retry; no setter-order or wire-byte change; no retry-policy change; no public error addition for channel exhaustion; no Permission identity change; no binding reclamation; no Send indication; no prepared-peer module or file-move-only refactor; no deletion of `internal/proto`; no broad test-harness rewrite.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T3.S1 | Complete via PR #53 | ChannelBind preserves typed TURN errors while existing 438/400 policy stays local | None | n/a |
| T3.S2 | New | Channel-number allocation is bounded and binding manager mutation surface contracts | None | Removes dead binding manager test-only mutation methods in this slice |

T3.S1 and T3.S2 are parallel-safe in behavior and have no genuine blocking edge. They touch nearby `udp_conn.go` regions, so the second branch may need an ordinary rebase after the first merges; a likely text conflict is not an architectural dependency.

## Implementation Slices

### Slice T3.S1 — Preserve typed TURN errors through ChannelBind policy

**What it delivers:** One PR changing ChannelBind error classification so the classifier-created error for every well-formed non-438 response contributes a `*stun.TurnError`. Code 400 also preserves `errCannotBindChannel` and `errChannelBindBadRequest`; a fresh/unknown binding exposes that error through its failure/terminal chain, while the existing previously-ready recovery intentionally consumes it and returns nil. Code 438 still updates nonce and returns `errTryAgain`, including after retry exhaustion; success and malformed-response behavior remain unchanged. Setter order and emitted bytes do not change.

**Implementation:** PR #53 is the slice's one intended product PR.

**Existing-work disposition:** New slice.

**Blocked by:** None.

**Single owner after merge:** `handleChannelBindErrorResponse` owns protocol-code classification; `handleBindChannelError` remains the single binding/Allocation disposition owner. No generic exchange owner is introduced.

**Authority completeness:** No persisted or durable fact becomes authoritative. The same slice covers every caller of ChannelBind response classification and the only consumers of the 400 sentinel.

**Transitional-seam budget:** None. Other authenticated request paths remain explicit by accepted decision, not as a temporary adapter.

**Blast radius:** Error chains returned by `PreparePeer`, recorded on failed bindings, and recorded as terminal cause for a fresh ChannelBind 400. `errors.Is` behavior for existing sentinels and 400 recovery/seal is preserved; direct classifier errors and externally visible fresh/unknown failures gain `errors.As(*stun.TurnError)`. Previously-ready 400 still returns nil after recovery. Retry counts, exhausted-438 disposition, nonce update, state transitions, public symbols, and wire bytes remain unchanged. Untraced effects are not accepted.

**Artifact classification:** Typed error propagation and 400 policy guards are shipped behavior and required lifecycle enforcement. Response-table and wire-capture tests are verification aids. Plans/issues/tasks are process metadata.

**Representation contract:** Supported domain = STUN success responses and error responses whose ERROR-CODE is well formed with codes 438, 400, or another code, plus malformed error responses, for messages returned by the transaction layer to ChannelBind; binding disposition additionally distinguishes fresh/unknown from previously-ready state. `pion/stun` owns message and ERROR-CODE parsing; `handleChannelBindErrorResponse` owns classification; `handleBindChannelError` owns lifecycle disposition. Guarantee = universal over this finite response-class/code/state table, not arbitrary STUN syntax. Terminating evidence = one case per table row, direct classifier assertions, existing recovery/seal tests, exact request-byte capture, race suite, preflight, and bounded review.

**Contract closure:** Not triggered. The error table is finite, has one classifier and one disposition owner, and ordinary focused tests cover every behaviorally distinct class.

**Evidence budget:** Direct classifier cases for 438 with nonce, 400, representative non-400/non-438 error, and malformed ERROR-CODE; assert the classifier's 400 error has `errors.As(*stun.TurnError)` and both existing `errors.Is` sentinels, while 438 remains only the retry sentinel. Real flow cases cover success, fresh-binding 400 with terminal-chain `errors.As`, previously-ready 400 returning nil and recovering, representative non-400/non-438 failure with `errors.As`, and three consecutive 438 responses preserving the existing exhausted-retry disposition without a typed error. Assert unchanged retry count/nonce and exact ChannelBind request bytes. Full race and preflight. No mutation required. Termination = listed cases, same-head hosted CI, one review, at most one replacement, and no `stop-for-decision` finding.

**TDD and preservation evidence:** Add failing `errors.As` assertions to representative 400 and server-error tests first. Keep the existing fresh/ready 400 outcomes green. Capture setters/bytes before changing classification to prove response-side work did not alter the request.

**Dispatch context budget:** This slice contract; `internal/client/udp_conn.go:667-803`; existing ChannelBind tests in `udp_conn_test.go` and `prepare_test.go`; Refresh/CreatePermission typed-error implementations as references only; and M1 failure-as-values/ChannelBind-400 rules. The change is one classifier plus focused tests and fits a small fresh context.

**Slice decision audit:** Strongest further split = typed generic errors and 400 dual-classification in separate PRs. Rejected because 400 is one row of the same finite classifier and splitting would temporarily make one well-formed code inconsistent. Strongest merge = combine with T3.S2. Rejected because response error identity and channel-number capacity have different owners, representations, and evidence; both are independently green. No blocker is required.

**Stop conditions:** A typed error cannot coexist with existing 400 `errors.Is` behavior; adding it changes the prepared-binding recovery or fresh-binding seal outcome; request bytes/setter order change; or uniform typed behavior would require adding authenticated Allocate 438 retry or a generic exchange interface.

### Slice T3.S2 — Bound channel-number allocation and contract the binding manager

**What it delivers:** One PR correcting the range count to 16,384; changing new-binding allocation to return explicit exhaustion instead of wrapping/overwriting; propagating exhaustion after a successful permission phase from `PreparePeer` as `ErrChannelBindFailed` without starting ChannelBind; preserving existing-peer lookup even at capacity; deleting unread `binding.mgr` and test-only `create`, `deleteByAddr`, `deleteByNumber`, and `size`; and re-anchoring their useful assertions on live get-or-create, bidirectional lookup, `all`, PreparePeer, and prepared-only WriteTo behavior.

**Existing-work disposition:** New slice.

**Blocked by:** None.

**Single owner after merge:** `bindingManager` is the only owner of the channel-number set, capacity check, and address↔number bijection. `PreparePeer` owns operation ordering and surfaces the manager's exhaustion as `ErrChannelBindFailed`.

**Authority completeness:** No persisted fact. The same slice covers the sole constructor for new bindings, capacity validation, both lookup consumers, and every production call site. Generic deletion/reclamation paths are removed rather than left as alternate mutation authority.

**Transitional-seam budget:** None. There is no wraparound compatibility mode, duplicate map representation, or temporary allocator. Permission-before-binding order remains accepted behavior, not a transitional seam.

**Blast radius:** Only the 16,385th distinct peer and later distinct peers change from corrupting map identity to explicit failure; existing peers, ordinary preparation, refresh, inbound ChannelData lookup, and prepared writes remain unchanged. Allocation memory remains bounded to 16,384 bindings. A permission transaction may precede local exhaustion because operation order is preserved. Public API stays the same; no new wire type or dependency. Untraced effects are not accepted.

**Artifact classification:** Capacity enforcement and address↔channel bijection are shipped behavior and required safety enforcement. Boundary, map-integrity, PreparePeer, inbound lookup, and WriteTo tests are verification aids. Plans/issues/tasks are process metadata.

**Representation contract:** Supported domain = canonical peer `netip.AddrPort` values accepted by `Allocation.PreparePeer`; one live Allocation whose permission phase succeeds; channel numbers in the closed integer interval `[0x4000,0x7fff]`; repeated same-peer and distinct-peer requests; inbound lookup by assigned number. Root canonicalization owns peer representation; permission handling owns admission to the binding phase; `bindingManager` owns the bijection and capacity. Guarantee = universal over the finite 16,384-number range and all manager construction paths once permission succeeds, with a canonical-subset guarantee over already-canonical peer addresses. Permission rejection, caller cancellation, and Allocation closure preserve their earlier existing outcomes and are outside the capacity guarantee. Terminating evidence = arithmetic boundary tests, full-range uniqueness/bijection test, existing-peer-at-capacity case, next-distinct exhaustion case with successful permission, production-path no-ChannelBind assertion, focused prepared/inbound preservation, race suite, preflight, and bounded review.

**Contract closure:** Not triggered. The finite range has one constructor and the complete boundary behavior is reasonably covered directly; no multi-owner semantic family remains after dead mutation paths are deleted.

**Evidence budget:** Arithmetic assertion that the inclusive range size is 16,384; allocate 16,384 distinct canonical peers and assert unique numbers plus forward/reverse lookup; repeat an existing peer at capacity and get the same binding; after successful permission, request the 16,385th distinct peer and assert map sizes/bijection unchanged and `ErrChannelBindFailed`; preload 16,383 entries and race two distinct peers for the final slot, asserting exactly one success, one exhaustion, and a 16,384-entry bijection; one PreparePeer harness case with successful permission proving no ChannelBind request after exhaustion; preservation cases for permission rejection/cancellation/closure before binding; existing prepared-only WriteTo and inbound channel lookup cases; full race and preflight. At most one mutation: remove the capacity guard and observe the 16,385th integrity test fail. No reclamation, timing repetitions, fuzzing, or extra platform cells. Termination = listed cases, same-head hosted CI, one review, at most one replacement, and no `stop-for-decision` finding.

**TDD and preservation evidence:** Write the full-range boundary and existing-peer-at-capacity tests first; the next-distinct case must expose current overwrite. Add the PreparePeer no-ChannelBind assertion. Then contract the test-only manager methods and adapt tests without deleting live behavior assertions.

**Dispatch context budget:** This slice contract; `internal/client/binding.go`; `internal/client/binding_test.go`; `internal/client/udp_conn.go:176-390,628-665`; prepared-only/inbound tests in `prepare_test.go`, `udp_conn_test.go`, and root `client_test.go`; and the prepared-only ADR. The bounded change is one in-memory manager plus two production callers and focused tests; it fits one fresh context.

**Slice decision audit:** Strongest further split = capacity guard first, dead-surface deletion second. Rejected because deleting alternate mutation methods establishes the same single constructor and keeps the evidence on the production seam; both changes fit a small context. Strongest merge = combine with T3.S1. Rejected because the two invariants have separate owners and no correctness dependency. No blocking edge is required; likely textual overlap is handled by rebase rather than false serialization.

**Stop conditions:** A supported production path deletes/reclaims bindings; preserving existing peers requires number reuse; capacity cannot be checked atomically with construction; exhaustion would require changing permission-before-binding order or adding public API; or the full-range test cannot distinguish overwrite from legitimate reuse.

## Acceptance Criteria

- [x] The classifier-created error for every well-formed non-438 ChannelBind response carries a typed `*stun.TurnError`; fresh/unknown failures expose it, while previously-ready 400 recovery intentionally consumes it and returns nil. The finite supported response/code/state domain, owners, universal guarantee, and evidence are declared in T3.S1.
- [x] ChannelBind 400 retains existing ready-binding recovery and fresh-binding Allocation seal behavior, and 438 retains nonce retry behavior.
- [x] ChannelBind request setters, bytes, and retry counts remain unchanged.
- [ ] Every live binding in one Allocation has a unique channel number in `[0x4000,0x7fff]`, and both maps remain a bijection; the finite supported peer/range domain, owner, universal/canonical-subset guarantee, and evidence are declared in T3.S2.
- [ ] An existing peer remains resolvable at capacity; after successful permission, the next distinct peer fails with `ErrChannelBindFailed`, does not overwrite state, and starts no ChannelBind request; permission rejection/cancellation/closure retain their earlier outcomes.
- [ ] `binding.mgr` and test-only create/delete/size mutation paths are absent; tests retain live behavior coverage through production-shaped seams.
- [ ] Prepared-only ChannelData, permission-per-IP identity, binding-per-transport-address identity, public API, and caller-owned socket rules remain unchanged.

## Validation Gates

For T3.S1 run the ChannelBind response table, fresh/ready 400 lifecycle tests, request-byte capture, full race, and repository preflight. For T3.S2 run range/bijection/exhaustion tests, PreparePeer no-ChannelBind exhaustion, prepared WriteTo/inbound preservation, full race, and preflight. For each PR request `ci-certify` only after exact-head local certification and verify the resulting same-head `ci-required` status before merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice` for every slice/PR, composed with the repository-specific CI-label override in the program index: this repository uses `ci-certify`, not the shared default `ci:certify`. Keep authenticated method policy local, preserve the prepared-only structural invariant and the distinct Permission/channel-binding identities, and diagnose failures before reruns. Stop rather than extracting a generic exchange, prepared-peer module, channel lease/reclamation policy, or broader cleanup batch.
