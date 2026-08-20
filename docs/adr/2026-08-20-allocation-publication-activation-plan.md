# Allocation Publication and Activation Implementation Plan

**Date:** 2026-08-20
**Status:** Proposed; awaiting owner approval before implementation
**Related:** [Issue #102](https://github.com/the-sarge/turn/issues/102), [UDPConn construction and transaction crossing plan](2026-08-19-udpconn-construction-crossing-plan.md), [Allocation lifecycle plan](2026-08-17-allocation-lifecycle-plan.md), [Allocate admission and public error vocabulary plan](2026-08-19-allocate-admission-errors-plan.md), [RFC 8656 Sections 7.2–7.3](https://www.rfc-editor.org/rfc/rfc8656.html#section-7.2)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** RAS architecture review `20260820T031106-489ebd815b66a06b0af58dcf` against `d3fd19c5cbb040795d61bf80ce0794c792a779ce` produced one unanimous candidate; nine adjudications accepted quiescent construction followed by publish-then-atomic-start and folded the three apparent clusters into that single slice

## Goal

Guarantee structurally that a sealed Allocation cannot become the Client's published live Allocation. Root `Client.Allocate` must publish a fully initialized but quiescent `UDPConn` before any Allocation lifecycle worker can seal it, while `UDPConn` remains the sole owner of activation, maintenance, seal, terminal cause, abort-before-release ordering, exactly one lifetime-zero Release, and caller join.

## Evidence and Supported Domain

At base `12b4233078fa88e6ba54317b57e62e95a5b8e892`, merged product `4a25a8e0b939039f06d6951a88f4571a452f4500`, and current head `d3fd19c5cbb040795d61bf80ce0794c792a779ce`, a synchronization-based test under `go test -race` constructed a zero-lifetime `UDPConn`, waited for its permanent first Refresh failure to invoke `OnDeallocated`, then published it. In every tree the callback cleared a still-nil Client pointer, publication installed the already-sealed connection, and the race detector correctly reported no memory race. The defect is lifecycle ordering: the one-shot deallocation notification is consumed before publication, so later admission mistakes a sealed pointer for the live Allocation.

RFC 8656 Section 7.2 floors a newly created Allocation at the 600-second default, but permits a response to a retransmitted Allocate request to carry the Allocation's current remaining lifetime. Section 7.3 requires the client to retain the actual lifetime returned by the server. This client exposes configurable transaction timing and `internal/proto.Lifetime` decodes the complete unsigned 32-bit seconds representation without a product-level lower-bound contract. Short remaining lifetimes therefore cannot be removed from the supported response domain by clamping or rejecting them in this slice; the publication invariant must hold independently of the returned duration.

The supported domain is the one production `Client.Allocate` assembly path for every successfully parsed Allocate lifetime, split by canonical versus invalid relayed address, followed by activation, immediate refresh success or permanent failure, caller Close, and later Allocate admission. `internal/proto.Lifetime` owns the wire representation, root `Client.Allocate` owns validation and publication, and `UDPConn.Start` owns activation. The guarantee is universal over those finite lifecycle classes and independent of the numeric lifetime value.

## Decision

`internal/client.NewUDPConn` will return an invariant-complete, quiescent `UDPConn`: all maps, channels, adapters, credentials, timers, and default intervals exist, but no timer goroutine is armed. The existing unexported build-plus-start split is narrowly reopened at the package boundary; the started-constructor behavior accepted by the 2026-08-19 construction plan does not survive as a second production path.

`UDPConn.Start` will be the sole activation operation. It will hold `closeMutex`, refuse to arm anything when `closeCh` is already closed, and arm the Allocation-refresh, permission-refresh, and binding-check timers before releasing the mutex. An immediate timer handler may run, but `startClose` cannot enter its guarded transition until all three timers are armed; sealing then stops all three. Repeated `Start` calls are harmless no-ops through the existing timer guards, and a sealed connection can never be rearmed.

For a successful Allocate exchange, root ordering becomes: construct the quiescent `UDPConn`; canonicalize the relayed transport address; on invalid address, call `Close` while unstarted and return the existing error; on valid address, publish the connection and release the admission claim in the existing Client-mutex critical section; release the Client mutex; call `UDPConn.Start`; return the public `Allocation`. The Client mutex must not be held across `Start`, because `startCloseLocked` holds `closeMutex` and calls `OnDeallocated`, which acquires the Client mutex. This preserves one lock order and avoids a Client-mutex/`closeMutex` inversion.

If the first refresh permanently fails immediately after activation, the connection is already published, so the existing `OnDeallocated` callback clears the right pointer. `Allocate` may still return the now-sealed `Allocation`; this is the existing self-seal contract, not a new construction error. Operations and caller `Close` expose the existing terminal cause, the Release is emitted exactly once, and a later `Allocate` is admitted. No post-start health check, new public error, or second terminal-state owner is introduced.

The observable pre-publication deallocation guard is rejected. A Client flag, generation handshake, or closed-state check would duplicate `UDPConn` terminal ownership and still require atomic publication-versus-seal arbitration to avoid a check-then-publish race. It would also leave invalid-relayed cleanup starting workers only to stop them. Publication-before-activation removes the state rather than detecting it after the fact.

**Narrowly reopened decisions:** The construction plan's claim that production needs only a build-plus-start `NewUDPConn` is superseded for this slice. The Allocation lifecycle plan's positive initial RFC-default-grant representation is widened to the actual successfully parsed lifetime returned by Allocate. Its fixed half-life cadence, rejection of adaptive refresh, and sole `UDPConn` lifecycle ownership remain binding. The admission plan remains binding: validation precedes publication, and publication plus admission-claim release remain one Client-owned critical section.

**Non-goals:** No lifetime clamp or minimum; adaptive refresh; generic timer or clock framework; constructor options/builder; new public API or error; TURN request or response wire change; retry-policy change; caller-socket close or deadline operation; Client terminality; second admission guard; broad test harness; unrelated permission, binding, transaction, or packet-path change.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
| --- | --- | --- | --- | --- |
| S1 | Proposed; awaiting owner approval | Quiescent construction, publish-before-activation, and atomic `UDPConn` activation | Owner approval of this plan | Started-constructor compatibility path |

## Implementation Slices

### Slice S1 — Publish before atomic Allocation activation

**What it delivers:** One PR changing `NewUDPConn` to quiescent construction, adding one atomic `UDPConn.Start` activation owner, moving production activation after successful publication, updating the four current constructor call sites explicitly, and adding deterministic lifecycle-ordering evidence without timing sleeps or repetition.

**Single owner after merge:** `newUDPConn`/`NewUDPConn` establish construction invariants; `UDPConn.Start` alone activates lifecycle workers under `closeMutex`; root `Client.Allocate` alone validates and publishes; `startCloseLocked` alone seals, notifies, aborts, and releases in the existing order.

**Authority completeness:** No persisted or restart state exists. Repository census at the audited head finds four `NewUDPConn` call sites: production `client.go`; `close_latency_test.go`; root `scripted_allocation_test.go`; and the internal constructor test. Production publishes then starts. The close-latency and scripted-allocation fixtures start explicitly because they model live Allocations. The internal test splits quiescent-construction assertions from explicit activation assertions. No build-plus-start compatibility constructor remains.

**Transitional-seam budget:** None at merge. There is one constructor, one activation method, and one production ordering. No second started constructor, Client-side health flag, sealed-state getter for publication, timer-start hook, clock abstraction, or compatibility wrapper may remain.

**Blast radius:** Concurrency and ordering change only at Allocation activation: timers move from constructor return to after Client publication, and activation becomes atomic against seal. A published-but-not-yet-activated connection can briefly receive inbound data through the existing queue, which is safe because construction already establishes delivery invariants; no public caller holds it until `Allocate` returns. Invalid-relayed cleanup closes an unstarted connection, preserving one Release without worker joins. Public API, error identities, emitted bytes, fixed intervals, retry rules, terminal-cause precedence, caller-owned socket behavior, and dependencies do not change. Untraced effects are not accepted: any timer armed before publication, any rearm after seal, a Client lock held across activation, a second activation owner, changed release count/order, changed returned error, or changed socket ownership stops the slice.

**Artifact classification:** Constructor/activation ordering and the close-state guard in `Start` are shipped behavior and required lifecycle safety enforcement. Focused tests, the observer/script fixtures, the single guard mutation, and validation commands are verification aids. This plan, issue, RAS receipt, PR, journal, and OmniFocus state are process or traceability metadata. No verification aid becomes a maintained product deliverable.

**Representation contract:** Supported lifetime representation is every value successfully decoded by `internal/proto.Lifetime`; the lifecycle equivalence classes are canonical relayed address versus invalid relayed address, quiescent versus activated versus sealed, immediate refresh success versus permanent failure, and idle versus published Client admission state. Root publication and `UDPConn` activation are the trusted owners. Guarantee = universal over this finite state/ordering model, not a claim about timer punctuality or refresh success. Terminating evidence is the focused public and internal tests below, the constructor/activation call-site census, one discriminating guard mutation, ordinary and race suites, exact-head preflight, bounded review, and same-head hosted CI.

**Contract closure:** Not triggered. The consequence is a material lifecycle failure, but the accepted invariant has one production assembly path, one activation owner, and one activation-versus-seal critical section. The finite valid/invalid publication paths and immediate self-seal behavior are reasonably covered by focused deterministic tests; no broader semantic matrix, timing repetitions, fuzzing, or platform expansion is needed.

**TDD and evidence budget:**

1. Add a channel-coordinated root test through the real registry/observer seam. Script an Allocate success with an immediate lifetime and gate the first permanent Refresh failure. At Refresh entry assert the candidate is already the Client's published pointer; after failure assert the pointer clears, exactly one lifetime-zero Release is emitted, the existing terminal cause is observable from caller `Close`, a later `Allocate` reaches the network instead of returning `ErrAlreadyAllocated`, and the caller-owned socket receives no Close or deadline operation.
2. Change the internal constructor test to prove `NewUDPConn` is quiescent and `Close` on it emits exactly one Release without starting timers. Add explicit `Start` coverage proving all three timers are armed together and that an immediate refresh self-seal stops all three before caller join. Add the sealed-before-Start case proving activation cannot rearm a closed connection.
3. Change the constructor and activation implementation minimally, then update each of the four call sites according to the census. Keep the invalid-relayed test as the unstarted-cleanup preservation gate.
4. Retain refresh success/438, permanent refresh failure, seal-versus-caller-Close, terminal-cause precedence, release-order, concurrent-admission, request-byte, inbound-delivery, and close-latency tests. Run focused files regularly, `go test ./...` once at completion, `go test -race ./...`, and `task preflight` against the exact candidate head.
5. One guard mutation is approved: temporarily restore start-before-publication at the root production assembly and observe the public stuck-pointer regression fail. No other mutation, sleep-based timing cell, repeated race run, fake clock, platform matrix, fuzzing, or harness extension is in budget.

**Review contract:** Outcome = no sealed Allocation can become the Client's published live Allocation. Preserve every existing behavior named above. Non-goals and blast-radius ceilings are binding. Review round budget = one fully briefed fresh RAS review and, only after accepted `fix-now` changes, at most one replacement review; independently disposition every finding under the shared review-loop protocol. A repeated precise root at the publication/activation invariant is `stop-for-decision`, not authority to widen the slice.

**Dispatch context budget:** This slice contract; `client.go:267-316,438-477`; `internal/client/udp_conn.go:93-170,461-540`; `internal/client/allocation.go:130-156`; `internal/client/periodic_timer.go`; constructor call sites in `close_latency_test.go`, `scripted_allocation_test.go`, and `internal/client/udp_conn_test.go`; root observer helpers in `allocate_ctx_test.go`; `allocation_test.go`; `internal/client/refresh_failure_test.go`; the three related lifecycle/construction/admission plans; issue #102; and RAS run `20260820T031106-489ebd815b66a06b0af58dcf`. Complete historical transcripts, unrelated ADRs, other TURN methods, external consumers, and packet-path profiles are unnecessary.

**Slice decision audit:** Strongest further split = land quiescent construction before publication ordering. Rejected because a quiescent production constructor without the root activation move is not independently useful and leaves the Allocation inactive; both edits are one invariant and one PR. Strongest merge = add lifetime validation or adaptive scheduling. Rejected because actual remaining lifetime is protocol-owned input and the structural invariant removes the timing dependency without changing policy. There is no blocking edge beyond owner approval.

**Stop conditions:** Root must hold the Client mutex across `Start`; `Start` must perform I/O; a second production activation path is required; invalid-relayed cleanup cannot retain one Release while unstarted; activation cannot be made atomic without changing terminal ownership; a public API/error or lifetime policy change becomes necessary; the public reproducer cannot distinguish start-before-publication from publish-before-start; or the slice cannot preserve exact Release ordering/count, terminal-cause behavior, admission, and caller-socket ownership.

## Acceptance Criteria

- [ ] No Allocation lifecycle timer can be armed before that `UDPConn` is the Client's published connection; a sealed connection cannot be published or rearmed.
- [ ] `NewUDPConn` returns an invariant-complete quiescent connection, `UDPConn.Start` is the only activation owner, and all four audited constructor call sites have the declared disposition with no started compatibility constructor.
- [ ] `Start` atomically arms all three timers against `startClose` under `closeMutex`, returns without the Client mutex held, and a concurrent immediate self-seal leaves all timers stopped for caller join.
- [ ] Invalid relayed addresses close the unstarted connection and still emit exactly one lifetime-zero Release without publication; valid connections publish and release the admission claim before activation.
- [ ] An immediate permanent first Refresh failure clears the published pointer, preserves the existing terminal cause and caller-join behavior, emits exactly one Release, and allows a later Allocate.
- [ ] Every successfully parsed Allocate lifetime remains accepted unchanged; fixed cadence, TURN bytes, retries, public API/errors, caller-owned socket behavior, and unrelated lifecycle behavior are unchanged.
- [ ] The declared focused evidence, one guard mutation, ordinary and race suites, exact-head preflight, bounded review, and same-head hosted CI satisfy the terminating evidence plan.

## Validation Gates

After owner approval, implement the one slice test-first on a feature branch. Keep its PR draft through the bounded review and exact-head `task preflight`, mark it ready only afterward, require a successful post-ready `ci` run whose `ci-required` job matches the live head, then squash-merge that exact head. Append the development journal only after the product PR merges, revalidate deferred findings against merged `main`, and only then complete issue #102's OmniFocus task or file surviving follow-ups.
