#!/usr/bin/env bash
set -euo pipefail

require_result() {
  local label="$1"
  local expected="$2"
  local actual="$3"

  if [[ "$actual" != "$expected" ]]; then
    printf 'error: %s result was %s, expected %s\n' "$label" "${actual:-unset}" "$expected" >&2
    exit 1
  fi
}

require_result classify success "${CLASSIFY_RESULT:-}"

case "${CI_FORCE_FULL:-false}" in
  true|false)
    ;;
  *)
    printf 'error: CI_FORCE_FULL must be true or false\n' >&2
    exit 1
    ;;
esac

case "${CI_MERGED_PR:-false}" in
  true)
    require_result secret-scan skipped "${SECRET_SCAN_RESULT:-}"
    ;;
  false)
    require_result secret-scan success "${SECRET_SCAN_RESULT:-}"
    ;;
  *)
    printf 'error: CI_MERGED_PR must be true or false\n' >&2
    exit 1
    ;;
esac

run_deep=false
if [[ "${CI_EVENT_NAME:-}" == schedule || "${CI_FORCE_FULL:-false}" == true ]]; then
  run_deep=true
fi

require_deep_pair() {
  # codeql and fuzz share routing: they run for source-affecting work and
  # deep (schedule/full) runs, and are skipped for merged-push smoke.
  local expected="$1"
  require_result codeql "$expected" "${CODEQL_RESULT:-}"
  require_result fuzz "$expected" "${FUZZ_RESULT:-}"
}

case "${CI_REF:-}" in
  refs/tags/*)
    require_result docs skipped "${DOCS_RESULT:-}"
    require_result core skipped "${CORE_RESULT:-}"
    require_result floor success "${FLOOR_RESULT:-}"
    require_deep_pair skipped
    require_result release success "${RELEASE_RESULT:-}"
    ;;
  *)
    require_result docs success "${DOCS_RESULT:-}"
    require_result release skipped "${RELEASE_RESULT:-}"

    case "${DOCS_ONLY:-}" in
      true)
        if [[ "$run_deep" == true ]]; then
          require_result core success "${CORE_RESULT:-}"
          require_result floor success "${FLOOR_RESULT:-}"
          require_deep_pair success
        else
          require_result core skipped "${CORE_RESULT:-}"
          require_result floor skipped "${FLOOR_RESULT:-}"
          require_deep_pair skipped
        fi
        ;;
      false)
        require_result core success "${CORE_RESULT:-}"
        if [[ "${CI_MERGED_PR:-false}" == true ]]; then
          require_result floor skipped "${FLOOR_RESULT:-}"
          require_deep_pair skipped
        else
          require_result floor success "${FLOOR_RESULT:-}"
          case "${SOURCE_CHANGED:-}" in
            true)
              require_deep_pair success
              ;;
            false)
              if [[ "$run_deep" == true ]]; then
                require_deep_pair success
              else
                require_deep_pair skipped
              fi
              ;;
            *)
              printf 'error: SOURCE_CHANGED must be true or false\n' >&2
              exit 1
              ;;
          esac
        fi
        ;;
      *)
        printf 'error: DOCS_ONLY must be true or false\n' >&2
        exit 1
        ;;
    esac
    ;;
esac

printf 'required CI lanes completed successfully\n'
