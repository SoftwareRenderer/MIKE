#!/usr/bin/env bash
# The frontend files this working tree changed — staged, unstaged or untracked — one path per line,
# relative to `frontend/`, which is the directory every consumer of this list runs in (`npm run
# test:related`, `npm run check`). `make check-fast` decides its frontend lane from whether this is empty.
#
# The same union of git states as scripts/touched-packages.sh, and a file whose last change deleted it is
# dropped: `vitest related` reads the files it is given.
#
# Excluded, because this is the iteration lane and not the gate: `tests/` (the e2e suite runs under
# `npx playwright test`, priced in docs/testing.md), `dist/` (build output — `make frontend` empties and
# refills it, and that is never an edit to check), and `node_modules/` (gitignored, so git never names it).
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

{
    git diff --name-only HEAD -- frontend 2>/dev/null
    git ls-files --others --exclude-standard -- frontend
} | sed 's|^frontend/||' | grep -v -e '^tests/' -e '^dist/' -e '^node_modules/' |
    sort -u | while read -r file; do
        [ -f "frontend/$file" ] && printf '%s\n' "$file"
    done
# An unedited tree has nothing to report, and grep exits 1 on no match: an empty list is the answer.
exit 0
