#!/usr/bin/env bash
set -euo pipefail

base="${CI_BASE_SHA:-${1:-}}"
head="${CI_HEAD_SHA:-${2:-HEAD}}"

if [[ -n "${GITLEAKS_GO_RUN_VERSION:-}" ]]; then
  gitleaks=(
    env GOTOOLCHAIN=local go run
    "github.com/zricethezav/gitleaks/v8@${GITLEAKS_GO_RUN_VERSION}"
  )
else
  gitleaks=("${GITLEAKS:-gitleaks}")
  if ! command -v "${gitleaks[0]}" >/dev/null 2>&1; then
    printf 'error: gitleaks is required for secret scanning\n' >&2
    exit 1
  fi
fi

repo_root="$(
  CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.."
  pwd
)"

# Policy self-test: the fork-owned allowlist in .gitleaks.toml must never
# disable detection wholesale. Plant one synthetic credential in a scratch
# directory and require gitleaks to find it before trusting a clean scan.
policy_fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/turn-gitleaks-policy.XXXXXX")"
cleanup_policy_fixture() {
  rm -rf -- "$policy_fixture_dir"
}
trap cleanup_policy_fixture EXIT

planted_left='e7322523fb86ed64c836a979cf8465fb'
planted_right='d436378c653c1db38f9ae87bc62a6fd5'
printf 'api_key = "planted-%s%s"\n' "$planted_left" "$planted_right" \
  >"$policy_fixture_dir/planted.txt"

set +e
"${gitleaks[@]}" dir \
  --no-banner \
  --config "$repo_root/.gitleaks.toml" \
  "$policy_fixture_dir" >/dev/null 2>&1
policy_status=$?
set -e

if [[ "$policy_status" -ne 1 ]]; then
  printf 'error: gitleaks policy fixture returned %s; expected the planted credential to be detected\n' "$policy_status" >&2
  exit 1
fi

cleanup_policy_fixture
trap - EXIT

if [[ -n "$base" ]] &&
  git rev-parse --verify -q "$base^{commit}" >/dev/null &&
  git rev-parse --verify -q "$head^{commit}" >/dev/null &&
  [[ "$base" != "$head" ]] &&
  [[ -n "$(git rev-list "$base..$head")" ]]; then
  printf 'scanning commit range %s..%s for secrets\n' "$base" "$head"
  exec "${gitleaks[@]}" git --redact --no-banner --config "$repo_root/.gitleaks.toml" --log-opts="$base..$head" .
fi

printf 'warning: no trustworthy non-empty range; scanning complete repository history\n' >&2
exec "${gitleaks[@]}" git --redact --no-banner --config "$repo_root/.gitleaks.toml" --log-opts=--all .
