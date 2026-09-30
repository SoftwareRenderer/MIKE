#!/usr/bin/env bash
# The frontend source files this working tree changed — staged, unstaged or untracked — as paths relative to
# frontend/. `make test-frontend-diff` feeds these to `vitest related`, which runs the unit files importing them.
#
# Only a file a unit test can import is listed: `vitest related` answers an empty run for a config or a shell
# script and the WHOLE suite for a `package.json`, so handing it either is a full run wearing a diff's clothes.
# A path that no longer exists is dropped for the same reason.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1

{
    git diff --name-only HEAD -- 'frontend/src/' 2>/dev/null
    git ls-files --others --exclude-standard -- 'frontend/src/'
} | sed -n 's|^frontend/||p' | grep -E '\.(ts|js|svelte)$' | sort -u | while read -r file; do
    [ -f "frontend/$file" ] && printf '%s\n' "$file"
done

# Nothing under src/ changed is a normal answer, not a failed one.
exit 0
