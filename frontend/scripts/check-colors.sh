#!/usr/bin/env bash
# A colour literal in a component is a colour no theme can reach.
#
# The palette is `src/app.css`'s `:root` block, and an installed `css` extension retints the whole app by
# overriding those names — `extensions/handlers/theme.ts` forwards every custom property to an extension
# frame, so a token reaches further than any value can. A `#88c0d0` in a component's own `<style>` sits
# outside that channel, and so does the `var(--color-accent-light, #88c0d0)` fallback that names a token whose
# real value is `#60a5fa`: a fallback is one colour written twice, and the sweep found six of them already
# disagreeing with the token beside them, plus ~60 `rgba()` literals repeating a token's colour at a second
# alpha. Alpha over a token is `rgb(from var(--token) r g b / a)`, which keeps the one home.
#
# Rule and gate: docs/testing.md → Frontend cleanliness gates.
#
# Two things are not violations:
#   - `src/app.css`, the palette itself — the one file where a colour value is allowed to be written;
#   - colour that is DATA handed to JS, which cannot carry a `var()` reference: the syntax palette
#     (`src/lib/syntax/token-style.ts`, `src/lib/utils/codemirror-theme.ts`) and the constants modules
#     (`src/lib/constants/badge-colors.ts`, `src/lib/constants/terminal-theme.ts`), each of which mirrors the
#     tokens named beside its values and is pinned by its own unit test. They are `*.ts`, so this scan never
#     reaches them; a new data colour belongs in one of those modules, never inlined in a template.
#
# The allowlist below is a deliberate exception, and it is checked rather than trusted: an entry that stops
# matching fails too, so it is crossed out when the code moves. Each entry is `path|text on the exempt line`.
#
#   src/lib/styles/extensions.css — the stripe border's mask reads a gradient's ALPHA, not its colour: the
#   mask source only has to be opaque, so there is no theme colour for a token to supply here.
#
# This watches hex only, which is what drifted. It does not see `rgb()`/`rgba()`/`hsl()` literals or a colour
# keyword, so a new one of those is the reviewer's question, not this script's.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ -d src ] || { echo "check-colors: no src/ next to $(dirname "${BASH_SOURCE[0]}")" >&2; exit 1; }

pattern='#[0-9a-fA-F]{3,8}\b'

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
