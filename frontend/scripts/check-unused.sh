#!/usr/bin/env bash
# Unused declarations, one step stricter than `npm run check`: noUnusedLocals + noUnusedParameters.
#
# Names starting with `_` are exempt because TypeScript exempts `_`-prefixed parameters natively but not
# locals: they are `$effect` subscription reads (`const _v = uiInjector.getPointVersion(...)`) and `_`
# destructuring placeholders, where the read is the purpose and deleting it unsubscribes the effect.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

machine="$(npx svelte-check --tsconfig ./tsconfig.unused.json --output machine)"

# No summary over a non-empty file set means svelte-check analyzed nothing, which must not read as clean.
if ! grep -qE 'COMPLETED [1-9][0-9]* FILES' <<<"$machine"; then
    echo "check-unused: svelte-check produced no summary — the check did not run" >&2
    exit 1
fi

hits="$(sed -nE "s/^[0-9]+ ERROR \"([^\"]+)\" ([0-9]+):[0-9]+ \"'([^']+)' is declared but (its value is never read|never used)\..*$/\1:\2 \3/p" \
    <<<"$machine" | grep -vE ' _[A-Za-z0-9_]*$')"

if [ -n "$hits" ]; then
    printf '%s\n' "$hits"
    echo "check-unused: $(grep -c . <<<"$hits") declaration(s) nothing reads" >&2
    exit 1
fi
