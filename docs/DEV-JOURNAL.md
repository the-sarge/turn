# Development Journal

**Append-only. New entries go at the END of this file.**

Oldest entry first, most recent entry last.

---

## Fork-owned portfolio CI gate landed (M0 Slice 1) - 2026-08-14 23:01 EDT

**Main:** `f0a71a4f5065`
**Actor:** Claude Code (implement-architecture-slice)

**Summary**

M0 Slice 1 of the fork molding program merged: all ten inherited pion `.goassets` workflow configs (plus `renovate.json`) replaced by one fork-owned, agent-gated portfolio CI workflow (PR the-sarge/turn#8, squash-merged as `f0a71a4`, closing #4).

**Completed**

- `.github/workflows/ci.yml`: one-shot `ci-certify` labeled-event certification with live head/base/synthetic-merge binding, push-to-main smoke, weekly deep schedule, exact-head `workflow_dispatch` diagnostics, and a `ci_required` aggregation gate.
- Portable lanes in `Taskfile.yml` + `scripts/ci/`: check (gofmt/vet/test/lint), race, bounded proto fuzz (3 targets × 30s), dependency-gate (verify/tidy-diff/govulncheck), platform cross-builds, docs whitespace, workflow contract (actionlint + yq), gitleaks secret scan with per-finding `.gitleaksignore` fingerprints and a planted-credential policy self-test.
- Dependency automation established via `.github/dependabot.yml` (gomod + github-actions incl. the nested composite action); Renovate was never functional on this fork (zero Actions runs, no bot PRs; helper required an absent pion-org secret).
- Mechanical import-order normalization of 13 files the module rename left unsorted (import lines only; golangci-lint v2.10.1 now clean).
- Plan contract re-audited in place pre-implementation (#7, plan commit `a0b62fd`): Dependabot as the dependency-automation vehicle, import-order normalization admitted, blast radius corrected to include Taskfile/scripts.

**Decisions**

- Inherited job dispositions (apidiff/REUSE dropped, CodeQL kept as non-uploading smoke, native OS matrix → Linux + cross-builds, tidy-check → dependency-gate, fuzz rebounded): recorded in PR #8's characterization table.
- gosec not adopted (39 pre-existing findings would need semantic Go changes); CodeQL is the SAST smoke.

**Validation**

- Review loop: RAS initial review `20260815T021005-10f23f4384a0fe1aecc94f52` (8 clusters) → fixes → exact-head verify at `a945610` (all fix-now resolved) → replacement review `20260815T023553-97372185b064bcf1efbc6870` (no Fix First).
- Hosted certification green on the exact merged head: run 31860275798 at `a945610` — classify, docs, core (incl. race + workflow contract), proto fuzz, CodeQL, secret scan, `ci-required` all success. Observed-red demonstration: run 31858307330 (docs lane red → `ci-required` red) plus a fail-closed binding rejection (run 31858249222).
- Local: all lanes green except the two plan-documented macOS-local failures (`TestCreateTCPConnectionInvalid`, `TestConnectRequest`), both cut surface, both green on Linux CI.

**Next**

- Slice 2 (#5) is dispatchable: cut to the kept surface, pin the upstream test fixture, tag `v5.1.0-gs.1`. Live frontier: `docs/adr/2026-08-14-turn-molding-program.md` and tracking issue #6.
- Deferred follow-ups (to be filed): Go-floor exercise vs deliberate `go` directive bump; docs-prefix classification tightening + pin-refresh ownership; pion tooling residue cleanup (`.github/fetch-scripts.sh`, `install-hooks.sh`).
- Operator follow-up: add a ruleset requiring `ci-required` (no branch protection exists yet, so the gate is advisory until then).

---

## Kept UDP client surface landed (M0 Slice 2) - 2026-08-15 00:08 EDT

**Main:** `1ee874ea3df1`
**Actor:** Codex (implement-architecture-slice)

**Summary**

M0 Slice 2 completed the fork's cut-and-stabilize track: PR [#14](https://github.com/the-sarge/turn/pull/14) removed the TURN server, TCP allocation path, examples, and end-to-end harness; retained the owned UDP client surface; and merged as `1ee874e`.

**Completed**

- Root UDP regressions now use the pinned upstream fixture `github.com/pion/turn/v5@v5.0.12`; fork code no longer exports or retains the cut server/TCP surface.
- The retained deadline helper moved into `internal/client/udp_conn.go` after the scoped contract re-audit in [#13](https://github.com/the-sarge/turn/pull/13); the normative plan and child issue pointer were updated before implementation continued.
- Dependencies and README were reduced to the owned surface, and annotated tag `v5.1.0-gs.1` was published at merge commit `1ee874ea3df126b674c12fec8c17024851c843fd`.

**Decisions**

- The cut remains compiler-owned: deleted declarations have no compatibility shims, and tests that need a full TURN server import the pinned upstream module. The accepted boundary and helper-relocation exception are recorded in the [normative plan](docs/adr/2026-08-14-cut-and-stabilize-plan.md).

**Validation**

- RAS review `20260815T032334-6f6bc4c837bcbd58911e2f00` found two fix-now clusters; exact-head verification cleared them, and replacement review `20260815T034127-bb7990b3bf0df55c68ab9529` returned no Fix First or Follow Up findings.
- Exact-head local preflight passed at `7bd3582`; pull-request CI run [31862982109](https://github.com/the-sarge/turn/actions/runs/31862982109) and merged-main run [31863300995](https://github.com/the-sarge/turn/actions/runs/31863300995) passed.
- The tag resolves through the Go module proxy to `1ee874e`; the tag-triggered release lane is recorded in run [31863488687](https://github.com/the-sarge/turn/actions/runs/31863488687).

**Next**

- Track 1 is complete. No implementation slice is newly ready: Track 2 remains gated on wiremux Slice 1.3, and Track 3 remains gated on Track 2 plus production traffic profiles. The live view is [tracking issue #6](https://github.com/the-sarge/turn/issues/6).
