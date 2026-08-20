# TURN Fork Molding Program — 2026-08-14

**Status:** Accepted; Track 1 complete (`v5.1.0-gs.1`); Track 2 complete (Slices 1-5 merged, PRs #25, #27, #29, #32, and #35; `v5.2.0-gs.1` published, wiremux adopted `v5.2.1-gs.1` via GridSwarm/wiremux#1181); Track 3 gated, plan pending. Versioning amended 2026-08-20: `v5.3.0-gs.1` is the last `-gs` tag ([plain-semver tags ADR](2026-08-20-plain-semver-tags.md))

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
| 2 | Modernize the kept API (M1) | [plan](2026-08-15-modernize-kept-api-plan.md) | the-sarge/turn#19 | Track 1 (complete); wiremux Slice 1.3 (complete, GridSwarm/wiremux#1162) | 5 | Complete — Slices 1-5 merged (PRs #25, #27, #29, #32, #35); `v5.2.0-gs.1` then `v5.2.1-gs.1` published; wiremux adoption merged (GridSwarm/wiremux#1181) |
| 3 | Optimize the packet path (M2) | plan pending | pending | Track 2; profiles from production wiremux traffic | TBD | GATED — requires a future `$architecture-handoff` run once its gate opens; no speculative optimization |

Cross-track slice edges: the transitional upstream-fixture seam introduced by Track 1 Slice 2 was removed by Track 2 Slice 5 (PR #35). Track 2 order was 1→2→{3‖4}→5; every slice merged independently green. Track 2 Slice 5 tags `v5.2.0-gs.1` and files the wiremux adoption issue post-merge; wiremux-side adoption is consumer work outside this program. No track has a dispatchable slice: Track 3 remains gated on its plan and production traffic profiles.

## Rules that bind every track

- No behavior change to kept code except through an accepted track plan; kept-surface preservation gates per plan.
- Single-owner and authority-complete invariants, transitional-seam budgets, artifact classification, and evidence budgets per the shared contract-closure baseline (no repository overlay exists).
- Shared review-loop baseline governs every PR; post-merge ritual per slice: review loop → merge → append-dev-journal → complete the OmniFocus slice task.
- Versioning and tag hygiene: never mutate published tags (`-gs` or plain); new capability ships as the next `v5.N.0` and fix-only releases as `v5.N.M` — the `-gs` suffix ended with `v5.3.0-gs.1` per the [plain-semver tags ADR](2026-08-20-plain-semver-tags.md).
- The fork never closes, deadlines, or interrupts the caller-owned `ClientConfig.Conn` (wiremux socket-ownership contract).
- Vocabulary: `notes/grill-allocation-lifecycle-CONTEXT.md`, `notes/grill-request-processing-CONTEXT.md`.
