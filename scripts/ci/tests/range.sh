#!/usr/bin/env bash
set -euo pipefail

# Exercise the public script entrypoints against real, disposable Git history.
repo_root="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/turn-ci-range.XXXXXX")"
trap 'rm -rf -- "$fixture"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
cd "$fixture"
git init -q --initial-branch=main
git config user.name 'CI range fixture'
git config user.email 'ci-range@example.invalid'
# Inherited whitespace is harmless unless a comparison reintroduces it.
printf 'initial \n' > README.md
git add README.md
git commit -qm initial
initial="$(git rev-parse HEAD)"
printf 'documentation\n' >> README.md
git commit -qam documentation
docs="$(git rev-parse HEAD)"
printf 'package fixture\n' > code.go
git add code.go
git commit -qm code
code="$(git rev-parse HEAD)"
printf 'trailing whitespace \n' >> README.md
git commit -qam whitespace
whitespace="$(git rev-parse HEAD)"
# A sibling fixes inherited whitespace and adds code. Endpoint comparison
# reintroduces whitespace and removes code; merge-base comparison adds only
# clean documentation. This distinguishes both callers' comparison policies.
git checkout -q --detach "$initial"
printf 'initial\n' > README.md
printf 'package sibling\n' > sibling.go
git add README.md sibling.go
git commit -qm sibling
sibling="$(git rev-parse HEAD)"
disconnected="$(printf 'disconnected\n' | git commit-tree "$docs^{tree}")"
git checkout -q --detach "$docs"
git branch -f main "$initial"
git update-ref refs/remotes/origin/main "$initial"

cases=0
check() {
  local name="$1" expected_class="$2" expected_docs="$3" output status
  shift 3
  local environment=(env CI_BASE_SHA= CI_HEAD_SHA= CI_DEFAULT_BRANCH= CI_REMOTE=
    CI_DOCS_GLOBS= GITHUB_BASE_REF= GITHUB_OUTPUT="$fixture/output" "$@")
  : > "$fixture/output"
  output="$("${environment[@]}" bash "$repo_root/scripts/ci-classify.sh" 2> "$fixture/classifier.err")"
  if [[ "$output" != "docs_only=$expected_class" ]] ||
    [[ "$(cat "$fixture/output")" != "docs_only=$expected_class" ]]; then
    printf 'FAIL %s: classifier returned %s\n' "$name" "$output" >&2
    exit 1
  fi
  status=0
  "${environment[@]}" bash "$repo_root/scripts/ci/check-docs.sh" > "$fixture/docs.out" 2> "$fixture/docs.err" || status=$?
  if [[ "$status" != "$expected_docs" ]]; then
    printf 'FAIL %s: docs status %s, expected %s\n' "$name" "$status" "$expected_docs" >&2
    cat "$fixture/docs.err" >&2
    exit 1
  fi
  cases=$((cases + 1))
  printf 'PASS %s\n' "$name"
}

check 'explicit docs endpoints' true 0 CI_BASE_SHA="$initial" CI_HEAD_SHA="$docs"
check 'explicit code endpoint overrides HEAD' false 0 CI_BASE_SHA="$initial" CI_HEAD_SHA="$code"
check 'explicit base overrides unavailable branch' true 0 CI_BASE_SHA="$initial" CI_DEFAULT_BRANCH=missing
check 'remote main fallback' true 0
check 'empty diff' false 0 CI_BASE_SHA="$docs"
check 'invalid base' false 1 CI_BASE_SHA=missing
check 'invalid head' false 1 CI_HEAD_SHA=missing
check 'unavailable branch' false 1 CI_DEFAULT_BRANCH=missing
check 'disconnected history' false 1 CI_BASE_SHA="$disconnected"
check 'divergent endpoint versus merge-base policy' false 0 CI_BASE_SHA="$sibling"
check 'committed whitespace' true 2 CI_BASE_SHA="$code" CI_HEAD_SHA="$whitespace"
check 'custom documentation globs' false 0 CI_BASE_SHA="$initial" CI_DOCS_GLOBS='docs/*'
printf 'dirty whitespace \n' >> README.md
check 'working-tree whitespace' true 2 CI_BASE_SHA="$initial"
git restore README.md

git update-ref refs/remotes/origin/topic "$docs"
git symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/topic
check 'remote HEAD discovery' false 0
check 'GitHub base overrides remote HEAD' true 0 GITHUB_BASE_REF=main
check 'explicit branch overrides GitHub base' false 0 CI_DEFAULT_BRANCH=topic GITHUB_BASE_REF=main
git update-ref refs/remotes/mirror/main "$initial"
check 'custom remote' true 0 CI_REMOTE=mirror
# Prefer the remote branch even if a same-named local branch differs.
git branch topic "$initial"
check 'remote branch precedes local branch' false 0 CI_DEFAULT_BRANCH=topic
git update-ref -d refs/remotes/origin/topic
check 'local branch fallback' true 0 CI_DEFAULT_BRANCH=topic
check 'local main fallback without remote' true 0 CI_REMOTE=absent
printf '%s range behavior cases passed\n' "$cases"
