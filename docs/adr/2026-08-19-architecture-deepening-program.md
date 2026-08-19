# TURN Architecture Deepening Program — 2026-08-19

**Status:** Complete; Tracks 1, 2, and 3 implemented by PRs #72, #74, and #76

## What this is

This program packages the 2026-08-19 architecture review and delegated grill of the remaining `the-sarge/turn` deepening opportunities. The three track plans are the normative source of truth for outcomes, boundaries, ownership, evidence, blockers, and stop conditions. GitHub issues and the OmniFocus mirror carry stable identities, current state, and pointers only. This program follows the completed 2026-08-17 architecture deepening program and does not reopen its settled ownership decisions.

## Outcomes that require no implementation

- Do not create a shared TURN error decoder. Allocate, Refresh, CreatePermission, and ChannelBind repeat `ERROR-CODE` parsing plus `stun.TurnError` construction (`client.go:242-253`; `internal/client/allocation.go:72-89`; `internal/client/udp_conn.go:611-628,809-832`), but the policy-free common core is only that attribute read and struct construction. A shared root/internal seam would be as complex as its implementation, while a deeper helper would recreate the rejected generic authenticated-exchange module.
- Do not schedule Allocation-construction work as a standalone track. The current bag combines server destination and arbitrary-destination capabilities with genuine grant and maintenance facts (`client.go:309-323`; `internal/client/allocation.go:16-35`; `internal/client/udp_conn.go:83-149`). Server-bound transport removes the false destination width. Re-audit construction after Track 1 and proceed only if current code still admits a reachable invalid construction, has multiple genuine production assemblers, or duplicates validity rules; fixture convenience and field regrouping are insufficient.
- Do not schedule Permission/Channel-binding attempt coalescing now. The current Permission and binding paths repeat one-flight/join/caller-cancellation bookkeeping but differ in map deletion, eligibility, state transitions, worker rollback, and lifecycle disposition (`internal/client/udp_conn.go:223-398`; `internal/client/permission.go:19-47`; `internal/client/binding.go:35-45`). Re-audit after Track 3 and proceed only if one `UDPConn`-owned implementation can hide the remaining concurrency protocol without policy flags, lifecycle callbacks, worker ownership, Permission deletion, binding transitions, or Allocation sealing. Current evidence predicts closure without code.
- Do not introduce a generic transport, broad clock, prepared-peer module, generic state engine, authenticated-exchange module, builder/options layer, shared responder-harness program, or protocol-codec cleanup.
- No new domain term is required. Client, Allocation, transaction, permission, channel binding, prepared peer, ChannelData, Data indication, and terminal cause retain their established meanings.

## Tracks, dependencies, and frontier

| # | Track | Plan | Parent issue | Blocked by | Slices | Status |
|---|---|---|---|---|---|---|
| 1 | Server-bound outbound transport | [plan](2026-08-19-server-bound-transport-plan.md) | #65 | None | 1 | Complete via PR #72 |
| 2 | Inbound Allocation delivery | [plan](2026-08-19-inbound-allocation-delivery-plan.md) | #66 | None | 1 | Complete via PR #74 |
| 3 | Channel-binding readiness | [plan](2026-08-19-channel-binding-readiness-plan.md) | #67 | None | 1 | Complete via PR #76 |

There are no genuine slice-level blocking edges. The tracks touch nearby root/internal seams but own independent behavior: outbound destination authority, inbound delivery, and binding readiness. Likely text conflicts require an ordinary rebase, not an architectural blocker. Recommended dispatch order is Track 1, Track 2, then Track 3 because it advances from the smallest broad seam contraction to the bounded inbound move and finally the most concurrency-sensitive state deepening; an operator may run the frontier in parallel if separate agents and worktrees can absorb rebases.

After Track 1 merges, perform the Allocation-construction re-audit before creating any construction issue. After Track 3 merges, perform the attempt-coalescing re-audit before creating any coalescing issue. Those checkpoints are not current slices and are not dispatchable work.

## Rules that bind every track

- Plan docs are normative. One audited slice maps to exactly one child issue, one intended PR, one fresh implementation context, and one OmniFocus slice task.
- Public API and emitted Allocate, Refresh, CreatePermission, ChannelBind, and ChannelData bytes and setter order remain unchanged.
- The caller owns `ClientConfig.Conn`; the fork never closes it, sets deadlines, or interrupts in-flight I/O. Client remains bound to one canonical TURN server.
- Root `Client` keeps server-source admission, TURN wire demultiplexing and decoding, Data-indication peer canonicalization, and transaction completion. `UDPConn` remains the sole Allocation lifecycle owner, including seal, abort-before-release ordering, exactly one lifetime-zero release, terminal cause, worker registration, and caller join.
- Writes remain prepared-only ChannelData. Permission identity remains per canonical peer IP; channel-binding identity remains per canonical peer transport address. Inbound data is not made prepared-only.
- Shared review-loop and contract-closure baselines govern every slice. Each PR uses TDD-shaped focused evidence, finite evidence and review budgets, exact-head `task preflight`, a draft-to-ready transition only after local certification, successful same-head hosted CI, merge, `append-dev-journal`, and then pointer/task updates.
- Representation and artifact gates in each plan are ceilings as well as floors. Do not broaden supported protocol syntax, timing matrices, cleanup, or verification infrastructure during review. Diagnose failures before reruns and stop when representation ownership, accepted outcome, blast radius, or the one-PR boundary changes.
