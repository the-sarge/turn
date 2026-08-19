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

---

## M1 Slice 2: exported Allocation, netip data plane - 2026-08-15 23:06 EDT

**Main:** `7ef5cd05e5d4`
**Actor:** Claude (implement-architecture-slice)

**Summary**

PR [#27](https://github.com/the-sarge/turn/pull/27) landed Track 2 (M1) Slice 2 of the modernize-kept-API plan as `7ef5cd0`, introducing the exported `Allocation` with a `netip` data plane and deleting the dishonest deadline surface. Child issue [#21](https://github.com/the-sarge/turn/issues/21) is closed; the frontier moved to Slices 3 ([#22](https://github.com/the-sarge/turn/issues/22)) and 4 ([#23](https://github.com/the-sarge/turn/issues/23)), which are parallel-safe.

**Completed**

- Exported `Allocation` (root package, thin explicit delegation over `internal/client.UDPConn`) returned by `Allocate() (*Allocation, error)`: `RelayedAddr() netip.AddrPort`, `PreparePeer(ctx, netip.AddrPort) error`, `ReadFrom` returning canonical `netip.AddrPort` labels, `WriteTo(payload, netip.AddrPort)`, `Close`. `Client.PrepareUDPPeer` and `errUDPAllocationNotFound` are deleted; `ErrInvalidPeer` and `ErrInvalidRelayedAddress` are exported.
- Deadline surface deleted: `readTimer`, `SetDeadline`/`SetReadDeadline`/`SetWriteDeadline`, and root `TestClientReadTimout`.
- Peer canonicalization now runs through the unmapping mode of Slice 1's canonicalizer at four seams (`PreparePeer`, `WriteTo`, inbound label creation, `Allocate` relayed validation); inbound peer labels are stored as canonical `netip.AddrPort` at creation so `ReadFrom` returns them without per-packet conversion; zoned peers reject with `ErrInvalidPeer` (the old zoned-alias stripping in `WriteTo` is deleted by design).
- An invalid server-reported relayed address (zero port, unspecified, multicast) is released with a lifetime-0 Refresh and rejected with `ErrInvalidRelayedAddress`; the canonical relayed address is validated once at `Allocate` and is authoritative for `RelayedAddr()`.
- Permission map keyed per peer IP (`netip.Addr`), preserving the prior IP-string fingerprint semantics; `internal/ipnet` deleted as fully unreferenced.
- The PR's docs commit marks Slice 2 complete in the plan and program index and moves the frontier to Slices 3 and 4.

**Decisions**

- Root tests' scripted TURN servers moved from hardcoded `0.0.0.0:3478` to dynamic `127.0.0.1:0` ports after local certification collided with an unrelated local process holding UDP 3478; assertions are unchanged. The normative contract remains [Slice 2 of the M1 plan](adr/2026-08-15-modernize-kept-api-plan.md).

**Validation**

- Red-first invalid-peer table and mapped-alias positive anchor at the root canonicalization owner; scripted invalid-relayed-address test proving `ErrInvalidRelayedAddress`, one lifetime-0 Refresh emission, and the cleared allocation pointer; guard mutation on the relayed-address validity check observed that test fail on all three assertions; reflection negative check that the removed surface does not resolve.
- `task verify` and exact-head `task preflight` passed at `f5898114906b35e72ff401679412636d1046d25c` against base `22af5e16481c3cc7f01fb28d91e0f8dede0331af`.
- RAS review `20260816T022140-f7f6355664c8098be7ab0f49` (one Fix First, docs-only, fixed in `409a66e`; the RAS verify rerun was skipped under the shared docs-only policy) and replacement review `20260816T024544-d2e19ff94fb82c8c9849739f` (clean: no Fix First, no Follow Up); hosted pull-request certification [31923068675](https://github.com/the-sarge/turn/actions/runs/31923068675) passed on the certified head including `ci-required`.

**Next**

- Slices 3 ([#22](https://github.com/the-sarge/turn/issues/22)) and 4 ([#23](https://github.com/the-sarge/turn/issues/23)) are the dispatchable frontier (parallel-safe); Slice 5 ([#24](https://github.com/the-sarge/turn/issues/24)) follows both and tags `v5.2.0-gs.1`. Live program view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).
- Deferred from review (no new issue: owned by Slice 3's failures-as-values rework of `internal/client/errors.go`): the `timeoutError`/`newTimeoutError` helper survives with only a test caller (validated against merged `7ef5cd0`, `internal/client/errors.go:28-45`, caller `internal/client/udp_conn_test.go:411`).

---

## Track 2 Slice 3: context-first allocation and failures as values - 2026-08-16 00:50 EDT

**Main:** `0d61cfe818df`
**Actor:** Claude (implement-architecture-slice)

**Summary**

PR [#29](https://github.com/the-sarge/turn/pull/29) landed Track 2 (M1) Slice 3 of the modernize-kept-API plan as `0d61cfe`: context-first allocation (`Allocate(ctx)`), one lifecycle idiom (`Listen` deleted, `pion/logging` removed), and failures as values (refresh failures terminalize with a recorded cause; `net.ErrClosed` idiom; exported sentinels). Child issue [#22](https://github.com/the-sarge/turn/issues/22) is closed; the frontier is Slice 4 ([#23](https://github.com/the-sarge/turn/issues/23)), with Slice 5 ([#24](https://github.com/the-sarge/turn/issues/24)) after it.

**Completed**

- `Allocate(ctx context.Context) (*Allocation, error)`: the transaction result channel is buffered (capacity 1) so a producer never blocks on a departed waiter; map membership under `mutexTrMap` is the single linearization point; a published response wins over cancellation; a closed channel returns the closed error wrapping `net.ErrClosed` (closure precedence over cancellation); `onRtxTimeout` performs the retransmit socket write outside the lock and re-checks ownership before publishing or re-arming, so cancellation never waits behind caller-socket I/O. Zero deadline/close calls on the caller's socket, pinned by an observer wrapper.
- `Listen()`, `listenTryLock`, and `errAlreadyListening` deleted; the fork's tests use an unexported `startTestPump` helper; negative reflection checks pin that `Client.Listen` and `ClientConfig.LoggerFactory` no longer resolve.
- `pion/logging` removed per the slice's per-site ledger (36 non-test sites at branch base, all dispositioned, none silently kept); `go.mod` keeps it only `// indirect` via `pion/stun/v3`.
- Permanent allocation-refresh failure terminalizes the allocation through `startClose` (its third caller, after caller close and ChannelBind-400) with cause `ErrAllocationRefreshFailed`: fixes `refreshAllocation`'s error-response branch that returned nil for every well-formed non-438 error (now a typed `*stun.TurnError`); the terminal cause is recorded in `startClose`'s guarded arm; a failed lifetime-0 emission is joined into it; post-seal `ReadFrom`/`WriteTo`/`PreparePeer` return `net.ErrClosed` wrapped with the cause; `Allocation.Close` always joins, then returns the emission result, the recorded terminal cause, or `net.ErrClosed` on repeated calls, with concurrent caller Closes linearized in one `closeMutex` hold.
- `errClosed`/`errAlreadyClosed` replaced by `net.ErrClosed`; error wraps converted to `%w`; exported sentinels `ErrClosed`, `ErrTransactionTimeout`, `ErrAllocationRefreshFailed`, `ErrPermissionRefreshFailed`, `ErrChannelBindFailed`, `ErrChannelBindingExpired`.
- The PR's docs commit marks Slice 3 complete in the plan and program index and moves the frontier to Slice 4.

**Validation**

- Red-first per the slice evidence budget: cancellation table test (cancel-before-send, both waits, published-success-wins), blocked-retransmit observer test, producer-race test, cancel-versus-`Client.Close` precedence test, late-success discard test with the documented same-`Conn` 437 retry consequence, refresh-failure table test (timeout / non-438 `*stun.TurnError` / stale-nonce exhaustion), seal-versus-`Close` race test (exactly one lifetime-0 emission), self-seal emission-failure join test, concurrent-caller-`Close` test.
- Guard mutations, one per enforcement owner, applied and reverted: removing the `ctx.Done()` select arm failed the cancellation test; removing the refresh-failure `startClose` call failed the terminalization test.
- Exact-head `task preflight` passed at `94bc8e9eabe511f12fdb13f9d3d0012165c3a56c` against base `d3d5c6606bb8d6b5f3d81d500c96f91f7511a133`.
- RAS review `20260816T035925-e190c1dfbb3d0770fba2903b` (two Fix First: concurrent-Close linearization, `ErrTransactionTimeout` evidence; both fixed in `94bc8e9`), RAS verify at the exact head (blocking projection clear, 6/6 prior clusters covered), replacement review `20260816T043018-22c0bd0a50663cbc25b6cc45` (clean); hosted pull-request certification [31927241117](https://github.com/the-sarge/turn/actions/runs/31927241117) passed on the certified head including `ci-required`.

**Next**

- Slice 4 ([#23](https://github.com/the-sarge/turn/issues/23)) is the dispatchable frontier; Slice 5 ([#24](https://github.com/the-sarge/turn/issues/24)) follows it and tags `v5.2.0-gs.1`. Live program view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).
- Deferred from review into [#30](https://github.com/the-sarge/turn/issues/30) (validated against merged `0d61cfe`): whether operations already in flight at seal time must carry the recorded terminal cause (`internal/client/udp_conn.go:256`, `:225-229`, `:727`), plus the inherited `timeoutError` test-only helper hygiene (`internal/client/errors.go:50-66`). Two marginal review observations were dispositioned without follow-ups: the scheduler-driven (not barrier-forced) concurrent-Close regression test, and the pre-existing mapped-transaction residue after a failed ignore-result initial write.

---

## Track 2 Slice 4: prepared-only writes - 2026-08-17 15:14 EDT

**Main:** `05c00489b0eb`
**Actor:** Claude (implement-architecture-slice)

**Summary**

PR [#32](https://github.com/the-sarge/turn/pull/32) landed Track 2 (M1) Slice 4 of the modernize-kept-API plan as `05c0048`: prepared-only writes. `Allocation.WriteTo` now requires a prepared, confirmed channel binding and either sends ChannelData or fails with zero network output; the Send-indication write path is deleted rather than guarded, so the lifetime ChannelData-only invariant is structural — no non-test code in the root package or `internal/client` can build a Send indication. Child issue [#23](https://github.com/the-sarge/turn/issues/23) is closed; the frontier is Slice 5 ([#24](https://github.com/the-sarge/turn/issues/24)), whose blockers (Slices 3 and 4) are both complete.

**Completed**

- `WriteTo` (`internal/client/udp_conn.go`) is the single guard: closed → `net.ErrClosed` (wrapped with the terminal cause when self-sealed); no prepared binding for the canonical peer → new exported `ErrNotPrepared` (root and `internal/client`), zero bytes, zero output; prepared but expired/failed/permission-refresh-failed → the recorded cause, zero output; otherwise ChannelData over the confirmed binding. Deleted: the Send-indication build, on-write permission creation, and the write-path `maybeBind`. `maybeBind` stays for the `checkBindingsTimer` refresh path; permissions and bindings are created only by `PreparePeer`, so refresh membership is unchanged.
- The `*net.TCPAddr` branch of `addr2PeerAddress` named by the plan had already been removed by Slice 2 (`peerAddress(netip.AddrPort)`); nothing to delete.
- Root `TestClientNonceExpiration` and `TestClientE2E` prepare the peer before the first write (the ChannelData-path preservation gate against the upstream fixture); `TestClientE2E`'s `disableChannelBind` variant and `channelBindFilterConn` are retired, as is `TestUDPConn` "WriteTo() returns real payload length" (an indication assertion); `TestUDPConn` "WriteTo()" marks its binding prepared.
- The PR's docs commit marks Slice 4 complete in the plan and program index and moves the frontier to Slice 5.

**Validation**

- Red-first per the slice evidence budget: `TestWriteToPreparedOnly` four-state table (unprepared / prepared-and-ready / prepared-then-terminal / closed) over the mock harness with write, permission, and bind counts — red on the unprepared row before the change (a Send indication, an on-write permission, and a bind were observed), green after; `TestPreparePeer` anchors "readiness success then ChannelData writes" and "permission refresh failure fails writes, never Send indication" unchanged; adapted root scenarios green against `pion/turn/v5@v5.0.12` before the deletion.
- One guard mutation applied and reverted: replacing the prepared check with binding creation failed only the unprepared row.
- Negative grep: no non-test file in the root package or `internal/client` references `stun.MethodSend` or `proto.SendIndication` (`internal/proto` keeps its helper under the no-trimming rule). `task check`, `task race`, `task docs-check` green; `go mod tidy` clean.
- Exact-head `task preflight` passed at `9d69147057e015ae113eed5ce594081115f46b79` against base `6f47379dfceeda13353a1c4ef209b60421378c6f`.
- RAS review `20260817T185346-76e5949a8c74a44d16df6fe6` (initial; zero Fix First, zero Follow Up, three low Do-Not-Act-On clusters). Dispositions: C-002 `fix-now` docs-only (the adapted nonce test's comment still said the write creates the permission; corrected in `9d69147`, RAS rerun skipped by the docs-only policy); C-001 and C-003 `defer`. Hosted pull-request certification [32058514479](https://github.com/the-sarge/turn/actions/runs/32058514479) passed on the certified head including `ci-required`; squash-merged pinned to that head.

**Next**

- Slice 5 ([#24](https://github.com/the-sarge/turn/issues/24)) is the dispatchable frontier: fork-owned `turntest` fixture, upstream `pion/turn/v5` dropped, README refreshed, tag `v5.2.0-gs.1`, wiremux adoption issue filed. Live program view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).
- Deferred from review into [#33](https://github.com/the-sarge/turn/issues/33) (validated against merged `05c0048`): the expired-binding branch of `WriteTo` (`internal/client/udp_conn.go:417-418`) and `bindingResult` (`:325-328`) has no test referencing `ErrChannelBindingExpired`; pre-existing, retained unchanged, one table row would cover it. Dispositioned without a follow-up: `permissionMap.find`/`insert` and `bindingManager.create` now have only test callers (focused-test arrangement helpers; hygiene, not behavior).

---

## M1 complete: turntest fixture, upstream removed, v5.2.0-gs.1 - 2026-08-17 16:22 EDT

**Main:** `a65f236b1fc3`
**Actor:** Claude (implement-architecture-slice)

**Summary**

Track 2 (M1) Slice 5 merged and the milestone closed: the fork now owns its integration fixture. PR #35 (squash `a65f236`) added the exported `turntest` package — a scripted in-process TURN responder covering exactly the request subset this client emits, with the knobs both this repository's and wiremux's tests need — re-pointed the root integration tests to it, and removed `github.com/pion/turn/v5` from the module graph entirely, retiring Track 1's documented transitional test-fixture seam. Annotated tag `v5.2.0-gs.1` was published on the merged head after green default-branch CI, closing M1 (#19).

**Completed**

- `turntest`: `New(Options)`/`Start(testing.TB, Options)`, `Addr()`, `AllocationCount()`, `InjectStaleNonce()`, `Close()` joining every server-owned goroutine; Allocate (437 on same-five-tuple re-Allocate), Refresh (lifetime-0 delete; one 438 after stale-nonce injection), CreatePermission (403 under `DenyPermissions`), ChannelBind (400 under `RejectChannelBind`), ChannelData both directions, Data indication for permitted-but-unbound peers; knobs `RelayIPOverride`, `AllocationLifetime`, `PermissionTimeout`/`ChannelBindTimeout`, `IPv6`. STUN framing/integrity parsed by `pion/stun`, TURN attributes by `internal/proto`; no hand parsing, example-level guarantee per the fixture ADR.
- Root `TestClientNonceExpiration` and `TestClientE2E` re-pointed first as characterization tests, then the upstream dependency deleted; `pion/logging` and `pion/transport/v4` remain `// indirect` via `pion/stun/v3` only. README rewritten for the M1 API. Plan and program index carry the completed frontier in the same PR.
- Post-merge ritual: tag `v5.2.0-gs.1` at `a65f236` (resolves via `go get`; `v5.0.13-gs.1`/`v5.1.0-gs.1` untouched); wiremux adoption issue filed (GridSwarm/wiremux#1180) and recorded in tracker #6; parent #19 closed.

**Validation**

Review loop per the shared baseline: one RAS review (run `20260817T194942-8b68fd404f053c8e2f92383e`); two docs-only `fix-now` findings fixed in `c5c69ba`, the RAS verify/replacement cycle skipped under the low/nit docs-only policy; dispositions recorded on PR #35. Full suite green under `-race`; acceptance import scans clean; `go mod tidy` clean; `task verify` and exact-head `task preflight` green at `c5c69ba`; hosted CI bound the certified head via the one-shot `ci-certify` label with `ci-required=SUCCESS` before merge; main CI green at `a65f236` before tagging.

**Next**

- Wiremux-side adoption of `v5.2.0-gs.1` (GridSwarm/wiremux#1180) — consumer work outside this program.
- Review follow-ups live in tracker #6: #30, #33, and new #36 (turntest regression tests for the 437 and Data-indication paths).
- Track 3 (M2, packet path) remains gated on its plan and production wiremux traffic profiles; program index is the live view.

---

## M1 review follow-ups batch closed: #30/#33/#36 - 2026-08-17 17:22 EDT

**Main:** `ad9e83ae1ac5`
**Actor:** Claude (planit)

**Summary**

The three open Track 2 (M1) review follow-ups closed in one batch: PR #38 (squash `ad9e83a`) resolved [#30](https://github.com/the-sarge/turn/issues/30) (seal precedence for in-flight operations, operator decision: adopt), [#33](https://github.com/the-sarge/turn/issues/33) (expired-binding branch of `WriteTo`, test-only), and [#36](https://github.com/the-sarge/turn/issues/36) (turntest regression tests for 437 re-Allocate and Data indication, test-only). The #30 error-wrapping fix is the batch's only shipped-behavior change. With this, every Track 2 review follow-up is resolved; only consumer adoption (GridSwarm/wiremux#1180) remains, outside this program.

**Completed**

- Seal precedence (#30): the closing permission worker (`ensurePermissionAttempt`) and `bindChannel`'s closing check now route through `closedErr()`, so an operation already in flight when the allocation terminalizes records `net.ErrClosed` wrapped with the recorded terminal cause instead of bare `net.ErrClosed`. `errors.Is(err, net.ErrClosed)` holds on every path before and after. Hygiene rider from the same issue: `timeoutError`/`newTimeoutError` moved from `internal/client/errors.go` into `udp_conn_test.go`, its only caller's file.
- Expiry coverage (#33): `TestWriteToPreparedOnly` gained the prepared-then-binding-expired row (back-dated `refreshedAt`, `ErrChannelBindingExpired`, zero bytes, zero datagrams) and `TestPreparePeer` the `bindingResult` re-entry case.
- Fixture regressions (#36): `TestSecondAllocateMismatch` (second `turn.Client` sharing the socket via a dual-fed read pump; typed `*stun.TurnError` with `stun.CodeAllocMismatch`) and `TestDataIndicationAfterBindingExpiry` (200ms `ChannelBindTimeout` waited out under a live permission; peer traffic reaches `Allocation.ReadFrom` via the Data-indication arm). No growth of the fixture's request subset per the fixture ADR.

**Validation**

- Red-first for #30: a stale-nonce harness knob holds a permission attempt in its retry loop across a `startClose` self-seal; the recorded attempt error asserted to carry both `net.ErrClosed` and the cause — red on bare `net.ErrClosed` before the fix, green after.
- Two guard mutations applied and reverted: deleting `WriteTo`'s expiry branch failed only the new #33 row; disabling turntest's `case permitted:` arm failed only the Data-indication test.
- `task verify` green (0 lint issues); `go test -race` green on `internal/client` and `turntest`; exact-head `task preflight` passed at `708e2ef` against base `3f77aa8`.
- RAS review `20260817T205444-b5e2d9201e08aa77a2a7e852` (initial; briefed with the three issue bodies verbatim plus ceilings): zero scoped clusters. The claude-opus reviewer failed structural validation; its two rejected records were recovered from the raw artifact and independently dispositioned `defer` (both low, verification-aid hygiene — see Next). No fix-now findings, so no replacement review. Hosted pull-request certification [32070054208](https://github.com/the-sarge/turn/actions/runs/32070054208) passed on the certified head including `ci-required`; squash-merged pinned to that head.

**Next**

- Program-external only: wiremux adoption of `v5.2.0-gs.1` (GridSwarm/wiremux#1180). Track 3 (M2) remains gated. Live program view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).
- Dispositioned without a follow-up (revalidated at merged `ad9e83a`; hygiene of verification aids, not behavior): `harness.permGate` is written after the connection's timer goroutines start (`internal/client/prepare_test.go:365`, same pattern as the pre-existing `:249`) — a formal data race kept latent by the 120s default permission-refresh interval; and the Data-indication test's reader goroutine swallows the `ReadFrom` error (`turntest/turntest_test.go:247`), so an allocation-side read failure would misreport as the generic timeout.

---

## Track 1 Slice 1: transaction registry terminal ownership - 2026-08-17 21:10 EDT

**Main:** `209bcb6e7a5a`
**Actor:** Codex

**Summary**

PR [#49](https://github.com/the-sarge/turn/pull/49) landed Track 1 Slice 1 of the transaction-registry architecture plan as `209bcb6`: `internal/client.TransactionRegistry` is now the sole owner of live transaction membership, retry scheduling, terminal claims, result publication, abort, and retirement. Client and allocation code retain socket and policy ownership and interact with the registry only through behavior-shaped methods. Child issue [#44](https://github.com/the-sarge/turn/issues/44) is closed; the remaining independently ready frontier is [#45](https://github.com/the-sarge/turn/issues/45), [#46](https://github.com/the-sarge/turn/issues/46), and [#47](https://github.com/the-sarge/turn/issues/47).

**Completed**

- Replaced the exposed transaction map, raw insert/find/delete operations, caller-managed timers, and caller-owned result channels with a private registry injected with only a send capability.
- Preserved the wire contract of one initial write plus six byte-identical retries, including ordering, backoff constants, retry cap, and caller address ownership; copied transaction bytes at registration so later caller mutation cannot alter retransmissions.
- Centralized every terminal claimant under remove-first ownership: response, initial-send failure, retry failure, retry exhaustion, context cancellation, and nonterminal abort-current snapshots. Initial and retry writes remain outside the registry lock, with ownership rechecked before timer arming or result publication.
- Added race-focused coverage for initial-send and retry interleavings, duplicate IDs, cancellation, abort snapshots, exact retry bytes and count, waited and fire-and-forget retirement, late responses, and root `Client.Close` behavior. The plan and program index record the completed slice and unchanged frontier.

**Validation**

- Red-first tests established each new registry behavior before implementation; focused tests, `go test ./...`, `go test -race ./...`, `task verify`, and the exact-head `task preflight` all passed. The preflight certified head `918abca228612d679651c6fcbd5da52dacc8031b` against base `0042dbe7adc42ac92b451073d2b41f68fa4c75f0`.
- RAS review `20260818T004756-8e2f217ed321464e033611e7` produced zero Fix First and zero follow-up findings. Three observations were independently rejected as unsupported caller mutation, non-contractual error text, and a pre-existing retry comment; dispositions are recorded on PR #49.
- Hosted pull-request certification [32086750083](https://github.com/the-sarge/turn/actions/runs/32086750083) passed on the reviewed head, including core, race, docs, Go floor, secret scan, CodeQL, proto fuzz smoke, and `ci-required`; PR #49 was squash-merged while pinned to that head.

**Next**

- Tracks 2 and 3 remain independently dispatchable: Allocation lifecycle ownership ([#45](https://github.com/the-sarge/turn/issues/45)), typed ChannelBind errors ([#46](https://github.com/the-sarge/turn/issues/46)), and bounded channel-number allocation ([#47](https://github.com/the-sarge/turn/issues/47)). Live program view: [tracking issue #48](https://github.com/the-sarge/turn/issues/48).
- No review follow-up issue was required and no untraced contract effect remains from this slice.

---

## Track 2 Slice 1: Allocation lifecycle ownership - 2026-08-17 22:04 EDT

**Main:** `2868aa537fb0`
**Actor:** Codex

**Summary**

PR [#51](https://github.com/the-sarge/turn/pull/51) landed Track 2 Slice 1 of the allocation-lifecycle architecture plan as `2868aa5`: `internal/client.UDPConn` is now the sole owner of Allocation lifecycle state and terminalization. The retired `allocation` and `clientHooks` wrappers are gone, the transaction-abort capability is mandatory at construction, and close preserves the required `abort -> deallocated -> release` order. Child issue [#45](https://github.com/the-sarge/turn/issues/45) is closed; the remaining independently ready frontier is [#46](https://github.com/the-sarge/turn/issues/46) and [#47](https://github.com/the-sarge/turn/issues/47).

**Completed**

- Moved allocation lifecycle state and maintenance behavior directly onto `UDPConn`, including Refresh, permission refresh, ChannelBind refresh, allocation expiry, failure publication, and one-shot close ownership.
- Made transaction abort an explicit required constructor capability and removed the no-abort close path; terminalization now aborts active transactions before publishing deallocation and releasing the socket.
- Preserved fixed refresh cadence and exact request bytes while adding coverage for ordinary and stale-nonce Refresh, CreatePermission and ChannelBind request shape/order, normalized bytes, permission preservation, terminal races, and close ordering.
- Updated the allocation plan and architecture program index in the product PR so Track 2 is complete and the live frontier points only to the two Track 3 slices.

**Validation**

- Red-first constructor coverage demonstrated that missing transaction abort was previously accepted; the new positional constructor capability makes omission a compile-time error and rejects a nil adapter before timers or workers start.
- Focused lifecycle tests, `go test ./...`, `go test -race ./...`, and `task verify` passed. Exact-head `task preflight` certified `bed672eeb7150f6a8a3b67c5b8da974fef983500` against base `ff01dd66d6dbb2703602e05d8eebc1a1283ebef2`.
- RAS review `20260818T014024-72e999465c65bac02bd2ce1e` produced zero Fix First and zero follow-up findings. Five low observations were independently rejected as policy outside the slice contract, dispatch-era wording, adjacent comment polish, unreachable narrow test-fixture state, and optional timeout strengthening; dispositions are recorded on PR #51.
- Hosted pull-request certification [32090055179](https://github.com/the-sarge/turn/actions/runs/32090055179) passed on the reviewed head, including core, race, docs, Go floor, secret scan, CodeQL, proto fuzz smoke, and `ci-required`; PR #51 was squash-merged while pinned to that head.

**Next**

- Track 3 remains independently dispatchable: typed ChannelBind errors ([#46](https://github.com/the-sarge/turn/issues/46)) and bounded channel-number allocation ([#47](https://github.com/the-sarge/turn/issues/47)). Live program view: [tracking issue #48](https://github.com/the-sarge/turn/issues/48).
- No review follow-up issue was required and no untraced contract effect remains from this slice.

---

## Track 3 Slice 1: typed ChannelBind errors - 2026-08-17 23:01 EDT

**Main:** `2a55017c466f`
**Actor:** Codex

**Summary**

PR [#53](https://github.com/the-sarge/turn/pull/53) landed Track 3 Slice 1 of the TURN-consistency plan as `2a55017`: every well-formed non-438 ChannelBind error response now preserves a typed `*stun.TurnError` through the existing error chain. Code 400 retains both ChannelBind sentinels and its established fresh-binding seal / previously-ready recovery policy; code 438 retains nonce update and retry behavior. Child issue [#46](https://github.com/the-sarge/turn/issues/46) is closed, and [#47](https://github.com/the-sarge/turn/issues/47) is the sole remaining program frontier.

**Completed**

- `handleChannelBindErrorResponse` remains the single classification owner: 438 exits through `errTryAgain`; every other well-formed error constructs one `*stun.TurnError`; 400 wraps it with `errCannotBindChannel` and `errChannelBindBadRequest`; malformed ERROR-CODE responses retain the unexpected-response class.
- `handleBindChannelError` remains the lifecycle-disposition owner. Fresh and unknown 400 failures still seal the Allocation and expose the typed cause through terminal operations; previously-ready 400 still restores the saved channel binding and returns nil; representative non-400 rejection reaches `PreparePeer` as a typed error.
- The finite response table now has two-sided typed/untyped assertions, the three-attempt 438 exhaustion case remains untyped, and existing request-shape checks continue to compare normalized ChannelBind bytes and setter order. The product PR also marked T3.S1 complete and advanced the committed program frontier to T3.S2.

**Validation**

- Red-first assertions demonstrated that 400, representative 403, and fresh/unknown terminal chains lacked `errors.As(*stun.TurnError)` before the production change; the narrow classifier update made them green while existing ready-400, malformed, 438, state, and request-byte cases remained green.
- Focused ChannelBind/PreparePeer tests, `go test ./...`, `task verify`, and `task race` passed. Exact-head `task preflight` certified `cfcb430d86ba5c7065596d7d8f1e16adb5fe966b` against base `a5396dc460ae39c090eddef314ee00b39179aa4a`, including race, dependency/vulnerability, Darwin/Windows build, workflow-routing, docs, and secret gates.
- Initial RAS review `20260818T022722-c68049f80055f298a810698f` produced one low `fix-now` verification-aid finding: the malformed response row lacked a negative typed-error assertion. Commit `cfcb430` added the two-sided table guard; verification `20260818T022722-c68049f80055f298a810698f-verification-1787020876700771000` resolved it at the exact head with focused/full tests and a disposable discriminating guard mutation. Replacement review `20260818T024150-9804b59991e2e4f2db9423b6` produced no fixes or follow-ups; its cosmetic duplicate-code-text observation was rejected as outside the accepted identity/lifecycle contract.
- Hosted pull-request certification [32093454305](https://github.com/the-sarge/turn/actions/runs/32093454305) passed on the certified head, including core, race, docs, Go floor, secret scan, CodeQL, proto fuzz smoke, and `ci-required`; PR #53 was squash-merged while pinned to that head.

**Next**

- T3.S2 ([#47](https://github.com/the-sarge/turn/issues/47)) remains independently dispatchable with no blockers: bound channel-number allocation and contract the binding manager. Live program view: [tracking issue #48](https://github.com/the-sarge/turn/issues/48).
- No review follow-up issue was required, and no untraced contract effect remains from T3.S1.

---

## Bounded channel allocation landed - 2026-08-18 00:07 EDT

**Main:** `0c55b10d85f2`
**Actor:** Codex

**Summary**

PR [#55](https://github.com/the-sarge/turn/pull/55) landed Track 3 Slice 2 of the TURN-consistency plan as `0c55b10`: one Allocation now assigns every channel number in the complete 16,384-value TURN range at most once, preserves existing peers at capacity, and fails a later distinct peer with `ErrChannelBindFailed` after permission succeeds without overwriting either map or starting ChannelBind. Child issue [#47](https://github.com/the-sarge/turn/issues/47) is closed, and the committed architecture-deepening program frontier is empty.

**Completed**

- `bindingManager` is the single capacity and address↔number bijection owner. Its locked get-or-create path returns existing peers first, rejects new peers at 16,384 entries, and leaves both maps unchanged on exhaustion.
- `PreparePeer` preserves permission-before-binding order, maps manager exhaustion to the existing public `ErrChannelBindFailed`, and rechecks caller cancellation and Allocation closure that become terminal while capacity ownership is contended.
- Removed the unread `binding.mgr` back-pointer and dead test-only `create`, `deleteByAddr`, `deleteByNumber`, and `size` paths; tests now use live get-or-create, bidirectional lookup, iteration, PreparePeer, prepared-only WriteTo, and inbound behavior.
- Added arithmetic/full-range uniqueness and bijection coverage, existing-peer-at-capacity and 16,385th-peer integrity cases, a two-caller final-slot race, production-path no-ChannelBind evidence, and deterministic post-permission cancellation/closure regressions. The product PR also marked T3.S2 and the full architecture-deepening program complete.

**Validation**

- Red-first capacity coverage exposed the old overwrite behavior; deleting the central capacity guard later made the 16,385th-peer regression fail, and restoring it returned the suite to green. Focused tests, full ordinary and race suites, and `task check` passed.
- Initial RAS review `20260818T031953-99a16cf38c93fe5931d3da07` found one `fix-now` cancellation/closure-precedence defect. Commit `8bc0020` fixed it; verification `20260818T031953-99a16cf38c93fe5931d3da07-verification-1787024444480741000` resolved it on the exact head with a clear blocking projection. Replacement review `20260818T034116-16f6c874c18233ac72fc074e` found no contract-relevant behavioral failure or review follow-up.
- Exact-head `task preflight` certified `8bc002027de7edf26e821c9ec168ee916cacfa64` against base `050dad8080a827a17b43d5cbd5355ae370cd5dbc`, including core/docs/race, dependency and vulnerability checks, Darwin/Windows builds, workflow/routing contracts, and secret scan.
- Hosted pull-request certification [32097298052](https://github.com/the-sarge/turn/actions/runs/32097298052) passed on the certified head, including core/race, docs, Go floor, secret scan, CodeQL, proto fuzz smoke, and `ci-required`; PR #55 was squash-merged while pinned to that head.

**Next**

- The architecture-deepening implementation frontier is empty. The live program view is [tracking issue #48](https://github.com/the-sarge/turn/issues/48); post-merge closure will reconcile its parent/mirror state and revalidate the one deferred verification-aid performance observation against `0c55b10`.

---

## Release v5.2.1-gs.1 - 2026-08-18 01:07 EDT

**Main:** `bf06a1147eee`
**Actor:** Claude (planit)

**Summary**

Annotated tag `v5.2.1-gs.1` published at `bf06a11` and resolving via `go get`. It packages everything merged since `v5.2.0-gs.1` with no exported API change: the M1 review-follow-ups batch (PR #38 — #30 seal precedence, #33/#36 test coverage) and the 2026-08-17 architecture deepening program (PRs #49/#51/#53/#55: transaction-registry terminal ownership, Allocation lifecycle consolidation on `UDPConn`, typed TURN errors through ChannelBind policy, bounded channel-number allocation). Cut so the pending wiremux adoption lands once instead of twice.

**Decisions**

- Consumer adoption retargeted from `v5.2.0-gs.1` to `v5.2.1-gs.1`: GridSwarm/wiremux#1180 (title, target line, and adoption note updated 2026-08-18; the migration map was verified unchanged against the new tag). Tracker #6 carries the release state.

**Validation**

- Local `task release-check` (check, docs-check, race, dependency-gate, platform-check, fuzz) green at `bf06a11` in a detached worktree before tagging; main push CI was already green at the same head.
- Tag-ref hosted CI run [32101085058](https://github.com/the-sarge/turn/actions/runs/32101085058) completed successfully, including the tag-only Release check lane.
- Module resolution confirmed: `go list -m github.com/the-sarge/turn/v5@v5.2.1-gs.1` resolves through the public proxy.

**Next**

- Wiremux adoption (GridSwarm/wiremux#1180) is the only remaining consumer step, on wiremux's schedule. Nothing is left to dispatch in this repository; Track 3 (M2 packet path) stays gated. Live view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).

---

## Linear channel-capacity checks landed - 2026-08-18 16:58 EDT

**Main:** `ce688e41b38c`
**Actor:** Claude (planit)

**Summary**

PR [#59](https://github.com/the-sarge/turn/pull/59) landed as `ce688e4` and closed [#57](https://github.com/the-sarge/turn/issues/57), the deferred verification-aid observation from the PR #55 review. The two full-range channel-capacity tests in `internal/client/binding_test.go` now detect channel-number reuse with a direct map lookup instead of testify's reflective `require.NotContains` map scan, which was quadratic across the 16,384-entry channel range. No product code, allocation policy, public API, or evidence domain changed.

**Completed**

- Replaced the per-insert `require.NotContains(t, <map>, <uint16>)` checks in `TestBindingManagerCapacity` and `TestBindingManagerConcurrentFinalSlot` with `_, reused := m[n]; require.False(t, reused, …)`, keeping the same failure locality and adding the reused channel number to the message. Every other assertion (range bounds, forward/reverse lookups, one-success/one-exhaustion counts, final `Len` bijection, 16,385th-peer exhaustion branch) is unchanged.
- Local timing for the two tests: ordinary 14.3 s → 0.26 s; `-race` 92.0 s → 1.5 s. The `internal/client` race package dropped from about 91 s to 3.8 s in preflight, and the hosted core job from 5m25s (PR #55) to 2m18s.

**Validation**

- Guard mutations, both reverted before commit: deleting the central `len(mgr.addrMap) >= maxChannelBindings` guard in `getOrCreate` failed the `TestBindingManagerCapacity` exhaustion branch and all three `TestPreparePeer/capacity exhaustion…` subtests; wrapping `assignChannelNumber` one slot early failed `binding_test.go:35` with `channel number 0x4000 reused for 192.0.2.1:16384` and the concurrent test at its `require.Len` bijection check.
- RAS review `20260818T204425-88bee19a74bb6dcb3f953764` at `e836827`: no Fix First, no Follow Up; one Do Not Act On cluster (C-001, the concurrent test's reuse check is redundant under `chanMap`'s number keying) independently dispositioned `reject` as pre-existing and equivalent to the replaced assertion. No replacement review was needed.
- Exact-head `task preflight` certified `e8368274c7c34db188cc9a2056674d3a65b09282` against base `002369092731a2888dea625970323e33c568c967` (core, docs, race, dependency gate, Darwin/Windows builds, workflow/routing contracts, secret scan).
- Hosted pull-request certification [32184654364](https://github.com/the-sarge/turn/actions/runs/32184654364) passed on the certified head, including core, docs, Go floor, secret scan, CodeQL, proto fuzz smoke, and `ci-required`; PR #59 was squash-merged while pinned to that head.

**Next**

- No follow-up issue was required. The architecture-deepening program frontier stays empty; wiremux adoption of `v5.2.1-gs.1` (GridSwarm/wiremux#1180) remains the only consumer step. Live view: [tracking issue #6](https://github.com/the-sarge/turn/issues/6).

---

## Portfolio CI standard landed - 2026-08-19 01:16 EDT

**Main:** `c115b54807e2`
**Actor:** Codex

**Summary**

PR [#61](https://github.com/the-sarge/turn/pull/61) standardized repository CI and squash-merged the exact reviewed head `597dd87a1880713ff6df95d64a72ba04ff5ae4cd` as `c115b54807e2464ca1533e3598ab761d2a660328`. Required pull-request CI now uses the fixed draft-gated portfolio workflow and repository-owned Taskfile routing, while deep and release validation remain non-required workflows.

**Completed**

- Replaced the certification-label, dispatch-SHA, status-bridge, classification-graph, and aggregate-result orchestration with one `pull_request` workflow whose `ci-required` job runs `task ci`; draft PR events start the workflow but execute no job steps.
- Added the byte-identical portfolio classifier, retained repository checks behind `task check` and `task docs-check`, and moved scheduled/manual deep validation plus tag release validation into non-required workflows.
- Applied active default-branch ruleset `21024823`, requiring pull requests, `ci-required` from the GitHub Actions integration, strict freshness, squash-only merging, and deletion/force-push protection.

**Validation**

- The repository and live-ruleset conformance audits reported no deviations. YAML parsing, actionlint, committed docs-only and source routing, Go 1.24 `task check`, bounded fuzz targets, and exact-head `task preflight` passed; preflight included race, vulnerability, cross-build, and secret checks.
- The bounded review resolved all accepted findings, exact-head verification covered all six initial clusters, and the replacement review produced no fix-first or follow-up findings.
- Draft run [32215127070](https://github.com/the-sarge/turn/actions/runs/32215127070) published `ci-required=skipped` with zero steps. After ready transition, run [32217828455](https://github.com/the-sarge/turn/actions/runs/32217828455) ran the source `check` path and passed `ci-required` on the exact merge head; GitHub reported the PR blocked while that check was pending and clean after success.
- Docs-only routing was proven locally with committed clean and malformed ranges. Journal PR [#62](https://github.com/the-sarge/turn/pull/62) then supplied hosted evidence: run [32218914235](https://github.com/the-sarge/turn/actions/runs/32218914235) selected `docs-check`, did not run the source `check` path, and passed. A manufactured default-branch advance was not created, so the live-behind observation remains unforced.

**Next**

- Deletion of the obsolete `ci-certify` label remains a separately authorized repository cleanup step.

---

## Server-bound outbound transport landed - 2026-08-19 12:49 EDT

**Main:** `2a3fcd73b02d`
**Actor:** Codex

**Summary**

PR [#72](https://github.com/the-sarge/turn/pull/72) landed Track 1 Slice T1.S1 as `2a3fcd7` and closed [#68](https://github.com/the-sarge/turn/issues/68). Root `Client` now binds the caller-owned socket to the one canonical configured TURN server, while transaction-registry and Allocation internals no longer store, accept, or select outbound destinations.

**Completed**

- Added the sole `Client.sendToServer` socket-address adapter and captured it in the transaction registry and Allocation construction.
- Removed transaction-entry destinations plus destination parameters from registry perform/start/context operations, `AllocationConfig`, `UDPConn`, Refresh, CreatePermission, ChannelBind, lifetime-zero release, retry, and ChannelData paths.
- Preserved exact request and ChannelData bytes, payload-length reporting, copied retry bytes, one-winner transaction behavior, cancellation, nonterminal Client close, abort-before-notification-before-release, invalid-relayed cleanup, and caller socket ownership.
- Added a real-Client observer regression for anonymous and authenticated Allocate destinations, strengthened the deleted-surface contract for internal Allocation destination authority, and committed the T1.S1/program completion transition in the product PR.

**Decisions**

- Root `Client` remains the sole configured-server and socket-address adaptation owner; the registry remains the transaction owner and `UDPConn` remains the Allocation lifecycle owner. The accepted contract and post-merge checkpoint remain in the [server-bound transport plan](adr/2026-08-19-server-bound-transport-plan.md) and [program index](adr/2026-08-19-architecture-deepening-program.md).

**Validation**

- The green destination-sensitive characterization observed both Allocate sends targeting the configured server; the destination-free registry test then failed at compile time before the narrow signature migration and passed afterward.
- Focused configured-server, inbound-source, request-shape, registry, cancellation, close, invalid-relayed, ChannelData, and lifecycle tests passed, followed by `go test ./...` and `go test -race ./...`.
- Initial RAS review `20260819T163835-891bd69426b2c32c6af9e866` produced no `Fix First` or follow-up clusters. Its sole low process-status observation was rejected because the governing workflow requires completion/frontier facts in the product PR before first review; no verification or replacement review was required.
- Exact-head `task preflight` certified `942e9299cacb0c86459890822dfb378272163f6d` against base `f722466a1864b1ca89e9e364daa962a7c1c5ba23`, including format, vet, lint, docs, race, dependency/vulnerability, Darwin/Windows build, workflow, and secret gates. Post-ready hosted run [32277868371](https://github.com/the-sarge/turn/actions/runs/32277868371) passed `ci-required` on that exact head before guarded squash merge.

**Next**

- T2.S1 ([#69](https://github.com/the-sarge/turn/issues/69)) and T3.S1 ([#70](https://github.com/the-sarge/turn/issues/70)) remain independent ready frontier slices. Allocation construction is a post-Track-1 evidence-gated re-audit, not a dispatchable implementation slice; the live program view is [tracking issue #71](https://github.com/the-sarge/turn/issues/71).
- No review follow-up issue was required, and the bounded production census found no untraced outbound destination effect.

---

## Inbound Allocation delivery landed - 2026-08-19 13:13 EDT

**Main:** `2fcdc3453f02`
**Actor:** Codex

**Summary**

PR [#74](https://github.com/the-sarge/turn/pull/74) landed Track 2 Slice T2.S1 as `2fcdc34` and closed [#69](https://github.com/the-sarge/turn/issues/69). Root `Client` retains server-source admission, TURN decoding, canonicalization, transaction completion, no-live discard, and the existing live unknown-channel error, while `UDPConn` now owns decoded peer-data delivery through queue admission and seal disposition.

**Completed**

- Added Allocation-owned Data-indication and ChannelData delivery operations, moved channel lookup behind `UDPConn`, and removed the old outward lookup plus caller-composed generic queue path.
- Added one delivery read/write guard that holds its read side across live validation, channel lookup, payload copy, and nonblocking admission; seal holds its write side only around the `closeCh` transition before transaction abort, deallocation notification, or lifetime-zero release.
- Preserved assigned-but-unprepared inbound ChannelData, canonical peer labels, copied payload ownership, full-queue UDP drop semantics, `io.ErrShortBuffer`, pre-seal queued data, existing error text, and the unchanged `ReadFrom` selection contract.
- Added public root-to-`Allocation.ReadFrom` regressions for both decoded forms and focused internal coverage for no-live/live/sealed state, known/unknown channels, queue saturation, copy ownership, short buffers, and both delivery/seal orders. The product PR also marked T2.S1 and the Track 2 program row complete.

**Decisions**

- Root remains the wire and canonical peer representation owner; `UDPConn` is the decoded-delivery and lifecycle owner; `bindingManager` remains the channel identity owner. The accepted finite domain, non-goals, and evidence ceiling remain in the [inbound Allocation delivery plan](adr/2026-08-19-inbound-allocation-delivery-plan.md) and [program index](adr/2026-08-19-architecture-deepening-program.md).

**Validation**

- Red-first tests exposed the missing Allocation delivery seam and the old post-seal enqueue behavior. Focused root/internal suites, `go test ./...`, `go test -race ./...`, and `task verify` passed before review.
- Initial RAS review `20260819T170459-f35179532ca8d7c279957e75` synthesized no `Fix First`, follow-up, or rejected clusters, so no verification or replacement review was required. Two configured reviewers completed; one Claude reviewer failed structural validation after RAS rejected an ungrounded record, and the run retained that warning.
- Exact-head `task preflight` certified `641a747b75d677fa757af7455971ffce7d29c471` against base `ab4e9c3d92df6af0f2049451e21e1a9e33ff908c`, including formatting, vet, tests, lint, docs, race, dependency/vulnerability, Darwin/Windows build, workflow, and secret gates.
- Post-ready hosted run [32280093792](https://github.com/the-sarge/turn/actions/runs/32280093792) passed `ci-required` on that exact head before the guarded squash merge.

**Next**

- T3.S1 ([#70](https://github.com/the-sarge/turn/issues/70)) remains an independently ready frontier slice. Post-merge closure will reconcile parent #66, tracking issue [#71](https://github.com/the-sarge/turn/issues/71), and the OmniFocus mirror; no review follow-up issue or remaining untraced delivery effect was identified.

---

## Channel-binding readiness landed - 2026-08-19 14:07 EDT

**Main:** `77b3dd93bd94`
**Actor:** Codex

**Summary**

PR [#76](https://github.com/the-sarge/turn/pull/76) landed Track 3 Slice T3.S1 as `77b3dd9` and closed [#70](https://github.com/the-sarge/turn/issues/70). Binding-local readiness now owns readiness phase, confirmation and prepared history, durable terminal causes, expiry, generation-token validity, and terminal collapse, while `UDPConn` retains TURN policy, active-attempt coordination, Permission orchestration, workers, and Allocation lifecycle.

**Completed**

- Added binding-local begin/resolve, preparation access, write access, and prepared-permission-loss operations over one readiness lock and a finite generation-token model.
- Moved refresh eligibility, inclusive ten-minute expiry, prepared-only admission, permission-loss ordering, fresh/previously-confirmed uncertainty, 400 preservation/failure, permanent causes, and non-resurrection behind that owner; new bindings no longer manufacture a confirmation timestamp.
- Kept each active attempt's done signal and transient result on `UDPConn`, ordered final outcome publication with Allocation seal, and prevented a completion returning after close from creating readiness or losing the full `net.ErrClosed`/terminal-cause chain.
- Replaced raw binding state/time/error/prepared test setup with explicit-time decision, access, ordering, cancellation, close, and public PreparePeer/WriteTo behavior evidence. The product PR also marked T3.S1 and the three-track program complete.

**Decisions**

- Binding readiness remains the only readiness mutation owner; `bindingManager` remains the channel identity/capacity owner, and `UDPConn` remains the TURN classification, coordination, Permission, worker, and Allocation lifecycle owner. The accepted finite domain and post-Track-3 checkpoint remain in the [channel-binding readiness plan](adr/2026-08-19-channel-binding-readiness-plan.md) and [program index](adr/2026-08-19-architecture-deepening-program.md).

**Validation**

- Red-first tests exposed the missing readiness seam and a close-winning completion that could still publish confirmation. Focused state/access, cancellation, close, request-shape, prepared-only write, 400, permission-refresh, error-chain, and retry tests passed with the full ordinary and race suites.
- The terminal-collapse mutation made the in-flight expiry/non-resurrection regression fail, and the active-token-to-generation mutation made the duplicate-resolution regression fail; both guards were restored and the focused tests passed afterward.
- Initial RAS review `20260819T173516-09333163618b2b3da1c3e01e` produced four `fix-now` clusters: close/outcome linearization, one expiry expression, discriminating duplicate-token evidence, and transient-versus-durable error storage. Exact-head verification resolved all four at `7b75571594ec3a8eb98fd7af24ad986fe20a88b4`; the sole optional unread-field observation remained rejected, and replacement review `20260819T175615-589ce85902c38121e581f9ae` was clean.
- Exact-head `task preflight` certified `7b75571594ec3a8eb98fd7af24ad986fe20a88b4` against base `250ed49e777ce2d7e978c7f8d53ffde0771a4f52`, including formatting, vet, tests, lint, docs, race, dependency/vulnerability, Darwin/Windows build, workflow, and secret gates. Post-ready hosted run [32285202550](https://github.com/the-sarge/turn/actions/runs/32285202550) passed `ci-required` on that exact head before guarded squash merge.

**Next**

- No deferred review finding survived for a follow-up issue and no untraced readiness effect remains. Post-merge closure will reconcile parent #67, tracking issue [#71](https://github.com/the-sarge/turn/issues/71), and the OmniFocus mirror; the attempt-coalescing re-audit is a post-Track-3 evidence checkpoint, not a dispatchable implementation slice.

---

## Allocation timing validity landed - 2026-08-19 15:26 EDT

**Main:** `4417a67775a9`
**Actor:** Codex

**Summary**

PR [#81](https://github.com/the-sarge/turn/pull/81) landed Track 4 Slice T4.S1 as `4417a67` and closed [#80](https://github.com/the-sarge/turn/issues/80). Public `NewClient` construction now rejects negative permission-refresh cadences and explicit cadences at or above the five-minute TURN Permission Lifetime while preserving zero-as-default and every positive cadence below five minutes.

**Completed**

- Added the sole public validity guard in `NewClient`, after nil-connection and canonical-server checks, with a private construction error and no duplicate validation in `NewUDPConn`.
- Corrected the `ClientConfig.PermissionRefreshInterval` documentation to name the field and document the two-minute zero default, strict five-minute upper bound, rejection behavior, and operational-delay ceiling.
- Added the accepted eight-row constructor table covering negative, zero, small positive, immediately below five minutes, exactly five minutes, above five minutes, and both existing-error precedence cases; preserved the existing 50-millisecond end-to-end cadence.
- Marked T4.S1, Track 4, and the four-track architecture deepening program complete in the product PR with no successor frontier.

**Decisions**

- `NewClient` remains the single owner of public cadence validity; `NewUDPConn` retains only zero-to-default interpretation, and `UDPConn` retains timer execution and Allocation lifecycle. The accepted universal duration predicate, non-goals, and evidence ceiling remain in the [allocation construction timing validity plan](adr/2026-08-19-allocation-construction-timing-validity-plan.md) and [program index](adr/2026-08-19-architecture-deepening-program.md).

**Validation**

- Red-first execution showed that negative, exactly-five-minute, and above-five-minute rows were accepted before the guard. The focused table and existing 50-millisecond end-to-end test passed afterward, followed by `go test ./...` and `go test -race ./...`.
- Initial RAS review `20260819T191429-d0be4a43ba7ef72c52ff16f5` produced no `Fix First` or follow-up clusters. Its two low observations—optional wording polish and private-sentinel identity coverage—were independently rejected as preference and out-of-budget verification-aid strengthening, so no verification or replacement review was required.
- Exact-head `task preflight` certified `a4aa4f789c1f26b326212fa09027b21c5fc40d27` against base `b9d8890e5c003e15b56f75c84ffb8f197ebc9a62`, including format, vet, tests, lint, docs, race, dependency/vulnerability, Darwin/Windows build, workflow, and full-history secret gates. Post-ready hosted run [32292763340](https://github.com/the-sarge/turn/actions/runs/32292763340) passed `ci-required` on that exact head before guarded squash merge.

**Next**

- No implementation frontier, deferred review finding, or untraced cadence effect remains. Post-merge closure will reconcile parent [#79](https://github.com/the-sarge/turn/issues/79) and the OmniFocus program mirror.
