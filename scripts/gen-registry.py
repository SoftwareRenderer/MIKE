#!/usr/bin/env python3
"""Generate extensions/registry.json from extension manifests.

Scans extensions/<type>/<name>/manifest.json for every extension and emits a
registry entry carrying only the minimum information the Browse Registry UI
needs (see AGENTS.md -> Extensions System). The full capability config stays in
each manifest; the registry only carries what the UI renders.

Usage:
    python3 scripts/gen-registry.py            # regenerate registry.json
    python3 scripts/gen-registry.py --check    # verify it is up to date (CI)

Run after adding/editing an extension manifest.
"""

import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXTENSIONS_DIR = os.path.join(ROOT, 'extensions')
REGISTRY_PATH = os.path.join(EXTENSIONS_DIR, 'registry.json')


def simplify_capabilities(manifest_caps):
    """Reduce a manifest capabilities object to what the registry UI renders.

    Only fields surfaced in the capabilities modal are kept:
      - ui:    injection_points ("Modifies") and permissions ("Permissions")
      - proxy: allowed_domains ("Allowed domains")
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
        elif key == 'proxy':
            if value.get('allowed_domains'):
                result['proxy'] = {'allowed_domains': value['allowed_domains']}
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


def build_entry(ext_type, name, manifest_path):
    with open(manifest_path) as f:
        manifest = json.load(f)

    rel = os.path.relpath(manifest_path, EXTENSIONS_DIR).replace(os.sep, '/')
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

    # manifest_url is always present (resolved relative to the registry URL).
    entry['manifest_url'] = rel

    # LSP extensions also ship a Dockerfile the registry can build from.
    if ext_type == 'lsp' and os.path.exists(os.path.join(os.path.dirname(manifest_path), 'Dockerfile')):
        entry['dockerfile_url'] = os.path.join(os.path.dirname(rel), 'Dockerfile').replace(os.sep, '/')

    caps = simplify_capabilities(manifest.get('capabilities'))
    if caps:
        entry['capabilities'] = caps
    return entry


def collect_entries():
    entries = []
    for dirpath, _dirnames, filenames in os.walk(EXTENSIONS_DIR):
        if 'manifest.json' not in filenames:
            continue
        rel = os.path.relpath(dirpath, EXTENSIONS_DIR)
        parts = rel.split(os.sep)
        if len(parts) != 2:
            continue  # only extensions/<type>/<name>/manifest.json
        ext_type, name = parts
        entries.append(build_entry(ext_type, name, os.path.join(dirpath, 'manifest.json')))
    entries.sort(key=lambda e: e['name'])
    return entries


def main():
    check_only = '--check' in sys.argv
    entries = collect_entries()
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
        print(f"  {e['type']}/{e['name']} v{e['version']}")


if __name__ == '__main__':
    main()
