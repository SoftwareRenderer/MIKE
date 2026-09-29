#!/usr/bin/env bash
# The Go this working tree changed — staged, unstaged or untracked — as package directories (the
# default) or as file paths (`files`). `make test-diff` and `make check-fast` vet and test exactly the
# default list.
#
# Directory names, not file names, because that is what `go test` takes. The root package is `.` (the
# spelling `./` is a different directory to the go tool). A directory whose Go files were all deleted,
# or that holds none — a path under a comment, a moved file — is dropped, since `go test` answers
# "no Go files" for it.
#
# Scoped like every other Go command here: extensions/ holds pulled artifacts from another module and
# frontend/ holds no Go, so neither is a package of this tree.
#
# `files` is the list `gofmt` is asked about, so it covers what `make lint` formats — backend, pkg,
# scripts and the root files — and prints one existing path per line. The directory list stops at
# GO_SCOPE, where scripts/ has its own targets (`make test-archgate`, `make test-i18n-parity`).
#
# Usage: touched-packages.sh [dirs|files]
set -uo pipefail

case "${1:-dirs}" in
dirs | files) mode=${1:-dirs} ;;
*)
    echo 'usage: touched-packages.sh [dirs|files]' >&2
    exit 2
    ;;
esac

touched() {
    git diff --name-only HEAD -- '*.go' 2>/dev/null
    git ls-files --others --exclude-standard -- '*.go'
}

if [ "$mode" = files ]; then
    touched | grep -E '^([^/]+\.go|backend/|pkg/|scripts/)' | sort -u | while read -r file; do
        [ -f "$file" ] && printf '%s\n' "$file"
    done
    # A tree with no changed .go file has nothing to format. grep exits 1 on no match; an empty list is
    # the answer, not a failure.
    exit 0
fi

touched | sed -e 's|[^/]*\.go$||' -e 's|^$|.|' | sort -u | while read -r dir; do
    case "$dir" in
    .) ls ./*.go >/dev/null 2>&1 && printf '. ' ;;
    backend/* | pkg/*) ls "$dir"/*.go >/dev/null 2>&1 && printf './%s ' "$dir" ;;
    esac
done
echo
