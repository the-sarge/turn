# TURN Architecture Deepening Program — 2026-08-17

**Status:** Accepted; all tracks implemented by PRs #49, #51, #53, and #55

## What this is

This program packages the post-M1 architecture review and delegated grilling of `the-sarge/turn`. The three current track plans are the normative source of truth for outcomes, boundaries, ownership, evidence, blockers, and stop conditions. GitHub issues and the OmniFocus mirror carry stable identities, current state, and pointers only. This program is separate from the existing TURN fork molding program's profile-gated packet-path Track 3 and does not open that performance track.

## Outcomes that require no implementation

- Do not create a generic authenticated TURN exchange module now. The repetition is real, but Allocate, Refresh, CreatePermission, and ChannelBind have distinct challenge, retry, success, and Allocation-lifecycle policies; a policy-flag/callback interface would be shallow. Only typed ChannelBind error consistency is accepted now; re-audit residual duplication after Tracks 1 and 2.
- Do not extract prepared-peer orchestration from `UDPConn`. The public `Allocation` module already hides permission sharing, channel binding, prepared-only admission, inbound lookup, refresh, seal, and worker join; a second internal owner would expose a wide interface and split lifecycle ownership. Revisit only if a second production consumer or a smaller evidence-backed seam appears.
- Preserve Client close as abort-current and idempotent; do not make Client permanently terminal or move Allocation release/join ownership into it without a new behavior decision.
- Keep root `Allocation`, canonical address handling, `turntest`, terminal `startClose`/`Close`, and `internal/proto` in their accepted shapes. Do not merge `internal/client` into root, absorb wiremux's composite, restore Send indications, trim protocol codecs, or optimize the packet path without the production profiles required by the existing molding program.
- Dead helpers are removed only inside a slice whose new owner makes them obsolete. Permission test helpers, broad responder consolidation, `errFake` relocation, and other cleanup not named in a slice are deferred rather than turned into a horizontal cleanup PR.
- Do not add adaptive Allocation refresh in this program. The shipped client omits Allocate LIFETIME, so its conforming path starts with RFC 8656's 600-second default and later requests that same grant. A varying-grant contract would require an explicit wire change or nonconforming-server support decision and was closed without code.
- No new domain term is required: Allocation, transaction, permission, channel binding, prepared peer, and terminal cause remain the program vocabulary established by the M1 plans and ADRs.

## Tracks, dependencies, and frontier

| # | Track | Plan | Parent issue | Blocked by | Slices | Status |
|---|---|---|---|---|---|---|
| 1 | Transaction registry deepening | [plan](2026-08-17-transaction-registry-plan.md) | #41 | None | 1 | COMPLETE via PR #49 |
| 2 | Allocation lifecycle deepening | [plan](2026-08-17-allocation-lifecycle-plan.md) | #42 | None | 1 | COMPLETE via PR #51 |
| 3 | TURN consistency and bounded state | [plan](2026-08-17-turn-consistency-plan.md) | #43 | None | 2 | COMPLETE via PRs #53 and #55 |

There are no genuine slice-level blocking edges. Track 1's registry supplies the abort-current capability consumed by the completed T2.S1 without changing its Allocation-side contract. Likely text conflicts between independently implemented tracks require rebasing, not artificial blockers.

Current frontier after PR #55: none. T1.S1, T2.S1, T3.S1, and T3.S2 are complete.

## Rules that bind every track

- Plan docs are normative. One audited slice maps to exactly one child issue, one intended PR, one fresh implementation context, and one OmniFocus slice task.
- Public API and emitted Allocate, Refresh, CreatePermission, ChannelBind, and ChannelData bytes/order remain unchanged. The accepted changes are transaction rollback/duplicate rejection/ownership, mandatory Allocation abort wiring with one lifecycle owner, typed ChannelBind error reachability, and explicit channel-number exhaustion.
- The caller owns `ClientConfig.Conn`; the fork never closes it, sets deadlines, or interrupts in-flight I/O. Late transaction writes remain accepted and must not re-arm after ownership loss.
- Writes remain prepared-only ChannelData. Permission identity remains per canonical peer IP; channel binding identity remains per canonical peer transport address. Send indications do not return.
- Allocation terminal state has one owner on `UDPConn`: worker-safe seal, exactly one lifetime-zero release, one terminal cause, and caller join. Invalid relayed addresses still construct and close an Allocation before rejection so the server resource is released.
- Shared review-loop and contract-closure baselines govern every slice. Each PR uses TDD-shaped focused evidence, at most one guard mutation per named owner, one fresh review plus at most one replacement, exact-head `task preflight`, a draft-to-ready transition only after local certification, a successful post-ready `ci-required` run on the exact live head, merge, `append-dev-journal`, then OmniFocus completion.
- Representation and artifact gates in each plan are ceilings as well as floors. Do not broaden supported protocol syntax, external-service behavior, timing matrices, cleanup, or verification aids during review. Diagnose failures before reruns and stop for a decision when a slice's owner, representation, outcome, or one-PR shape must change.
- The existing TURN fork molding program and its tracking issue remain authoritative for M0/M1/M2 status, versioning, release tags, and the profile-gated packet-path track. This architecture program adds no release tag or consumer adoption event.
