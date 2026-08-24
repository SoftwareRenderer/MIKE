#!/usr/bin/env python3
"""Generate ../MIKE-extensions/registry.json from extension manifests.

Scans ../MIKE-extensions/<type>/<name>/manifest.json for every extension and emits
a git-pinned registry entry: the repo URL (`repo`), the current HEAD commit of the
extensions repo (`commit`, a full SHA-1 or SHA-256 hash), and the subdirectory
within the repo that holds the extension (`subdir`). On install the server clones
the repo, checks out exactly `commit`, verifies `git rev-parse HEAD` matches, and
only then installs the `subdir` content into `./extensions/<author>/<name>`. The
full capability config stays in each manifest; the registry only carries what the
Browse UI renders — including `featured: true`, which pins the entry to the top
of the Browse list (above non-featured registry entries) until it is installed.

Usage:
    python3 scripts/gen-registry.py            # regenerate registry.json
    python3 scripts/gen-registry.py --check    # verify it is up to date (CI)

Run after adding/editing an extension manifest or bumping a published commit.
"""

import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXTENSIONS_DIR = os.path.normpath(os.path.join(ROOT, '..', 'MIKE-extensions'))
REGISTRY_PATH = os.path.join(EXTENSIONS_DIR, 'registry.json')

# A documentation-only template for third-party, self-hosted extensions. It is
# NOT emitted into the published registry (a live index must not contain a fake,
# non-installable entry); it documents the git-pinned (commit-verified) format an
# author would use: host the extension in a git repo, have a curator review a
# specific commit and pin its full 40-char SHA-1 or 64-char SHA-256 hash, and the
# server checks out + verifies that exact commit before installing.
#
# The values are placeholders: `commit` is deliberately not a valid hash and
# `repo` is not a real repository, so this entry can never be installed.
PLACEHOLDER_ENTRY = {
    'name': 'your-self-hosted-extension',
    'version': '1.0.0',
    'type': 'ui',
    'display_name': 'Your Self-Hosted Extension (template)',
    'description': 'TEMPLATE - replace this entry with your own. Points at a git '
                   'repo pinned to a vetted commit. On install the server clones '
                   'the repo, checks out exactly this commit, verifies git '
                   'rev-parse HEAD matches, and only then installs. The pinned '
                   'commit may be a full 40-char SHA-1 or 64-char SHA-256 hash.',
    'author': 'Your Name',
    'homepage': 'https://github.com/you',
    # --- Git-pinned (self-hosted) placeholder properties ---
    'repo': 'https://github.com/you/kanban-extensions',
    # Full 40-char SHA-1 or 64-char SHA-256 hash of the reviewed commit (vetted
    # by the curator). Deliberately not a valid hash so this template cannot be
    # installed.
    'commit': 'REPLACE_WITH_40_CHAR_SHA1_OR_64_CHAR_SHA256_OF_VETTED_COMMIT',
    # Optional: path within the repo to the extension root (repo root if empty).
    'subdir': 'your-extension',
    'capabilities': {
        'ui': {
            'injection_points': ['createTask'],
            'permissions': ['createTask.description.insertText'],
        },
    },
}


