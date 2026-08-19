# Transaction Registry Deepening Implementation Plan

**Date:** 2026-08-17
**Status:** Implemented by PR #49
**Track:** 1 of 3 in the 2026-08-17 architecture deepening program
**Depends on:** Nothing — safe to start first
**Related:** [Program index](2026-08-17-architecture-deepening-program.md), [Modernize the kept API plan](2026-08-15-modernize-kept-api-plan.md), [TURN molding program](2026-08-14-turn-molding-program.md)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Independently audited against `e251b6b` on 2026-08-17; the slice passed after its initial-send, fire-and-forget, Client-close, socket-authority, and evidence contracts were closed; implementation completed by PR #49

## Goal

Turn the current shallow `Transaction` plus `TransactionMap` pair into one deep transaction registry whose small internal interface owns each TURN transaction from registration through retirement. The registry concentrates registration, initial-send rollback, retry scheduling, one-winner terminal claims, response publication, waiter cancellation, Allocation abort, Client close, and removal. Root `Client` keeps protocol-method policy and caller-owned socket authority; callers stop composing the registry's concurrency protocol.

## Current Shape (verified 2026-08-17 at `e251b6b`)

`Client` exposes both `trMap` and a separate `mutexTrMap` (`client.go:58-72`). `startTransaction` constructs and inserts a transaction, then performs the initial socket write and arms its timer (`client.go:358-385`). A failed initial write returns after insertion without deleting or closing the transaction and without arming a timer (`client.go:376-380`), leaving a stranded registry entry until a later global close.

The rest of the ownership protocol is spread across root callers: ordinary waits (`client.go:388-408`), Allocate-specific cancellation arbitration (`client.go:410-464`), inbound completion (`client.go:546-573`), Client close and Allocation abort (`client.go:173-190`), and retransmission/exhaustion (`client.go:600-648`). Callers rely on an incomplete convention: most terminal claims use map membership while holding `mutexTrMap`, but initial registration at `client.go:376` occurs outside that outer lock, and `CloseAndDeleteAll` removes/closes entries without stopping their timers first. Socket I/O must remain outside the ownership lock; only a successful claimant may publish or close; timer callbacks and completed writes must discover lost ownership and avoid re-arming. Neither current type enforces the full protocol.

The internal module exposes the mechanics instead of the policy: raw message/destination fields, result channels, timer start/stop, retry counters, close, and independent map insert/find/delete/close operations (`internal/client/transaction.go:18-219`). The map has its own lock in addition to `Client.mutexTrMap`, so correctness depends on a caller convention that neither type can enforce. Root tests insert map entries and inspect map size to exercise inbound handling (`client_test.go:77-114`), while the difficult invariants are tested above the seam through cancellation and blocked-write races (`allocate_ctx_test.go:241-548`).

All production transaction calls target the one canonical TURN server held by `Client` (`client.go:214,253,392`; `internal/client/allocation.go:98`; `internal/client/udp_conn.go:583,774`). Destination string comparison in `TransactionMap.CloseAndDeleteAllTo` (`internal/client/transaction.go:197-213`) therefore does not represent an independent multi-destination domain for the one-server, one-live-Allocation Client. `Client.Close` currently aborts transactions present at the time of the call and is idempotent; it does not make the Client permanently terminal (`client.go:173-180`). M1 preserves abort-all and idempotence but does not promise future transaction support explicitly. This plan deliberately preserves the current nonterminal behavior and pins it with a post-Close transaction test, while also preserving M1's caller-owned socket rule (`2026-08-15-modernize-kept-api-plan.md:135-169`).

## Decision

One private transaction registry owns the live transaction set and every transition that changes ownership. A live transaction is identified by the STUN transaction ID produced and parsed by `pion/stun`; duplicate registration of an already-live ID is rejected rather than overwriting the owner. The registry copies outbound bytes at registration and sends only through an injected send-only capability. It never receives `net.PacketConn`, so closing the socket, setting deadlines, changing read behavior, and interrupting in-flight writes are structurally outside its authority.

The registry exposes behavior-shaped operations to root `Client`: begin a waited or fire-and-forget transaction, complete a matching inbound response, wait with the caller-specific cancellation policy, and abort current work. It does not expose raw insert/find/delete, result-channel writes, timer controls, or retry counters. Exact interface spelling is implementation-local; the acceptance contract is the ownership behavior, not a preselected Go type signature.

The registry's mutex is the sole linearization owner for registration and terminal claims. Its finite phases are registered, initial-send-in-flight, waiting, retry-write-in-flight, claimed, and retired. A claimant removes the transaction before publishing a result or closing a waiter. Initial and retransmit socket writes occur outside that lock. After either unlocked write returns, the registry re-checks ownership under the same claim guard before publishing an error, arming, or re-arming. If an initial send succeeds after a response or abort already claimed the entry, that claimant wins and no timer is armed. If an initial send fails, the send error remains the begin caller's result even when abort raced; the registry only rolls back if it still owns the entry. Late responses and completed writes for removed transactions are discarded without blocking.

