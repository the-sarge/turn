#!/usr/bin/env bash
set -euo pipefail

GOFMT="${GOFMT:-gofmt}"
unformatted="$(mktemp)"
go_files="$(mktemp)"

trap 'rm -f "$unformatted" "$go_files"' EXIT HUP INT TERM

if [[ "$#" -gt 0 ]]; then
	if ! "$GOFMT" -l "$@" >"$unformatted"; then
		echo "error: $GOFMT failed while checking Go formatting." >&2
		exit 1
	fi
else
	if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		echo "error: check-gofmt.sh must run inside a git worktree unless files are passed explicitly." >&2
		exit 1
	fi
	git ls-files -z '*.go' >"$go_files"
	if [[ ! -s "$go_files" ]]; then
		exit 0
	fi
	if ! xargs -0 "$GOFMT" -l <"$go_files" >"$unformatted"; then
		echo "error: $GOFMT failed while checking Go formatting." >&2
		exit 1
	fi
fi

if [[ ! -s "$unformatted" ]]; then
	exit 0
fi

echo "error: Go files are not gofmt-formatted:" >&2
cat "$unformatted" >&2
echo "run: $GOFMT -w <files listed above>" >&2
exit 1
