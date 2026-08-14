# TURN Fork Molding Program — Scope

**Status:** Draft for grilling. Nothing in this document is decided until it survives a grill/consider pass.
**Date:** 2026-08-14
**Baseline:** `main` @ `cca6b57` (payload = pion/turn#585 head `c7dcea7` + `4b818fc` transaction-wait abort + `/v5` module rename), published as tag `v5.0.13-gs.1` (at `e8db91a`, same payload).
**Context:** `the-sarge/turn` is a permanent fork of `pion/turn`, per the fork-pivot amendment in wiremux's `docs/adr/2026-08-09-turn-carrier-prerequisites-plan.md` (GridSwarm/wiremux#1161). The goal of this program is to mold the fork into exactly what wiremux needs: cut unused surface, modernize, refactor, and optimize for wiremux's consumption alone.

## Consumer contract

Wiremux's sole planned consumer is the Slice 1.3 allocator/composite: root `Client` performing UDP allocation over a caller-owned `net.PacketConn`, `Client.PrepareUDPPeer` deterministic readiness, the lifetime ChannelData-only write invariant, waiter-local cancellation, and joined Pion-worker allocation close after socket-owner unblock. Wiremux does not consume the TURN server, TCP/TLS transports, ICE, or credential-generation helpers, and its track's acceptance criteria exclude them explicitly.

## Inventory at baseline

| Surface | Size (approx) | Consumer-contract role |
|---|---|---|
| root `client.go`, `errors.go`, `stun_conn.go` | ~950 LOC | **Keep** — the consumed API |
| `internal/client` (UDP path: allocation, binding, client, periodic_timer, permission, transaction, trylock, udp_conn) | ~3,300 LOC | **Keep** — implements readiness, invariant, close-join |
| `internal/proto` | ~2,300 LOC | **Keep** — wire encoding |
| `internal/ipnet` | ~70 LOC | **Keep** |
| `internal/client` TCP path (`tcp_alloc.go`, `tcp_conn.go`) | ~750 LOC | **Cut candidate** — TURN/TCP excluded from track |
| root server (`server.go`, `server_config.go`, `relay_address_generator_*.go`, `lt_cred.go`) | ~1,000 LOC | **Cut candidate** (see D1) |
| `internal/server`, `internal/allocation`, `internal/auth` | ~5,900 LOC | **Cut candidate** (see D1) |
| `examples/` (23 packages), `e2e/` | — | **Cut candidate** — upstream demo surface |
| `.github/workflows` (10 pion reusable-workflow configs), CodeQL, fuzz, release, renovate | — | **Replace** (see D4) |

Direct dependencies at baseline: `pion/logging`, `pion/randutil`, `pion/stun/v3`, `pion/transport/v4`, `stretchr/testify`, `golang.org/x/sys`, `golang.org/x/time`. Server cuts likely orphan some of these (audit at cut time, e.g. `x/time` rate limiting and parts of `transport/v4`).

## Open decisions (grill these first)

- **D1 — Server as test fixture.** Wiremux's Slice 1.3 evidence plan records local-close receipts against a real TURN server; the fork's own client tests also spin the in-repo server. Cutting the server halves the repo but forces a fixture strategy: keep `internal/server`+`internal/allocation` as test-only code, pin an external fixture (e.g. upstream pion/turn as a test dependency), or rewrite fixtures against a minimal stub. This decision gates the size of the entire cut.
- **D2 — Upstream security posture.** After divergence, pion/turn security fixes no longer arrive by merge. Decide the mechanism: watch upstream advisories and cherry-pick into the kept surface, or accept the fork as sole owner of its remaining ~6,600 LOC. The smaller the kept surface, the cheaper either answer.
- **D3 — Versioning scheme.** `v5.0.13-gs.N` implies upstream-tracking semantics the fork no longer has. Once molding breaks API, decide: continue `-gs.N` pre-releases, move to `v5.1.0-gs.1`-style minors, or re-baseline the module as a clean `v5`/`v6` line. Wiremux pins exact versions either way, so this is a communication decision, not a compatibility one.
- **D4 — CI replacement.** The ten inherited workflows call pion's reusable workflows (lint, API compat, e2e, fuzz, release, renovate) tuned to upstream's needs. Decide the fork's own gate: minimal (build, vet, race tests, gofmt) versus retaining API-compat and fuzz jobs against the kept surface. Ties into the portfolio CI standard.
- **D5 — Pre-existing test failures.** `TestCreateTCPConnectionInvalid` (`internal/allocation`) and `TestConnectRequest` (`internal/server`) fail deterministically on macOS at baseline, before any fork changes. If D1 cuts these packages, the failures vanish with them; if not, triage them (a local `codex/duplicate-tcp-deadlock` branch touching `CreateTCPConnection` may be related).

## Modernization and optimization axes (after the cut)

- API: reshape `ClientConfig`/`Client` for the single wiremux consumer — context-first cancellation is already half-arrived via `PrepareUDPPeer`; extend it rather than keeping two idioms.
- Allocations and hot path: the read pump and ChannelData encode/decode are wiremux's per-packet path; profile before optimizing, but `internal/proto` predates modern zero-copy idioms.
- Dependency diet: each cut re-audits `go.mod`; target is the minimum set the kept surface needs.
- Go modernity: baseline is `go 1.24`; adopt current idioms (range-over-int, `min`/`max`, structured logging decision vs `pion/logging`) as files are touched, not as a sweep.

## Program shape (candidate, not committed)

1. **Slice M0 — Cut and stabilize:** resolve D1, delete cut surface, replace CI (D4), green gate on the kept surface. No behavior change to kept code.
2. **Slice M1 — Modernize the kept API** for the wiremux consumer (informed by Slice 1.3's adoption experience — sequencing with wiremux adoption is itself grillable: adopt `v5.0.13-gs.1` first, or land M0 first).
3. **Slice M2 — Optimize the packet path** with profiles from real wiremux traffic.

## Next step

Grill this document (or run `ras consider` on it), resolve D1–D4, then package the surviving program through `architecture-handoff` into dispatchable slices. The two `notes/grill-*-CONTEXT.md` vocabulary files on `main` are the prepared ubiquitous-language context for those sessions.
