#!/usr/bin/env bash
set -euo pipefail

# Whitespace hygiene over the working tree and a trustworthy commit range.
# git owns the diff representation; this script adds no parsing of its own.
git diff --check

# shellcheck source=scripts/ci/commit-range.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/commit-range.sh"
if ! resolve_ci_range; then
  printf 'error: docs lane cannot determine a trustworthy commit range\n' >&2
  exit 1
fi

if ! merge_base="$(git merge-base "$CI_RANGE_BASE" "$CI_RANGE_HEAD")"; then
  printf 'error: docs lane cannot determine a trustworthy merge base\n' >&2
  exit 1
fi

git diff --check "$merge_base" "$CI_RANGE_HEAD"

printf 'docs lane: whitespace hygiene passed\n'
