#!/usr/bin/env python3
"""The wall clock an e2e suite pays for waits it never actually performs.

    cd frontend && python3 sleep-gate.py                    # both measurements, exit 1 on a finding
    cd frontend && python3 sleep-gate.py -v                 # + every site, with its reason
    cd frontend && python3 sleep-gate.py --update-ceiling   # re-freeze after removing sleeps

`npm run check:sleeps` runs this, and `make test-frontend` runs it beside its self-test,
`sleep-gate_test.py`. `docs/testing.md → The sleep gate` owns the rules; what follows is the shortest
version of each.

A `page.waitForTimeout(n)` is wall clock spent whether or not the thing it waits for has arrived: too
short under load, forever too long when green. Almost every wait has a condition behind it — a locator
(`toBeVisible`/`toBeHidden`/`toHaveCount`), the DOM's own settle (`expect.poll`/`waitForFunction`), an
executor turn (`waitForSendButton`/`waitForTurnAnswered` in `tests/e2e/helpers.ts`), or a write landing
somewhere readable (re-read it through the API or IndexedDB).

Measurement 1 — a sleep with no reason on its line must be 0. A duration that IS the subject (drag settle
time, an animation or debounce window the spec measures, a sampling cadence) is allowed, and says so in a
comment on the same line, long enough to be a reason:

    await page.waitForTimeout(1100);  // moved_at has second precision; the order needs distinct seconds

A reason belongs on the line because a comment above a sleep usually explains the code around it, and
explaining the neighbouring lines is how an arbitrary delay starts looking documented.

Measurement 2 — how many sleeps the suite keeps, frozen as `SLEEP_CEILING`. A reasoned sleep still costs
every run, so adding one needs the ceiling raised; deleting one lands the smaller number.
"""
import os
import re
import sys

SPEC_SUFFIXES = ('.spec.ts', '.svelte.ts', '.ts')
SKIP_DIRS = {'node_modules', '.svelte-kit', 'dist', 'test-results', 'playwright-report'}
SLEEP_RE = re.compile(r'\bwaitForTimeout\s*\(')
# Long enough that a reason has to be a phrase, short enough that a real one fits on a line.
MIN_REASON_WORDS = 4
CALL = 'waitForTimeout'

# Measured with `python3 sleep-gate.py --update-ceiling`. It only ever goes down; raising it is a
# decision a person makes in a commit message, so the constant is the register.
SLEEP_CEILING = 18


def read(path):
    with open(path, encoding='utf-8') as fh:
        return fh.read()


def split_code_and_comment(line, in_block):
    """(code, comment) for one line, with `in_block` saying whether it starts inside a /* */ block.

    A quote tracker, not a TypeScript parser: a quoted body is dropped from the code half so a call named
    inside a string is not a site, and it gives up on a template literal that spans lines — which errs by
    reporting a site rather than hiding one.
    """
    code, comment, buf = [], [], []
    quote = None
    i = 0
    while i < len(line):
        ch = line[i]
        nxt = line[i + 1] if i + 1 < len(line) else ''
        if in_block:
            if ch == '*' and nxt == '/':
                in_block = False
                i += 2
                continue
            i += 1
            continue
        if quote:
            if ch == '\\':
                i += 2
                continue
            if ch == quote:
                quote = None
                buf.append(ch)
            i += 1
            continue
        if ch in ('"', "'", '`'):
            quote = ch
            buf.append(ch)
            i += 1
            continue
        if ch == '/' and nxt == '/':
            comment.append(line[i + 2:])
            break
        if ch == '/' and nxt == '*':
            in_block = True
            i += 2
            continue
        buf.append(ch)
        i += 1
    code.append(''.join(buf))
    return ''.join(code), ''.join(''.join(comment)), in_block


def reason_of(comment):
    """The comment when it is long enough to be a reason, else None."""
    words = [w for w in re.split(r'[^0-9A-Za-z]+', comment.strip()) if w]
    return comment.strip() if len(words) >= MIN_REASON_WORDS else None


def spec_files(root):
    """The spec sources the gate reads: everything under `tests/` that can hold a wait."""
    out = []
    for dirpath, dirnames, filenames in os.walk(os.path.join(root, 'tests')):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            if not name.endswith(SPEC_SUFFIXES):
                continue
            out.append(os.path.relpath(os.path.join(dirpath, name), root))
    return out


def sites(root):
    """Every call site: (relpath, line number, reason or None)."""
    found = []
    for rel in spec_files(root):
        lines = read(os.path.join(root, rel)).splitlines()
        in_block = False
        for n, line in enumerate(lines):
            code, comment, in_block = split_code_and_comment(line, in_block)
            if SLEEP_RE.search(code):
                found.append((rel, n + 1, reason_of(comment)))
    return found


def update_ceiling(path, count):
    with open(path, encoding='utf-8') as fh:
        source = fh.read()
    new, n = re.subn(r'(?m)^SLEEP_CEILING = \d+$', f'SLEEP_CEILING = {count}', source)
    if n != 1:
        raise SystemExit(f'{path}: expected exactly one SLEEP_CEILING line, rewrote {n}')
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(new)


def main(argv, root='.', gate_path=None):
    verbose = '-v' in argv
    recount = '--update-ceiling' in argv
    found = sites(root)
    unreasoned = [s for s in found if not s[2]]
    reasoned = [s for s in found if s[2]]

    for rel, line, _ in unreasoned:
        print(f'  SLEEP {rel}:{line}  — no reason on the line: wait for a condition (a locator, '
              f'`expect.poll`, a turn helper), or say on that line why this duration is the measurement')
    if verbose:
        for rel, line, why in reasoned:
            print(f'   keep  {rel}:{line}  {why}')
    print('----')
    print(f'{len(unreasoned):5d}  sleeps with no reason on the line (must be 0)')
    print(f'{len(reasoned):5d}  sleeps whose duration is the measurement, each with its reason')
    print(f'{len(found):5d}  {CALL} calls in tests/ (ceiling {SLEEP_CEILING})')

    if recount:
        update_ceiling(gate_path or os.path.abspath(__file__), len(found))
        print(f'ceiling re-frozen at {len(found)}')
        return 1 if unreasoned else 0

    failed = bool(unreasoned)
    if len(found) > SLEEP_CEILING:
        print(f'  COUNT  the suite grew from {SLEEP_CEILING} to {len(found)} sleeps. A reason makes a '
              f'sleep legal, not free — every one of them is paid on every run. Replace it with the '
              f'condition behind it, or re-freeze with `python3 sleep-gate.py --update-ceiling` and say '
              f'why in the commit.')
        failed = True
    return 1 if failed else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