Allocate's context may cancel its private transaction. `PreparePeer` cancellation remains waiter-local and does not cancel shared CreatePermission or ChannelBind work; Allocation sealing uses the registry's abort-current operation to stop all current Allocation work. The lifetime-zero release starts only after that abort and is a new fire-and-forget transaction outside the aborted set. Abort-current is an atomic snapshot cut: begins linearized before the cut are claimed, while begins linearized after it survive. `Client.Close` uses that operation but does not set a permanent closed flag.

Root `Client` owns the caller's `net.PacketConn` and adapts only its write capability into the registry; scripted, failing, and blocking send functions are local test adapters. The registry forwards its copied raw request bytes unchanged on the initial write and every retry. Retransmission interval, cap, count, order, and accepted late-write behavior remain unchanged.

**Rejected alternative (do not do this):** Move only retransmission into `Transaction` while leaving registration, claims, cancellation, inbound completion, and abort in root callers. That preserves the shallow ownership protocol and adds another callback crossing.

**Rejected alternative (do not do this):** Make `Client.Close` permanently reject future transactions or directly seal a live Allocation. The accepted contract and sole consumer currently assign release/join ownership to `Allocation.Close`; Client terminality is a separate behavior decision.

**Rejected alternative (do not do this):** Keep destination-scoped abort by comparing `net.Addr.String()`. The supported Client has one canonical server and at most one live Allocation; string identity is weaker than the actual ownership domain.

**Non-goals:** No retransmission-policy change, broad clock abstraction, socket ownership change, STUN/TURN byte change, authenticated-method refactor, root/internal package merge, protocol-codec trimming, or public API change.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T1.S1 | Complete via PR #49 | One transaction registry owns begin, rollback, claims, retry, cancellation, abort, close, and retirement | None | Removed `TransactionMap`, `mutexTrMap`, and caller-composed result/timer operations in the same slice |

## Implementation Slices

### Slice T1.S1 — Make the transaction registry the terminal owner

**What it delivers:** One PR that first pins and fixes initial-send rollback as its tracer bullet, then moves registration, duplicate-ID rejection, inbound completion, retransmission/exhaustion, Allocate cancellation arbitration, fire-and-forget retirement, Allocation abort, and Client close into one private registry. Root `Client` delegates these behaviors and no longer owns `mutexTrMap` or calls raw transaction/map/timer/channel methods. Obsolete `TransactionResult.From`, `TransactionResult.Retries`, `Transaction.Retries`, raw `TransactionMap` methods, and the unreachable wait-on-fire-and-forget error are removed when no production or behavior-level test caller remains.

**Implementation:** PR #49 is the slice's one intended product PR; stale local architecture-review branches were not used as an implementation baseline.

**Blocked by:** None.

**Single owner after merge:** The private transaction registry is the only mutation owner for live-set membership, retry state, terminal claim, result publication/closure, and retirement. Root `Client` owns TURN method policy and delegates; the socket owner remains the external caller.

**Authority completeness:** No persisted fact becomes authoritative. For the in-memory live-set authority, the same slice covers construction, duplicate validation, every producer/closer, and final removal. No raw generic mutation path remains.

**Transitional-seam budget:** None. The first tracer-bullet commit may temporarily fix rollback in `startTransaction`, but the intended PR cannot merge until the registry owns the whole protocol and the raw map/caller-lock seam is removed.

**Blast radius:** Concurrency and ordering across all TURN requests; error delivery for initial/retransmit failures, exhaustion, cancellation, abort, and Client close; root/internal package interaction; tests that currently manipulate `trMap`. Duplicate-ID rejection is a new surfaced internal begin failure instead of silent ownership replacement. Client close remains a nonterminal abort-current snapshot. Destination scoping is removed only because the supported production domain is one canonical server and one live Allocation. Initial writes may race response, abort, or Close; terminal claims may race an in-flight retry. Abort now stops each claimed timer immediately, whereas the old bulk close deleted/closed entries and let callbacks later discover absence. Public API and emitted bytes are unchanged. No performance claim is accepted. Security posture is unchanged. Untraced effects are not accepted: if any producer, closer, timer callback, or map mutation exists outside the registry after migration, stop before merge.

**Artifact classification:** Registry behavior and its ownership guards are shipped behavior and required safety enforcement. Race tests, scripted/failing/blocking socket adapters, and the optional single guard mutation are verification aids. This plan, issue pointers, review receipts, and OmniFocus tasks are process or traceability metadata. No verification aid becomes a maintained product deliverable.

