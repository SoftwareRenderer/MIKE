#!/usr/bin/env bash
# The instant a release package is stamped with.
#
#   scripts/release-instant.sh <version>     # epoch seconds on stdout, who decided on stderr
#
# scripts/package-release.sh pins every archive member's mtime to this instant and exports it as
# SOURCE_DATE_EPOCH for the manifest generator, so the archive mtimes and version.json's `published`
# are one instant rather than two guesses. Pinning is what makes a package byte-comparable: a date
# from this machine's clock would fold a build time into every digest.
#
# The ladder is SOURCE_DATE_EPOCH -> the commit the version's tag peels to -> this machine's clock; only
# the last rung costs reproducibility, and it announces itself on stderr. Why the commit's *committer*
# date decides, rather than the author's or an annotated tag's tagger date:
# docs/auto-update.md → The release instant.
set -euo pipefail

# Which checkout's tags to read. Default is this script's own repository; scripts/release-instant_test.sh
# points it at a throwaway one, the way BRIDGE_MAIN points bridge-stamp.sh at something else.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="${REPO_DIR:-$ROOT}"

version="${1:-}"
if [[ -z "$version" ]]; then
    echo "usage: $0 <version>" >&2
    exit 2
fi
# The version becomes a ref name below, and scripts/package-release.sh checks the same shape — a value
# that cannot name a tag is a bug in the caller, not a reason to consult the clock.
if [[ ! "$version" =~ ^[0-9]+(\.[0-9]+)*$ ]]; then
    echo "release-instant: version must be a dotted number, got '$version'" >&2
    exit 2
fi

if [[ -n "${SOURCE_DATE_EPOCH:-}" ]]; then
    if [[ ! "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
        echo "release-instant: SOURCE_DATE_EPOCH must be epoch seconds, got '$SOURCE_DATE_EPOCH'" >&2
        exit 1
    fi
    echo "release-instant: SOURCE_DATE_EPOCH" >&2
    printf '%s\n' "$SOURCE_DATE_EPOCH"
    exit 0
fi

# `v$version` is the convention `make package` derives its version from; the bare name is what a
# repository without the prefix tags. `^{commit}` peels an annotated tag to the commit it points at,
# and is a no-op for a lightweight one, so both kinds answer with the same thing.
for ref in "refs/tags/v$version" "refs/tags/$version"; do
    commit="$(git -C "$REPO_DIR" rev-parse -q --verify "$ref^{commit}" 2>/dev/null)" || continue
    epoch="$(git -C "$REPO_DIR" log -1 --format=%ct "$commit")"
    if [[ ! "$epoch" =~ ^[0-9]+$ ]]; then
        echo "release-instant: $ref resolves to $commit, which has no committer date" >&2
        exit 1
    fi
    echo "release-instant: $ref ($(git -C "$REPO_DIR" show -s --date=iso-strict --format=%cd "$commit"))" >&2
    printf '%s\n' "$epoch"
    exit 0
done

# The rung that costs byte-comparability. A version nobody tagged is the usual reason: `make package`
# falls back to 0.0.0 on a checkout with no tag at all.
echo "release-instant: no tag for $version under $REPO_DIR; using this machine's clock, so this build is not reproducible" >&2
date +%s
