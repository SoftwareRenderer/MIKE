#!/usr/bin/env python3
"""Self-test for scripts/svelte-check-once.sh — the one compile that answers `npm run check` and
`npm run check:unused` is only safe while its two derivations and its cache rule are right, and all three
can go quietly wrong: a report parsed too narrowly reports zero errors, a cached report reused after an edit
reports the tree as it was, and a run that analyzed nothing reports "no errors".

Usage (from frontend/):   python3 scripts/svelte-check-once_test.py

Each case plants either a machine report or a whole miniature frontend tree with a stub svelte-check, so
the compile-and-cache path is measured by how many times the stub ran rather than by how long it took.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, 'svelte-check-once.sh')
cases_run = 0
failures = []

TS = 1790680933766


def line(severity, path, row, col, message):
    return '%d %s "%s" %d:%d %s' % (TS, severity, path, row, col, json.dumps(message))


def report(*lines, files=1338, errors=None, warnings=None):
    body = [line(l[0], l[1], l[2], l[3], l[4]) for l in lines]
    n_err = sum(1 for l in lines if l[0] == 'ERROR') if errors is None else errors
    n_warn = sum(1 for l in lines if l[0] == 'WARNING') if warnings is None else warnings
    body.append('%d COMPLETED %d FILES %d ERRORS %d WARNINGS 3 FILES_WITH_PROBLEMS'
                % (TS, files, n_err, n_warn))
    return '\n'.join(body) + '\n'


TYPE_ERROR = ('src/a.ts', 4, 7, "Type '\"a\" | \"b\"' is not assignable to type 'number'.")
UNUSED = ('src/a.ts', 9, 11, "'notExempt' is declared but its value is never read.")
EXEMPT = ('src/b.svelte', 2, 15, "'_v' is declared but its value is never read.")
PARAM_UNUSED = ('src/a.ts', 20, 33, "'ignored' is declared but never used.")
A11Y = ('src/b.svelte', 6, 1, 'A11y: <img> element should have an alt attribute.')


def run(mode, report_text=None, cwd=None, env=None):
    """One invocation of the script. With report_text it reads that report; otherwise it compiles."""
    environ = dict(os.environ)
    environ.pop('MIKE_SVELTE_REPORT', None)
    if report_text is not None:
        fd, path = tempfile.mkstemp(suffix='.txt')
        with os.fdopen(fd, 'w') as fh:
            fh.write(report_text)
        environ['MIKE_SVELTE_REPORT'] = path
    environ.update(env or {})
    proc = subprocess.run(['bash', SCRIPT, mode], cwd=cwd or HERE + '/..', env=environ,
                          capture_output=True, text=True)
    return proc


def check(desc, ok):
    global cases_run
    cases_run += 1
    if not ok:
        failures.append(desc)


# --- the two verdicts out of one report ---------------------------------------------------------------

mixed = report(('ERROR',) + TYPE_ERROR, ('ERROR',) + UNUSED, ('ERROR',) + EXEMPT,
               ('ERROR',) + PARAM_UNUSED, ('WARNING',) + A11Y)

p = run('check', mixed)
check('check fails on the type error', p.returncode == 1)
check('check prints the type error with its escaped quotes decoded',
      "Type '\"a\" | \"b\"' is not assignable" in p.stdout and 'src/a.ts:4:7' in p.stdout)
check('check stays out of the unused class',
      'notExempt' not in p.stdout and 'ignored' not in p.stdout and '_v' not in p.stdout)
check('check prints the warning without failing on it', 'A11y' in p.stdout)
check('check counts only its own errors in the summary',
      'svelte-check found 1 errors and 1 warnings' in p.stdout)
p = run('unused', mixed)
check('unused fails on the two unused declarations', p.returncode == 1)
check('unused prints file:line name', 'src/a.ts:9 notExempt' in p.stdout and
      'src/a.ts:20 ignored' in p.stdout)
check('unused keeps the `_`-prefix exemption', '_v' not in p.stdout)
check('unused names its own count', '2 declaration(s) nothing reads' in p.stderr)

clean_unused = report(('ERROR',) + EXEMPT)
check('a report of nothing but exempt declarations is green for unused',
      run('unused', clean_unused).returncode == 0)
check('...and green for check too, though the compile itself exited non-zero',
      run('check', clean_unused).returncode == 0)

check('a warning-only report is green for check',
      run('check', report(('WARNING',) + A11Y)).returncode == 0)

# A run that analyzed nothing must not read as clean: the summary names the file count it checked.
for desc, text in [
    ('zero files analyzed', report(('ERROR',) + EXEMPT, files=0)),
    ('no summary at all', line('ERROR', 'src/a.ts', 4, 7, 'boom') + '\n'),
]:
    for mode in ('check', 'unused'):
        proc = run(mode, text)
        check('%s: %s does not pass' % (mode, desc), proc.returncode == 1)
        check('%s: %s says the check did not run' % (mode, desc), 'did not run' in proc.stderr)

failed = '%d FAILURE "svelte-check blew up"\n' % TS + report()
for mode in ('check', 'unused'):
    proc = run(mode, failed)
    check('%s: a svelte-check FAILURE line is not green' % mode, proc.returncode == 1)
    check('%s: a svelte-check FAILURE line names the failure' % mode, 'blew up' in proc.stderr)

# --- the compile-and-cache rule the compile-and-cache rule, with a stub svelte-check ---------------------------------------------

STUB = """#!/usr/bin/env bash
printf 'x\\n' >> "$MIKE_STUB_COUNT"
cat "$MIKE_STUB_REPORT"
exit "${MIKE_STUB_EXIT:-0}"
"""

ROOT_FILES = ['tsconfig.json', 'tsconfig.unused.json', 'svelte.config.js', 'package.json',
              'package-lock.json', 'vite.config.ts', 'vite-env.d.ts']


def write(path, text):
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(text)


def tree():
    """A miniature frontend/: one source file, the configs the fingerprint hashes, a stub svelte-check."""
    root = tempfile.mkdtemp()
    for name in ROOT_FILES:
        write(os.path.join(root, name), '{}\n')
    os.makedirs(os.path.join(root, 'src'))
    os.makedirs(os.path.join(root, 'scripts'))
    shutil.copy(SCRIPT, os.path.join(root, 'scripts', 'svelte-check-once.sh'))
    write(os.path.join(root, 'src', 'a.ts'), 'export const a = 1;\n')
    stub = os.path.join(root, 'stub.sh')
    write(stub, STUB)
    os.chmod(stub, 0o755)
    return root


def compile_run(root, report_text, mode='check', edit=None):
    """Run the real compile path in `root`. Returns (proc, compiles) — compiles counts the stub calls this
    invocation needed, which is the whole question about the cache."""
    if edit is not None:
        write(os.path.join(root, 'src', 'a.ts'), edit)
    stub_report = os.path.join(root, 'stub-report.txt')
    write(stub_report, report_text)
    count = os.path.join(root, 'stub.count')
    write(count, '')
    env = {'MIKE_SVELTE_CHECK': os.path.join(root, 'stub.sh'),
           'MIKE_STUB_REPORT': stub_report,
           'MIKE_STUB_COUNT': count,
           'MIKE_STUB_EXIT': '1' if 'ERROR' in report_text else '0'}
    proc = subprocess.run(['bash', os.path.join(root, 'scripts', 'svelte-check-once.sh'), mode],
                          cwd=root, env={**os.environ, **env}, capture_output=True, text=True)
    with open(count, encoding='utf-8') as fh:
        return proc, sum(1 for _ in fh)


root = tree()
exempt_only = report(('ERROR',) + EXEMPT)
p, n = compile_run(root, exempt_only)
check('the first run compiles', n == 1 and p.returncode == 0)
p, n = compile_run(root, exempt_only)
check('the second run reads the cached report, not the compiler', n == 0 and p.returncode == 0)
check('...and gives the same verdict from it', 'svelte-check found 0 errors' in p.stdout)
p, n = compile_run(root, exempt_only, mode='unused')
check('the other verdict costs no second compile', n == 0 and p.returncode == 0)

typed = report(('ERROR',) + TYPE_ERROR)
unused = report(('ERROR',) + UNUSED)
p, n = compile_run(root, unused, mode='unused', edit='export const a = 1;\nconst notExempt = 2;\n')
check('an edited file re-runs the compiler', n == 1)
check('and the new declaration reaches the verdict that owns it',
      p.returncode == 1 and 'src/a.ts:9 notExempt' in p.stdout)

p, n = compile_run(root, unused, mode='check')
check('the unused class never lands in the check verdict', n == 0 and p.returncode == 0)

p, n = compile_run(root, '', mode='check', edit='export const a = 2;\n')
check('a run that produced no report exits non-zero', p.returncode == 1)
check('...and names what it refused', 'without a report' in p.stderr)
p, n = compile_run(root, typed, mode='check')
check('the refused run was not cached, so the next run compiles again', n == 1)
check('and the real error is reported once the compiler answers',
      p.returncode == 1 and 'src/a.ts:4:7' in p.stdout)

p, n = compile_run(root, typed, mode='check')
check('the answered run is cached, so nothing recompiles', n == 0 and p.returncode == 1)

other = tree()
shutil.copy(os.path.join(root, 'src', 'a.ts'), os.path.join(other, 'src', 'a.ts'))
p, n = compile_run(other, typed, mode='check')
check('a second tree starts from its own cache, not the first one\'s report', n == 1 and p.returncode == 1)

# The tree the run happens in is the tree that owns the script, so this one destroys src/ only: an empty
# file set is the report that must never be mistaken for a clean check.
shutil.rmtree(os.path.join(root, 'src'))
p, n = compile_run(root, exempt_only, mode='check')
check('a tree with no src/ is refused, not reported clean',
      p.returncode == 1 and n == 0 and 'refusing' in p.stderr)


if failures:
    print('svelte-check-once_test: %d FAILURE(S) of %d cases' % (len(failures), cases_run))
    for f in failures:
        print('  %s' % f)
    print('A missed error here is a typecheck that passes: the derivation and the cache rule are the gate.')
    sys.exit(1)
print('svelte-check-once_test: ok — %d cases, both verdicts and the cache rule hold' % cases_run)
