# CI configuration ownership

Repository maintainers own validation policy. Review tool and action updates weekly with the dependency queue and before releases, following the [toolchain policy](../README.md#development). Keep required PR jobs independent: shared behavior belongs below the jobs, without changing their guards, triggers, permissions, or setup policy merely to remove repeated YAML.

## Shared behavior

| Owner | Callers | Contract |
| --- | --- | --- |
| `scripts/ci/commit-range.sh` | `scripts/ci-classify.sh`, `scripts/ci/check-docs.sh` | Resolve explicit endpoints or infer a base from the default branch; verify refs with Git. Preserve `CI_BASE_SHA`, `CI_HEAD_SHA`, `CI_DEFAULT_BRANCH`, `GITHUB_BASE_REF`, and `CI_REMOTE` precedence. |
| `scripts/ci-classify.sh` | `task ci` | Compare selected endpoints; choose full validation when the range is unavailable, empty, or includes non-documentation. `CI_DOCS_GLOBS` remains the script override; Task supplies its existing documentation-glob policy. |
| `scripts/ci/check-docs.sh` | `task docs-check` | Check working-tree whitespace and the range from the common ancestor; fail if a trustworthy range or merge base is unavailable. |
| `.github/actions/consumer-floor` | Release and deep-check `floor` jobs | Resolve `go.mod` through `go list` under the validation Go pin, install the latest patch of the declared major/minor version, disable caching, and test with `GOTOOLCHAIN=local`. |
| `task deep-check` | `task preflight`, deep-check `core` job | Run core checks, docs, race, dependency, platform, and workflow checks in that order. Preflight additionally scans secrets and prints the head/base receipt. |

`scripts/ci/tests/range.sh` exercises the public classifier and docs entrypoints using real disposable Git histories. It runs in `task workflow-check`. These focused regressions preserve the declared behavior; they do not claim exhaustive Git or shell conformance. Maintainers should update the tests when the supported behavior changes and retire them when their entrypoints disappear or authoritative tooling replaces them. Keep this verification aid bounded; workflow-to-Task token validation remains [issue #116](https://github.com/the-sarge/turn/issues/116).

## Pins and equality groups

Dependabot proposes GitHub Actions changes; maintainers review and merge each intended family together. Its directory list includes the workflows and both local composite actions. YAML repeats below are intentional. At this change, parsed `uses` values were checked for equality within these groups:

| Group | Locations | Update obligation |
| --- | --- | --- |
| `actions/checkout` | Nine steps across `ci.yml`, `deep-checks.yml`, and `release.yml` | Keep one reviewed SHA and matching version comments across all nine steps. |
| `actions/setup-go` | Required CI, `setup-validation`, and `consumer-floor` | Keep one reviewed SHA and matching version comments; preserve each caller's different Go-version and cache inputs. |
| `github/codeql-action` | Deep-check `init`, `autobuild`, and `analyze` steps | Update the three subactions to the same reviewed release SHA together. |
| `arduino/setup-task` | Required CI | Preserve the action SHA and the separate repository-owned Task executable version supplied from `scripts/tool-versions.env`. |

Before merging an action update, inspect the `uses` values across `.github/workflows` and `.github/actions`, verify equality within the named groups, and run `task workflow-check`. This is a maintenance check over known manifests, not a new general-purpose policy scanner or required CI job.

`scripts/tool-versions.env` owns validation executable versions; the validation Go pin may advance separately from the consumer floor in `go.mod`. `Taskfile.yml` owns `LINT_STANDARD_SHA256`, paired with the byte-identical `.golangci.yml` from `the-sarge/infra` at `config/golangci/.golangci.yml`. Update that digest and configuration together; `task lint-config-check` enforces the pair. A pin update must satisfy the README's consumer-floor and validation-toolchain checks.

## Inherited configuration inventory

| Artifact | Observed consumers and provenance | Disposition |
| --- | --- | --- |
| `.goreleaser.yml` | Inherited Pion configuration with builds disabled; no tracked workflow or Task target invokes GoReleaser. The release ADR describes Task validation and annotated tags. | Retain until external/manual consumers are established; absence of an in-tree caller does not establish that removal is safe. |
| `codecov.yml` | Header claims copying from `pion/.goassets`; contains exclusions for removed examples. No tracked coverage uploader exists. | Retain without modifying inherited settings until external Codecov usage and ownership are established. Do not assume an active upstream synchronization process from the header alone. |
| `.gitleaksignore`, `.gitleaks.toml` | Fork-owned secret scanning retains historical fingerprints and a policy self-test through `scripts/ci/check-secrets.sh`. | Preserve the fingerprints and scan behavior. They account for history, not just current-tree files. |

Read-only GitHub inspection on 2026-09-15 returned no repository webhooks and no check runs at `308408e027173c6c7420c104c1dbaebe65380ea4`. The Actions registry also retains an entry for a REUSE workflow absent from the current tree. These observations do not enumerate external app installations or manual automation. External release/coverage consumers remain unconfirmed; no inherited configuration is removed by this work.
