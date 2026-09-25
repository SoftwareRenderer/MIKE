#!/usr/bin/env bash
# No component talks to the server: `fetch(` lives under src/lib/api/, and a component that calls the
# network itself answers one outage in its own words. What the gate enforces: docs/testing.md → Frontend
# cleanliness gates.
#
# `frozen` is the debt that predates the rule — `count path` per file, a ratchet in both directions: a
# fetch in an unlisted file fails, and so does a listed file holding fewer, so the list moves only with
# the code. The pattern is case-sensitive, which keeps `apiFetch(` and `prefetch(` out of the count.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ -d src ] || { echo "check-fetch: no src/ next to $(dirname "${BASH_SOURCE[0]}")" >&2; exit 1; }

pattern='(^|[^A-Za-z0-9_$])fetch[[:space:]]*\('

frozen=(
    "3 src/lib/components/FileBrowser.svelte"
    "1 src/lib/components/FileBrowserView.svelte"
    "4 src/lib/components/FileEditor.svelte"
    "1 src/lib/components/FileSearchPanel.svelte"
)

frozen_count() {
    local line
    for line in "${frozen[@]}"; do
        [ "${line#* }" = "$1" ] && { echo "${line%% *}"; return; }
    done
    echo 0
}

status=0
paths="$(printf '%s\n' "${frozen[@]#* }"; grep -rlE "$pattern" --include='*.svelte' src | sort)"
for path in $(printf '%s\n' "$paths" | grep '\.svelte$' | sort -u); do
    want="$(frozen_count "$path")"
    got=0
    [ -f "$path" ] && got="$(grep -cE "$pattern" "$path")"
    if [ "$got" -gt "$want" ]; then
        echo "check-fetch: $path puts ${want} fetch(es) on the wire from a component, $got found" >&2
        grep -nE "$pattern" "$path" | sed 's/^/    /' >&2
        status=1
    elif [ "$got" -lt "$want" ]; then
        echo "check-fetch: $path is frozen at $want and holds $got — cross the line out of scripts/check-fetch.sh" >&2
        status=1
    fi
done

if [ "$status" -eq 0 ]; then
    total="$(printf '%s\n' "${frozen[@]}" | awk '{ n += $1 } END { print n + 0 }')"
    echo "check-fetch: ${#frozen[@]} component file(s), $total fetch(es), all of them listed"
fi
exit $status
