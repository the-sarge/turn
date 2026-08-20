# TURN Seam Deepening Program — 2026-08-19

**Status:** Accepted; Tracks 1, 2, and 3 implemented by PRs #96, #98, #100, #103, and #105; Track 4 pending

## What this is

This program packages the third architecture review of `the-sarge/turn` (2026-08-19, after the completed [08-17](2026-08-17-architecture-deepening-program.md) and [08-19](2026-08-19-architecture-deepening-program.md) deepening programs) and its self-grilled design. Seven candidates were grilled; six survive as four tracks and six slices, one is closed without code. The four track plans are the normative source of truth for outcomes, boundaries, ownership, evidence, blockers, and stop conditions. GitHub issues and the OmniFocus mirror carry stable identities, current state, and pointers only. This program reopens no settled ownership decision of the earlier programs.

Audit receipt: all six slices were independently slice-audited against `dad68d4868efc8b041114f7a4efdeaa3b229edfb` on 2026-08-19 before publication; the audit's accepted corrections are folded into the plans and are not repeated here.

## Outcomes that require no implementation

- Do not relocate the channel-binding attempt handle (`bindingAttempt`, `muBind`) either into `binding` or into a `UDPConn`-side structure. The 08-19 channel-binding readiness plan placed attempt coordination on `UDPConn` deliberately; no cost has been demonstrated; the relocation has no deletion-test signal. The asymmetry with permission (which owns its attempt in Track 3) is principled and recorded in the [permission owns its attempt ADR](2026-08-19-permission-owns-its-attempt.md).
- Do not move `REQUESTED-ADDRESS-FAMILY` before `MESSAGE-INTEGRITY` inside any slice. The authenticated Allocate's post-integrity placement is a wire fact; Track 4 pins current bytes, and [#83](https://github.com/the-sarge/turn/issues/83) owns the behaviour decision.
- Do not build a general scripted-responder harness, add queue-size/lock/timer/clock knobs to the test constructors, or introduce an options/builder layer for `UDPConn` (Track 1 ceilings). Do not pass the transaction registry into `UDPConn` in place of the two crossing closures.
- Do not add a second admission guard, export a nil-context error, make nil context panic, or keep the relayed address on `AllocationConfig` "for symmetry" (Track 2 ceilings).
- Do not share an attempt helper, handle, or join loop between permission and channel binding; attempt coalescing stays closed (Track 3 ceiling).
- Do not generalize the Allocate exchange into a shared authenticated-exchange module, and do not change address-family inference semantics (Track 4 ceilings).
- Observed and not proposed: 428 shipped lines in `internal/proto` with no shipped consumer, including `stun_conn.go`'s second copy of ChannelData framing (kept by the cut-and-stabilize plan: proto wholesale, fuzz targets, proto-only upstream watch); the channel-number range constants duplicated in `binding.go:16-20` and `proto/chann.go:56-57` with disagreeing comments; `ClientConfig`'s unexported test-only `bindingRefreshInterval`/`bindingCheckInterval`; `PeriodicTimer.IsRunning` with no production caller; the six minimal-conn `UDPConn{}` literals (capacity-one queue, completion-versus-seal, attempt-result ownership) retained by Track 1; `turntest` knobs unused by this repository's tests (possibly wiremux's; not touched).
- Domain terms: this program names **Attempt**, **Readiness**, and **Release** in `CONTEXT.md`; no other term changes.

## Tracks, dependencies, and frontier

| # | Track | Plan | Parent issue | Blocked by | Slices | Status |
|---|---|---|---|---|---|---|
| 1 | UDPConn construction and transaction crossing | [plan](2026-08-19-udpconn-construction-crossing-plan.md) | [#85](https://github.com/the-sarge/turn/issues/85) | None | 2 | Complete via PRs #96 and #98 |
| 2 | Allocate admission and public error vocabulary | [plan](2026-08-19-allocate-admission-errors-plan.md) | [#86](https://github.com/the-sarge/turn/issues/86) | None | 2 | Complete via PRs #100 and #103 |
| 3 | Permission readiness | [plan](2026-08-19-permission-readiness-plan.md) | [#87](https://github.com/the-sarge/turn/issues/87) | None | 1 | Complete via PR #105 |
| 4 | Allocate exchange | [plan](2026-08-19-allocate-exchange-plan.md) | pending | None | 1 | FRONTIER (T4.S1) |

There are no genuine blocking edges; T3.S1 has now landed after T1.S1, T1.S2, T2.S1, and T2.S2, leaving T4.S1 as the independently green frontier. Known file overlaps: T1 and T3 both touch `internal/client/prepare_test.go`; T1.S2 and T2.S1 both touch `AllocationConfig`; T4.S1 is root-only but shares `client.go` (`sendAllocateRequest`, `Allocate`) with T1.S2 and T2.S1. Text conflicts between independently implemented slices require rebasing, not artificial blockers. Recommended next slice: T4.S1.

## Rules that bind every track

- Plan docs are normative. One audited slice maps to exactly one child issue, one intended PR, one fresh implementation context, and one OmniFocus slice task.
- Public API and emitted Allocate, Refresh, CreatePermission, ChannelBind, and ChannelData bytes and setter order remain unchanged. Track 4 pins bytes; #83 owns any change. Error-text changes in Track 2 keep every error identity.
- The caller owns `ClientConfig.Conn`; the fork never closes it, sets deadlines, or interrupts in-flight I/O. Client remains bound to one canonical TURN server and its close remains abort-current and non-terminal.
- Root `Client` keeps server-source admission, TURN wire demultiplexing and decoding, Data-indication peer canonicalization, transaction completion, Allocate admission, and the one production assembler of `AllocationConfig`. `UDPConn` remains the sole Allocation lifecycle owner: seal, abort-before-release ordering, exactly one lifetime-zero release, terminal cause, worker registration, and caller join. `TransactionRegistry` owns transaction semantics. `binding` and `permission` own their readiness as their plans state.
- Writes remain prepared-only ChannelData. Permission identity remains per canonical peer IP; channel-binding identity remains per canonical peer transport address. Inbound data is not made prepared-only.
- Shared review-loop and contract-closure baselines govern every slice; this repository has no overlay. Each PR uses TDD-shaped focused evidence, finite evidence and review budgets, exact-head `task preflight`, a draft-to-ready transition only after local certification, successful same-head hosted CI, merge, `append-dev-journal`, and then pointer/task updates.
- Representation and artifact gates in each plan are ceilings as well as floors. Do not broaden test harnesses, protocol syntax, timing matrices, cleanup, or verification infrastructure during review. Diagnose failures before reruns and stop when representation ownership, accepted outcome, blast radius, or the one-PR boundary changes.
