#!/usr/bin/env bash
# One svelte-check compile, both typecheck verdicts.
#
# `npm run check` and `npm run check:unused` are two answers out of one compile: tsconfig.unused.json is
# tsconfig.json plus noUnusedLocals + noUnusedParameters, so the strict run reports every diagnostic the
# looser one reports plus the unused class. `npm run check:relation` (this script's `relation` mode)
# re-measures that relation on a tree that is red on purpose, and it is the check behind everything below.
#
# The machine report is cached, so the two typecheck steps of `make check` cost one compile. A report is
# reused only when the fingerprint of the typechecked file contents matches. mtimes are not in the
# fingerprint: a checkout or a `cp -a` moves them without moving a byte, and a stale report read as clean is
# the one failure this script must not have. The `COMPLETED … FILES` guard is re-checked on a cached report.
#
# Names starting with `_` are exempt from the unused class. TypeScript exempts `_`-prefixed parameters
# natively but not locals, and those locals are `$effect` subscription reads
# (`const _v = uiInjector.getPointVersion(...)`) and `_` destructuring placeholders, where the read is the
# purpose and deleting it unsubscribes the effect.
#
# Usage: svelte-check-once.sh <check|unused|relation>
#   check    every error except the unused class — the verdict `npm run check` gives
#   unused   the unused class minus the `_` exemption — the verdict `npm run check:unused` gives
#   relation re-prove the set relation with a scratch file (runs alone, two compiles)
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

CACHE_DIR=${MIKE_SVELTE_CACHE:-node_modules/.cache/svelte-check-once}
REPORT=$CACHE_DIR/machine.txt
STRICT_TS=${MIKE_STRICT_TSCONFIG:-tsconfig.unused.json}
LOOSE_TS=${MIKE_LOOSE_TSCONFIG:-tsconfig.json}
SVELTE_CHECK=${MIKE_SVELTE_CHECK:-node_modules/.bin/svelte-check}

# Every file svelte-check reads, by content: the tsconfig `include` globs, the configs that decide what those
# files mean, and the lockfile, because a dependency upgrade moves the type surface without moving a source
# file. A path the include list does not reach only costs an extra compile. A missing src/ is refused rather
# than fingerprinted as empty — an empty file set is the one report that must never read as clean.
fingerprint() {
    if [ ! -d src ]; then
        echo 'svelte-check-once: no src/ under the workspace — refusing to call that checked' >&2
        return 1
    fi
    local -a dirs=(src)
    [ -d scripts ] && dirs+=(scripts)
    {
        find "${dirs[@]}" -type f -not -path '*/node_modules/*' \
            \( -name '*.ts' -o -name '*.svelte' \) -print0 |
            LC_ALL=C sort -z | xargs -0 -r sha256sum
        sha256sum tsconfig.json "$STRICT_TS" "$LOOSE_TS" svelte.config.js package.json package-lock.json \
            vite.config.ts vite-env.d.ts
    } | sha256sum | cut -d' ' -f1
}

# run_machine <tsconfig> <outfile> — the machine report to <outfile>, svelte-check's exit as the status.
# stderr goes to a file: on a green run it is noise, on a red one it is the reason, and the caller shows it.
run_machine() {
    local ts=$1 out=$2
    "$SVELTE_CHECK" --tsconfig "./$ts" --output machine >"$out" 2>"$CACHE_DIR/stderr.log"
}

