# TURN Fork Molding Program — 2026-08-14

**Status:** Accepted; Track 1 complete (`v5.1.0-gs.1`); Track 2 in progress (Slices 1-3 complete, PRs #25, #27, and #29; Slice 4 on the frontier); Track 3 gated, plan pending

## What this is

The program that molds `the-sarge/turn` into wiremux's minimal owned TURN client library. Plan docs in `docs/adr/` are the normative source of truth; issues, task-manager mirrors, and this index carry pointers and current state only. Grill and pivot history: [scope doc](2026-08-14-molding-program-scope.md), [owned-library ADR](2026-08-14-owned-library-fork.md), wiremux fork-pivot amendment (GridSwarm/wiremux#1161); Track 2 adoption-experience census is summarized in the [M1 plan](2026-08-15-modernize-kept-api-plan.md) Current Shape.

## Outcomes that require no implementation

- Fork identity settled: owned library, upstream read-only ([ADR](2026-08-14-owned-library-fork.md)).
- Security posture settled: proto-only upstream watch; dependency CVEs via normal updates (scope doc D2).
- Versioning settled: `v5.N.0-gs.1` minors, permanent `-gs` suffix (scope doc D3).
- Pre-existing macOS test failures closed with no fix: deleted with cut surface (scope doc D5).
- Upstream PR pion/turn#585 left open passively; its outcome gates nothing.
- Wiremux's composite is not absorbed into the fork: the socket-ownership split certified in wiremux Slice 1.3 stands; M1 thins the composite's compensations without moving the seam ([M1 plan](2026-08-15-modernize-kept-api-plan.md) rejected alternatives).
- Prepared-only writes and the fork-owned fixture are settled by ADR ([prepared-only writes](2026-08-15-prepared-only-writes.md), [fork-owned fixture](2026-08-15-fork-owned-test-fixture.md)).

## Tracks, dependencies, and frontier

| # | Track | Plan | Parent issue | Blocked by | Slices | Status |
|---|---|---|---|---|---|---|
| 1 | Cut and stabilize (M0) | [plan](2026-08-14-cut-and-stabilize-plan.md) | the-sarge/turn#3 | None | 2 | Complete: PR #8, PR #14; `v5.1.0-gs.1` published |
| 2 | Modernize the kept API (M1) | [plan](2026-08-15-modernize-kept-api-plan.md) | the-sarge/turn#19 | Track 1 (complete); wiremux Slice 1.3 (complete, GridSwarm/wiremux#1162) | 5 | FRONTIER — Slices 1 (PR #25), 2 (PR #27), and 3 (PR #29) complete; Slice 4 dispatchable; 5 after 4 |
| 3 | Optimize the packet path (M2) | plan pending | pending | Track 2; profiles from production wiremux traffic | TBD | GATED — requires a future `$architecture-handoff` run once its gate opens; no speculative optimization |

Cross-track slice edges: the transitional upstream-fixture seam introduced by Track 1 Slice 2 is removed by Track 2 Slice 5. Track 2 order: 1→2→{3‖4}→5 (1→2 and 2→{3,4} are sequencing edges on the same functions; 3 and 4 are parallel-safe after 2; {3,4}→5 is genuine; each slice independently green). Track 2 Slice 5 tags `v5.2.0-gs.1` and files the wiremux adoption issue; wiremux-side adoption is consumer work outside this program. Recommended starter: Track 2 Slice 1.

## Rules that bind every track

- No behavior change to kept code except through an accepted track plan; kept-surface preservation gates per plan.
- Single-owner and authority-complete invariants, transitional-seam budgets, artifact classification, and evidence budgets per the shared contract-closure baseline (no repository overlay exists).
- Shared review-loop baseline governs every PR; post-merge ritual per slice: review loop → merge → append-dev-journal → complete the OmniFocus slice task.
- Versioning and tag hygiene: never mutate published `-gs` tags; new capability ships as the next `v5.N.0-gs.1`.
- The fork never closes, deadlines, or interrupts the caller-owned `ClientConfig.Conn` (wiremux socket-ownership contract).
- Vocabulary: `notes/grill-allocation-lifecycle-CONTEXT.md`, `notes/grill-request-processing-CONTEXT.md`.
