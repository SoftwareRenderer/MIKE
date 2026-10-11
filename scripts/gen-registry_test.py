#!/usr/bin/env python3
"""`scripts/gen-registry.py` in both directions.

A published entry promises that its pinned commit holds the extension, and what an install materializes
is the whole subdirectory — so every scenario runs against a throwaway extensions repo, once with the
rule kept and once with exactly that rule broken. A generator that keeps a stale pin installs bytes no
curator vetted, silently, while `--check` prints "up to date".

    make test-gen-registry
"""

import json
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
GENERATOR = HERE / 'gen-registry.py'

MANIFEST = {
    'name': 'demo',
    'version': '1.0.0',
    'display_name': 'Demo',
    'description': 'The fixture extension.',
    'author': 'tester',
    'capabilities': {'ui': {'injection_points': ['fileEditor'],
                           'permissions': ['fileEditor.setContent']}},
}

ran = 0
failures = []


def check(name, ok, detail=''):
    global ran
    ran += 1
    if ok:
        print(f'ok   {name}')
        return
    failures.append(name)
    print(f'FAIL {name}')
    if detail:
        print('     ' + detail.replace('\n', '\n     '))


def git(repo, *args):
    """One git command, with the identity and signing pinned so a developer's config cannot leak in."""
    return subprocess.run(
        ['git', '-c', 'user.name=fixture', '-c', 'user.email=fixture@example.com',
         '-c', 'commit.gpgsign=false', '-c', 'init.defaultBranch=main', '-C', str(repo), *args],
        capture_output=True, text=True, check=True).stdout.strip()


class Repo:
    """A `MIKE/` + `MIKE-extensions/` pair laid out the way the generator resolves its paths."""

    def __init__(self, root: Path):
        self.mike = root / 'MIKE'
        self.ext = root / 'MIKE-extensions'
        (self.mike / 'scripts').mkdir(parents=True)
        self.ext.mkdir()
        shutil.copy(GENERATOR, self.mike / 'scripts' / 'gen-registry.py')
        self.manifest = self.write('ui/demo/manifest.json', json.dumps(MANIFEST, indent=2) + '\n')
        self.write('ui/demo/Module.js', 'export const v = 1;\n')
        git(self.ext, 'init', '-q')
        self.commit('first')

    def write(self, rel, text):
        path = self.ext / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def commit(self, message):
        git(self.ext, 'add', '-A')
        git(self.ext, 'commit', '-q', '-m', message)

    def head(self):
        return git(self.ext, 'rev-parse', 'HEAD')

    def run(self, *args):
        proc = subprocess.run([sys.executable, str(self.mike / 'scripts' / 'gen-registry.py'), *args],
                              capture_output=True, text=True, cwd=self.mike)
        return proc.returncode, proc.stdout + proc.stderr

    def entries(self):
        return {f"{e['type']}/{e['name']}": e for e in json.loads(self.registry().read_text())}

    def registry(self):
        return self.ext / 'registry.json'

    def pin(self):
        return self.entries()['ui/demo']['commit']


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / 'case1'
        root.mkdir()
        r = Repo(root)
        code, out = r.run()
        check('1 a first run pins HEAD', code == 0 and r.pin() == r.head(), out)
        code, out = r.run()
        check('1 a run that touched nothing leaves the pin alone', code == 0 and r.pin() == r.head(), out)
        code, out = r.run('--check')
        check('1 --check passes a registry the tree matches', code == 0 and 'up to date' in out, out)

    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / 'case2'
        root.mkdir()
        r = Repo(root)
        r.run()
        old = r.pin()
        # The bug this file exists for: code shipped, manifest untouched.
        r.write('ui/demo/Module.js', 'export const v = 2;\n')
        r.commit('fix the renderer')
        code, out = r.run('--check')
        check('2 --check refuses a pin the extension has grown past', code == 1 and 'out of date' in out, out)
        code, out = r.run()
        check('2 a code-only commit moves the pin', code == 0 and r.pin() == r.head() != old,
              f'{r.pin()} vs {old} / {r.head()}')
        check('2 and the version it reports is the manifest’s own',
              r.entries()['ui/demo']['version'] == '1.0.0', json.dumps(r.entries()['ui/demo']))
        code, out = r.run('--check')
        check('2 --check passes once the pin moved', code == 0 and 'up to date' in out, out)
        code, out = r.run()
        check('2 and the new pin is stable on the next run', code == 0 and r.pin() == r.head(), out)

    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / 'case3'
        root.mkdir()
        r = Repo(root)
        r.run()
        r.write('ui/demo/Module.js', 'export const v = 3;\n')
        code, out = r.run()
        check('3 uncommitted code is nobody’s business: the pin stays at HEAD',
              code == 0 and r.pin() == r.head(), out)
        r.write('ui/demo/manifest.json', json.dumps({**MANIFEST, 'version': '1.1.0'}, indent=2) + '\n')
        code, out = r.run()
        check('3 an uncommitted manifest is refused by name',
              code == 1 and 'uncommitted changes' in out and 'ui/demo' in out, out)
        r.commit('bump to 1.1.0')
        code, out = r.run()
        check('3 once committed, a manifest change moves the pin too',
              code == 0 and r.pin() == r.head() and r.entries()['ui/demo']['version'] == '1.1.0', out)

    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / 'case4'
        root.mkdir()
        r = Repo(root)
        r.run()
        # `featured` is set in the generated file by a curator, and the Browse list orders on it.
        entries = json.loads(r.registry().read_text())
        entries[0]['featured'] = True
        r.registry().write_text(json.dumps(entries, indent=4, ensure_ascii=False) + '\n')
        code, out = r.run('--check')
        check('4 a curated field is not a drift --check reports', code == 0 and 'up to date' in out, out)
        code, out = r.run()
        check('4 a regeneration carries the curated field across',
              code == 0 and r.entries()['ui/demo'].get('featured') is True, out)
        code, out = r.run('--check')
        check('4 so --check stays green across the regeneration', code == 0 and 'up to date' in out, out)
        pinned = r.pin()
        r.write('ui/demo/Module.js', 'export const v = 4;\n')
        r.commit('styling')
        code, out = r.run()
        check('4 a curated entry follows the same pin rule',
              code == 0 and r.pin() == r.head() != pinned and r.entries()['ui/demo'].get('featured') is True,
              out)
        entries = json.loads(r.registry().read_text())
        del entries[0]['featured']
        r.registry().write_text(json.dumps(entries, indent=4, ensure_ascii=False) + '\n')
        code, out = r.run()
        check('4 deleting a curated field by hand is how it gets deleted',
              code == 0 and 'featured' not in r.entries()['ui/demo'], out)

    print(f'\n{ran} checks, {len(failures)} failure(s).')
    if failures:
        print('REGISTRY GENERATOR SELF-TEST FAILED: ' + '; '.join(failures))
        return 1
    print('REGISTRY GENERATOR SELF-TEST PASSED: a pin cannot outlive the content it names, and a '
          'curation cannot be regenerated away.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
