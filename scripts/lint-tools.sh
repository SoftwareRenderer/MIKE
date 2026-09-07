#!/usr/bin/env bash
# Resolve the linters `make check` needs, and fail loudly when one is absent.
#
# A gate that quietly skips a missing tool reports success for a check that never ran - the repo's
# most common false "done". So a missing tool is an error carrying its own install command.
#
# Not a `tool` directive in go.mod: that would add build-only tools to go.mod/go.sum, and AGENTS.md
# requires regenerating the notices whenever a dependency changes. These are dev tools, not ours.
#
# Usage:
#   scripts/lint-tools.sh path <tool>     # print the absolute path, or fail with the fix
set -euo pipefail

usage() {
	cat <<'USAGE'
usage: scripts/lint-tools.sh path <tool>
USAGE
}

# Where Go puts an installed binary: GOBIN if set, else the first GOPATH's bin, else
# $HOME/go/bin. `go install` picks the same place, so the install hint and this lookup cannot
# disagree about where a tool landed.
resolve() {
	local tool="$1"
	local -a dirs=()

	local gobin
	gobin="$(go env GOBIN 2>/dev/null || true)"
	if [[ -n "$gobin" ]]; then dirs+=("$gobin"); fi

	local gopath
	gopath="$(go env GOPATH 2>/dev/null || true)"
	if [[ -n "$gopath" ]]; then
		# GOPATH is an OS path list; each entry has its own bin.
		local IFS=':'
		local entry
		for entry in $gopath; do
			[[ -n "$entry" ]] && dirs+=("$entry/bin")
		done
	fi

	dirs+=("$HOME/go/bin")

	local dir
	for dir in "${dirs[@]}"; do
		if [[ -x "$dir/$tool" ]]; then
			printf '%s\n' "$dir/$tool"
			return 0
		fi
	done

	# Finally PATH, which may hold a distro/vendored build we know nothing about.
	if command -v "$tool" >/dev/null 2>&1; then
		command -v "$tool"
		return 0
	fi
	return 1
}

# The install command for each tool, pinned the way AGENTS.md runs them. Kept next to the
# lookup so a new linter cannot gain a lookup rule without also gaining an install hint.
install_hint() {
	case "$1" in
	staticcheck) echo 'go install honnef.co/go/tools/cmd/staticcheck@latest' ;;
	deadcode)    echo 'go install golang.org/x/tools/cmd/deadcode@latest' ;;
	*)           echo "go install <module>/cmd/$1@latest" ;;
	esac
}

main() {
	local cmd="${1:-}"
	local tool="${2:-}"

	if [[ "$cmd" != "path" || -z "$tool" ]]; then
		usage >&2
		return 2
	fi

	local found
	if ! found="$(resolve "$tool")"; then
		cat >&2 <<-EOF
		$tool: not found — required by make check.
		Install it with:
		    $(install_hint "$tool")
		then re-run make check. (Re-sourcing your shell is not needed: this script looks in
		GOBIN, every GOPATH/bin and \$HOME/go/bin directly, not just on PATH.)
		EOF
		return 1
	fi

	printf '%s\n' "$found"
}

main "$@"
