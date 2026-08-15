#!/usr/bin/env bash
set -euo pipefail

base="${CI_BASE_SHA:-${1:-}}"
head="${CI_HEAD_SHA:-${2:-HEAD}}"

if [[ -z "$base" ]] || ! git rev-parse --verify -q "$base^{commit}" >/dev/null || ! git rev-parse --verify -q "$head^{commit}" >/dev/null; then
  printf 'warning: unable to determine a trustworthy diff; failing closed as source-affecting\n' >&2
  changed_files=""
  indeterminate=true
elif ! merge_base="$(git merge-base "$base" "$head")"; then
  printf 'warning: unable to determine a trustworthy merge base; failing closed as source-affecting\n' >&2
  changed_files=""
  indeterminate=true
else
  changed_files="$(git diff --name-only --no-renames "$merge_base" "$head")"
  indeterminate=false
fi

changed_count=0
docs_only=true
source_changed=false
dependencies_changed=false
workflows_changed=false
platform_changed=false
release_changed=false

if [[ "$indeterminate" == true || -z "$changed_files" ]]; then
  docs_only=false
  source_changed=true
else
  while IFS= read -r changed_path; do
    [[ -n "$changed_path" ]] || continue
    changed_count=$((changed_count + 1))
    path_is_docs=false

    case "$changed_path" in
      *.md)
        path_is_docs=true
        ;;
      *)
        docs_only=false
        source_changed=true
        ;;
    esac

    case "$changed_path" in
      go.mod|go.sum)
        dependencies_changed=true
        ;;
    esac

    case "$changed_path" in
      .github/workflows/*|.github/actions/*|.github/dependabot.yml|.gitleaks.toml|.gitleaksignore|.golangci.yml|.golangci.yaml|Taskfile.yml|Taskfile.yaml|scripts/*)
        workflows_changed=true
        ;;
    esac

    if [[ "$path_is_docs" == false ]]; then
      case "$changed_path" in
        *_windows.go|*_darwin.go|*_linux.go|*_unix.go|*windows*|*darwin*|*macos*|Dockerfile|Dockerfile.*|docker/*)
          platform_changed=true
          ;;
      esac
    fi

    case "$changed_path" in
      CHANGELOG.md|.goreleaser.yml|.goreleaser.yaml|.github/workflows/release.yml|.github/workflows/release.yaml)
        release_changed=true
        ;;
    esac
  done <<< "$changed_files"
fi

emit() {
  local name="$1"
  local value="$2"

  printf '%s=%s\n' "$name" "$value"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    printf '%s=%s\n' "$name" "$value" >> "$GITHUB_OUTPUT"
  fi
}

emit changed_count "$changed_count"
emit docs_only "$docs_only"
emit source_changed "$source_changed"
emit dependencies_changed "$dependencies_changed"
emit workflows_changed "$workflows_changed"
emit platform_changed "$platform_changed"
emit release_changed "$release_changed"

if [[ -n "$changed_files" ]]; then
  printf '%s\n' "$changed_files" | sed 's/^/changed: /'
fi
