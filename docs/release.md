# Releasing turn

This is a source-only Go library release. Version choices follow the [plain-semver decision](adr/2026-08-20-plain-semver-tags.md); use an annotated, immutable tag on a commit reachable from `main`. Never move or reuse a published tag.

1. Choose the version deliberately, update `CHANGELOG.md` and the README install pin on a feature branch in a dedicated worktree, and merge the preparation PR after required CI succeeds.
2. In a clean dedicated worktree with a feature branch at the exact merged candidate commit, review the tool pins in `scripts/tool-versions.env` and run `task release-gate`, `task workflow-check`, and `task secret-scan` with the pinned validation Go version. Record the candidate SHA and successful validation. Verify that the commit is an ancestor of current `origin/main` and that the version tag and GitHub Release do not already exist.
3. Create an annotated tag at that exact commit and push only the new tag. Wait for every job in the tag-triggered `Release checks` workflow to succeed at the tagged commit before publishing the GitHub Release.
4. Create the GitHub Release with `gh release create --verify-tag`, using the version's changelog notes and the release-check run URL. State compatibility boundaries and tested versus build-only platforms. There are no binary assets or checksum assets for this library.
5. Verify the published tag and Release, then verify the explicit version and `@latest` through `proxy.golang.org`. Append the tag SHA, workflow URL, Release URL, and proxy evidence to `docs/DEV-JOURNAL.md` through a separate PR.

If validation fails, diagnose the failure before publication. A tag already pushed remains immutable; code corrections require a new version. A transient workflow failure may be retried at the same unchanged tag. The tag workflow validates releases; creating the GitHub Release is a separate deliberate step.
