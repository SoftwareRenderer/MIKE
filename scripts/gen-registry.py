#!/usr/bin/env python3
"""Generate ../MIKE-extensions/registry.json from extension manifests.

Scans ../MIKE-extensions/<type>/<name>/manifest.json for every extension and emits
a git-pinned registry entry: the repo URL (`repo`), the current HEAD commit of the
extensions repo (`commit`, a full SHA-1 or SHA-256 hash), and the subdirectory
within the repo that holds the extension (`subdir`). On install the server clones
the repo, checks out exactly `commit`, verifies `git rev-parse HEAD` matches, and
only then installs the `subdir` content into `./extensions/<author>/<name>`. The
full capability config stays in each manifest; the registry only carries what the
Browse UI renders.

An entry's `commit` names the content an install materializes, so a pin holds only while that commit
holds this extension's whole subdirectory; a commit that changes any file under it moves the pin on the
next run. `scripts/gen-registry_test.py` holds both directions of that bargain.

Usage:
    python3 scripts/gen-registry.py            # regenerate registry.json
    python3 scripts/gen-registry.py --check    # verify it is up to date (CI)

Run after any commit under ../MIKE-extensions; an entry whose commit did not move is one whose content
did not change.
"""

import json
import os
import re
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXTENSIONS_DIR = os.path.normpath(os.path.join(ROOT, '..', 'MIKE-extensions'))
REGISTRY_PATH = os.path.join(EXTENSIONS_DIR, 'registry.json')

# The registry fields no manifest declares. A curator sets these in `registry.json` itself, so a
# regeneration carries them across instead of deleting the curation: presence and absence both come
# from the previous file, which makes deleting one by hand the way it gets deleted. Read by
# backend/extensions' `RegistryEntry.Featured` and frontend `lib/composables/use-browse-order.ts`.
CURATED_FIELDS = ('featured',)

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


class UnresolvedTemplate(Exception):
    """A `{{...}}` placeholder that no default in the manifest's own config_schema can answer."""


def resolve_template(value, config_schema):
    """Resolve one `{{property_field[]}}` placeholder using the config_schema defaults.

    This repeats backend/extensions' `ConfigSchema.resolveTemplate`. Both sides have to agree exactly on the
    `[]` suffix, on reading the first element of the default array, on the `key_field` split, and on leaving
    anything that does not match unchanged.
    """
    if not isinstance(value, str) or len(value) < 5 or not value.startswith('{{') \
            or not value.endswith('}}'):
        return value
    ref = value[2:-2]
    if not ref.endswith('[]'):
        return value
    resolved = _template_target(ref[:-2], config_schema)
    if resolved is None:
        raise UnresolvedTemplate('%s resolves to nothing in this manifest’s config_schema' % value)
    return resolved


def _template_target(ref, config_schema):
    """Returns the default value the placeholder names, or None when no property provides it."""
    for prop in (config_schema or {}).get('properties') or []:
        if not isinstance(prop, dict):
            continue
        default = prop.get('default')
        if not isinstance(default, list) or not default or not isinstance(default[0], dict):
            continue
        prefix = str(prop.get('key', '')) + '_'
        if ref.startswith(prefix):
            field = default[0].get(ref[len(prefix):])
            if isinstance(field, str):
                return field
    return None


