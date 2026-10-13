#!/usr/bin/env bash
# Build the downloadable mike-server packages: one archive per target at
# dist/mike-server-<version>-<os>-<arch>.tar.gz, plus dist/sha256sum.txt.
#
#   scripts/package-release.sh [linux/amd64 linux/arm64 ...]
#
# `make package` calls this and supplies VERSION and RELEASE_TAGS. Both binaries are built for the
# target's GOARCH, because the committed sandbox/sandbox-bridge belongs to whoever last ran
# `make bridge`. CGO stays off: SQLite here is modernc.org, so the result is static.
set -euo pipefail

usage() {
    echo "usage: $0 <os/arch> ..." >&2
    exit 2
}

(( $# > 0 )) || usage

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/dist"
STAGING_ROOT="$OUT/staging"

# Checked before anything is built: a package without its prompts serves a failing task, not an error.
require() {
    if [[ ! -e "$ROOT/$1" ]]; then
        echo "package-release: $1 is missing; nothing is packaged without it" >&2
        exit 1
    fi
}

require prompts
if ! compgen -G "$ROOT/prompts/*.tmpl" > /dev/null; then
    echo "package-release: no prompts/*.tmpl; nothing is packaged without the prompt templates" >&2
    exit 1
fi
require extensions/README.md
require examples/kanban.Dockerfile
require examples/pi.Dockerfile
require LICENSE
require NOTICE
require THIRD_PARTY_NOTICES.md
# Without a built frontend the server refuses its own boot.
require frontend/dist/index.html
# The whole tree is embedded, so a file that does not belong there is a file in the download: a
# sourcemap publishes the sources. `make frontend` empties the directory.
stray=$(find "$ROOT/frontend/dist" \( -name '*.map' -o -name '.*' \) ! -name '.gitkeep' -print)
if [[ -n "$stray" ]]; then
    echo "package-release: these would be embedded in the binary:" >&2
    printf '    %s\n' $stray >&2
    exit 1
fi

if ! command -v go > /dev/null; then
    echo "package-release: go is not on PATH" >&2
    exit 1
fi

# The release manifest is generated from the checksum file, so python3 is needed to finish a package.
if ! command -v python3 > /dev/null; then
    echo "package-release: python3 is not on PATH; dist/version.json cannot be written without it" >&2
    exit 1
fi

# Both come from the Makefile, and both reach a filename, a sed program and -ldflags below, so the
# script checks them rather than trusting how it was called.
if [[ ! "${VERSION:-}" =~ ^[0-9]+(\.[0-9]+)*$ ]]; then
    echo "package-release: VERSION must be a dotted number, got '${VERSION:-}' (make package derives it from the git tag)" >&2
    exit 1
fi
if [[ ! "${RELEASE_TAGS:-}" =~ ^[a-z0-9]+(,[a-z0-9]+)*$ ]]; then
    echo "package-release: RELEASE_TAGS must be comma-separated build tags, got '${RELEASE_TAGS:-}'" >&2
    exit 1
fi

# Which machine an ELF binary is for: e_machine, the little-endian 16 bits at offset 18. `go version`
# names only the toolchain.
elf_arch() {
    if [[ "$(od -An -tx1 -N4 "$1" | tr -d ' \n')" != "7f454c46" ]]; then
        echo "not-elf"
        return
    fi
    local lo hi machine
    lo=$(od -An -tu1 -j18 -N1 "$1" | tr -d ' \n')
    hi=$(od -An -tu1 -j19 -N1 "$1" | tr -d ' \n')
    machine=$(( lo + hi * 256 ))
    case "$machine" in
        62) echo "linux/amd64" ;;
        183) echo "linux/arm64" ;;
        *) echo "elf-machine-$machine" ;;
    esac
}

# Sorted names, no uid/gid or mtime from this machine, no gzip timestamp: byte-comparable runs.
TAR_OPTS=(--sort=name --owner=0 --group=0 --numeric-owner --mtime='UTC 1970-01-01')

