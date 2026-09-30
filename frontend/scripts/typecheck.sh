#!/usr/bin/env bash
# The one type-check `make check` runs, and the only place a type error is claimed absent.
#
# `svelte-check` over `tsconfig.unused.json`, which adds `noUnusedLocals` and `noUnusedParameters` to
# `tsconfig.json` and changes no `include`: it finds everything `npm run check` finds, so running both would
# read the same 1340 files twice. It covers test files too, because `tsconfig.json` includes `src/**/*.ts`.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

machine="$(npx svelte-check --tsconfig ./tsconfig.unused.json --output machine)"

# No summary over a non-empty file set means svelte-check analyzed nothing, which must not read as clean.
if ! grep -qE 'COMPLETED [1-9][0-9]* FILES' <<<"$machine"; then
    echo "typecheck: svelte-check produced no summary — the check did not run" >&2
    exit 1
fi

# Names starting with `_` are exempt from the unused-declaration messages only: TypeScript exempts
# `_`-prefixed parameters but not locals, and these are `$effect` subscription reads where the read is the
# purpose — deleting it unsubscribes the effect. No other message is exempt.
exempt="'_[A-Za-z0-9_]*' is (declared but its value is never read|never used)"

files="$(grep -oE 'COMPLETED [0-9]+ FILES' <<<"$machine" | awk '{print $2}')"

reportable="$(grep -E '^[0-9]+ ERROR ' <<<"$machine" | grep -vE "$exempt" \
    | sed -E 's/^[0-9]+ ERROR "([^"]+)" ([0-9]+):[0-9]+ "(.*)"$/\1:\2 \3/')"

if [ -n "$reportable" ]; then
    printf '%s\n' "$reportable"
    echo "typecheck: $(grep -c . <<<"$reportable") error(s) over ${files} files" >&2
    exit 1
fi

exempted="$(grep -E '^[0-9]+ ERROR ' <<<"$machine" | grep -cE "$exempt")"

echo "typecheck: clean over ${files} files; ${exempted} \`_\`-prefixed read(s) exempt as an \$effect subscription"
