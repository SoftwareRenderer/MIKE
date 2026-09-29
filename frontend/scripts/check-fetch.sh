#!/usr/bin/env bash
# Nothing outside the API layer puts a request on the wire. `apiFetch` (src/lib/api/helpers.ts) is the one
# place that decides what a failed fetch means, so a second caller answers the same outage in its own words:
# a bare `TypeError`, no offline flag raised, and the write the user just typed is simply lost instead of
# queued. The rule and its two outcomes: docs/reconnection.md → How a request ends.
#
# Two things are not violations:
#   - src/lib/api/, which is the layer this points at;
#   - a test file, whose `fetch(` is a stub or an adversarial payload string, never a request the app makes.
#
# The exemption list below is a deliberate exception, and it is checked rather than trusted: an exempt path
# holding no fetch fails too, so the entry is crossed out when the code moves.
#
#   src/lib/i18n/load-pack.ts — a translation pack that 404s or answers malformed JSON means "English plus a
#   report". apiFetch would instead raise the offline flag and start queueing writes because a pack was
#   missing. docs/i18n.md.
#
# The pattern wants a call position: `fetch` preceded by no identifier character (case-sensitively, so
# `apiFetch` and `prefetch` are other tokens), then `(`, then an argument or the line's end. Prose that names
# `fetch()` with nothing in the parens is not a call — one capability description says so.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ -d src ] || { echo "check-fetch: no src/ next to $(dirname "${BASH_SOURCE[0]}")" >&2; exit 1; }

pattern='(^|[^A-Za-z0-9_$])fetch[[:space:]]*\(([[:space:]]*$|[^[:space:])])'

exempt=(
    "src/lib/i18n/load-pack.ts"
)

scanned="$(find src -type f \( -name '*.svelte' -o -name '*.ts' \) \
    -not -path 'src/lib/api/*' -not -path '*/__tests__/*' | sort)"
# A scan that found nothing is not a clean scan; scripts/svelte-check-once.sh refuses for the same reason.
[ -n "$scanned" ] || { echo "check-fetch: no source file to scan under $PWD/src" >&2; exit 1; }

is_exempt() {
    local path="$1" entry
    for entry in "${exempt[@]}"; do
        [ "$entry" = "$path" ] && return 0
    done
    return 1
}

status=0
while IFS= read -r path; do
    [ -n "$path" ] || continue
    got=0
    [ -f "$path" ] && got="$(grep -cE "$pattern" "$path")"
    if is_exempt "$path"; then
        if [ "$got" -eq 0 ]; then
            echo "check-fetch: $path is exempt and holds no fetch — cross it out of scripts/check-fetch.sh" >&2
            status=1
        fi
    elif [ "$got" -ne 0 ]; then
        echo "check-fetch: $path puts $got request(s) on the wire itself; the api layer owns what a failed one means" >&2
        grep -nE "$pattern" "$path" | sed 's/^/    /' >&2
        status=1
    fi
done <<< "$(printf '%s\n%s\n' "$scanned" "$(printf '%s\n' "${exempt[@]}")" | sort -u)"

if [ "$status" -eq 0 ]; then
    total="$(cat "${exempt[@]}" | grep -cE "$pattern")"
    echo "check-fetch: $(grep -c . <<<"$scanned") file(s) outside lib/api/ hold no fetch; the ${#exempt[@]} exempt file(s) hold $total"
fi
exit $status