# verdict <mode> <report-file> <label> — both answers come out of one machine report. Warnings are printed
# and decide nothing: svelte-check exits 0 with warnings unless --fail-on-warnings, which `npm run check`
# never passed.
verdict() {
    python3 - "$1" "$2" "$3" <<'PY'
import json, re, sys

mode, report_path, label = sys.argv[1], sys.argv[2], sys.argv[3]
FIELD = r'"(?:[^"\\]|\\.)*"'
LINE = re.compile(r'^\d+ (ERROR|WARNING) (' + FIELD + r') (\d+):(\d+) (' + FIELD + r')$')
# The two messages noUnusedLocals/noUnusedParameters produce. They are `check:unused`'s question, so `check`
# never reports them: the looser config never emitted them.
UNUSED = re.compile(r"'([^']+)' is declared but (?:its value is never read|never used)\.")
EXEMPT = re.compile(r'^_[A-Za-z0-9_]*$')

errors, warnings, summary, failure = [], [], None, None
with open(report_path, errors='replace') as fh:
    for line in fh:
        line = line.rstrip('\n')
        if line.startswith('#'):
            continue
        m = LINE.match(line)
        if m:
            item = (json.loads(m.group(2)), int(m.group(3)), int(m.group(4)), json.loads(m.group(5)))
            (errors if m.group(1) == 'ERROR' else warnings).append(item)
        elif line.endswith('FILES_WITH_PROBLEMS'):
            summary = line
        elif ' FAILURE ' in line:
            failure = line

if failure:
    print('%s: svelte-check reported a failure: %s' % (label, failure), file=sys.stderr)
    sys.exit(1)
# No summary over a non-empty file set means svelte-check analyzed nothing, which must not read as clean.
if not summary or not re.search(r'COMPLETED [1-9][0-9]* FILES', summary):
    print('%s: svelte-check produced no summary — the check did not run' % label, file=sys.stderr)
    sys.exit(1)

unused_hits, other = [], []
for item in errors:
    m = UNUSED.search(item[3])
    if m:
        if not EXEMPT.match(m.group(1)):
            unused_hits.append(item)
    else:
        other.append(item)

if mode == 'unused':
    for path, line, _col, message in unused_hits:
        name = UNUSED.search(message).group(1)
        print('%s:%s %s' % (path, line, name))
    if unused_hits:
        print('%s: %d declaration(s) nothing reads' % (label, len(unused_hits)), file=sys.stderr)
        sys.exit(1)
    sys.exit(0)

for path, line, col, message in other:
    print('%s:%d:%d\nError: %s\n' % (path, line, col, message))
for path, line, col, message in warnings:
    print('%s:%d:%d\nWarn: %s\n' % (path, line, col, message))
print('====================================')
print('svelte-check found %d errors and %d warnings' % (len(other), len(warnings)))
sys.exit(1 if other else 0)
PY
}

# relation: the set relation the rest of this script rests on, measured on demand.
relation() {
    local scratch=src/__svelte_check_once_relation__.svelte
    local base=$CACHE_DIR/relation-loose.txt strict=$CACHE_DIR/relation-strict.txt
    if [ -e "$scratch" ]; then
        echo "relation: $scratch already exists — a killed run left it, remove it first" >&2
        return 1
    fi
    mkdir -p "$CACHE_DIR" || exit 1
    # Three diagnostics at once: one only the strict config reports, one both report, one warning.
    cat >"$scratch" <<'SCRATCH'
<script lang="ts">
    let scratchUnusedLocal = 1;
    export const scratchWrongType: number = 'not a number';
</script>

<img src="x.png" />
SCRATCH
    trap 'rm -f "$scratch"' EXIT INT TERM

    run_machine "$LOOSE_TS" "$base"
    local loose_rc=$?
    run_machine "$STRICT_TS" "$strict"
    local strict_rc=$?
    rm -f "$scratch"
    trap - EXIT INT TERM

    python3 - "$base" "$strict" "$loose_rc" "$strict_rc" "$scratch" <<'PY'
import json, re, sys

FIELD = r'"(?:[^"\\]|\\.)*"'
LINE = re.compile(r'^\d+ (ERROR|WARNING) (' + FIELD + r') (\d+):(\d+) (' + FIELD + r')$')
UNUSED = re.compile(r"'[^']+' is declared but (?:its value is never read|never used)\.")


def read(path):
    errs, warns, odd = set(), set(), []
    with open(path, errors='replace') as fh:
        for line in fh:
            line = line.rstrip('\n')
            if line.startswith('#') or ' START ' in line or line.endswith('FILES_WITH_PROBLEMS'):
                continue
            m = LINE.match(line)
            if m:
                item = (json.loads(m.group(2)), int(m.group(3)), json.loads(m.group(4)), json.loads(m.group(5)))
                (errs if m.group(1) == 'ERROR' else warns).add(item)
            else:
                odd.append(line)
    return errs, warns, odd


loose, loosew, odd_l = read(sys.argv[1])
strict, strictw, odd_s = read(sys.argv[2])
scratch = sys.argv[5]
lost = loose - strict
added = strict - loose
stray = sorted(e for e in added if not UNUSED.search(e[3]))
# The injected type error names no identifier in its message, so the scratch file and the phrase identify it.
type_err_loose = [e for e in loose if e[0] == scratch and 'not assignable' in e[3]]
type_err_strict = [e for e in strict if e[0] == scratch and 'not assignable' in e[3]]
unused_added = [e for e in added if 'scratchUnusedLocal' in e[3]]
unused_lost = [e for e in lost if 'scratchUnusedLocal' in e[3]]

print('  errors   : loose %d, strict %d   (exit %s / %s)' % (len(loose), len(strict), sys.argv[3], sys.argv[4]))
print('  warnings : loose %d, strict %d' % (len(loosew), len(strictw)))
print('  loose \\ strict : %d — must be 0, nothing the looser config saw may be lost' % len(lost))
print('  strict \\ loose : %d, of which outside the unused class: %d — both must hold the rule'
      % (len(added), len(stray)))
print('  injected type error seen by the looser config   : %s (must be yes)'
      % ('yes' if type_err_loose and type_err_strict else 'NO'))
print('  injected unused local seen by the looser config : %s (must be no)'
      % ('yes' if unused_lost else 'no'))
print('  warning sets identical under both configs : %s' % ('yes' if loosew == strictw else 'NO'))
for line in (odd_l + odd_s)[:6]:
    print('  unexpected line: %s' % line)
for e in sorted(lost)[:10]:
    print('    lost : %s:%d %s' % (e[0], e[1], e[3]))
for e in stray[:10]:
    print('    stray: %s:%d %s' % (e[0], e[1], e[3]))
bad = (lost or stray or not added or not type_err_loose or not type_err_strict
       or not unused_added or unused_lost or loosew != strictw or odd_l or odd_s)
print('relation: %s' % ('FAILED — the strict set is not the looser set plus the unused class'
                        if bad else 'strict = loose + the unused class, nothing else'))
sys.exit(1 if bad else 0)
PY
    local rc=$?
    if [ -e "$scratch" ]; then
        echo "relation: $scratch was not removed" >&2
        return 1
    fi
    [ -s "$CACHE_DIR/stderr.log" ] && cat "$CACHE_DIR/stderr.log" >&2
    return $rc
}

