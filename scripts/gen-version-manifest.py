#!/usr/bin/env python3
"""Generate (or verify) dist/version.json — the manifest the update check reads.

    scripts/gen-version-manifest.py --dist dist --version 1.4.2 [--base-url URL] [--check]

The digests come from the sha256sum.txt that `scripts/package-release.sh` just wrote, so the manifest
and the checksum file cannot disagree: this script reads one file, never re-hashes an archive.

`--check` compares the file on disk against what this script would write and exits non-zero on a
difference. It ignores `published`, which records when the release was announced and is the only field
that is not derived from the archives.
"""

import argparse
import datetime
import json
import os
import re
import sys

SCHEMA = 1
DEFAULT_BASE_URL = "https://getmike.dev/download"

# The names package-release.sh writes, minus the version it stamps in.
ASSET_RE = re.compile(r"^mike-server-(?P<version>[0-9]+(?:\.[0-9]+)*)-(?P<os>[a-z0-9]+)-(?P<arch>[a-z0-9]+)\.tar\.gz$")
SHA_RE = re.compile(r"^[0-9a-f]{64}$")
VERSION_RE = re.compile(r"^[0-9]+(?:\.[0-9]+)*$")


def fail(msg: str) -> "None":
    print(f"gen-version-manifest: {msg}", file=sys.stderr)
    sys.exit(1)


def read_checksums(path: str) -> "list[tuple[str, str]]":
    """(name, digest) pairs, in the order the file lists them."""
    try:
        with open(path, encoding="utf-8") as fh:
            lines = fh.read().splitlines()
    except OSError as exc:
        fail(f"{path}: {exc}; run `make package` first")

    entries = []
    for line in lines:
        if not line.strip():
            continue
        # `sha256sum` separates the digest from the name with two spaces (or one, in binary mode).
        parts = line.split()
        if len(parts) != 2:
            fail(f"{path}: cannot read the line {line!r}")
        digest, name = parts[0], parts[1].lstrip("*")
        if not SHA_RE.match(digest):
            fail(f"{path}: {name} carries {digest!r}, want 64 lowercase hex characters")
        entries.append((name, digest))
    if not entries:
        fail(f"{path}: no entries")
    return entries


def build(version: str, base_url: str, entries: "list[tuple[str, str]]", published: str) -> dict:
    if not VERSION_RE.match(version):
        fail(f"--version must be a dotted number, got {version!r}")
    if not base_url.startswith("https://"):
        # The client refuses a non-HTTPS asset too; refusing here keeps a mis-set URL from producing a
        # manifest that announces a release nobody can install.
        fail(f"--base-url must start with https://, got {base_url!r}")

    assets = {}
    for name, digest in entries:
        match = ASSET_RE.match(name)
        if not match:
            fail(f"{name} is not a release archive name; refusing to guess where it belongs")
        if match["version"] != version:
            fail(f"{name} is version {match['version']}, want {version}")
        key = f"{match['os']}/{match['arch']}"
        if key in assets:
            fail(f"two archives name {key}")
        assets[key] = {"url": f"{base_url}/{name}", "sha256": digest}

    if not assets:
        fail("no asset matched this version; the manifest would announce nothing")
    return {
        "schema": SCHEMA,
        "latest": version,
        "published": published,
        "assets": dict(sorted(assets.items())),
    }


def render(doc: dict) -> str:
    return json.dumps(doc, indent=2, sort_keys=True) + "\n"


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dist", default="dist")
    ap.add_argument("--version", required=True)
    ap.add_argument("--base-url", default=os.environ.get("UPDATE_BASE_URL", DEFAULT_BASE_URL))
    ap.add_argument("--published", default=os.environ.get("SOURCE_DATE_EPOCH", ""))
    ap.add_argument("--check", action="store_true", help="verify the file on disk instead of writing it")
    args = ap.parse_args()

    entries = read_checksums(os.path.join(args.dist, "sha256sum.txt"))

    published = args.published
    # SOURCE_DATE_EPOCH keeps a reproducible build's manifest reproducible too.
    if published.isdigit():
        published = datetime.datetime.fromtimestamp(int(published), datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")
    elif not published:
        published = datetime.datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")

    if not re.match(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$", published):
        fail(f"--published must be a UTC instant like 2026-09-18T00:00:00Z, got {published!r}")

    doc = build(args.version, args.base_url, entries, published)
    path = os.path.join(args.dist, "version.json")
    text = render(doc)

    if not args.check:
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
        print(f"gen-version-manifest: {path} ({len(doc['assets'])} asset(s), latest {doc['latest']})")
        return

    try:
        with open(path, encoding="utf-8") as fh:
            current = fh.read()
    except OSError as exc:
        fail(f"--check: {exc}")

    try:
        on_disk = json.loads(current)
    except json.JSONDecodeError as exc:
        fail(f"--check: {path} is not JSON: {exc}")

    # `published` is the announcement moment, not a fact about the bytes, so it is not compared.
    for field, want in (("schema", doc["schema"]), ("latest", doc["latest"]), ("assets", doc["assets"])):
        if on_disk.get(field) != want:
            fail(f"--check: {path} disagrees about {field}\n    on disk: {on_disk.get(field)!r}\n    expected: {want!r}")
    print(f"gen-version-manifest: {path} agrees with sha256sum.txt")


if __name__ == "__main__":
    main()
