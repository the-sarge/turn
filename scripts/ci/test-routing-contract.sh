#!/usr/bin/env bash
set -euo pipefail

repo_root="$(
  CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.."
  pwd
)"
fixture_repo="$(mktemp -d "${TMPDIR:-/tmp}/turn-routing-contract.XXXXXX")"
result_file="$fixture_repo/result.txt"

cleanup() {
  rm -rf -- "$fixture_repo"
}
trap cleanup EXIT

git -C "$fixture_repo" init --quiet --initial-branch=main
git -C "$fixture_repo" config user.name "Routing Contract Test"
git -C "$fixture_repo" config user.email "routing-contract@example.invalid"
printf 'base\n' >"$fixture_repo/base.txt"
git -C "$fixture_repo" add base.txt
git -C "$fixture_repo" commit --quiet --message base
base_sha="$(git -C "$fixture_repo" rev-parse HEAD)"

assert_output() {
  local case_name="$1"
  local output="$2"
  local expected="$3"

  if ! grep --fixed-strings --line-regexp --quiet "$expected" <<<"$output"; then
    printf 'error: %s did not emit %s\n%s\n' "$case_name" "$expected" "$output" >&2
    exit 1
  fi
}

classify_case() {
  local case_name="$1"
  local expected_docs_only="$2"
  local expected_source_changed="$3"
  shift 3

  git -C "$fixture_repo" checkout --quiet --detach "$base_sha"
  for changed_path in "$@"; do
    mkdir -p "$fixture_repo/$(dirname -- "$changed_path")"
    printf 'fixture for %s\n' "$case_name" >"$fixture_repo/$changed_path"
  done
  git -C "$fixture_repo" add --all
  git -C "$fixture_repo" commit --quiet --message "$case_name"
  local head_sha
  head_sha="$(git -C "$fixture_repo" rev-parse HEAD)"

  local classify_output
  classify_output="$({
    cd "$fixture_repo"
    CI_BASE_SHA="$base_sha" CI_HEAD_SHA="$head_sha" "$repo_root/scripts/ci/classify-changes.sh"
  })"

  assert_output "$case_name" "$classify_output" "docs_only=$expected_docs_only"
  assert_output "$case_name" "$classify_output" "source_changed=$expected_source_changed"
}

classify_case root-markdown true false README.md
classify_case docs-markdown true false docs/guide.md
classify_case notes-markdown true false notes/design.md
classify_case source-under-docs false true docs/x/probe.go
classify_case source-under-notes false true notes/x/probe.sh
classify_case ordinary-source false true client.go
classify_case mixed-source false true README.md client.go

indeterminate_output="$({
  cd "$fixture_repo"
  CI_BASE_SHA=not-a-commit CI_HEAD_SHA=HEAD "$repo_root/scripts/ci/classify-changes.sh"
} 2>/dev/null)"
assert_output indeterminate-range "$indeterminate_output" 'docs_only=false'
assert_output indeterminate-range "$indeterminate_output" 'source_changed=true'

require_case() {
  local expected_status="$1"
  local case_name="$2"
  shift 2

  set +e
  env \
    CI_EVENT_NAME=pull_request \
    CI_FORCE_FULL=false \
    CI_MERGED_PR=false \
    CI_REF=refs/pull/1/merge \
    CLASSIFY_RESULT=success \
    DOCS_ONLY=true \
    SOURCE_CHANGED=false \
    DOCS_RESULT=success \
    CORE_RESULT=skipped \
    FLOOR_RESULT=skipped \
    FUZZ_RESULT=skipped \
    CODEQL_RESULT=skipped \
    SECRET_SCAN_RESULT=success \
    RELEASE_RESULT=skipped \
    "$@" \
    "$repo_root/scripts/ci/require-results.sh" >"$result_file" 2>&1
  local actual_status=$?
  set -e

  case "$expected_status:$actual_status" in
    pass:0|fail:[1-9]*)
      ;;
    *)
      printf 'error: %s expected %s, got exit %s\n' "$case_name" "$expected_status" "$actual_status" >&2
      sed 's/^/  /' "$result_file" >&2
      exit 1
      ;;
  esac
}

require_case pass docs-pr
require_case fail docs-pr-unexpected-core CORE_RESULT=success
require_case pass source-pr DOCS_ONLY=false SOURCE_CHANGED=true CORE_RESULT=success FLOOR_RESULT=success FUZZ_RESULT=success CODEQL_RESULT=success
require_case fail source-pr-missing-floor DOCS_ONLY=false SOURCE_CHANGED=true CORE_RESULT=success FLOOR_RESULT=skipped FUZZ_RESULT=success CODEQL_RESULT=success
require_case fail source-pr-missing-fuzz DOCS_ONLY=false SOURCE_CHANGED=true CORE_RESULT=success FLOOR_RESULT=success FUZZ_RESULT=skipped CODEQL_RESULT=success
require_case pass merged-source CI_EVENT_NAME=push CI_MERGED_PR=true DOCS_ONLY=false SOURCE_CHANGED=true CORE_RESULT=success FLOOR_RESULT=skipped SECRET_SCAN_RESULT=skipped
require_case pass full-docs CI_EVENT_NAME=workflow_dispatch CI_FORCE_FULL=true CORE_RESULT=success FLOOR_RESULT=success FUZZ_RESULT=success CODEQL_RESULT=success
require_case fail full-docs-missing-floor CI_EVENT_NAME=workflow_dispatch CI_FORCE_FULL=true CORE_RESULT=success FLOOR_RESULT=skipped FUZZ_RESULT=success CODEQL_RESULT=success
require_case pass tag CI_EVENT_NAME=push CI_REF=refs/tags/v5.1.0-gs.2 DOCS_RESULT=skipped FLOOR_RESULT=success SECRET_SCAN_RESULT=success RELEASE_RESULT=success
require_case fail tag-missing-floor CI_EVENT_NAME=push CI_REF=refs/tags/v5.1.0-gs.2 DOCS_RESULT=skipped FLOOR_RESULT=skipped SECRET_SCAN_RESULT=success RELEASE_RESULT=success

printf 'routing contract: ok\n'