# The first thing a buyer reads. Neither podman nor runsc is inside the archive, and the binary finds
# prompts/, sandbox-bridge and extensions/ relative to the working directory, so both are stated.
write_readme() {
    local file="$1" name="$2" version="$3"
    cat > "$file" <<'BODY'
# mike-server @VERSION@

Part of `@NAME@`. The web application is compiled into this binary; nothing here needs a web
server, a node install, or access to the source tree.

## Contents

- `mike-server` — the server. It carries its own copy of the prompt templates and the web app.
- `prompts/` — the templates the executor renders into a system prompt; what is here answers for its
  own name and the copy in the binary answers what is not, so a deleted file costs nothing. Its
  `skills/` holds the skill packages every task is offered; an agent opens those, the server never does.
- `sandbox/sandbox-bridge` — the program that runs inside every task container.
- `examples/` — Dockerfiles to build a project's sandbox image from: point the project's sandbox
  settings at one and build the image. They are samples, so an update never rewrites them.
- `extensions/` — where installed extensions are written. Only its README ships here; the rest
  of the tree is created by Settings → Extensions.
- `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`

## Running it

Needs `podman`, `git` and gVisor's `runsc` on `PATH`. A sandboxed project runs its tool calls and
its terminal inside gVisor containers and refuses to start at all when `runsc` is missing.

```sh
cd @NAME@
export MIKE_ENCRYPTION_KEY=$(openssl rand -hex 32)
./mike-server
```

`MIKE_ENCRYPTION_KEY` is required: it encrypts what you save in Settings. Generate it once and keep
it somewhere as durable as `data/` — a new key cannot read what an older one encrypted, and a saved
provider key that will not decrypt is sent upstream unchanged. Behind a reverse proxy that forwards a
public domain, also set `MIKE_ALLOWED_HOSTS`. `./mike-server --help` lists the flags and their
environment twins.

There is no password to set. A browser signs in with a **passkey**, and the first boot of an empty
`data/` prints the one-time pairing code that buys that first registration, boxed so it stands out
in the terminal MIKE started in. Every device after that is paired from Settings → Devices & passkeys on
a browser that is already signed in. Lost every passkey? Stop the server and run
`./mike-server --reset-passkeys`; it deletes the passkeys and every live session, then exits.

It listens on `127.0.0.1:3000` over TLS, with a certificate generated into `data/tls/` on first
boot — import `data/tls/mike-ca.crt` into a browser's trust store to make its certificate trusted.
`--listen 0.0.0.0` answers other machines; open MIKE there by a hostname, never a bare address,
because a passkey belongs to a name.

## Where state goes

The working directory: `data/` (database, TLS material, flight recorder), `.worktrees/`,
`extensions/`, and the `prompts/` and `sandbox/` directories beside them. Every one of those is
found relative to where the server is started, so start it from here. `examples/` is the one
directory nothing is written to or read from while the server runs.

## Verifying the download

From the directory that holds the archive, before extracting:

```sh
sha256sum -c sha256sum.txt
```

That proves the archive matches the `sha256sum.txt` sitting next to it, which a mirror that served a
different archive would also replace. Ask for the digest over a second channel — the release
announcement, or the person who pointed you at the download — before trusting a first fetch.
BODY
    sed -i "s/@NAME@/$name/g; s/@VERSION@/$version/g" "$file"
}

names=()
for target in "$@"; do
    os="${target%%/*}"
    arch="${target##*/}"
    if [[ "$os" == "$target" || -z "$arch" ]]; then
        echo "package-release: '$target' is not os/arch" >&2
        exit 2
    fi
    case "$os/$arch" in
        linux/amd64 | linux/arm64) ;;
        *) echo "package-release: unsupported target $target (the sandbox needs Linux + podman)" >&2; exit 2 ;;
    esac

    name="mike-server-$VERSION-$os-$arch"
    stage="$STAGING_ROOT/$name"
    archive="$OUT/$name.tar.gz"

    rm -rf "$stage"
    mkdir -p "$stage/extensions" "$stage/examples" "$stage/prompts" "$stage/sandbox"

    # The server is built as a release: it reads no frontend directory. The bridge takes no tag.
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -tags "$RELEASE_TAGS" -trimpath -ldflags "-s -w -X mike/backend/server.Version=$VERSION" -o "$stage/mike-server" .
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -ldflags "-s -w" -o "$stage/sandbox/sandbox-bridge" ./pkg/bridge/agent
    chmod 0755 "$stage/mike-server" "$stage/sandbox/sandbox-bridge"

    # Ask the header, so an arm64 package cannot carry an amd64 bridge.
    for bin in "$stage/mike-server" "$stage/sandbox/sandbox-bridge"; do
        built="$(elf_arch "$bin")"
        if [[ "$built" != "$os/$arch" ]]; then
            echo "package-release: $bin is built for $built, want $os/$arch" >&2
            exit 1
        fi
    done

    # What the server reads at run time, plus the samples it never opens: the extensions tree is
    # written at run time, and examples/ is only ever opened by whoever builds an image from one.
    cp "$ROOT"/prompts/*.tmpl "$stage/prompts/"
    # Skill packages ship as found. They are copied rather than parsed: an agent opens them through the
    # read-only skills mount.
    if [[ -d "$ROOT/prompts/skills" ]]; then
        cp -R "$ROOT/prompts/skills" "$stage/prompts/skills"
        find "$stage/prompts/skills" -type f -exec chmod 0644 {} +
    fi
    cp "$ROOT/extensions/README.md" "$stage/extensions/"
    cp "$ROOT/examples/kanban.Dockerfile" "$ROOT/examples/pi.Dockerfile" "$stage/examples/"
    cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$ROOT/THIRD_PARTY_NOTICES.md" "$stage/"

    write_readme "$stage/README.md" "$name" "$VERSION"
    chmod 0644 "$stage/README.md" "$stage"/prompts/*.tmpl "$stage/extensions/README.md" \
        "$stage"/examples/*.Dockerfile "$stage"/LICENSE "$stage"/NOTICE "$stage"/THIRD_PARTY_NOTICES.md

    tar "${TAR_OPTS[@]}" -cf - -C "$STAGING_ROOT" "$name" | gzip -n -9 > "$archive"
    echo "package-release: $archive"
    names+=("$name.tar.gz")
done

# The list is relative to dist/ so it names the files as a buyer sees them.
( cd "$OUT" && rm -f sha256sum.txt && sha256sum "${names[@]}" > sha256sum.txt )
echo "package-release: $OUT/sha256sum.txt"

# The manifest the update check reads. Its digests are read out of sha256sum.txt rather than
# recomputed, so the two files a buyer and an install compare cannot disagree.
UPDATE_BASE_URL="${UPDATE_BASE_URL:-https://getmike.dev/download}" \
    python3 "$ROOT/scripts/gen-version-manifest.py" --dist "$OUT" --version "$VERSION"
echo "package-release: $OUT/version.json"
# Nothing here signs the archives, so this is as far as the build can vouch for them.
echo "package-release: publish sha256sum.txt by a route other than the download, or sign the archives"