def simplify_capabilities(manifest_caps, config_schema=None):
    """Reduce a manifest's capabilities to the fields the registry UI shows.

    Only fields surfaced in the capabilities modal are kept:
      - ui:      injection_points ("Modifies") and permissions ("Permissions")
      - network: allowed_domains ("Allowed domains")
      - lsp:   language ("Language") and file_types ("File types", with their labels)
             and environment_note ("the one sentence shown when the server cannot
             resolve a workspace") and dependency_cache (where it reads resolved
             dependencies from, and whether it would write there)
      - i18n:  locale ("Language"), messages_url and has_prompts ("does it translate the
             prompt templates too") — the locale is the bare language subtag the pack
             renders the UI in, and it is the field read before the manifest is fetched:
             the first-run offer matches a non-English browser against it
             (frontend `lib/utils/locale-offer.ts`). It doubles as the pack's identity in
             the Browse list.
      - agent: sandbox.mount only (which host folder the agent wants, and where it appears inside the
             container); css: no detail rows, so an empty object is enough
    Everything else (command, args, headers, wasm_module, ...) lives in the
    manifest and is intentionally omitted here.

    i18n and lsp are the exceptions, for one reason: what the user is asked before installing, and which
    language pack the first-run offer suggests, are both decided from this index, before any manifest has
    been fetched. The facts behind those decisions therefore have to be published here
    (frontend/src/lib/utils/extension-permissions.ts and `lib/utils/locale-offer.ts`). The agent's suggested
    folders follow the same rule: leave them out and the install dialog never asks about them.
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
            # The declared file types, trimmed to what the UI renders. Syntax profiles stay in the
            # manifest — the registry never needs them, and forwarding them would put a whole
            # grammar into every index entry.
            file_types = []
            for ft in value.get('file_types') or []:
                if not isinstance(ft, dict) or not ft.get('extension'):
                    continue
                projected = {'extension': ft['extension'], 'language_id': ft.get('language_id', '')}
                if ft.get('label'):
                    projected['label'] = ft['label']
                file_types.append(projected)
            if file_types:
                kept['file_types'] = file_types
            sandbox = value.get('sandbox') or {}
            note = (sandbox.get('environment') or {}).get('note')
            if note:
                kept['environment_note'] = note
            # Where this server reads its resolved dependencies from. Projected so the *pre-install*
            # view can disclose that installing this reads a directory on the user's machine — the host
            # path itself never comes from here, and neither does a write permission: the extension's
            # own container always reads the directory, and `read_only` is no longer projected because
            # nothing reads it.
            dep = sandbox.get('dependency_cache')
            if isinstance(dep, dict) and dep.get('container_path') and dep.get('env'):
                kept['dependency_cache'] = {'container_path': dep['container_path'], 'env': dep['env']}
            if kept:
                result['lsp'] = kept
        elif key == 'css':
            result[key] = {}
        elif key == 'agent':
            # Only the suggested folders are published. The install dialog asks the user about them, and
            # `env` needs no answer because it gives no access to the machine. An agent with nothing to
            # suggest still gets an empty object, so Browse shows its badge.
            kept = {}
            mounts = []
            for mount in (value.get('sandbox') or {}).get('mount') or []:
                if not isinstance(mount, dict):
                    continue
                host = resolve_template(mount.get('host_path'), config_schema)
                container = resolve_template(mount.get('container_path'), config_schema)
                if not isinstance(host, str) or not host or not isinstance(container, str) or not container:
                    continue
                mode = resolve_template(mount.get('mode'), config_schema)
                mounts.append({'host_path': host, 'container_path': container,
                               'mode': mode if isinstance(mode, str) else ''})
            if mounts:
                kept['sandbox'] = {'mount': mounts}
            result[key] = kept
        elif key == 'i18n':
            # A translation pack is a css pack with different bytes: it runs nothing, so what is
            # worth publishing is which language it is and what it covers. The locale is the whole
            # reason this block is projected at all — it is matched against a browser language
            # before the pack is fetched, so it cannot live only in the manifest. `has_prompts`
            # joins it because a translated interface with English prompts is a different offer,
            # and the user should see which one they get before clicking.
            locale = (value.get('locale') or '').strip()
            kept = {}
            if locale:
                kept['locale'] = locale
            if value.get('messages_url'):
                kept['messages_url'] = value['messages_url']
            if kept:
                kept['has_prompts'] = bool(value.get('prompts_dir'))
                result['i18n'] = kept
    return result


def build_entry(ext_type, name, manifest_path, repo, commit, subdir=None):
    with open(manifest_path) as f:
        manifest = json.load(f)

    # subdir is the extension's directory relative to the repo root
    # (e.g. "ui/github-issue-importer").
    subdir = subdir or os.path.relpath(os.path.dirname(manifest_path), EXTENSIONS_DIR).replace(os.sep, '/')
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

    # Git-pinned source fields.
    entry['repo'] = repo
    entry['commit'] = commit
    entry['subdir'] = subdir

    try:
        caps = simplify_capabilities(manifest.get('capabilities'), manifest.get('config_schema'))
    except UnresolvedTemplate as exc:
        print('%s: %s — the index has to carry the directory it means, because that is what the '
              'install review asks about; fix the reference or the config_schema default it names'
              % (subdir, exc), file=sys.stderr)
        sys.exit(1)
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


def git_show_manifest(commit, subdir):
    """The manifest as `commit` holds it, or None if that commit has no such file.

    An entry's advertised fields are a promise about the commit it names, and the commit is the only
    place that promise can be read back. `git_subdir_tree()` asks the same question of the bytes.
    """
    if not commit or not subdir:
        return None
    try:
        out = subprocess.run(
            ['git', '-C', EXTENSIONS_DIR, 'show', '%s:%s/manifest.json' % (commit, subdir)],
            capture_output=True, text=True, check=True,
        ).stdout
    except (subprocess.SubprocessError, FileNotFoundError):
        return None
    return out


def git_subdir_tree(commit, subdir):
    """The tree hash of `subdir` as `commit` holds it, or '' when that commit has no such directory.

    An install materializes the whole subdirectory, so this is what two commits have to agree on for a
    pin to still name the extension its entry describes.
    """
    if not commit or not subdir:
        return ''
    try:
        return subprocess.run(
            ['git', '-C', EXTENSIONS_DIR, 'rev-parse', '--verify', '--quiet',
             '%s:%s' % (commit, subdir)],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    except (subprocess.SubprocessError, FileNotFoundError):
        return ''


def pinned_entry(existing_entry, ext_type, name, repo):
    """Project the entry from the commit the registry currently pins, or None if it cannot be."""
    content = git_show_manifest(existing_entry.get('commit'), existing_entry.get('subdir'))
    if content is None:
        return None
    try:
        json.loads(content)
    except json.JSONDecodeError:
        return None
    return build_entry_from_text(existing_entry, content, ext_type, name, repo)


def build_entry_from_text(existing_entry, content, ext_type, name, repo):
    path = os.path.join(tempfile.gettempdir(), 'mike-pin-check-%s.json' % os.getpid())
    with open(path, 'w') as f:
        f.write(content)
    try:
        entry = build_entry(ext_type, name, path, repo, existing_entry.get('commit', ''),
                            subdir=existing_entry.get('subdir'))
    finally:
        os.unlink(path)
    return entry


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
        manifest_path = os.path.join(dirpath, 'manifest.json')
        entry = build_entry(ext_type, name, manifest_path, repo, head)
        prev = existing.get((ext_type, name)) or {}
        subdir = entry['subdir']

        # A pin can only name committed content: the manifest the entry is projected from has to be the
        # one HEAD holds, or the index advertises fields its pinned commit does not contain. The entry
        # itself is projected from that commit below, never from the previous registry file, which is a
        # projection and would agree with whatever it was last written with.
        with open(manifest_path) as f:
            worktree_manifest = f.read()
        head_manifest = git_show_manifest(head, subdir)
        if head_manifest is None:
            print('%s: %s is not committed at HEAD (%s); commit the extension first — a pin can '
                  'only point at committed content' % (subdir, name, head[:12] or 'no HEAD'),
                  file=sys.stderr)
            sys.exit(1)
        if json.loads(worktree_manifest) != json.loads(head_manifest):
            print('%s: %s has uncommitted changes relative to %s; commit them before publishing, '
                  'or the registry would advertise content its pinned commit does not contain'
                  % (subdir, name, head[:12]), file=sys.stderr)
            sys.exit(1)

        # A run that touched nothing has to leave the registry stable, so the previous pin is kept only
        # while it still holds this content on both counts: the manifest the entry is projected from, and
        # the subdirectory the install checks out, which is the content an install actually receives.
        pinned = pinned_entry(prev, ext_type, name, repo) if prev else None
        pin_tree = git_subdir_tree(prev.get('commit', ''), subdir)
        if pinned is not None and pin_tree and entry_matches(entry, pinned) \
                and pin_tree == git_subdir_tree(head, subdir):
            entry['commit'] = prev['commit']

        for field in CURATED_FIELDS:
            if field in prev:
                entry[field] = prev[field]
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
