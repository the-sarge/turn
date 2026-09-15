#!/usr/bin/env bash
# Answers exactly one question for `task ci`: is this change docs-only?
# Prints `docs_only=true` or `docs_only=false` and always exits 0.
# Fails closed: any doubt (no base, empty diff, non-doc file) => false.
#
# Env:
#   CI_BASE_SHA        explicit base commit (default: merge-base with the default branch)
#   CI_HEAD_SHA        head commit (default: HEAD)
#   CI_DEFAULT_BRANCH  default branch name (default: GITHUB_BASE_REF, else the origin/HEAD
#                      target, else main)
#   CI_REMOTE          remote name (default: origin)
#   CI_DOCS_GLOBS      space-separated shell globs treated as documentation
#                      (default: '*.md docs/* DEV-JOURNAL.md LICENSE LICENSE.*')
set -euo pipefail
set -f # never pathname-expand the globs

docs_globs="${CI_DOCS_GLOBS:-*.md docs/* DEV-JOURNAL.md LICENSE LICENSE.*}"

emit() {
  printf 'docs_only=%s\n' "$1"
  if test -n "${GITHUB_OUTPUT:-}"; then
    printf 'docs_only=%s\n' "$1" >> "$GITHUB_OUTPUT"
  fi
  exit 0
}

# shellcheck source=scripts/ci/commit-range.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/ci/commit-range.sh"
if ! resolve_ci_range; then
  printf 'ci-classify: cannot determine a trustworthy base; failing closed\n' >&2
  emit false
fi

changed="$(git diff --name-only --no-renames "$CI_RANGE_BASE" "$CI_RANGE_HEAD")"
if test -z "$changed"; then
  printf 'ci-classify: empty diff; failing closed\n' >&2
  emit false
fi

while IFS= read -r path; do
  test -n "$path" || continue
  matched=false
  for glob in $docs_globs; do
    # shellcheck disable=SC2254
    case "$path" in
      $glob) matched=true; break ;;
    esac
  done
  if test "$matched" = false; then
    printf 'ci-classify: %s is not documentation\n' "$path" >&2
    emit false
  fi
done <<< "$changed"

emit true
