#!/usr/bin/env python3
"""Generate all app icons (PWA + favicon) from SVG source.

Requires: python3, cairosvg, Pillow (PIL)

Usage:
    python3 scripts/gen-icons.py
"""

import struct
import shutil
import cairosvg
from PIL import Image

SVG_PATH = 'frontend/src/lib/assets/icons/pwa-icon.svg'
FAVICON_SVG = 'frontend/src/lib/assets/icons/favicon.svg'
ICON_DIR = 'frontend/src/lib/assets/icons'
PUBLIC_DIR = 'frontend/public'


def generate_pwa_icons():
    """Generate 192x192 and 512x512 PWA icons with transparent corners."""
    for size in [192, 512]:
        png_data = cairosvg.svg2png(url=SVG_PATH, output_width=size, output_height=size)
        out_path = f"{ICON_DIR}/pwa-icon-{size}.png"
        with open(out_path, 'wb') as f:
            f.write(png_data)
        shutil.copy2(out_path, f"{PUBLIC_DIR}/pwa-icon-{size}.png")
        img = Image.open(out_path)
        print(f"  pwa-icon-{size}.png: {img.mode}, corner={img.getpixel((0, 0))}")


def generate_favicon():
    """Generate favicon.png (32x32) and favicon.ico (16/32/48) with transparent corners."""
    # PNG favicon
    png_data = cairosvg.svg2png(url=FAVICON_SVG, output_width=32, output_height=32)
    with open(f"{PUBLIC_DIR}/favicon.png", 'wb') as f:
        f.write(png_data)
    img = Image.open(f"{PUBLIC_DIR}/favicon.png")
    print(f"  favicon.png: {img.mode}, corner={img.getpixel((0, 0))}")

    # ICO favicon (multi-size)
    sizes = [16, 32, 48]
    chunks = []
    for size in sizes:
        png = cairosvg.svg2png(url=FAVICON_SVG, output_width=size, output_height=size)
        chunks.append(png)

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

    with open(f"{PUBLIC_DIR}/favicon.ico", 'wb') as f:
        f.write(ico)
    print(f"  favicon.ico: {len(ico)} bytes ({len(chunks)} sizes)")


if __name__ == '__main__':
    import os
    os.makedirs(PUBLIC_DIR, exist_ok=True)
    print("Generating PWA icons...")
    generate_pwa_icons()
    print("Generating favicon...")
    generate_favicon()
    print("Done!")