def git_origin():
    """Return the normalized HTTPS URL of the extensions repo's origin."""
    try:
        out = subprocess.run(
            ['git', '-C', EXTENSIONS_DIR, 'remote', 'get-url', 'origin'],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    except (subprocess.SubprocessError, FileNotFoundError):
        return 'https://github.com/SoftwareRenderer/MIKE-extensions'
    # Normalize git@host:owner/repo.git and https://host/owner/repo.git both to
    # https://host/owner/repo so the registry carries a cloneable HTTPS URL.
    m = re.match(r'^(?:git@([^:]+):|https?://([^/]+)/)(.+?)(?:\.git)?$', out)
    host = m.group(1) or m.group(2) if m else ''
    if m and host:
        return 'https://{}/{}'.format(host, m.group(3))
    return out


def git_head_commit():
    """Return the full HEAD commit hash of the extensions repo."""
    try:
        return subprocess.run(
            ['git', '-C', EXTENSIONS_DIR, 'rev-parse', 'HEAD'],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    except (subprocess.SubprocessError, FileNotFoundError):
        return ''


def simplify_capabilities(manifest_caps):
    """Reduce a manifest capabilities object to what the registry UI renders.

    Only fields surfaced in the capabilities modal are kept:
      - ui:      injection_points ("Modifies") and permissions ("Permissions")
      - network: allowed_domains ("Allowed domains")
      - lsp:   language ("Language") and extensions ("File extensions")
      - css/agent: no detail rows, so an empty config is enough
    Everything else (command, args, headers, wasm_module, ...) lives in the
    manifest and is intentionally omitted here.
    """
    if not isinstance(manifest_caps, dict):
        return {}
    result = {}
    for key, value in manifest_caps.items():
        if not isinstance(value, dict):
            continue
        if key == 'ui':
            kept = {}
            if value.get('injection_points'):
                kept['injection_points'] = value['injection_points']
            if value.get('permissions'):
                kept['permissions'] = value['permissions']
            if kept:
                result['ui'] = kept
        elif key == 'network':
            if value.get('allowed_domains'):
                result['network'] = {'allowed_domains': value['allowed_domains']}
        elif key == 'lsp':
            kept = {}
            if value.get('language'):
                kept['language'] = value['language']
            if value.get('extensions'):
                kept['extensions'] = value['extensions']
            if kept:
                result['lsp'] = kept
        elif key in ('css', 'agent'):
            result[key] = {}
    return result


def build_entry(ext_type, name, manifest_path, repo, commit):
    with open(manifest_path) as f:
        manifest = json.load(f)

    # subdir is the extension's directory relative to the repo root
    # (e.g. "ui/github-issue-importer").
    subdir = os.path.relpath(os.path.dirname(manifest_path), EXTENSIONS_DIR).replace(os.sep, '/')
    entry = {
        'name': name,
        'version': manifest.get('version', ''),
        'type': ext_type,
        'display_name': manifest.get('display_name', name),
        'description': manifest.get('description', ''),
        'author': manifest.get('author', ''),
    }
    for field in ('homepage', 'license_url'):
        if manifest.get(field):
            entry[field] = manifest[field]

    # Optional: featured extensions pin to the top of the Browse list until
    # installed. Only emitted when the manifest sets it.
    if manifest.get('featured'):
        entry['featured'] = True

    # Git-pinned source fields.
    entry['repo'] = repo
    entry['commit'] = commit
    entry['subdir'] = subdir

    caps = simplify_capabilities(manifest.get('capabilities'))
    if caps:
        entry['capabilities'] = caps
    return entry


def read_existing_registry():
    """Return a dict keyed by (type, name) of the previously generated registry."""
    existing = {}
    try:
        with open(REGISTRY_PATH) as f:
            data = json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return existing
    for entry in data:
        if isinstance(entry, dict) and 'name' in entry and 'type' in entry:
            existing[(entry['type'], entry['name'])] = entry
    return existing


def entry_matches(entry, other):
    """True if two entries are identical except for the pinned `commit`."""
    if not other:
        return False
    e, o = dict(entry), dict(other)
    e.pop('commit', None)
    o.pop('commit', None)
    return e == o


def collect_entries(existing):
    repo = git_origin()
    head = git_head_commit()
    entries = []
    for dirpath, _dirnames, filenames in os.walk(EXTENSIONS_DIR):
        if 'manifest.json' not in filenames:
            continue
        rel = os.path.relpath(dirpath, EXTENSIONS_DIR)
        parts = rel.split(os.sep)
        if len(parts) != 2:
            continue  # only extensions/<type>/<name>/manifest.json
        ext_type, name = parts
        entry = build_entry(ext_type, name, os.path.join(dirpath, 'manifest.json'), repo, head)
        # Only bump the commit hash when the extension actually changed. If the
        # extension content is unchanged from the last published registry, keep
        # its previously pinned commit so the registry stays stable across runs
        # that don't touch that extension.
        prev = existing.get((ext_type, name))
        if entry_matches(entry, prev) and prev.get('commit'):
            entry['commit'] = prev['commit']
        entries.append(entry)
    entries.sort(key=lambda e: e['name'])
    return entries


def main():
    check_only = '--check' in sys.argv
    entries = collect_entries(read_existing_registry())
    output = json.dumps(entries, indent=4, ensure_ascii=False) + '\n'

    if check_only:
        try:
            with open(REGISTRY_PATH) as f:
                current = f.read()
        except FileNotFoundError:
            current = None
        if current != output:
            print('registry.json is out of date; run python3 scripts/gen-registry.py')
            sys.exit(1)
        print('registry.json is up to date')
        return

    with open(REGISTRY_PATH, 'w') as f:
        f.write(output)
    print(f'Generated {REGISTRY_PATH} with {len(entries)} entries:')
    for e in entries:
        print(f"  {e['type']}/{e['name']} v{e['version']} @ {e['commit'][:12]}")


if __name__ == '__main__':
    main()