**Representation contract:** Supported domain = outbound STUN messages built by this client with `pion/stun` transaction IDs; admitted inbound STUN success/error responses from the canonical server; real-time retry callbacks; Allocate cancellation; Allocation abort; Client close; initial/retransmit send failure; and the fire-and-forget lifetime-zero release. `pion/stun` owns message and transaction-ID parsing; the registry owns the copied raw bytes, live-ID set, and finite phases. The ownership guarantee is universal over the finite state/event matrix below, not over arbitrary STUN syntax or hostile send implementations. Socket authority is structural: the registry receives only a send capability. Byte preservation is universal over each supplied raw `[]byte`: every successful initial/retry call receives an equal copy; method-level turntest flows are examples because builders remain outside this slice. Public-API preservation is finite over the exported root identifier/signature manifest and compiler checks, not an inference from runtime tests. Terminating evidence = the matrix, focused behavior tests, static implementation/export audit, `go test -race ./...`, and one review plus at most one replacement.

**Contract closure:** Triggered because violating one-winner ownership can panic on send/close, strand waiters, leak live entries, or re-arm canceled work, and the invariant is reached independently through response, cancellation, timeout, socket error, Allocation abort, and Client close.

| Semantic class | Expected disposition | Enforcement owner | Terminating evidence | Status |
|---|---|---|---|---|
| Initial send succeeds while registry still owns entry | Transition to waiting and arm exactly one timer; waiter or fire-and-forget path returned | Registry begin | Focused begin test plus existing request tests | Covered |
| Initial send succeeds after matching response claimed | Response wins; no timer is armed; waiter observes response | Registry begin/completion claim | Blocked-initial-write response race | Covered |
| Initial send succeeds after abort or Client close claimed | Abort wins; no timer is armed; waiter observes closed semantics | Registry begin/abort claim | Blocked-initial-write abort/Close race | Covered |
| Initial send fails without competing claim | Registration rolled back; original send error returned; no timer/live entry | Registry begin | New failing-send regression | Covered |
| Initial send fails after abort or Client close claimed | Send error remains begin caller result; no timer/live entry; abort has already woken any registered waiter | Registry begin/abort claim | Blocked-initial-write error race | Covered |
| Duplicate live transaction ID | Second registration rejected; original owner preserved | Registry begin | Focused duplicate-ID test | Covered |
| Matching response claims first | Timer retired, entry removed, exactly one response published | Registry completion | Inbound response test through `Client.HandleInbound` | Covered |
| Allocate cancellation claims first | Timer retired, entry removed/closed, cancellation cause returned | Registry cancel claim | Existing Allocate cancellation table adapted to registry seam | Covered |
| Response/close claims before cancellation | Waiter consumes the already-owned result; response or close precedence preserved | Registry claim plus wait | Existing response-wins and close-vs-cancel tests | Covered |
| Retry fires below exhaustion | Socket write occurs unlocked; re-arm only if ownership remains after write | Registry retry | Existing blocked-retransmit test plus ownership re-check assertion | Covered |
| Response or close claims during retry write | Claim wins; returned write cannot publish or re-arm | Registry completion/abort plus retry guard | Blocked-retransmit response/close cases | Covered |
| Waited retransmit write fails or budget exhausts | Entry claimed once; typed failure published; no re-arm; exhaustion occurs after seven total identical writes (initial plus six retries) | Registry retry | Focused waited failure/exhaustion cases with byte/count assertions | Covered |
| Fire-and-forget matching response | Entry retires without publication or blocked sender | Registry completion | Focused release-response retirement case | Covered |
| Fire-and-forget write failure or exhaustion | Entry retires without publication, re-arm, or leak | Registry retry | Focused release failure/exhaustion retirement case | Covered |
| Allocation abort or Client close claims live work | All transactions registered before the atomic cut wake with closed semantics and their timers stop | Registry abort-current | Existing close/abort tests under race detector | Covered |
| Root transaction begins after Client close cut | New request survives and completes through `Client.HandleInbound` because Client close is deliberately nonterminal | Root `Client` delegation plus registry begin | `Client.performTransaction` or `Client.Allocate` after `Client.Close` | Covered |
| Late response after any retirement | Response is discarded without publication or blocked sender | Registry completion | Focused late-response case | Covered |

