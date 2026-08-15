#!/usr/bin/env bash
set -euo pipefail

# Whitespace hygiene over the working tree, plus the selected commit range
# when CI provides one. git owns the diff representation; this script adds
# no parsing of its own.
git diff --check

base="${CI_BASE_SHA:-}"
head="${CI_HEAD_SHA:-HEAD}"

if [[ -n "$base" ]] &&
  git rev-parse --verify -q "$base^{commit}" >/dev/null &&
  git rev-parse --verify -q "$head^{commit}" >/dev/null; then
  merge_base="$(git merge-base "$base" "$head")"
  git diff --check "$merge_base" "$head"
fi

printf 'docs lane: whitespace hygiene passed\n'
