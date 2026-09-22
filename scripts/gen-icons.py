#!/usr/bin/env python3
"""Generate the PWA icons and the favicon from the one app logo.

Requires: python3, cairosvg, Pillow (PIL)

Usage:
    python3 scripts/gen-icons.py            # render
    python3 scripts/gen-icons.py --check    # the logo source resolves and every output exists

Everything is written to `frontend/public`, which is the only copy `index.html`, the PWA manifest
in `vite.config.ts` and `backend/server/middleware.go` read. A PNG written anywhere else is an
output nothing reads.
"""

import os
import struct
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ICON_SOURCE = os.path.join(REPO, 'frontend/src/lib/assets/icons/mike.svg')
PUBLIC_DIR = os.path.join(REPO, 'frontend/public')

PWA_ICONS = [('pwa-icon-192.png', 192), ('pwa-icon-512.png', 512)]
FAVICON_PNG = 'favicon.png'

# favicon.ico answers the browser's own probe; no app file names it, so --check skips it.
REFERENCED = PWA_ICONS + [(FAVICON_PNG, 32)]


def source_or_exit():
    if not os.path.isfile(ICON_SOURCE):
        raise SystemExit(f'gen-icons: logo source not found: {ICON_SOURCE}')
    return ICON_SOURCE


def render(size):
    import cairosvg
    return cairosvg.svg2png(url=source_or_exit(), output_width=size, output_height=size)


def generate_pwa_icons():
    """Render the two manifest sizes; the corner pixel shows whether transparency survived."""
    from PIL import Image
    for name, size in PWA_ICONS:
        out = os.path.join(PUBLIC_DIR, name)
        with open(out, 'wb') as f:
            f.write(render(size))
        img = Image.open(out)
        print(f'  {name}: {img.mode}, corner={img.getpixel((0, 0))}')


def generate_favicon():
    """Render favicon.png (named by index.html) and a multi-size favicon.ico for the browser probe."""
    from PIL import Image
    out = os.path.join(PUBLIC_DIR, FAVICON_PNG)
    with open(out, 'wb') as f:
        f.write(render(32))
    img = Image.open(out)
    print(f'  favicon.png: {img.mode}, corner={img.getpixel((0, 0))}')

    chunks = [render(size) for size in (16, 32, 48)]
    ico = struct.pack('<HHH', 0, 1, len(chunks))  # reserved, type=1(icon), count
    total_size = 6 + 16 * len(chunks)
    for png in chunks:
        w = struct.unpack('>I', png[16:20])[0]
        h = struct.unpack('>I', png[20:24])[0]
        entry = struct.pack('<BBBBHHII',
            w if w < 256 else 0,
            h if h < 256 else 0,
            0, 0, 1, 32,
            len(png),
            total_size
        )
        ico += entry
        total_size += len(png)
    for png in chunks:
        ico += png

    with open(os.path.join(PUBLIC_DIR, 'favicon.ico'), 'wb') as f:
        f.write(ico)
    print(f'  favicon.ico: {len(ico)} bytes ({len(chunks)} sizes)')


def check():
    """The source resolves and every referenced output exists and holds bytes — not an mtime
    comparison, since a rename or a fresh clone resets every mtime.
    """
    source_or_exit()
    problems = []
    for name, _ in REFERENCED:
        out = os.path.join(PUBLIC_DIR, name)
        if not os.path.isfile(out):
            problems.append(f'{name}: missing')
        elif os.path.getsize(out) == 0:
            problems.append(f'{name}: empty')
    if problems:
        for p in problems:
            print(f'  {p}')
        raise SystemExit(f'gen-icons --check: {len(problems)} icon(s) need regenerating')
    print(f'gen-icons --check: {len(REFERENCED)} icons present, logo source resolves')


if __name__ == '__main__':
    if '--check' in sys.argv[1:]:
        check()
        sys.exit(0)
    source_or_exit()
    os.makedirs(PUBLIC_DIR, exist_ok=True)
    print('Generating PWA icons...')
    generate_pwa_icons()
    print('Generating favicon...')
    generate_favicon()
    print('Done!')
