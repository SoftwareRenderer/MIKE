#!/usr/bin/env bash
# A colour literal in a component is a colour no theme can reach.
#
# The palette is `src/app.css`'s `:root` block, and an installed `css` extension retints the app by
# overriding those names — `extensions/handlers/theme.ts` forwards every custom property to an extension
# frame, so a token reaches further than any value can. A value written in a component's own `<style>` keeps
# the base palette's colour after a theme changes it, in the app chrome and inside every extension frame.
#
# Four spellings of that escape are watched:
#   1. hex, including a `var(--token, #hex)` fallback: one colour written twice, able to disagree with the
#      token beside it.
#   2. `rgb()`/`rgba()`/`hsl()` literals — a token's colour repeated at an alpha of their own. Alpha over a
#      token is `rgb(from var(--token) r g b / a)`.
#   3. a colour keyword — `color: white` where `--color-btn-text` is the palette's name for it.
#   4. `var(--token)` glued to a hex alpha pair (`var(--color-accent)22`). It parses only while the token is
#      textually hex, so a theme answering `rgb(…)` or `oklch(…)` drops the tint instead of retinting it.
#
# Not violations: `src/app.css` (the palette itself); colour that is DATA handed to JS, which cannot carry a
# `var()` reference — the syntax palette and the `src/lib/constants/` colour modules, which are `*.ts` and so
# never reach this scan, and where a new data colour belongs; and `color-mix(in srgb, var(--token) N%,
# transparent)`, which names the token.
#
# The allowlist below is checked rather than trusted: an entry that stops matching fails too, so it is
# crossed out when the code moves. Each entry is `path|text on the exempt line`.
#
#   src/lib/styles/extensions.css — the stripe border's mask reads a gradient's ALPHA, not its colour, so
#   the mask source only has to be opaque and there is no theme colour for a token to supply here.
#
# Rule and gate: docs/testing.md → Frontend cleanliness gates.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ -d src ] || { echo "check-colors: no src/ next to $(dirname "${BASH_SOURCE[0]}")" >&2; exit 1; }

KEYWORDS='white|black|red|green|blue|yellow|orange|purple|gray|grey'
# Families 1-4 above. `rgb(from var(…))` starts with `from`, not a digit, so family 2's answer is not caught.
pattern="#[0-9a-fA-F]{3,8}\b|rgba?\([0-9]|hsla?\([0-9]|:[[:space:]]*(${KEYWORDS})[[:space:]]*[;}\"']|solid[[:space:]]+(${KEYWORDS})\b|var\(--[a-z0-9-]+\)[0-9a-fA-F]{2}([^0-9a-zA-Z%]|\$)"

allow=(
    "src/lib/styles/extensions.css|linear-gradient(#fff 0 0)"
)

# Where chrome paints: components, and the stylesheets they import. Not `*.ts` — see the data-colour note above.
scanned="$(find src -type f \( -name '*.svelte' -o -name '*.css' \) -not -path 'src/app.css' | sort)"
# A scan that found nothing is not a clean scan; typecheck.sh refuses for the same reason.
[ -n "$scanned" ] || { echo "check-colors: no source file to scan under $PWD/src" >&2; exit 1; }

matched=()
for ((i = 0; i < ${#allow[@]}; i++)); do matched+=(0); done

offences=""
offence_count=0
exempt_count=0
while IFS= read -r path; do
    [ -n "$path" ] || continue
    # Read from stdin so grep prints `line:text` and the path stays the one this loop chose.
    while IFS= read -r hit; do
        [ -n "$hit" ] || continue
        lineno="${hit%%:*}"
        text="${hit#*:}"
        exempt=0
        for ((i = 0; i < ${#allow[@]}; i++)); do
            [ "${allow[i]%%|*}" = "$path" ] || continue
            if grep -qF -- "${allow[i]#*|}" <<<"$text"; then
                exempt=1
                matched[i]=1
                exempt_count=$((exempt_count + $(grep -oE "$pattern" <<<"$text" | wc -l)))
                break
            fi
        done
        if [ "$exempt" -eq 0 ]; then
            offences+="$path:$lineno: ${text#"${text%%[![:space:]]*}"}"$'\n'
            offence_count=$((offence_count + $(grep -oE "$pattern" <<<"$text" | wc -l)))
        fi
    done < <(grep -nE "$pattern" < "$path")
done <<< "$scanned"

status=0
for ((i = 0; i < ${#allow[@]}; i++)); do
    if [ "${matched[i]}" -eq 0 ]; then
        echo "check-colors: '${allow[i]}' is exempt and holds no colour literal — cross it out of scripts/check-colors.sh" >&2
        status=1
    fi
done

if [ -n "$offences" ]; then
    printf '%s' "$offences" | sed 's/^/    /' >&2
    echo "check-colors: $offence_count colour literal(s) in $(grep -c . <<<"${offences%$'\n'}") line(s); the palette in src/app.css owns every colour" >&2
    exit 1
fi

if [ "$status" -eq 0 ]; then
    palette="$(grep -oE "$pattern" < src/app.css | wc -l)"
    echo "check-colors: $(grep -c . <<<"$scanned") file(s) outside src/app.css hold no colour literal; the palette holds $palette and the ${#allow[@]} exempt rule(s) hold $exempt_count"
fi
exit $status
