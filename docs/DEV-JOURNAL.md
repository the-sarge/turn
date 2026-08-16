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

---

## CI floor and routing maintenance landed - 2026-08-15 01:09 EDT

**Main:** `8b95ebb13fb0`
**Actor:** Codex (planit)

**Summary**

PR [#16](https://github.com/the-sarge/turn/pull/16) closed CI follow-ups [#9](https://github.com/the-sarge/turn/issues/9), [#10](https://github.com/the-sarge/turn/issues/10), and [#11](https://github.com/the-sarge/turn/issues/11), merging the Go-floor lane, fail-closed documentation routing, pin-governance policy, and inherited Pion tooling cleanup as `8b95ebb`.

**Completed**

- Added a hosted `Go floor` job that derives the consumer minor version through the Go module parser, selects its latest patch, disables toolchain switching, runs `go test ./...`, and participates in `ci-required` for source-affecting, full, scheduled, and tag runs.
- Restricted documentation-only classification to `*.md`; unknown file types under `docs/` or `notes/` now route as source-affecting.
- Added a maintained finite routing table for `classify-changes.sh` and `require-results.sh` under `task workflow-check`, including negative aggregate-result cases.
- Documented repository-maintainer ownership, weekly review cadence, and pre-release review for all seven validation-tool pins.
- Removed the inactive mutable-ref Pion hook fetch/installer, gopher asset and license sidecar, and the obsolete `.goassets` ignore entry.

**Decisions**

- Kept `go 1.24.0` as the consumer floor because the current tree passes under Go 1.24; the validation pin may advance independently, while any future floor increase remains an explicit release decision. The accepted contract and alternatives are recorded in [PR #16](https://github.com/the-sarge/turn/pull/16).
- Chose documented manual pin ownership instead of adding `tools/go.mod`, avoiding a second module and tool-invocation refactor for this bounded maintenance batch.

**Validation**

- Local Go 1.24 tests and exact-head `task preflight` passed at `ca932013739fc889ef864be141226ac984cef8f3` against base `7fcd051a6b80a3ebfbfecf584e793a84fe634d7f`.
- RAS review `20260815T044607-bc15af9854aae3deea10123c` returned no Fix First or Follow Up findings.
- Hosted pull-request certification [31865895365](https://github.com/the-sarge/turn/actions/runs/31865895365) passed on the exact certified head, including the new floor job, routing contract, core/race, fuzz, CodeQL, secret scan, and `ci-required`.

**Next**

- No deferred finding from this batch survived for tracking. The repository's live program view remains [tracking issue #6](https://github.com/the-sarge/turn/issues/6).

---

## Track 2 Slice 1: consumer-crossing API cut - 2026-08-15 21:04 EDT

**Main:** `633781fc93e6`
**Actor:** Claude (implement-architecture-slice)

**Summary**

PR [#25](https://github.com/the-sarge/turn/pull/25) landed Track 2 (M1) Slice 1 of the modernize-kept-API plan as `633781f`, cutting the client-side public surface to exactly what wiremux crosses and making the server boundary `netip`-native. Child issue [#20](https://github.com/the-sarge/turn/issues/20) is closed; the frontier moved to Slice 2 ([#21](https://github.com/the-sarge/turn/issues/21)).

**Completed**

- Deleted STUN binding (`STUNServerAddr`, `SendBindingRequest`/`SendBindingRequestTo`, `STUNServerAddr()`, the STUN branch of `HandleInbound`, root `TestClientWithSTUN` — the nil-conn subtest survives as `TestNewClientRejectsNilConn`), public `CreatePermission`, getters `TURNServerAddr`/`Username`/`Realm`, config `Realm`/`Software`/`Net`/`RequestedAddressFamily` (with `addr_family.go`; socket inference stays), and the always-zero `evenPort`/`reservationToken` machinery.
- Replaced `TURNServerAddr string` with `Server netip.AddrPort`, validated at `NewClient` by the strict mode of one unexported canonicalizer (`canonical.go`); `pion/transport/v4` is no longer a direct dependency and has no non-test import (it remains `// indirect` via `pion/stun/v3`).
- `HandleInbound(data []byte, from net.Addr) error` now admits only datagrams whose canonical source is the configured `Server` (nil error and zero delivery otherwise); errors are returned only for malformed or unexpected protocol input from the server.
- Unexported the protocol internals `WriteTo`/`PerformTransaction`/`OnDeallocated` by replacing the `internal/client.Client` interface with func fields on `AllocationConfig`; exported `ErrAlreadyAllocated`.
- The PR's docs commit marks Slice 1 complete in the plan and program index and moves the frontier to Slice 2.

**Decisions**

- Zone rejection runs before `Unmap` in the canonicalizer so a zoned IPv4-mapped value cannot silently canonicalize (RAS review cluster C-003); the normative contract remains [Slice 1 of the M1 plan](adr/2026-08-15-modernize-kept-api-plan.md).

**Validation**

- Red-first canonicalizer table tests (both modes) and `TestHandleInboundAdmitsOnlyServer`; guard mutation on the server-source comparison observed the ignore test fail; negative compile checks for every removed symbol.
- `task verify` and exact-head `task preflight` passed at `389356365f3227953326f90126bee97077ec25a1` against base `4d779e51dbdc04d5a77bf53aac23b405d588cee7`.
- RAS review `20260815T201904-9ec0634b372be667f254b48e` (one Fix First, fixed) and exact-head verification at `3893563` with a clear blocking projection; hosted pull-request certification [31918388633](https://github.com/the-sarge/turn/actions/runs/31918388633) passed on the certified head including `ci-required`.

**Next**

- Slice 2 ([#21](https://github.com/the-sarge/turn/issues/21), exported `Allocation` with a `netip` data plane, deadline surface deleted) is the dispatchable frontier; Slices 3 and 4 follow it in parallel, then Slice 5 tags `v5.2.0-gs.1`. Live program view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).
- Deferred from review (no new issue: already owned by the plan): retained `Listen()` exits its loop on the new `errUnexpectedServerDatagram`; `Listen` is contract-frozen this slice and deleted by Slice 3 (validated against merged `633781f`, `client.go:214-218`).
