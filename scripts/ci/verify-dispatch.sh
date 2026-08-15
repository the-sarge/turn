#!/usr/bin/env bash
set -euo pipefail

expected="${CI_EXPECTED_SHA:-}"
base="${CI_BASE_SHA:-}"
actual="${CI_ACTUAL_SHA:-}"

require_full_sha() {
  local label="$1"
  local value="$2"

  if [[ ! "$value" =~ ^[0-9a-fA-F]{40}$ ]]; then
    printf 'error: %s must be a full 40-character commit SHA\n' "$label" >&2
    exit 1
  fi
}

require_commit() {
  local label="$1"
  local value="$2"

  if ! git rev-parse --verify -q "$value^{commit}" >/dev/null; then
    printf 'error: %s %s is not available as a commit\n' "$label" "$value" >&2
    exit 1
  fi
}

require_full_sha expected_sha "$expected"
require_full_sha base_sha "$base"
require_full_sha actual_sha "$actual"

expected="$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')"
base="$(printf '%s' "$base" | tr '[:upper:]' '[:lower:]')"
actual="$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')"

if [[ "$actual" != "$expected" ]]; then
  printf 'error: dispatched head %s does not match expected_sha %s\n' "$actual" "$expected" >&2
  exit 1
fi

require_commit expected_sha "$expected"
require_commit base_sha "$base"

if ! git merge-base --is-ancestor "$base" "$expected"; then
  printf 'error: base_sha %s is not an ancestor of expected_sha %s\n' "$base" "$expected" >&2
  exit 1
fi

printf 'dispatch binding verified for expected head %s and base %s\n' "$expected" "$base"
