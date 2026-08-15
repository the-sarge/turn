#!/usr/bin/env bash
set -euo pipefail

# CI routing contract for .github/workflows/ci.yml. yq (an authoritative
# YAML parser) owns the representation; every assertion here reads parsed
# structure, never raw text. The contract prevents automatic pull-request
# certification from returning: certification must stay one-shot behind the
# ci-certify label, and every job must stay bounded by a timeout.

YQ="${YQ:-yq}"
workflow=".github/workflows/ci.yml"

fail() {
  printf 'error: workflow contract: %s\n' "$1" >&2
  exit 1
}

yq_read() {
  # shellcheck disable=SC2086
  $YQ eval "$1" "$workflow"
}

[[ -f "$workflow" ]] || fail "$workflow is missing"

workflow_count="$(git ls-files '.github/workflows/*' | wc -l | tr -d ' ')"
[[ "$workflow_count" == 1 ]] || fail "expected exactly one workflow file, found $workflow_count"

pr_types="$(yq_read '.on.pull_request.types | join(",")')"
[[ "$pr_types" == "labeled" ]] || fail "on.pull_request.types must be exactly [labeled], got: $pr_types"

pr_branches="$(yq_read '.on.pull_request.branches | join(",")')"
[[ "$pr_branches" == "main" ]] || fail "on.pull_request.branches must be exactly [main], got: $pr_branches"

prt="$(yq_read 'has("on") and (.on | has("pull_request_target"))')"
[[ "$prt" == "false" ]] || fail "pull_request_target must not be used"

concurrency_group="$(yq_read '.concurrency.group')"
[[ -n "$concurrency_group" && "$concurrency_group" != "null" ]] || fail "workflow-level concurrency.group is required"

missing_timeouts="$(yq_read '[.jobs | to_entries[] | select(.value | has("timeout-minutes") | not) | .key] | join(",")')"
[[ -z "$missing_timeouts" ]] || fail "jobs missing timeout-minutes: $missing_timeouts"

label_guard="$(yq_read '.jobs.classify.steps[0].env.CI_CERTIFICATION_LABEL')"
[[ "$label_guard" == "ci-certify" ]] || fail "classify must bind the ci-certify certification label, got: $label_guard"

aggregate_needs="$(yq_read '.jobs.ci_required.needs | join(",")')"
for required_job in classify docs core floor fuzz codeql secret_scan release; do
  [[ ",$aggregate_needs," == *",$required_job,"* ]] || fail "ci_required.needs must include $required_job"
done

printf 'workflow contract: ok\n'
