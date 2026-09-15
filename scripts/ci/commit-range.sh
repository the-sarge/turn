#!/usr/bin/env bash
# Shared resolution only; callers own comparison and failure policy.
# On success CI_RANGE_BASE and CI_RANGE_HEAD contain verified commit refs.
# Explicit bases remain endpoints; only an inferred base is a merge base.
set -euo pipefail

resolve_ci_range() {
  local remote="${CI_REMOTE:-origin}"
  local default_branch="${CI_DEFAULT_BRANCH:-${GITHUB_BASE_REF:-}}"
  CI_RANGE_BASE="${CI_BASE_SHA:-}"
  CI_RANGE_HEAD="${CI_HEAD_SHA:-HEAD}"

  if [[ -z "$default_branch" ]]; then
    default_branch="$(git symbolic-ref -q --short "refs/remotes/$remote/HEAD" 2>/dev/null | sed "s#^$remote/##" || true)"
    default_branch="${default_branch:-main}"
  fi

  if [[ -z "$CI_RANGE_BASE" ]]; then
    if git rev-parse --verify -q "$remote/$default_branch^{commit}" >/dev/null; then
      CI_RANGE_BASE="$(git merge-base "$remote/$default_branch" "$CI_RANGE_HEAD" 2>/dev/null || true)"
    elif git rev-parse --verify -q "$default_branch^{commit}" >/dev/null; then
      CI_RANGE_BASE="$(git merge-base "$default_branch" "$CI_RANGE_HEAD" 2>/dev/null || true)"
    fi
  fi

  [[ -n "$CI_RANGE_BASE" ]] &&
    git rev-parse --verify -q "$CI_RANGE_BASE^{commit}" >/dev/null &&
    git rev-parse --verify -q "$CI_RANGE_HEAD^{commit}" >/dev/null
}
