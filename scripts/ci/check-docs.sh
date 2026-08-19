#!/usr/bin/env bash
set -euo pipefail

# Whitespace hygiene over the working tree and a trustworthy commit range.
# git owns the diff representation; this script adds no parsing of its own.
git diff --check

remote="${CI_REMOTE:-origin}"
base="${CI_BASE_SHA:-}"
head="${CI_HEAD_SHA:-HEAD}"

default_branch="${CI_DEFAULT_BRANCH:-${GITHUB_BASE_REF:-}}"
if [[ -z "$default_branch" ]]; then
  default_branch="$(git symbolic-ref -q --short "refs/remotes/$remote/HEAD" 2>/dev/null | sed "s#^$remote/##" || true)"
  default_branch="${default_branch:-main}"
fi

if [[ -z "$base" ]]; then
  if git rev-parse --verify -q "$remote/$default_branch^{commit}" >/dev/null; then
    base="$(git merge-base "$remote/$default_branch" "$head" 2>/dev/null || true)"
  elif git rev-parse --verify -q "$default_branch^{commit}" >/dev/null; then
    base="$(git merge-base "$default_branch" "$head" 2>/dev/null || true)"
  fi
fi

if [[ -z "$base" ]] ||
  ! git rev-parse --verify -q "$base^{commit}" >/dev/null ||
  ! git rev-parse --verify -q "$head^{commit}" >/dev/null; then
  printf 'error: docs lane cannot determine a trustworthy commit range\n' >&2
  exit 1
fi

if ! merge_base="$(git merge-base "$base" "$head")"; then
  printf 'error: docs lane cannot determine a trustworthy merge base\n' >&2
  exit 1
fi

git diff --check "$merge_base" "$head"

printf 'docs lane: whitespace hygiene passed\n'
