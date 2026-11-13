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
# function reported here always fails the gate and must be deleted or justified here.
#
# Two passes. Pass 1 walks deadcode's call graph. Pass 2 reads the text instead, because the graph
# cannot see every dead method: deadcode counts a method live as soon as its type satisfies a used
# interface, so a method nothing calls still looks reachable. Pass 2's header says what it checks.
#
# Usage: scripts/deadcode-check.sh
set -euo pipefail

DEADCODE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lint-tools.sh"
DEADCODE="$("$DEADCODE" path deadcode)"

# Every main this tree builds, in one invocation so a function reachable from any of them is live:
# the server binary, the bridge's other main, and the script mains. The script mains belong here
# because this step is the only dead-code check that ever sees them — scripts/ is outside the
# build-and-test scope, so a declaration nothing calls there is measured nowhere else.
ROOTS=("." "./pkg/bridge/agent" "./scripts/archgate" "./scripts/i18nparity" "./scripts/sandboxprobe")

# Test-only accessors: "<package-dir>:<funcName>". Scoped by directory as well as name so
# allowing `Write` in pkg/logging cannot also excuse an unrelated `Write` elsewhere.
ALLOWLIST="
backend/agent/loader.go:RegisterAdapterFactory
backend/crypto/encryption.go:Derivations
backend/prompts/overlay.go:ActiveOverlay
pkg/logging/logging.go:TestSink
pkg/logging/logging.go:setOutput
pkg/logging/logging.go:Write
pkg/logging/logging.go:String
pkg/logging/logging.go:Reset
pkg/logging/logging.go:Lines
backend/auth/pairing.go:Live
backend/sandbox/build.go:ActiveBuildCount
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
# backend/crypto: Derivations is how the one-KDF-per-Box test counts without measuring wall time.
# The same key bytes come out of every derivation, so comparing keys proves repeatability, not the
# count; a wall-time bound is the only other measurement and no Go test here may depend on timing.
#
# backend/auth: Live is how many pairing codes are outstanding. No route answers a count - a mint
# answers one code and a full set answers a refusal - so the boot decision a backend/server test owns
# ("an unpaired install prints a code, a paired one prints nothing") has no other read-back.
#
# backend/prompts: ActiveOverlay is the read side of SetOverlay. Which (dir, locale) pair the server
# installed is the wiring a backend/server test owns, and dir has no other read-back - ActiveLocale
# answers the locale alone.

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

rc=0

echo "deadcode: $hits unreachable reported, $violations unexplained, $stale stale allowlist entries"

if (( stale > 0 )); then
	cat >&2 <<-EOF
	$stale allowlist entries match no unreachable function. deadcode no longer reports them, so
	delete them from ALLOWLIST in scripts/deadcode-check.sh (and their reason comment).
	EOF
	rc=1
fi

if (( violations > 0 )); then
	cat >&2 <<-EOF
	deadcode found unreachable functions with no allowlist entry.
	Either delete them, or — if a test is the only caller — add "<file>:<name>" to ALLOWLIST in
	scripts/deadcode-check.sh with a sentence saying what the accessor observes.
	EOF
	rc=1
fi

# ---------------------------------------------------------------------------
# Pass 2: a method nothing calls.
#
# Calling a method needs a selector - `x.Name` - so if no file in scope ever writes `x.Name`, nothing
# can call that method. Scope: backend, pkg, scripts and the root package, which covers every main
# listed in ROOTS.
#
# Only production files are scanned for declarations; staticcheck reports methods on test types
# (U1000).
#
# Two gaps, and both make the pass report fewer methods rather than more. Whole-line comments are
# dropped before the scan, because one naming a method hid a real hit; comments after code, and block
# comments, are not dropped. And a method named like a struct field counts as used.

# Methods a dependency calls, so no file in this tree ever names one:
#   UnmarshalJSON   encoding/json calls it through json.Unmarshaler on any type declaring it.
#   WebAuthn*       go-webauthn reads a credential's user through the webauthn.User interface, which
#                   backend/auth's operator implements.
DEPENDENCY_ALLOWLIST="
UnmarshalJSON
WebAuthnID
WebAuthnName
WebAuthnDisplayName
WebAuthnCredentials
"

is_dependency_allowed() {
	local name="$1" entry
	for entry in $DEPENDENCY_ALLOWLIST; do
		[[ "$entry" == "$name" ]] && return 0
	done
	return 1
}

go_files=()
while IFS= read -r -d '' f; do
	go_files+=("$f")
done < <(find backend pkg scripts -name '*.go' -print0 2>/dev/null)
for f in *.go; do
	if [[ -f "$f" ]]; then go_files+=("$f"); fi
done

declare -A selected=()
while IFS= read -r n; do
	if [[ -n "$n" ]]; then selected["$n"]=1; fi
done < <(cat "${go_files[@]}" 2>/dev/null | grep -v '^[[:space:]]*//' |
	grep -ohE '\.[A-Za-z_][A-Za-z0-9_]*' | sed 's/^\.//' | sort -u || true)

never_called=0
call_violations=0
call_stale=0
methods=0
declare -A dependency_used=()

while IFS=: read -r file line rest; do
	case "$file" in *_test.go) continue ;; esac
	case "$rest" in 'func ('*) ;; *) continue ;; esac
	name="${rest#*) }"
	name="${name%%(*}"
	if [[ ! "$name" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then continue; fi
	methods=$((methods + 1))
	if [[ -n "${selected[$name]:-}" ]]; then continue; fi
	never_called=$((never_called + 1))

	if is_dependency_allowed "$name"; then
		dependency_used["$name"]=1
		printf '  ok   %-52s (a dependency calls it)\n' "$file: $name"
		continue
	fi

	printf '  DEAD %-52s %s\n' "$file" "$name"
	call_violations=$((call_violations + 1))
done < <(grep -n '^func (' "${go_files[@]}" 2>/dev/null || true)

# An entry goes stale once this tree calls the name itself, the same way pass 1's entries go stale.
for entry in $DEPENDENCY_ALLOWLIST; do
	if [[ -z "${dependency_used[$entry]:-}" ]] && [[ -n "${selected[$entry]:-}" ]]; then
		printf '  STALE %-52s this tree calls it now; delete it from DEPENDENCY_ALLOWLIST\n' "$entry"
		call_stale=$((call_stale + 1))
	fi
done

printf 'selector pass: %d methods, %d never called, %d unexplained, %d stale allowlist entries\n' \
	"$methods" "$never_called" "$call_violations" "$call_stale"

if (( call_stale > 0 )); then
	cat >&2 <<-EOF
	$call_stale DEPENDENCY_ALLOWLIST entries name a method this tree now calls, so pass 2 no longer
	reports them. Delete them from DEPENDENCY_ALLOWLIST in scripts/deadcode-check.sh.
	EOF
	rc=1
fi

if (( call_violations > 0 )); then
	cat >&2 <<-EOF
	Pass 2 found methods no file calls. Pass 1 cannot see these, because the method's type satisfies a
	used interface. Delete the method, or - if a dependency calls it by name - add it to
	DEPENDENCY_ALLOWLIST with the dependency's name.
	EOF
	rc=1
fi

exit $rc