**Evidence budget:** One initial-write table covering ordinary success/failure and response/abort/Close races; one duplicate-ID case; one inbound success case through the real Client seam; the existing Allocate cancellation table covering pre-send, both waits, response-wins, and close precedence; one blocked retransmit response/close table; one waited retransmit failure/exhaustion table asserting seven total byte-identical writes; one fire-and-forget response-retirement case; one fire-and-forget failure/exhaustion-retirement case; one root `Client` request after `Client.Close`, completed through `HandleInbound`; one late-response discard case; existing Allocation abort/close coverage; full race suite. Preserve the existing interval constants, exponential progression, and cap mechanically; finite diff inspection of that arithmetic is the gate, with no wall-clock repetition or broad clock abstraction. At most one mutation is permitted if initial and retry completion share one ownership guard: remove that guard and observe both blocked-write classes fail. If the implementation has separate guards, omit mutation rather than expanding the budget. No fuzz, platform matrix beyond repository preflight, or repeated timing run is required. The task terminates after these cases, repository preflight, hosted same-head CI, one review, and at most one replacement review are green with no `stop-for-decision` finding.

**TDD and preservation evidence:** Write the initial-send table and duplicate-ID tests first. Adapt inbound tests to create work through the registry/Client begin path instead of inserting map entries. Preserve existing assertions for cancellation cause, close precedence, late success, and no socket-control authority. Add exact seven-write exhaustion, supplied-byte equality, both fire-and-forget terminal paths, and a root post-Close request completed through `HandleInbound`; existing tests do not establish those claims. Keep retry interval/cap arithmetic mechanically unchanged and audit its diff. Builders and setter order stay outside this slice, while existing turntest flows provide example-level method preservation.

**Dispatch context budget:** This slice contract; the Decision and contract-closure matrix above; `client.go:57-80,133-190,214,253,358-464,546-648`; `internal/client/transaction.go`; `internal/client/allocation.go:18-46,98`; `internal/client/udp_conn.go:474-520,583,774`; `allocate_ctx_test.go:24-548`; `client_test.go:61-114`; `close_latency_test.go:22-140`; `internal/client/transaction_test.go`; and M1's transaction ownership/socket rules at `2026-08-15-modernize-kept-api-plan.md:135-169`. No earlier architecture report or full M1 chronology is required. The bounded surface is two implementation files plus consumers and focused preservation tests; implementation, one review-fix pass, and exact-head verification fit one fresh context.

**Slice decision audit:** Strongest further split = ship the initial-write rollback as a separate PR. Rejected because it is one TDD step in the same begin path and would leave the shallow ownership protocol intact while adding a second review/merge cycle; the full migration remains context-sized and cannot leave two owners at merge. Strongest adjacent merge = combine with Allocation lifecycle consolidation. Rejected because the registry and Allocation are separate mutation owners with different failure/state matrices, the combined context would be too broad, and Allocation can consume the merged registry through a stable internal operation. No blocking edge exists because this slice owns only current transaction behavior.

**Stop conditions:** A supported producer or closer cannot be routed through one registry without changing public behavior; the registry would need to hold its mutex across caller socket I/O; preserving Client-close semantics requires a permanent closed flag; Allocate cancellation would have to cancel shared PreparePeer work; the lifetime-zero release cannot start outside the aborted set; exact retransmission bytes/order/count change; or a second behaviorally distinct counterexample appears at the same one-winner invariant and registry enforcement owner after an accepted fix.

## Acceptance Criteria

- [x] Every supported transaction terminal event has exactly one registry claim owner, and no result is published or channel closed after another actor owns removal. Supported domain, owner, universal guarantee, and terminating evidence are the representation contract and matrix above.
- [x] An initial socket-write failure leaves no live transaction and no armed timer while returning the original error.
- [x] Socket writes occur outside the registry ownership lock, and a removed transaction never publishes or re-arms after an in-flight write returns.
- [x] Allocate cancellation, response-wins, close precedence, Allocation abort, Client close, and fire-and-forget release preserve their accepted observable outcomes.
- [x] The registry receives only a send capability, so it cannot close, deadline, read from, or interrupt the caller-owned socket.
- [x] `Client.mutexTrMap`, raw caller mutation of `TransactionMap`, destination-string abort matching, and dead retry/from result surface are absent.
- [x] The finite exported root API manifest is unchanged, and the registry forwards an equal copy of every supplied raw request on the initial write and all six retries; method-level turntest flows remain green.

## Validation Gates

Run focused transaction, inbound, cancellation, late-response, blocked-retransmit, and Allocation-close tests first; then `go test ./...`, `go test -race ./...`, and repository `task preflight` against the exact candidate head/base. Keep the PR in draft through review and local certification, mark it ready afterward, and verify the latest post-ready `ci` run reports `ci-required` success for the same live head before merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice` for this PR. Preserve the caller-owned socket, prepared-only write, exact-wire, one-winner, and Allocation-before-Client lifecycle invariants. Diagnose any race or hosted-CI failure before rerunning. Stop rather than widening into Client terminality, retransmission policy, authenticated exchange, or Allocation lifecycle implementation.
