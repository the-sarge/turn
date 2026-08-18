# Allocation Lifecycle Ownership Implementation Plan

**Date:** 2026-08-17
**Status:** Accepted; not implemented
**Track:** 2 of 3 in the 2026-08-17 architecture deepening program
**Depends on:** Nothing — safe to start independently
**Related:** [Program index](2026-08-17-architecture-deepening-program.md), [Transaction registry plan](2026-08-17-transaction-registry-plan.md), [Modernize the kept API plan](2026-08-15-modernize-kept-api-plan.md), [Prepared-only writes ADR](2026-08-15-prepared-only-writes.md), [RFC 8656](https://www.rfc-editor.org/rfc/rfc8656.html)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Independently audited against `e251b6b` on 2026-08-17; T2.S1 passed after its abort-capability and finite lifecycle contracts were closed; the proposed adaptive-refresh successor was closed without code because it is unreachable in the conforming exact-wire domain; no implementation or overlapping open issue/PR exists

## Goal

Make `UDPConn` the single implementation owner of one Allocation's nonce, grant, maintenance outcomes, terminal cause, release, and worker join. Remove the shallow embedded `allocation` half and its upward callbacks while retaining the already-deep `startClose`/`Close` lifecycle and the supported fixed refresh schedule.

## Current Shape (verified 2026-08-17 at `e251b6b`)

`UDPConn` embeds an `allocation` value (`internal/client/udp_conn.go:43-64`). Construction nests three client hooks inside that value, then installs upward callbacks from the embedded half back to `UDPConn` for permission-refresh and Allocation-refresh failure (`internal/client/udp_conn.go:66-100`; `internal/client/client.go:13-22`; `internal/client/allocation.go:44-70`). The embedded type owns nonce, lifetime, Refresh requests, permission refresh, and two timers, while `UDPConn` owns binding state, close state, terminal cause, worker registration, and joins. Understanding one Allocation lifecycle therefore requires following state and callback direction across three files.

The healthy terminal owner already exists on `UDPConn`: `Close` distinguishes first caller, repeated caller, and prior self-seal, stops and joins timers/workers, and returns the release or terminal result (`internal/client/udp_conn.go:434-471`). `startClose`/`startCloseLocked` are worker-safe, guard exactly one seal and lifetime-zero emission, abort pending transactions before release, record one terminal cause, and never self-join (`internal/client/udp_conn.go:474-520`). Existing tests exercise refresh-failure disposition, concurrent caller closes, refresh-failure seal versus close, release-emission error joining, permission-refresh failure, and ChannelBind-400 recovery/seal (`internal/client/refresh_failure_test.go:107-299`; `internal/client/prepare_test.go:287-304`; `internal/client/udp_conn_test.go:416-495`).

The Allocation refresh timer is created once at half the initial grant (`internal/client/udp_conn.go:102-106`) and `PeriodicTimer` retains that interval (`internal/client/periodic_timer.go:14-60`). Deeper audit rejected the proposed adaptive-refresh slice for the supported production domain: Allocate sends no LIFETIME attribute (`client.go:198-207,233-246`), so RFC 8656 Section 7.2 gives the 600-second default; later Refresh requests ask for the current 600-second grant (`internal/client/allocation.go:83-93,160-170`), and RFC Section 8.2 continues that grant. A 3,600→600 path exists only through nonconforming/example-level stand-ins unless this client changes Allocate bytes. Exact-wire preservation and the owned one-consumer contract take precedence, so fixed half-life scheduling is intentional in this program.

## Decision

`UDPConn` remains the one Allocation lifecycle module and directly owns every field and method now held by the embedded `allocation`. Delete the `allocation` type and `clientHooks` wrapper. Keep implementation split across appropriately named files if useful, but methods use one receiver and no upward lifecycle callback. Allocation-refresh failure directly calls `startClose`; permission-refresh failure directly terminalizes prepared bindings. Root-to-internal construction still supplies socket-write, transaction, deallocation-notification, and abort-current adapters; tests supply local stand-ins. These package-crossing production/mock adapters do not mutate lifecycle state.

The abort-current adapter is required at every `UDPConn` construction site. Production assembly creates that dependency before sending Allocate and uses a construction representation that cannot omit it; `NewUDPConn` never substitutes a default no-op and defensively rejects programmer-invalid test/internal construction before starting timers or workers. A nonnil function cannot prove that its body is effective, so prompt-close and abort-before-release tests exercise the real production adapter. Production rejection after successful server Allocate is structurally unreachable because root assembly establishes the required capability before the request; if implementation makes rejection reachable instead, that path must release the server Allocation before returning. Before Track 1 lands the adapter delegates to the existing transaction map; after Track 1 it delegates to the registry. `startCloseLocked` invokes it before `OnDeallocated` and before the lifetime-zero fire-and-forget Refresh, so the release transaction starts outside the aborted live set.

Preserve terminal semantics exactly: workers may seal but never join themselves; the caller is the join point; exactly one seal emits lifetime zero; one terminal cause wins; a release-emission failure joins a self-seal cause; repeated caller close returns `net.ErrClosed`. Permission-refresh exhaustion terminalizes prepared bindings but does not seal the Allocation. ChannelBind 400 on a previously confirmed binding preserves that binding and does not seal; 400 on a fresh binding seals. Invalid relayed addresses remain construct-then-close so the server Allocation is released before rejection (`client.go:326-355`). `Client.Close` remains separate.

Keep Allocation, permission, and binding timer cadence unchanged. Ordinary Refresh success still records the parsed lifetime; 438 still updates nonce and retries up to the existing limit; permanent failure still seals. The stored lifetime does not create an adaptive schedule in this program because conforming supported traffic cannot vary it. If future work adds a requested Allocate lifetime, changes the exact wire contract, or explicitly supports nonconforming grant changes, adaptive scheduling requires a new grilling/handoff with a reachable representation contract.

**Rejected alternative (do not do this):** Extract a standalone Allocation lifecycle/seal object. It would have one production caller, require callbacks into `UDPConn` for bindings/workers/close, and split the existing healthy owner.

**Rejected alternative (do not do this):** Add adaptive Allocation refresh for a hypothetical 3,600→600 grant now. That transition is unreachable through the conforming shipped client, and making it reachable would require an Allocate LIFETIME/wire change or an explicit nonconforming-server robustness decision.

**Rejected alternative (do not do this):** Improve `PeriodicTimer` generically or rewrite permission/binding scheduling. The accepted win is lifecycle locality, not generic goroutine plumbing.

**Rejected alternative (do not do this):** Reject an invalid relayed address before constructing `UDPConn`. The accepted path must emit lifetime zero to avoid leaving a server Allocation until expiry.

**Non-goals:** No public `Allocation` API change; no Client terminality change; no adaptive timer; no requested Allocate lifetime; no nonconforming-server grant support; no prepared-peer module; no Permission/binding identity change; no Send indication; no transaction/retransmission change; no socket ownership or wire-byte change.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T2.S1 | New | `UDPConn` becomes the sole Allocation lifecycle implementation; callbacks and embedded half delete | None | Removes the embedded `allocation`/`clientHooks` seam in this slice |

## Implementation Slices

### Slice T2.S1 — Consolidate Allocation lifecycle ownership in `UDPConn`

**What it delivers:** One behavior-preserving PR moving nonce, lifetime, permission-refresh, Allocation-refresh, and root adapter fields/methods from embedded `allocation` onto `UDPConn`; deleting `allocation` and `clientHooks`; replacing upward failure callbacks with direct `UDPConn` calls; making the abort-current capability mandatory in production assembly without substituting a no-op; retiring the internal no-abort construction path; and preserving fixed timer cadence, `startClose`/`Close`, public root `Allocation`, and all supported outcomes.

**Existing-work disposition:** New slice. There is no open issue or PR for this work.

**Blocked by:** None. The current abort-current adapter already supplies the capability; Track 1 changes its implementation owner without changing this Allocation-side contract.

**Single owner after merge:** `UDPConn` is the sole mutation owner for nonce, granted lifetime, maintenance disposition, terminal cause, seal, release, and join. The current map or future transaction registry owns transaction mechanics. Root `Client` supplies adapters and receives deallocation notification but does not mutate Allocation state.

**Authority completeness:** No persisted fact. Every constructor and mutation of nonce/lifetime/terminal state is migrated in this slice, every production/test constructor supplies an explicit abort-current adapter, production assembly makes omission structurally unreachable, and no raw lifecycle callback remains. The constructor never manufactures a default no-op; behavior tests, not a nonnil check, prove that the production adapter aborts.

**Transitional-seam budget:** None. Fixed `PeriodicTimer` instances are intentionally retained supported implementation, not duplicate ownership or a temporary seam. The abort-current adapter is a real production/mock package seam and remains after Track 1 with a different implementation.

**Blast radius:** Internal receiver/field movement throughout `internal/client/allocation.go` and `udp_conn.go`; `NewUDPConn` signature/validation and all root/test constructors; refresh, permission, binding, close, and error paths. Public API, emitted bytes, timer cadence, retry disposition, error chains, worker counts, and caller-owned socket behavior are preserved. The internal missing-abort/no-abort test path is intentionally retired; production already supplies abort at `client.go:338-343`. Untraced effects are not accepted: any lifecycle callback from a nested owner or mutation of nonce/lifetime/terminal cause outside `UDPConn` after merge is a stop.

**Artifact classification:** Consolidated lifecycle behavior and seal/join guards are shipped behavior and required safety enforcement. Existing/adapted lifecycle, race, mock-adapter, and construction tests are verification aids. Plans, issues, and tasks are process metadata. No maintained verification aid is introduced.

**Representation contract:** Supported domain = production `UDPConn` construction with explicitly supplied required adapters and a positive initial RFC-default grant; ordinary Refresh success and 438 retry; permanent Refresh failure; permission-refresh success/failure; fresh and previously-ready ChannelBind 400; caller close; worker self-seal; concurrent seal/close; in-flight transaction abort; invalid relayed-address cleanup; and release-emission success/failure. Root `Client` owns canonical server/relayed-address construction and establishes abort capability before Allocate; `pion/stun`/`internal/proto` own response parsing; `UDPConn` owns lifecycle transitions. Guarantee = universal over the finite lifecycle table below. Missing-capability internal construction is a programmer-invalid state rejected before live work; the production representation cannot reach it after server allocation, and no default/no-op is supplied. Terminating evidence = one focused case per row, production adapter prompt-close/ordering evidence, race suite, preflight, and bounded review.

**Contract closure:** Not triggered. Consequences are material, but the supported lifecycle is finite, already concentrated on `startClose`/`Close`, and reasonably covered by the focused preservation table below; multiple callers alone do not satisfy the closure trigger.

| Semantic class | Expected disposition | Enforcement owner | Terminating evidence | Status |
|---|---|---|---|---|
| Missing abort-current adapter in internal/test construction | Programmer-invalid construction is rejected before timer/worker start; constructor supplies no default/no-op; production assembly cannot omit it after Allocate | Root assembly representation + `NewUDPConn` defensive guard | Focused invalid-construction test and production assembly audit; replace no-abort harness case | Planned |
| Ordinary Refresh success or one/more 438 then success | Nonce/lifetime update and current fixed cadence preserved | `UDPConn` Refresh methods | Existing Refresh/nonce tests adapted | Planned |
| Caller closes healthy Allocation | Seal once, abort, emit lifetime zero, join, return emission result | `startCloseLocked` + `Close` | Existing close tests | Planned |
| Allocation Refresh permanently fails | Worker-safe self-seal; operations wake; caller later joins and observes cause | `startClose` | Existing refresh-failure table | Planned |
| Permission Refresh permanently fails | Prepared bindings terminalize; Allocation remains live; no fallback write | `failPreparedBindings` | `prepare_test.go` permission-refresh case | Planned |
| Permission Refresh succeeds | Permission refreshed time advances; prepared bindings and Allocation remain usable | `UDPConn` permission-refresh path | Existing success case adapted through consolidated receiver | Planned |
| Fresh ChannelBind receives 400 | Worker-safe self-seal with ChannelBind cause | `startClose` | Existing fresh-binding 400 test | Planned |
| Previously-ready ChannelBind refresh receives 400 | Saved binding remains usable; Allocation does not seal | `recoverChannelBindBadRequest` | `udp_conn_test.go` ready-binding cases | Planned |
| Self-seal races caller close | Exactly one seal/release; caller result follows winning state | `closeMutex` + seal guard | Existing seal-vs-close race | Planned |
| Concurrent caller closes | One observes release result; duplicates return `net.ErrClosed` | `closeMutex` + `callerClosed` | Existing concurrent-close test | Planned |
| Release emission fails during self-seal | Failure joins terminal cause; caller join observes both | `startCloseLocked` | Existing emission-failure test | Planned |
| In-flight shared transaction during seal | Abort wakes worker before join; release starts after abort | `startCloseLocked` ordering + adapter | Prompt-close/abort-order tests | Planned |
| Invalid relayed address after server success | Construct and close, emit release, clear Client pointer, then reject | root `Client.Allocate` + `UDPConn.Close` | Existing invalid-relayed test | Planned |

**Evidence budget:** One missing-adapter programmer-error case plus static production assembly audit proving the adapter exists before Allocate; replacement of the internal no-abort close-latency case with a real-adapter abort-order case; existing prompt-close and waiter-local-cancellation cases; ordinary Refresh/438 success; refresh-failure table; permission-refresh success and failure; fresh and ready-binding 400; concurrent caller close; seal-vs-close race; self-seal emission failure; invalid-relayed release; full race suite. At most one mutation: bypass the one-seal guard and observe the exact-one release race fail. No timing repetitions, fuzz, or platform matrix beyond repository preflight. Termination = table cases, preflight, same-head hosted CI, one review, at most one replacement, and no `stop-for-decision` finding.

**TDD and preservation evidence:** Add the missing-adapter programmer-error case, production assembly audit, and real-adapter abort-before-release ordering assertions first. Explicitly replace the no-abort internal harness case at `close_latency_test.go:76-96`; it is a retired verification path, not supported shipped behavior. Then migrate receivers/fields in small commits while the lifecycle table remains green, including ordinary permission-refresh success. Preserve exact Refresh/CreatePermission/ChannelBind setters through parsed attribute-order and normalized-message comparisons that ignore generated transaction-ID values.

**Dispatch context budget:** This slice contract and table; all of `internal/client/allocation.go`; all receiver uses in `internal/client/udp_conn.go` including construction, permission, Refresh, ChannelBind, and close; `internal/client/client.go`; root `client.go:299-355`; every `NewUDPConn` constructor found by repository search; `allocation_test.go`; `close_latency_test.go`; `internal/client/{client,prepare,udp_conn,refresh_failure}_test.go`; and the relevant M1 one-owner/socket rules. No earlier architecture report is required. This is a medium-large receiver/ownership migration with focused test adaptation and fits one fresh context.

**Slice decision audit:** Strongest further split = ownership-only collapse independent of abort validation, followed by registry-abort adoption. Rejected because the accepted deep lifecycle must not retain optional close cancellation, and both changes cross the same constructor and `startCloseLocked` seam; splitting would leave a knowingly weak intermediate contract. Splitting fields from callback deletion is also rejected because it would retain two lifecycle owners. Strongest adjacent merge = merge with Track 1. Rejected because registry and Allocation are distinct owners and the combined concurrency matrices exceed one fresh context. No blocker exists: the current adapter provides abort-current now, while Track 1 later changes only its implementation. The rejected adaptive successor is closed without code rather than left as a dangling removal obligation.

**Stop conditions:** A lifecycle field cannot move without creating a second mutation owner; missing abort cannot fail before live timers/workers; direct failure handling requires a worker to call `Close` and self-join; abort cannot precede release; current terminal error/result precedence, timer cadence, or request attributes change; or a supported constructor cannot supply the required adapter.

## Acceptance Criteria

- [ ] `UDPConn` is the sole mutation owner for Allocation nonce, granted lifetime, maintenance disposition, terminal cause, seal, release, and join; the supported lifecycle domain, owner, universal guarantee, and finite evidence are declared above.
- [ ] Embedded `allocation`, `clientHooks`, and upward allocation/permission failure callbacks are absent.
- [ ] Production assembly structurally supplies abort-current before Allocate, the constructor never substitutes a no-op, programmer-invalid omission is rejected before timer/worker start, real-adapter tests prove effective abort, and seal invokes abort before deallocation notification and lifetime-zero release.
- [ ] Exactly one seal emits exactly one lifetime-zero Refresh, workers never self-join, caller `Close` remains the join point, and terminal result precedence is unchanged.
- [ ] Permission-refresh success/failure, fresh/ready ChannelBind-400 disposition, ordinary Refresh/438 behavior, invalid-relayed cleanup, and fixed timer cadence match the finite preservation table.
- [ ] Public API, caller-owned socket behavior, prepared-only writes, and normalized TURN request attributes/order remain unchanged.

## Validation Gates

Run constructor/abort-order, ordinary Refresh/438, terminal-state, permission-refresh, fresh/ready ChannelBind-400, invalid-relayed-address, and close-race tests first; then `go test ./...`, `go test -race ./...`, and `task preflight` against the exact candidate head/base. Request `ci-certify` only after local certification and verify the resulting same-head `ci-required` status before merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice`, composed with the repository-specific CI-label override in the program index: this repository uses `ci-certify`, not the shared default `ci:certify`. Preserve caller-owned socket, one-seal/one-release, worker-safe seal/caller join, prepared-only writes, fixed supported timer cadence, and exact-wire invariants. Diagnose failures before reruns. Stop rather than widening into adaptive scheduling, nonconforming-server behavior, a generic time module, prepared-peer extraction, Client terminality, or authenticated exchange.
