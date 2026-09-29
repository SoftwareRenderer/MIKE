#!/usr/bin/env bash
# The Go packages holding a .go file this working tree changed — staged, unstaged or untracked.
# `make test-diff` runs the tests of exactly these.
#
# Directory names, not file names, because that is what `go test` takes. The root package is `.` (the
# spelling `./` is a different directory to the go tool). A directory whose Go files were all deleted,
# or that holds none — a path under a comment, a moved file — is dropped, since `go test` answers
# "no Go files" for it.
#
# Scoped like every other Go command here: extensions/ holds pulled artifacts from another module and
# frontend/ holds no Go, so neither is a package of this tree.
set -uo pipefail

{
    git diff --name-only HEAD -- '*.go' 2>/dev/null
    git ls-files --others --exclude-standard -- '*.go'
} | sed -e 's|[^/]*\.go$||' -e 's|^$|.|' | sort -u | while read -r dir; do
    case "$dir" in
        .)        ls ./*.go            >/dev/null 2>&1 && printf '. ' ;;
        backend/*|pkg/*) ls "$dir"/*.go >/dev/null 2>&1 && printf './%s ' "$dir" ;;
    esac
done
echo
