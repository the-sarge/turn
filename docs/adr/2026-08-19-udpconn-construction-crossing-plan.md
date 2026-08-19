# UDPConn Construction and Transaction Crossing Implementation Plan

**Date:** 2026-08-19
**Status:** T1.S1 implemented by PR #96; T1.S2 accepted and not yet implemented
**Track:** 1 of 4 in the 2026-08-19 seam deepening program
**Depends on:** Nothing — safe to start first; T1.S1 first is strongly recommended for T1.S2 but is not a blocker
**Related:** [Program index](2026-08-19-seam-deepening-program.md), [Allocation lifecycle plan](2026-08-17-allocation-lifecycle-plan.md), [Transaction registry plan](2026-08-17-transaction-registry-plan.md), [Server-bound transport plan](2026-08-19-server-bound-transport-plan.md), [Allocation construction timing validity plan](2026-08-19-allocation-construction-timing-validity-plan.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Self-grilled and independently slice-audited against `dad68d4868efc8b041114f7a4efdeaa3b229edfb` on 2026-08-19 (see the program index for the audit receipt)

## Goal

Make `NewUDPConn` the only way a `UDPConn` comes to exist — in production and, apart from six retained internal-seam literals, in tests — by separating building an Allocation (every invariant established, no goroutines) from starting it (timers armed), and make the Client→`UDPConn` transaction crossing two named operations instead of one function with a mode flag. The `UDPConn` interface shrinks (no mode flag, no result bag), tests enter through the constructor rather than the field set, and the crossing's production adapters become plain method values on the transaction registry.

## Current Shape (verified 2026-08-19 at `dad68d4868efc8b041114f7a4efdeaa3b229edfb`)

`NewUDPConn` builds every field and starts three `PeriodicTimer` goroutines in one call (`internal/client/udp_conn.go:95-161`). Because no production path needs a built-but-quiescent Allocation, tests that want one bypass the constructor: `udp_conn_test.go` constructs `UDPConn{…}` struct literals at 13 sites (`internal/client/udp_conn_test.go:74,93,108,271,358,373,910,940,970,1005,1019,1043,1070`), each populating a different subset of private fields; `mockClient.configure` writes the three private func fields `writeTo`, `performTransaction`, and `onDeallocated` after the fact (`internal/client/client_test.go:12-47`); and one test invokes the refresh worker directly (`internal/client/udp_conn_test.go:280`). Seven of those literals (`:271,358,373,910,940,1043,1070`) exist only to carry credentials and/or the mock; the remaining six build a minimal conn to force a capacity-one read queue (`:74,93,108`), a completion-versus-seal linearization (`:970`), or a minimal-conn attempt-result-ownership check (`:1005,1019`), which the inbound delivery and channel-binding readiness plans accepted as controlled internal tests.

Across packages, `AllocationConfig{…}` is hand-built ten times (`close_latency_test.go:45`, `allocation_test.go:41`, `client_test.go:242`, `internal/client/refresh_failure_test.go:76`, `internal/client/prepare_test.go:100`, `internal/client/udp_conn_test.go:56,132,674,756,819`), the same CreatePermission/ChannelBind "succeed" script is written nine times, and the credential quintet appears fourteen times. Root harnesses `allocHarness` (`allocation_test.go:27-88`) and `inboundDeliveryHarness` (`client_test.go:225-277`) do the same job as the internal `prepareHarness` (`internal/client/prepare_test.go:24-135`).

The transaction crossing is `AllocationConfig.PerformTransaction func(msg *stun.Message, dontWait bool) (TransactionResult, error)` (`internal/client/allocation.go:19-23`). One boolean is re-split at five layers: `refreshAllocation(lifetime, dontWait)` returns before reading the response when true (`internal/client/allocation.go:46-68`); the crossing carries it; root `Client.performTransaction(msg, ignoreResult)` branches to `registry.Start` or `registry.Perform` (`client.go:344-352`); `begin(msg, ignoreResult)` allocates or omits the result channel (`internal/client/transaction.go:106-117`). With the flag true the returned `TransactionResult` is always zero and `Msg` must not be read — an invariant documented nowhere on `AllocationConfig`. Exactly two call sites exist: the waited refresh (`internal/client/allocation.go:129`) and the lifetime-zero release emitted from `startCloseLocked` (`internal/client/udp_conn.go:516`). `TransactionResult{Msg, Err}` duplicates the returned error: every entry point returns `(result, result.Err)` (`internal/client/transaction.go:214-220`) and `Msg` is the only field any caller reads. Tests use the flag as a de facto method name (`internal/client/refresh_failure_test.go:45-58`) or sniff `MethodRefresh && dontWait` to detect release ordering (`close_latency_test.go:46-50`).

Root `Client` still owns one production assembler of `AllocationConfig` (`client.go:315-328`), validates configuration at `NewClient` only (T4.S1), and never validates in `NewUDPConn`. That ownership is preserved.

## Decision

**Build, then start.** `internal/client` gains an unexported build step that allocates every `UDPConn` field — maps, channels, the read queue, the three `PeriodicTimer`s constructed but not started — and establishes every invariant a method relies on (`closeCh`, `permMap`, `bindingMgr`, `readCh` non-nil; intervals defaulted), and an unexported `start()` that arms the timers. Exported `NewUDPConn(config, abort)` is exactly build followed by start and remains the single production constructor; its observable behavior, including the panic on a missing abort capability, is unchanged. Calling `Close` on a built-but-unstarted conn is valid and joins nothing: `PeriodicTimer.Stop`/`StopAndWait` on a never-started timer are already no-ops (`internal/client/periodic_timer.go:72-96`).

**One scripted constructor per test package.** `internal/client` tests construct through one helper that builds (unstarted) a `UDPConn` with default credentials and a caller-supplied script — the three crossing closures plus write capture — held by the harness so a test may rescript after construction without touching private fields; a test that needs timers calls `start()` explicitly. `mockClient` and `configure` are deleted. The seven credential-carrying struct literals and the direct worker invocation migrate to the helper. The six capacity-one-queue, completion-versus-seal, and minimal-conn attempt-result literals stay as they are: they are the accepted way to force an ordering or a minimal state, and widening the helper with queue-size or lock knobs is the harness growth this track refuses. Root tests keep `client.NewUDPConn` (timers started at long intervals, as today) behind one `newScriptedAllocation(t)` helper that replaces `allocHarness` and `inboundDeliveryHarness`; `close_latency_test.go` keeps its own harness because it exercises real `Client` wiring, a different job. Root harness count goes from three to two, not to one.

**Two named crossings.** `AllocationConfig` carries `PerformTransaction func(*stun.Message) (*stun.Message, error)` and `StartTransaction func(*stun.Message) error`. `TransactionResult` is deleted: `TransactionRegistry.Perform` and `PerformWithContext` return `(*stun.Message, error)` and keep a private result type on the channel; `Start` keeps returning `error`. Root passes method values — `PerformTransaction: c.transactions.Perform`, `StartTransaction: c.transactions.Start` — and deletes `Client.performTransaction`; `performAllocateTransaction` stays because it adds the cancelable wait. Inside `UDPConn`, `refreshAllocation(lifetime)` waits and parses and `emitRelease()` builds the lifetime-zero Refresh and calls `StartTransaction`; both share one private request builder. `startCloseLocked` calls `emitRelease` at the same point it calls `refreshAllocation(0, true)` today, so abort → `onDeallocated` → release ordering, exactly-one release, and terminal-cause joining are unchanged. `TestClientNonceExpiration`, the refresh-failure flows, and the close-latency ordering test are retained with their assertions rephrased against the named closures.

Exact private Go spellings (`newUDPConn`, `start`, `newTestConn`, `emitRelease`, `buildRefresh`) may vary; the operations and ownership above are the contract.

**Rejected alternative (do not do this):** Add an options struct, functional options, builder, or timing module for `UDPConn` construction. T4.S1 closed construction regrouping; this track changes when goroutines start, not how configuration is shaped.

**Rejected alternative (do not do this):** Grow the test helper into a general scripted-responder framework with queue-size, lock, timer, or clock knobs. The 08-17 program deferred broad responder consolidation and the 08-19 program rejected a shared responder-harness program; one constructor-shaped helper per package is the ceiling.

**Rejected alternative (do not do this):** Keep `dontWait` but document it, or keep `TransactionResult` as a one-field bag. The registry already exposes the two operations by name; the seam should carry them by name.

**Rejected alternative (do not do this):** Pass the `*TransactionRegistry` itself into `UDPConn` instead of two closures. The closure seam is the test seam (one production adapter, one scripted adapter); passing the registry would force every internal test to drive it at the wire.

**Non-goals:** No change to emitted bytes, setter order, retransmission policy, abort-before-release ordering, Allocation lifecycle ownership, `AllocationConfig` validation or field regrouping beyond the two crossing fields, public API, clock, timer semantics, queue size, or `turntest`. No fake clock; no removal of the accepted controlled-linearization tests; no root test helper beyond `newScriptedAllocation`.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T1.S1 | Complete via PR #96 | `UDPConn` built by one constructor in production and tests; `mockClient.configure` and credential-carrying struct literals gone; one scripted helper per package | None | Private-field test construction |
| T1.S2 | New | Two named transaction crossings; `TransactionResult` and the `dontWait` flag gone; release emission has a name | None (T1.S1 first strongly recommended) | Mode flag on the crossing |

## Implementation Slices

### Slice T1.S1 — Build, then start: one construction seam for UDPConn

**What it delivers:** One PR splitting `NewUDPConn` into unexported build and start steps (`NewUDPConn` = both, behavior unchanged), adding one scripted internal test constructor and one root `newScriptedAllocation` helper, deleting `mockClient`/`configure`, migrating the seven credential-carrying struct literals and the direct worker invocation to the helper, and collapsing the duplicated `AllocationConfig`/script/credential literals to the helper in both packages.

**Implementation:** PR #96 is this slice's one product PR.

**Existing-work disposition:** New slice. No open PR, branch, or review finding targets this seam as of 2026-08-19.

**Blocked by:** None.

**Single owner after merge:** `newUDPConn` is the only place a `UDPConn`'s invariants are established; `start()` is the only place its timers are armed; `NewUDPConn` is the only production constructor. Allocation lifecycle, seal, release, and join stay on `UDPConn` exactly as the Allocation lifecycle plan assigns them. Root `Client.Allocate` remains the one production assembler of `AllocationConfig`.

**Authority completeness:** No persisted fact. The construction invariants (non-nil channels/maps, defaulted intervals) are established in the build step and relied on by every method; the slice covers the constructor, `Close` on a built-unstarted conn, and every test construction path.

**Transitional-seam budget:** None at merge. The six accepted literals (`udp_conn_test.go:74,93,108,970,1005,1019` today: capacity-one queue, completion-versus-seal, minimal-conn attempt-result ownership) are retained deliberately as internal-seam tests, not as a second construction path; they construct minimal state to force an ordering and must not grow. No `mockClient`, `configure`, or credential-carrying literal may remain.

**Blast radius:** Internal package construction and its tests; root test harnesses. No production behavior change: `NewUDPConn` still returns a started conn with identical timer intervals and the same panic on missing abort. Concurrency: `start()` arms the same three timers at the same point; no new lock, goroutine, or ordering. Interfaces: no public API or `AllocationConfig` change. Failure modes: `Close` on an unstarted conn returns the same emission result and joins no goroutines. Performance, security, dependencies: none. Untraced effects are not accepted: any behavior difference observable through `NewUDPConn`, any new helper knob, or any remaining private-field write from a test stops the slice.

**Artifact classification:** The build/start split is shipped behavior (construction of the Allocation). The internal and root test helpers, migrated tests, and retained linearization tests are verification aids and do not become maintained product deliverables. Plans, issues, and tasks are process metadata.

**Representation contract:** Supported domain = construction of one `UDPConn` from one valid `AllocationConfig` plus abort capability, in production (built and started) and in tests (built; optionally started); the six retained literal sites. Representation owner = `newUDPConn` for invariants and `start` for timers. Guarantee = universal over the finite set of construction paths in this repository (the production call, the helper, and the enumerated retained literals), verified by grep-level negative evidence, not over arbitrary future tests. Terminating evidence = the focused constructor tests, the migrated suites green under `-race`, the negative-grep checks below, `task preflight`, same-head hosted CI, one review plus at most one replacement.

**Contract closure:** Not triggered. Production behavior is unchanged and construction has one reachable production path; focused constructor tests and the existing lifecycle suite are proportionate.

**Evidence budget:** One focused test that a built-unstarted conn has no running timers, accepts `Close`, and emits exactly one release; one new focused test that `NewUDPConn` returns a conn whose three timers report `IsRunning()` (the existing `PeriodicTimer` tests are retained); migrated `prepare_test`, `refresh_failure_test`, `udp_conn_test`, root `allocation_test`/`client_test` green; `go test -race ./...`. Negative evidence: no `mockClient` or `configure` symbol; no test outside the six retained sites constructs `UDPConn{`; the retained sites are listed in the PR description. No guard mutation is required (no enforcement guard changes). No new platforms, repetitions, or timing cells.

**TDD and preservation evidence:** Write the built-unstarted constructor test first (no goroutines, `Close` valid, one release), then split the constructor; write the internal helper and migrate one harness at a time keeping each suite green; write `newScriptedAllocation` and migrate `allocation_test` then `client_test`; delete `mockClient`/`configure` last. Preserved behavior gates: existing request-shape, lifecycle, refresh-failure, prepared-only, and inbound-delivery tests unchanged in assertion.

**Dispatch context budget:** This slice contract; `internal/client/udp_conn.go:95-161`; `internal/client/client_test.go`; `internal/client/prepare_test.go:24-135`; `internal/client/refresh_failure_test.go:25-86`; the `UDPConn{` and `mockClient{` sites in `internal/client/udp_conn_test.go`; root `allocation_test.go:27-88`, `client_test.go:225-277`, `close_latency_test.go:25-79`; the Allocation lifecycle plan. No root inbound/transaction implementation, no `turntest`, and no historical review transcript is required. The change is one constructor split plus mechanical harness consolidation and fits one fresh context; any helper knob beyond script and write capture exceeds it.

**Slice decision audit:** Strongest further split = split the constructor in one PR and consolidate harnesses in another. Rejected: the split alone has no observable payoff and leaves the private-field fossil in place, so the first PR would not be independently valuable. Strongest merge = fold T1.S2 in because both touch `AllocationConfig` literals. Rejected: T1.S2 changes a crossing's semantics while T1.S1 changes only construction; keeping them apart keeps each PR's preservation argument simple, and T1.S1 first makes T1.S2 cheaper. No blocking edge is needed in either direction.

**Stop conditions:** Any production-observable difference between old and new `NewUDPConn`; a test that still needs a private-field write after migration (other than the six retained sites) — indicates the helper shape is wrong; pressure to add queue-size, lock, timer, or clock knobs; a root test that needs an unexported internal constructor; or the migration cannot stay green suite by suite.

### Slice T1.S2 — Two named transaction crossings

**What it delivers:** One PR replacing `PerformTransaction(msg, dontWait) (TransactionResult, error)` with `PerformTransaction(msg) (*stun.Message, error)` plus `StartTransaction(msg) error`, deleting `TransactionResult` and root `Client.performTransaction`, wiring root with registry method values, splitting `refreshAllocation(lifetime)` from `emitRelease()` inside `UDPConn`, and rephrasing the refresh-failure and close-latency tests against the named closures.

**Existing-work disposition:** New slice.

**Blocked by:** None. T1.S1 first is strongly recommended: its helpers are where the crossing closures are scripted, so the signature change lands in two helpers plus mechanical return-type edits in every scripted `performTransaction` closure (`TransactionResult` has roughly 110 references across seven test files) instead of also rewriting ten literals and `mockClient`. Not a blocker; the slice is independently green either way, and text conflicts are rebased.

**Single owner after merge:** `TransactionRegistry` is the only owner of wait-versus-fire-and-forget semantics (already true; the flag re-split it); `UDPConn.emitRelease` is the only emitter of the lifetime-zero Refresh; `startCloseLocked` remains the only caller of it and the only owner of abort → `onDeallocated` → release ordering.

**Authority completeness:** No persisted fact. Both crossing operations, their production adapters (registry method values), their scripted adapters (helpers), and both `UDPConn` consumers (waited refresh, release) are covered in the slice.

**Transitional-seam budget:** None at merge. `TransactionResult`, the `dontWait`/`ignoreResult` parameters, and `Client.performTransaction` must be absent; no compatibility shim may translate the old shape.

**Blast radius:** `internal/client/allocation.go`, `udp_conn.go:56-62,516`, `transaction.go` return types, root `client.go:203-208,242-246` (`sendAllocateRequest` reads `trRes.Msg`), `client.go:315-360` (wiring and `performAllocateTransaction` return type), `internal/client/transaction_test.go`, and the scripted helpers; `sendAllocateRequest` overlaps T4.S1's edits (rebase, not a blocker). Ordering: `emitRelease` is called at the same point `refreshAllocation(0, true)` is called today, inside `startCloseLocked` after `abortTransactions()` and `onDeallocated`; exactly-one release and terminal-cause joining are unchanged. Bytes: the Refresh request built by the shared builder is byte-identical after normalizing to the observed transaction ID (same setters, same order). Failure modes: a `StartTransaction` send error is still joined into the terminal cause exactly as today's `emitErr`. Public API: none (`TransactionResult` is internal). Performance/security/dependencies: none. Untraced effects are not accepted: any change in release ordering, count, or bytes stops the slice.

**Artifact classification:** The crossing shape and `emitRelease` are shipped behavior. Rephrased tests are verification aids. Plans/issues are process metadata.

**Representation contract:** Supported domain = the two in-contract crossing operations and their two `UDPConn` consumers; Refresh request bytes for lifetime L and lifetime 0. Representation owner = `TransactionRegistry` for transaction semantics, `UDPConn` for which operation each consumer uses. Guarantee = universal over that finite set. Terminating evidence = focused tests below, existing lifecycle and close-latency tests, `go test -race ./...`, `task preflight`, hosted CI, one review plus at most one replacement.

**Contract closure:** Not triggered. The invariant (release emitted once, fire-and-forget, at the accepted point) has one enforcement owner and one reachable path; the existing abort/release ordering tests plus the focused tests below cover it.

**Evidence budget:** One focused test that the waited refresh uses `PerformTransaction` and parses the response; one that release uses `StartTransaction` with a lifetime-zero Refresh byte-identical to today's after normalizing to the observed transaction ID, using the existing `assertRequestShape` technique (`internal/client/udp_conn_test.go:194-213`: expected bytes built from an explicit setter list with `stun.NewTransactionIDSetter(actual.TransactionID)`; `TestRefreshAllocationPreservesRequestAndRetriesStaleNonce` already pins lifetime L, add the lifetime-0 case); retained `TestClientNonceExpiration`, refresh-failure flows, abort/release ordering, and close-latency tests with assertions rephrased; `go test -race ./...`. Negative evidence: no `TransactionResult`, `dontWait`, `ignoreResult`, or `Client.performTransaction` symbol remains. At most one guard mutation: skip `emitRelease` in `startCloseLocked` and observe the existing exactly-one-release test fail.

**TDD and preservation evidence:** Pin the current lifetime-zero Refresh shape (txid-normalized) and the waited-refresh flow as characterization tests first; introduce `StartTransaction` alongside, migrate release, then migrate waited refresh, then delete the old field and `TransactionResult`; rephrase tests last. Preserved gates: request-shape tests, `close_latency_test` ordering, refresh-failure terminal-cause tests.

**Dispatch context budget:** This slice contract; `internal/client/allocation.go`; `internal/client/udp_conn.go:56-62,486-530`; `internal/client/transaction.go:21-25,53-104,198-220`; `internal/client/transaction_test.go`; root `client.go:181-273,315-360`; the scripted helpers (post-T1.S1) or the literals they replace; the Allocation lifecycle and transaction registry plans. Fits one fresh context: two field renames, one method split, one type deletion, and bounded test edits.

**Slice decision audit:** Strongest further split = rename the crossing first, delete `TransactionResult` later. Rejected: the deletion is the point (the result bag is the duplicated error), and both are mechanical. Strongest merge = fold into T1.S1. Rejected above. No edge is necessary; T1.S1 first reduces churn.

**Stop conditions:** The release must be emitted anywhere other than `startCloseLocked`; the shared Refresh builder cannot reproduce today's bytes; a caller needs the response of a fire-and-forget transaction; or a third crossing operation becomes necessary.

## Acceptance Criteria

- [x] `NewUDPConn` behaves exactly as before (started timers, same intervals, same abort panic) and is the only production constructor; an unexported build step establishes every invariant without goroutines and `Close` on a built-unstarted conn is valid. Domain: the finite construction paths in this repository; owner: `newUDPConn`/`start`; guarantee: universal over that set via the negative-grep evidence in T1.S1.
- [x] No test writes `UDPConn` private func fields after construction; `mockClient` and `configure` do not exist; `UDPConn{` literals exist only at the six retained controlled-linearization/queue sites named in the T1.S1 PR.
- [x] One scripted internal constructor and one root `newScriptedAllocation` replace `prepareHarness`'s construction, `allocHarness`, and `inboundDeliveryHarness`; `close_latency_test` keeps its own harness.
- [ ] `AllocationConfig` exposes `PerformTransaction(msg) (*stun.Message, error)` and `StartTransaction(msg) error`; `TransactionResult`, `dontWait`, `ignoreResult`, and `Client.performTransaction` are absent; root wires registry method values.
- [ ] The lifetime-zero release is emitted once from `startCloseLocked` via `StartTransaction`, byte-identical to today's after transaction-ID normalization, after abort and `onDeallocated`, and a failed emission is still joined into the terminal cause. Domain: the one release path; owner: `UDPConn.emitRelease`; guarantee: universal; evidence: txid-normalized byte equality plus the existing exactly-one-release and ordering tests.
- [ ] Emitted Allocate/Refresh/CreatePermission/ChannelBind/ChannelData bytes, setter order, retransmission policy, public API, and Allocation lifecycle ownership are unchanged.

## Validation Gates

Per slice: the focused tests named in its evidence budget, `go test ./...`, `go test -race ./...`, and `task preflight`. Keep each PR draft through bounded review and exact-head local certification, mark it ready afterward, and require the latest post-ready `ci` run's `ci-required` job to succeed on the exact live head before squash merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice` for every slice/PR; this repository has no overlay. Keep construction on `newUDPConn`/`start`, transaction semantics on `TransactionRegistry`, release emission on `UDPConn.emitRelease`, and the one production assembler on root `Client.Allocate`. Stop rather than adding a helper knob, an options layer, a clock, or a third crossing.
