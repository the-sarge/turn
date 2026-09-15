# Changelog

## v5.3.1 — 2026-09-15

Maintenance release following `v5.3.0-gs.1`, and the first owned-library release with a plain semver tag under the [versioning decision](docs/adr/2026-08-20-plain-semver-tags.md).

### Fixes and maintenance

- Require a live permission before `turntest` delivers peer datagrams, including when a channel binding is still live. Consumer tests that relied on delivery after permission expiry must renew permission or expect the packet to be dropped.
- Update dependencies and the validation toolchain to Go 1.27.1. The consumer Go floor remains 1.27.
- Strengthen test synchronization, bounded delivery, cleanup ownership, and protocol assertions; expose shared test helpers and named scenarios.
- Clarify existing socket ownership, allocation closure, queued-read, and client transaction-abort behavior in API comments.
- Consolidate CI range handling and validation policy ownership.

### Compatibility and platforms

No public API additions or removals, and no direct production TURN-client logic changes. Dependency updates are included. This remains an owned UDP TURN client library under `github.com/the-sarge/turn/v5`; the plain tag does not restore upstream Pion API compatibility. `turntest` remains a test-only fixture with an example-level guarantee.

Source-only release; no binary artifacts. Release validation exercises Linux/amd64 in GitHub Actions and Darwin/arm64 locally. Darwin/amd64 and Windows/amd64 are cross-build-only targets, without runtime validation in the release gate.

Earlier versions are recorded in the [development journal](docs/DEV-JOURNAL.md).
