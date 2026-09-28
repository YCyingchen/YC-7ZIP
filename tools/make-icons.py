#!/usr/bin/env python3
"""Generate the YC-7ZIP app icons for the fnOS package and the web UI.

The mark is a stack of archive blocks above a tray — the same shape the
in-page logo uses — drawn at 4x and downsampled so the edges stay clean at
64px. Run with the managed Python runtime:

    "$MIMO_PYTHON" tools/make-icons.py
"""

from __future__ import annotations

import os
import sys
from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent.parent
FPK_UI_IMAGES = ROOT / "deploy" / "fpk" / "app" / "ui" / "images"
ASSETS_IMAGES = ROOT / "assets" / "images"

BG = (11, 15, 20, 255)
ACCENT = (61, 220, 151, 255)
ACCENT_MID = (61, 220, 151, 200)
ACCENT_FAINT = (61, 220, 151, 140)

SUPERSAMPLE = 4


def draw_mark(size: int) -> Image.Image:
    """Render the mark at `size` pixels, anti-aliased via supersampling."""
    s = size * SUPERSAMPLE
    img = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    # Rounded dark body so the icon reads on both light and dark backgrounds.
    radius = int(s * 0.22)
    d.rounded_rectangle([0, 0, s - 1, s - 1], radius=radius, fill=BG)

    unit = s / 32.0

    def box(x, y, w, h, r, colour):
        d.rounded_rectangle(
            [x * unit, y * unit, (x + w) * unit, (y + h) * unit],
            radius=r * unit,
            fill=colour,
        )

    # Three stacked archive blocks with rising opacity, then the tray.
    box(12, 4, 8, 5, 1.2, ACCENT_FAINT)
    box(12, 10, 8, 5, 1.2, ACCENT_MID)
    box(12, 16, 8, 5, 1.2, ACCENT)
    box(9, 22, 14, 6, 2.0, ACCENT)

    return img.resize((size, size), Image.LANCZOS)


def main() -> int:
    made = []

    # fnOS package icons: exact 64x64 and 256x256 PNG at the package root.
    for name, size in (("icon_64.png", 64), ("icon_256.png", 256)):
        FPK_UI_IMAGES.mkdir(parents=True, exist_ok=True)
        target = FPK_UI_IMAGES / name
        draw_mark(size).save(target)
        made.append(target)

    fpk_root = ROOT / "deploy" / "fpk"
    (fpk_root / "ICON.PNG").write_bytes((FPK_UI_IMAGES / "icon_64.png").read_bytes())
    (fpk_root / "ICON_256.PNG").write_bytes((FPK_UI_IMAGES / "icon_256.png").read_bytes())
    made.extend([fpk_root / "ICON.PNG", fpk_root / "ICON_256.PNG"])

    # Favicon for the web UI.
    ASSETS_IMAGES.mkdir(parents=True, exist_ok=True)
    favicon = ASSETS_IMAGES / "icon.png"
    draw_mark(192).save(favicon)
    made.append(favicon)

    for p in made:
        print(f"{p.relative_to(ROOT)}  {os.path.getsize(p)} bytes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
