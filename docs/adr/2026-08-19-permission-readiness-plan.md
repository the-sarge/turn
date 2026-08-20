# Permission Readiness Implementation Plan

**Date:** 2026-08-19
**Status:** Complete via PR #105
**Track:** 3 of 4 in the 2026-08-19 seam deepening program
**Depends on:** Nothing — parallel-safe; T1.S1 first is recommended to avoid `prepare_test.go` conflicts but is not a blocker
**Related:** [Program index](2026-08-19-seam-deepening-program.md), [Channel-binding readiness plan](2026-08-19-channel-binding-readiness-plan.md), [Prepared-only writes ADR](2026-08-15-prepared-only-writes.md), [Permission owns its attempt ADR](2026-08-19-permission-owns-its-attempt.md), [Allocation lifecycle plan](2026-08-17-allocation-lifecycle-plan.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Self-grilled and independently slice-audited against `dad68d4868efc8b041114f7a4efdeaa3b229edfb` on 2026-08-19 (see the program index for the audit receipt)

## Goal

Make `permission` own its own readiness — the begin/join/resolve lifecycle of its CreatePermission attempt and the permitted fact that results — behind intent-level operations with one private lock never held across I/O, so `UDPConn` asks whether a peer is permitted and joins or starts an attempt instead of driving three sync primitives and raw fields declared in another file. `UDPConn` keeps worker registration, the CreatePermission transaction and stale-nonce retry, seal precedence, map deletion on failure, and refresh disposition. This is the last raw-accessor module in `internal/client`; it is deliberately *not* given channel binding's durable/attempt split because permission has no durable machine to separate (see the ADR).

## Current Shape (verified 2026-08-19 at `dad68d4868efc8b041114f7a4efdeaa3b229edfb`)

`permission` exposes two raw accessors `state()`/`setState()` over a two-value atomic enum, two raw fields `attemptDone`/`attemptErr`, and three locks — `mutex`, `attemptMutex`, and `permissionMap.mutex` (`internal/client/permission.go:13-39`). Every transition is driven from `UDPConn`: `createPermission` holds `perm.mutex` across the whole CreatePermission transaction (`internal/client/udp_conn.go:178-193`), yet its only caller is the single in-flight attempt worker that `ensurePermissionAttempt` guarantees (`internal/client/udp_conn.go:269-306`), so that lock is vestigial; `awaitPermission` polls `state()` and reads `attemptErr` under the permission's `attemptMutex` (`internal/client/udp_conn.go:233-264`); `ensurePermissionAttempt` creates, publishes, and clears `attemptDone`/`attemptErr` from the worker goroutine (`internal/client/udp_conn.go:269-306`). `createPermission` deletes the map entry on *every* non-nil CreatePermission result, including a stale-nonce 438 that the retry loop then retries (`udp_conn.go:184-185`), so a 438-then-success sequence today leaves a permitted permission outside `permMap` (unrefreshed, and re-created by the next PreparePeer); this is unobserved by any test. `permissionMap.insert` and `find` have no production caller (`internal/client/permission.go:49-56,75-81`; test callers at `prepare_test.go:172,206,237`, `udp_conn_test.go:899,925`, `permission_test.go`), and the `pm.insert(addr, &permission{st: permStatePermitted})` setup at `udp_conn_test.go:899-925` is dead: `WriteTo` never reads `permMap`.

The verified permission lifecycle is: idle (in map, no attempt) → attempting (`attemptDone != nil`) → permitted (permanent for the object) | failed (first attempt failed: `attemptErr` set, entry deleted from `permMap`; the next `PreparePeer` creates a fresh permission). Permission refresh failure never changes permission state — it terminalizes prepared bindings via `failPreparedBindings` (`internal/client/allocation.go:142-155`, `udp_conn.go:406-411`). `permissionMap.addrs()` returns every map entry, permitted or still attempting (`permission.go:89-99`). There is no per-permission expiry. A waiter that joins an attempt after the Allocation seals receives the recorded terminal cause (seal precedence, `udp_conn.go:287-292`). Permission identity is per canonical peer IP (`permission.go:41-46`).

Tests probe readiness by `permMap.find(peer).state() == permStatePermitted` (`prepare_test.go:172,206,237`); the seal-precedence test calls `ensurePermissionAttempt` and reads `perm.attemptMutex`/`attemptErr` directly (`prepare_test.go:610-640`); and `permission_test.go` (76 lines) tests the accessors and map CRUD rather than any transition.

## Decision

`permission` owns its readiness storage and attempt lifecycle behind three operations: `beginOrJoin()` returns the in-flight attempt handle and whether the caller started a fresh attempt (and must therefore run it and resolve it); the handle owns that generation's done signal and immutable completion result; `resolve(err)` applies one attempt result — nil marks permitted, non-nil records the failure — publishes it to the handle, closes the done signal, and clears the in-flight marker; `state()` reports permitted-or-not plus the last resolved failure. A waiter awakened for one handle consumes that handle's failure before consulting later permission readiness, so a stale caller that starts or completes another attempt on the same cached object cannot replace the joined outcome. Exact private spellings may vary; there is no other mutation path, no raw accessor, no exported-to-package lock. One private lock guards all of it and is never held across a transaction, a worker registration, or a wait. The `permState` enum, the atomics, `perm.mutex`, `permissionMap.insert`, and `permissionMap.find` are deleted; `permissionMap` keeps `getOrCreate`, `delete`, and `addrs` and its per-IP key.

`UDPConn.awaitPermission` becomes: loop { if permitted, return nil; `done, fresh := perm.beginOrJoin()`; if fresh { register a worker — if registration fails because the Allocation is closing, `perm.resolve(closedErr)` so joined waiters wake with the terminal cause — else start the worker }; select on done, ctx, closeCh; read `state()`: permitted → nil, failure → that error, neither → loop }. The worker runs the existing `maxRetryAttempts`/`errTryAgain` loop around the CreatePermission transaction with the existing seal-precedence check. On a final CreatePermission failure `UDPConn` deletes the map entry **before** calling `perm.resolve(err)`, preserving today's delete-before-wake order (`udp_conn.go:185` precedes `:298-302`) so that a caller arriving after the wake-up always gets a fresh permission from `getOrCreate`; the seal-precedence short-circuit and the registration-failure path resolve without deleting, as today. One accepted correction: the map entry is deleted only on the *final* failure, not on an intermediate 438 — a 438-then-success sequence leaves the permitted permission in the map (refreshed, and joined by later callers), where today it is orphaned. `createPermission` no longer takes any permission lock. Refresh membership (`permMap.addrs()` covering permitted-but-unbound peers), `refreshPermissions`, `failPreparedBindings`, retry count, stale-nonce retry, worker accounting, and waiter-local cancellation are unchanged.

**Rejected alternative (do not do this):** Mirror channel binding's split — durable readiness in `permission`, attempt coordination in `UDPConn`. Permission's durable readiness is one bit; the split would leave a bool-bag and keep the attempt protocol in the caller. The ADR records why the asymmetry with binding is deliberate.

**Rejected alternative (do not do this):** Share an attempt helper, handle type, or join loop between permission and binding. Attempt coalescing was closed without code in the 08-19 program because the two paths differ in map deletion, eligibility, transitions, worker rollback, and disposition; this slice must not reopen it.

**Rejected alternative (do not do this):** Move worker registration, the CreatePermission transaction, retry, map deletion, or refresh disposition into `permission`. Those are `UDPConn` policy.

**Rejected alternative (do not do this):** Add per-permission expiry, permission removal on refresh failure, or any change to refresh membership. Not observed behavior; not in scope.

**Non-goals:** No change to permission identity, CreatePermission bytes, retry count, refresh cadence or membership rule (every map entry), prepared-binding terminalization on refresh failure, seal precedence, worker join, public API, channel binding, or `bindingAttempt`/`muBind` placement (the ADR closes that relocation). The only accepted behavior change is the 438 orphan correction named above. The pre-existing cached-pointer interleaving that can run a successful replacement attempt on an already detached permission remains outside this slice; preserving the exact result of an earlier joined attempt does not authorize restoring detached membership or otherwise repairing that adjacent behavior.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T3.S1 | Complete via PR #105 | `permission` owns begin/join/resolve and the permitted fact behind three operations; `UDPConn` drives no permission lock or raw field; vestigial lock, enum, atomics, `insert`/`find` gone | None | Raw permission state protocol in `UDPConn` |

## Implementation Slices

### Slice T3.S1 — Permission owns its attempt and readiness

**What it delivers:** One PR introducing the three permission operations, migrating `awaitPermission`/`ensurePermissionAttempt`/`createPermission` to them, deleting the raw accessors, enum, atomics, `perm.mutex`, `insert`, `find`, and the dead `pm.insert` test setup, and replacing accessor/CRUD tests with a table over begin/join/resolve plus preserved PreparePeer behavior.

**Implementation:** PR #105 is this slice's one product PR.

**Existing-work disposition:** New slice.

**Blocked by:** None.

**Single owner after merge:** `permission` is the only mutation owner of the permitted fact, the in-flight attempt marker, the done signal, and the last resolved failure. `UDPConn` is the only owner of worker registration, the CreatePermission transaction and retry, seal precedence, `permMap` deletion, and refresh disposition. `permissionMap` owns per-IP identity.

**Authority completeness:** No persisted fact. The slice covers construction (`getOrCreate`), every producer (fresh attempt success, fresh attempt failure, worker-registration failure while closing, seal during attempt), every consumer (`awaitPermission` waiters, `refreshPermissions` membership), and deletion.

**Transitional-seam budget:** None at merge. No raw accessor, second lock, or caller-side attempt bookkeeping may remain; no shared helper with binding may be introduced.

**Blast radius:** `internal/client/permission.go`, `udp_conn.go:178-306`, `permission_test.go`, `prepare_test.go` readiness probes and the seal-precedence test (`:610-640`, rewritten against `beginOrJoin`/`state`), `udp_conn_test.go:895-930`. Accepted behavior change: a permission that succeeds after an intermediate 438 stays in `permMap` (refresh membership gains that entry; today it is orphaned). Concurrency: one private permission lock replaces three primitives; it is never held across I/O, registration, or waiting; no lock order with `closeMutex` or binding locks is introduced (worker registration and seal checks happen outside the permission lock, as today). Ordering: waiters joining an attempt after seal still observe the terminal cause; a fresh attempt that cannot register a worker resolves with the closed error so joined waiters wake; a failed joined attempt retains its own result even if a stale caller starts or completes a replacement attempt before that waiter reads. Failure modes: a finally failed fresh attempt still deletes the map entry before waking waiters; stale-nonce retry unchanged except that an intermediate 438 no longer deletes the entry. Interfaces: none public. Performance/security/dependencies: none. Untraced effects are not accepted: a lock held across the transaction, a waiter that can block forever, or a changed refresh membership stops the slice.

**Artifact classification:** Permission readiness operations are shipped behavior and required lifecycle enforcement (waiter wake-up). The transition table and migrated tests are verification aids. Plans/issues are process metadata.

**Representation contract:** Supported domain = one permission's finite lifecycle: idle; fresh attempt started; joined in-flight attempt; resolve success; resolve failure; resolve with closed error when the Allocation is closing; waiter cancellation; waiter observing seal; join after resolve; and observation of one joined attempt after a stale caller starts or completes a replacement attempt on the same cached permission object. Owner = `permission` for state and the immutable result on each attempt handle; `UDPConn` for policy and for consuming the joined handle before later readiness. Guarantee = universal over that finite matrix, not over server behavior and not a repair of detached permission membership. Terminating evidence = the matrix below, preserved PreparePeer tables, one deterministic joined-failure/replacement ordering regression, `go test -race ./...`, `task preflight`, hosted CI, one review plus at most one replacement.

**Contract closure:** Triggered — a waiter that never wakes or a permission marked permitted without a successful attempt is a broken lifecycle obligation, and the lifecycle is reached independently through fresh callers, joining callers, cancellation, worker-registration failure, and seal.

| Semantic class | Expected disposition | Enforcement owner | Evidence | Guard mutation when required | Status |
|---|---|---|---|---|---|
| Idle permission, first caller | Fresh attempt; caller becomes the runner | `permission.beginOrJoin` | Table | n/a | Covered |
| Second caller while attempt in flight | Joins the same done signal; does not start a second attempt; CreatePermission sent once | `permission.beginOrJoin` | Table plus existing shared-attempt PreparePeer test | n/a | Covered |
| Resolve success | Permitted; done closed; later callers return without an attempt | `permission.resolve` | Table | Delete the permitted transition and observe the repeat-PreparePeer test fail | Covered |
| Resolve failure (fresh attempt) | Failure recorded; done closed; waiters receive the error; `UDPConn` deletes the map entry; next caller gets a fresh permission | `permission.resolve` + `UDPConn` | Table plus existing permission-rejection PreparePeer test | n/a | Covered |
| Joined failure while a stale caller starts or completes a replacement attempt on the same cached object | The joined waiter receives its attempt generation's failure before any later permission readiness is considered; detached membership behavior is unchanged | Permission attempt handle + `UDPConn.awaitPermission` | Deterministic attempt-A-fails/replacement-B-succeeds ordering regression | n/a | Covered |
| Worker registration fails (Allocation closing) | Resolve with the closed error; joined waiters wake with the terminal cause | `UDPConn` + `permission.resolve` | Controlled closing-before-registration case | n/a | Covered |
| Seal during attempt | Runner observes seal precedence and resolves with the terminal cause; waiters wake; no map deletion | `UDPConn` worker + `permission.resolve` | Seal-precedence test rewritten against `beginOrJoin`/`state` | n/a | Covered |
| Stale-nonce 438 then success | Permitted; map entry retained; CreatePermission sent twice | `UDPConn` worker + `permission.resolve` | Focused PreparePeer case (accepted correction) | n/a | Covered |
| Waiter cancellation | Only that waiter returns `context.Cause`; attempt continues; a later caller joins or observes the result | `UDPConn.awaitPermission` | Existing waiter-local cancellation test | n/a | Covered |
| Join after resolve | Observes state without waiting | `permission.state` | Table | n/a | Covered |
| Refresh membership | Every map entry (permitted or attempting) is included in refresh exactly as today; refresh neither adds nor removes entries; finally failed entries leave via the worker's deletion | `permissionMap.addrs` (unchanged) | Existing refresh tests | n/a | Covered |

**Evidence budget:** The matrix rows are the complete budget: a table over begin/join/resolve/state with no `UDPConn`; the existing shared-attempt, rejection, cancellation, and refresh PreparePeer tests retained with `permMap.find(...).state()` probes replaced by observed PreparePeer outcomes and permission-count assertions; the seal-precedence test rewritten against the new operations; one focused 438-then-success case; one deterministic regression where attempt A fails and replacement attempt B succeeds before A's joined waiter reads; `go test -race ./...`. One guard mutation: bypass the permitted transition and observe a repeat PreparePeer start a second attempt. Negative evidence: no `permState`, `setState`, `state()` raw accessor, `perm.mutex`, `attemptMutex`, `insert`, or `find` symbol. No fake clock, repetition, platform, or fuzz scope.

**TDD and preservation evidence:** Write the permission transition table first; introduce the three operations beside the raw fields; migrate `awaitPermission` and the worker; delete `perm.mutex` from `createPermission`; delete raw accessors, enum, `insert`/`find`, and the dead test setup last. Preserved gates: PreparePeer behavior tables, CreatePermission request-shape test, refresh-failure terminalization, close/join tests.

**Dispatch context budget:** This slice contract; `internal/client/permission.go`; `internal/client/udp_conn.go:178-306,406-411`; `internal/client/allocation.go:102-112,142-155`; `permission_test.go`; the readiness probes in `prepare_test.go`; the channel-binding readiness plan (for the asymmetry reasoning) and the permission ADR. No binding, transaction, or root code. Fits one fresh context: one ~100-line module, three `UDPConn` methods, bounded tests.

**Slice decision audit:** Strongest further split = introduce the operations first, delete raw access later. Rejected: leaves two mutation paths for one PR with no payoff. Strongest merge = relocate binding's attempt handle in the same PR for symmetry. Rejected by the ADR: different module, different ownership argument, and the program forbids a shared attempt helper. No blocking edge is needed.

**Stop conditions:** The permission lock must be held across the transaction or a wait; a waiter can block with no resolve; the design wants a shared attempt helper or handle with binding; refresh membership or prepared-binding terminalization would change; or `UDPConn` still needs a raw accessor.

## Acceptance Criteria

- [x] Every permission state and attempt transition in the supported finite domain has one owner in `permission`; `UDPConn` drives no permission lock or raw field. Domain: the matrix; owner: `permission`; guarantee: universal over the finite matrix; evidence: table, preserved PreparePeer tests, one guard mutation.
- [x] Concurrent PreparePeer callers for one peer IP share one CreatePermission attempt; a failed fresh attempt wakes waiters with that exact attempt generation's error even if a stale caller starts or completes a replacement first; the next caller starts fresh; a closing Allocation wakes joined waiters with the terminal cause.
- [x] `permState`, `setState`, the raw `state()` accessor, `perm.mutex`, `attemptMutex`, `permissionMap.insert`, `permissionMap.find`, and the dead `pm.insert` test setup do not exist; no shared helper with channel binding exists.
- [x] Permission identity, CreatePermission bytes, retry count, refresh cadence and membership rule, prepared-binding terminalization, seal precedence, waiter-local cancellation, and public API are unchanged; the only behavior change is that a permission succeeding after an intermediate 438 remains in `permMap`.

## Validation Gates

The transition table, the preserved PreparePeer/refresh/close tests, `go test ./...`, `go test -race ./...`, `task preflight`; draft through review and certification, ready, post-ready `ci-required` success on the exact head, squash merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice`; this repository has no overlay. Keep readiness and the attempt handle on `permission`, policy and workers on `UDPConn`, identity on `permissionMap`. Stop rather than sharing anything with channel binding or holding a lock across I/O.
