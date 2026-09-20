#!/usr/bin/env bash
# Fail on dead code - but not on an accessor that exists only to be asserted on.
#
# deadcode must run from BOTH roots in one invocation: `deadcode .` walks only the server binary and
# reports pkg/bridge/protocol's constructors as unreachable, though they are reached from
# ./pkg/bridge/agent, the module's other main. AGENTS.md records the trap; encoding it here means it
# is not re-learned per session.
#
# A reported function whose name also appears in a _test.go in its own package is a test-only accessor.
# Those are listed as an explicit allowlist below rather than matched by a name pattern, so a NEW
# unreachable function always fails the gate and must be deleted or justified here.
#
# Usage: scripts/deadcode-check.sh
set -euo pipefail

DEADCODE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lint-tools.sh"
DEADCODE="$("$DEADCODE" path deadcode)"

# Module roots, in one invocation so a function reachable from either is live.
ROOTS=("." "./pkg/bridge/agent")

# Test-only accessors: "<package-dir>:<funcName>". Scoped by directory as well as name so
# allowing `none` in backend/extensions cannot also excuse an unrelated `none` elsewhere.
ALLOWLIST="
backend/agent/loader.go:RegisterAdapterFactory
backend/crypto/encryption.go:Derivations
backend/extensions/i18n_prompts.go:none
backend/llama/tools_names.go:AllToolNames
backend/prompts/overlay.go:ActiveOverlay
backend/stream/sandbox_stats.go:TickOnce
backend/term/terminal.go:activeSessionCount
pkg/logging/logging.go:TestSink
pkg/logging/logging.go:setOutput
pkg/logging/logging.go:Write
pkg/logging/logging.go:String
pkg/logging/logging.go:Reset
pkg/logging/logging.go:Lines
"

# pkg/logging, one reason each. deadcode walks a call graph, so it cannot see three legitimate shapes:
#   TestSink / setOutput   the log redirect a test captures through; reached only from _test.go.
#   Write                  satisfies io.Writer - slog's handler calls it through the interface, which a
#                          call-graph walk cannot see (same reason bridge's protocol constructors look
#                          dead).
#   String / Reset / Lines what a capturing test reads back.
#
# backend/agent: RegisterAdapterFactory is the mock-adapter seam backend/exec uses to hold that a
# sandboxed task probes the binary its own adapter named. Production always loads WASM, so no
# production path reaches it - and deleting it would put that test out of reach.
#
# backend/stream: TickOnce drives one sample and one broadcast in lockstep, which is how every
# sampler test steps the ticker; production runs the same two halves on their own intervals.
#
# backend/crypto: Derivations is how the one-KDF-per-Box test counts without measuring wall time.

is_allowed() {
	local file="$1" name="$2" entry
	for entry in $ALLOWLIST; do
		[[ "$entry" == "$file:$name" ]] && return 0
	done
	return 1
}

# A name referenced from a _test.go in the same directory is a test seam, not dead code. The allowlist
# stays the authority; this lookup only explains a hit, and catches an allowlist entry whose test has
# been deleted (that hit becomes unexplained again, which is correct).
test_referenced() {
	local dir="$1" name="$2"
	find "$dir" -maxdepth 1 -name '*_test.go' -print0 2>/dev/null |
		xargs -0 grep -lE "\b${name}\b" 2>/dev/null | grep -q . 
}

violations=0
hits=0
declare -A used=()

# -test is deliberately NOT passed: it counts test callers as reachability, which would hide the
# very case this script filters on, and would also let a function used only by a deleted
# production path slip through.
while IFS= read -r line; do
	[[ -z "$line" ]] && continue
	hits=$((hits + 1))

	file="${line%%:*}"
	# Strip "path:line:col: unreachable func: " — taking the tail after the marker rather than the
	# last dot of the whole line, because a bare function name has no dot and `##*.` would then
	# hand back the entire line.
	func_part="${line##*func: }"
	name="${func_part##*.}"
	name="${name%%[ (]*}"
	dir="$(dirname "$file")"

	if is_allowed "$file" "$name"; then
		used["$file:$name"]=1
		printf '  ok   %-52s (test-only accessor)\n' "$file: $name"
		continue
	fi

	if test_referenced "$dir" "$name"; then
		printf '  note %-52s referenced by a test but not allowlisted\n' "$file: $name"
	fi

	printf '  DEAD %-52s %s\n' "$file" "$name"
	violations=$((violations + 1))
done < <("$DEADCODE" "${ROOTS[@]}" 2>/dev/null || true)

# The other direction: an entry for a function deadcode no longer reports guards nothing, and it
# will keep "explaining" a hit that a later edit makes real again. The check above only catches an
# entry whose test was deleted; this catches an entry whose hit disappeared.
stale=0
for entry in $ALLOWLIST; do
	[[ -n "${used[$entry]:-}" ]] && continue
	printf '  STALE %-52s allowlisted, but deadcode reports it as reachable\n' "$entry"
	stale=$((stale + 1))
done

echo "deadcode: $hits unreachable reported, $violations unexplained, $stale stale allowlist entries"

if (( stale > 0 )); then
	cat >&2 <<-EOF
	$stale allowlist entries match no unreachable function. deadcode no longer reports them, so
	delete them from ALLOWLIST in scripts/deadcode-check.sh (and their reason comment).
	EOF
	exit 1
fi

if (( violations > 0 )); then
	cat >&2 <<-EOF
	deadcode found unreachable functions with no allowlist entry.
	Either delete them, or — if a test is the only caller — add "<file>:<name>" to ALLOWLIST in
	scripts/deadcode-check.sh with a sentence saying what the accessor observes.
	EOF
	exit 1
fi
