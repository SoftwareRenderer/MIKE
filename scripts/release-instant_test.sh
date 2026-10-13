#!/usr/bin/env bash
# Tests for scripts/release-instant.sh. Run: scripts/release-instant_test.sh
#
# What breaks if these regress is a release nobody can reproduce: the archives digest differently on
# every machine, so sha256sum.txt and version.json stop agreeing with whatever the last build
# published. The instant has three sources and only the last one is allowed to be this machine's
# clock, so each rung gets its own assertion — including the case where the clock is correct to use.
set -uo pipefail

cd "$(dirname "$0")/.."
SCRIPT=scripts/release-instant.sh
fail=0
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

check() { # name expected actual
    if [[ "$2" == "$3" ]]; then
        echo "ok   $1"
    else
        echo "FAIL $1: got '$3', want '$2'"
        fail=1
    fi
}

ri() { # version -> epoch on stdout; the who-decided line goes to stderr
    env -u SOURCE_DATE_EPOCH REPO_DIR="$repo" "$SCRIPT" "$1" 2>/dev/null
}

near_now() { # label value
    local now delta
    now="$(date +%s)"
    delta=$(( $2 - now ))
    (( delta < 0 )) && delta=$(( -delta ))
    if (( delta <= 5 )); then
        echo "ok   $1"
    else
        echo "FAIL $1: '$2' is $(( $2 - now ))s from now"
        fail=1
    fi
}

# --- fixture -------------------------------------------------------------------------
# One repo, two commits: the tagged one in 2020, HEAD in 2024. A resolver that read HEAD instead of
# the tagged commit answers the later epoch, and every date below would still look plausible.
repo="$work/repo"
mkdir -p "$repo"
git -C "$repo" init -q -b main
git -C "$repo" config user.email test@example.invalid
git -C "$repo" config user.name Test
git -C "$repo" config commit.gpgsign false

commit() { # date message -> commits empty, returns its own epoch
    GIT_AUTHOR_DATE="$1 00:00:00 +0000" GIT_COMMITTER_DATE="$1 00:00:00 +0000" \
        git -C "$repo" commit -q --allow-empty -m "$2"
    git -C "$repo" log -1 --format=%ct
}

tagged_epoch="$(commit 2020-01-01 tagged)"
head_epoch="$(commit 2024-06-01 head)"
git -C "$repo" tag v1.2.3 HEAD~1
check "the fixture's two commits are distinguishable" 1 "$([[ "$head_epoch" != "$tagged_epoch" ]] && echo 1 || echo 0)"

# --- which commit decides ------------------------------------------------------------
check "the tagged commit's date, not HEAD's" "$tagged_epoch" "$(ri 1.2.3)"

# A repository that tags without the prefix is resolved the same way.
git -C "$repo" tag 2.0.0 HEAD~1
check "a tag with no v prefix resolves" "$tagged_epoch" "$(ri 2.0.0)"

# The fixture puts this tag's tagger date days away from its commit's, because `log --format=%ct <tag>`
# peels too: without that gap this assertion would pass without ever exercising the peel.
GIT_COMMITTER_DATE="2023-03-03 03:03:03 +0000" git -C "$repo" tag -a -m annotated 3.0.0 HEAD~1
tag_date="$(git -C "$repo" for-each-ref --format='%(taggerdate:unix)' refs/tags/3.0.0)"
check "the fixture's annotated tag really was made later than its commit" 1 \
    "$([[ "$tag_date" -gt "$tagged_epoch" ]] && echo 1 || echo 0)"
check "an annotated tag answers the commit's date, not the tagger's" "$tagged_epoch" "$(ri 3.0.0)"

# --- the rungs above it --------------------------------------------------------------
check "SOURCE_DATE_EPOCH beats the tag" 1234 \
    "$(env -u SOURCE_DATE_EPOCH REPO_DIR="$repo" SOURCE_DATE_EPOCH=1234 "$SCRIPT" 1.2.3 2>/dev/null)"

status=0
output="$(env -u SOURCE_DATE_EPOCH REPO_DIR="$repo" SOURCE_DATE_EPOCH=yesterday "$SCRIPT" 1.2.3 2>&1)" || status=$?
check "a SOURCE_DATE_EPOCH that is not epoch seconds is refused" 1 "$status"
check "the refusal names the variable" 0 "$([[ "$output" == *SOURCE_DATE_EPOCH* ]] && echo 0 || echo 1)"

# --- the rung that is allowed to read the clock --------------------------------------
# The version a checkout with no matching tag packages under.
near_now "an untagged version falls back to the clock" "$(ri 9.9.9)"

# A tag that resolves to nothing (here a symref to a branch that isn't there) falls through to the clock:
# it answers no date and stops no build. A `rev-parse` failure is already empty stdout plus a non-zero
# status, which is exactly what the resolver's loop reads as "no tag".
git -C "$repo" symbolic-ref refs/tags/v5.0.0 refs/heads/nope 2>/dev/null
near_now "a tag that resolves to nothing falls back to the clock" "$(ri 5.0.0)"

plain="$work/not-a-repo"
mkdir -p "$plain"
# One assertion, not two: a non-numeric answer is a huge distance from now, so near_now rejects garbage
# without a separate digit check.
near_now "a tree with no git at all answers the current epoch" \
    "$(env -u SOURCE_DATE_EPOCH REPO_DIR="$plain" "$SCRIPT" 1.2.3 2>/dev/null)"

# --- the refusal that keeps a caller's bug out of a ref name -------------------------
# The version reaches a ref name, so a value that cannot name one is the caller's bug. It is refused
# rather than handed to git, which reads an unknown ref as "no tag" and would quietly use the clock.
status=0
output="$(env -u SOURCE_DATE_EPOCH REPO_DIR="$repo" "$SCRIPT" '1.2;rm -rf' 2>&1)" || status=$?
check "a version that is not a dotted number is refused" 2 "$status"
check "and it answers no epoch" "" \
    "$(env -u SOURCE_DATE_EPOCH REPO_DIR="$repo" "$SCRIPT" '1.2;rm -rf' 2>/dev/null)"
status="$(env -u SOURCE_DATE_EPOCH "$SCRIPT" >/dev/null 2>&1; echo $?)"
check "a call with no version is refused" 2 "$status"

echo
if (( fail )); then
    echo "release-instant_test: FAILED"
    exit 1
fi
echo "release-instant_test: all ok"
