# Release tags drop the `-gs` pre-release suffix

**Date:** 2026-08-20
**Status:** Accepted; applies from the first release after `v5.3.0-gs.1`
**Amends:** [Molding program scope](2026-08-14-molding-program-scope.md) decision D3, the versioning consequence in [the owned-library ADR](2026-08-14-owned-library-fork.md), and the tag-hygiene rule in the [molding program index](2026-08-14-turn-molding-program.md)

## Decision

The next release of `github.com/the-sarge/turn/v5` is tagged as plain semver — `v5.3.1` for a fix-only release or `v5.4.0` for new capability — and every later release follows the same shape. The `-gs.N` pre-release suffix is retired. Published `-gs` tags (`v5.0.13-gs.1` through `v5.3.0-gs.1`) are never moved or deleted; the existing tag-hygiene rule stands.

## Why

D3 kept the suffix permanent on the premise that marking every tag as a semver pre-release would stop Go's version selection from ever auto-upgrading wiremux, so that every consumer bump stayed deliberate. That premise does not hold:

- The fork's own releases are all pre-releases (`v5.0.13-gs.1` through `v5.3.0-gs.1`), but the repository also carries the plain tags `v5.0.0`–`v5.0.12` inherited from upstream `pion/turn`, whose `go.mod` still declares `github.com/pion/turn/v5`. Verified 2026-08-20: `go list -m github.com/the-sarge/turn/v5@latest` resolves to `v5.0.12`, which cannot be used under this module path, while an `upgrade` query from wiremux's existing `v5.2.1-gs.1` requirement (`go get -u`, `@upgrade`) selects `v5.3.0-gs.1` because Go prefers the highest pre-release once the consumer already requires one. The suffix therefore neither blocks pre-release-to-pre-release upgrades nor produces a useful `@latest`.
- Dependabot and Renovate offer pre-release bumps when the consumer is already on a pre-release, which wiremux is.
- The property that actually keeps consumer upgrades deliberate is minimal version selection: Go never moves a `go.mod` requirement unless someone edits it or runs an upgrade query. That is true with or without a suffix.

What the suffix still bought was a visual fork marker, and the module path `github.com/the-sarge/turn/v5` already provides that (the Go proxy and pkg.go.dev namespace by module path, so there is no collision with upstream `github.com/pion/turn/v5` tags). Its remaining effect was cost: pre-release tags read as unstable to tooling and people.

## Consequences

- Plain `v5.3.1` and `v5.4.0` sort above `v5.3.0-gs.1`, so wiremux's next adoption is an ordinary deliberate bump; no retagging is involved.
- After the first plain tag, `go get github.com/the-sarge/turn/v5@latest` resolves to that release instead of the unusable inherited `v5.0.12`. wiremux continues to pin exact versions.
- The owned-library stance is unchanged: this remains wiremux's library, and API removal or divergence between milestone versions is still expected within the `/v5` module path; a plain tag is not a compatibility promise to other consumers.
- Release mechanics are unchanged: local `task release-gate` in a detached worktree at the candidate head, an annotated tag, the tag-push `Release checks` workflow, proxy-resolution verification, and a journal entry.
- README install guidance and the three amended documents point here rather than restating the rule.
