# TURN Architecture Deepening Program — 2026-08-19

**Status:** Active; Tracks 1, 2, and 3 complete, Track 4 has one audited frontier slice

## What this is

This program packages the 2026-08-19 architecture review and delegated grill of the remaining `the-sarge/turn` deepening opportunities. The four track plans are the normative source of truth for outcomes, boundaries, ownership, evidence, blockers, and stop conditions. GitHub issues and the OmniFocus mirror carry stable identities, current state, and pointers only. This program follows the completed 2026-08-17 architecture deepening program and does not reopen its settled ownership decisions.

## Outcomes that require no implementation

- Do not create a shared TURN error decoder. Allocate, Refresh, CreatePermission, and ChannelBind repeat `ERROR-CODE` parsing plus `stun.TurnError` construction (`client.go:242-253`; `internal/client/allocation.go:72-89`; `internal/client/udp_conn.go:611-628,809-832`), but the policy-free common core is only that attribute read and struct construction. A shared root/internal seam would be as complex as its implementation, while a deeper helper would recreate the rejected generic authenticated-exchange module.
- Do not redesign Allocation construction as a bag, builder, options layer, or timing module. The post-Track-1 re-audit found one reachable invalid public cadence and one production assembler, so Track 4 closes only the public validity defect; field regrouping and duplicate internal validation remain unjustified (`client.go:36-53,129-160,285-323`; `internal/client/allocation.go:16-35`; `internal/client/udp_conn.go:92-157`).
- Do not schedule Permission/Channel-binding attempt coalescing. The post-Track-3 re-audit confirms that the paths still differ in map deletion, eligibility, readiness transitions, worker rollback, and lifecycle disposition; one shared helper would require policy flags or callbacks and would split current ownership (`internal/client/udp_conn.go:187-423`; `internal/client/permission.go:19-47`; `internal/client/binding.go`). The checkpoint closes without code.
- Do not introduce a generic transport, broad clock, prepared-peer module, generic state engine, authenticated-exchange module, builder/options layer, shared responder-harness program, or protocol-codec cleanup.
- No new domain term is required. Client, Allocation, transaction, permission, channel binding, prepared peer, ChannelData, Data indication, and terminal cause retain their established meanings.

## Tracks, dependencies, and frontier

| # | Track | Plan | Parent issue | Blocked by | Slices | Status |
|---|---|---|---|---|---|---|
| 1 | Server-bound outbound transport | [plan](2026-08-19-server-bound-transport-plan.md) | #65 | None | 1 | Complete via PR #72 |
| 2 | Inbound Allocation delivery | [plan](2026-08-19-inbound-allocation-delivery-plan.md) | #66 | None | 1 | Complete via PR #74 |
| 3 | Channel-binding readiness | [plan](2026-08-19-channel-binding-readiness-plan.md) | #67 | None | 1 | Complete via PR #76 |
| 4 | Allocation construction timing validity | [plan](2026-08-19-allocation-construction-timing-validity-plan.md) | pending | None | 1 | T4.S1 frontier after issue publication |

There are no genuine slice-level blocking edges. Tracks 1–3 are complete. Track 4 depended only on the completed Track 1 construction checkpoint and now has the sole frontier slice; its public configuration guard is independent of the completed inbound-delivery and binding-readiness seams.

The required post-track checkpoints are closed. Allocation construction still admitted a reachable invalid public cadence after Track 1, producing Track 4's focused T4.S1. Attempt coalescing still lacked a policy-free single owner after Track 3 and closes without implementation.

## Rules that bind every track

- Plan docs are normative. One audited slice maps to exactly one child issue, one intended PR, one fresh implementation context, and one OmniFocus slice task.
- Public API and emitted Allocate, Refresh, CreatePermission, ChannelBind, and ChannelData bytes and setter order remain unchanged.
- The caller owns `ClientConfig.Conn`; the fork never closes it, sets deadlines, or interrupts in-flight I/O. Client remains bound to one canonical TURN server.
- Root `Client` keeps server-source admission, TURN wire demultiplexing and decoding, Data-indication peer canonicalization, and transaction completion. `UDPConn` remains the sole Allocation lifecycle owner, including seal, abort-before-release ordering, exactly one lifetime-zero release, terminal cause, worker registration, and caller join.
- Writes remain prepared-only ChannelData. Permission identity remains per canonical peer IP; channel-binding identity remains per canonical peer transport address. Inbound data is not made prepared-only.
- Shared review-loop and contract-closure baselines govern every slice. Each PR uses TDD-shaped focused evidence, finite evidence and review budgets, exact-head `task preflight`, a draft-to-ready transition only after local certification, successful same-head hosted CI, merge, `append-dev-journal`, and then pointer/task updates.
- Representation and artifact gates in each plan are ceilings as well as floors. Do not broaden supported protocol syntax, timing matrices, cleanup, or verification infrastructure during review. Diagnose failures before reruns and stop when representation ownership, accepted outcome, blast radius, or the one-PR boundary changes.