mode=${1:-}
case "$mode" in
check | unused) ;;
relation) relation; exit $? ;;
*)
    echo 'usage: svelte-check-once.sh <check|unused|relation>' >&2
    exit 2
    ;;
esac

label=check
[ "$mode" = unused ] && label=check-unused

# Test hook: derive the verdict from a report that already exists, so a fixture can ask the derivation the
# questions a real compile takes ten seconds to answer. The compile-and-cache path below is what every real
# run takes; scripts/svelte-check-once_test.py covers both.
if [ -n "${MIKE_SVELTE_REPORT:-}" ]; then
    verdict "$mode" "$MIKE_SVELTE_REPORT" "$label"
    exit $?
fi

mkdir -p "$CACHE_DIR" || exit 1
fp=$(fingerprint) || exit 1
if [ -f "$REPORT" ] && [ "$(head -n 1 "$REPORT" 2>/dev/null)" = "# fingerprint $fp" ]; then
    from=cache
else
    from=compile
    tmp=$REPORT.tmp.$$
    run_machine "$STRICT_TS" "$tmp"
    rc=$?
    # A run that produced no report is not a verdict and must not be cached as one, whatever it exited with.
    if ! grep -qE '^[0-9]+ COMPLETED [0-9]+ FILES' "$tmp"; then
        rm -f "$tmp"
        [ -s "$CACHE_DIR/stderr.log" ] && cat "$CACHE_DIR/stderr.log" >&2
        echo "svelte-check-once: svelte-check exited $rc without a report — nothing cached" >&2
        exit 1
    fi
    { printf '# fingerprint %s\n' "$fp"; cat "$tmp"; } >"$REPORT" && rm -f "$tmp"
    if [ -s "$CACHE_DIR/stderr.log" ]; then cat "$CACHE_DIR/stderr.log" >&2; fi
    rm -f "$CACHE_DIR/stderr.log"
fi

# svelte-check's own exit code is never the verdict: the strict config reports the exempt `_`-prefixed
# declarations too, so a run can exit 1 while both verdicts are green. Only the derived sets decide.
verdict "$mode" "$REPORT" "$label"
rc=$?
if [ "$mode" = check ] && [ $rc -eq 0 ]; then
    echo "svelte-check-once: $STRICT_TS report read from $from; $LOOSE_TS needed no second compile"
fi
exit $rc
