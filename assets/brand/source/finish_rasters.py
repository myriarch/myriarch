# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Finish the PNGs rendered by resvg and build favicon.ico.

Usage: python -I finish_rasters.py <brand_dir> <favicon-48.png>

- PNGs with a solid background (apple-touch-icon, social-preview, og-image)
  are stored as RGB without alpha (iOS fills transparency with black).
- Every PNG is re-encoded with zlib optimisation if that is smaller
  (pixels are unchanged).
- favicon.ico holds the pixel-tuned 16, 32 and 48 px bitmaps (each size is
  its own rendering, not a downscale) and is verified after writing.
"""
import io
import os
import sys

from PIL import Image

OPAQUE = {"apple-touch-icon.png", "social-preview.png", "og-image.png"}
EXPECTED = {
    "favicon-16.png": (16, 16), "favicon-32.png": (32, 32), "apple-touch-icon.png": (180, 180),
    "icon-192.png": (192, 192), "icon-512.png": (512, 512),
    "social-preview.png": (1280, 640), "og-image.png": (1200, 630),
}


def reencode(path, opaque):
    im = Image.open(path)
    im.load()
    if opaque:
        rgba = im.convert("RGBA")
        if rgba.getextrema()[3][0] != 255:
            raise SystemExit(f"{path}: expected an opaque image")
        im = rgba.convert("RGB")
    buf = io.BytesIO()
    im.save(buf, format="PNG", optimize=True)
    data = buf.getvalue()
    if opaque or len(data) < os.path.getsize(path):
        with open(path, "wb") as f:
            f.write(data)
    return Image.open(path)


def main():
    bdir, fav48 = sys.argv[1], sys.argv[2]
    for name, size in EXPECTED.items():
        p = os.path.join(bdir, name)
        im = reencode(p, name in OPAQUE)
        assert im.size == size, (name, im.size)
        print(f"{name:22} {im.size[0]}x{im.size[1]} {im.mode:4} {os.path.getsize(p):7} B")

    im16 = Image.open(os.path.join(bdir, "favicon-16.png")).convert("RGBA")
    im32 = Image.open(os.path.join(bdir, "favicon-32.png")).convert("RGBA")
    im48 = Image.open(fav48).convert("RGBA")
    ico = os.path.join(bdir, "favicon.ico")
    im48.save(ico, format="ICO", sizes=[(16, 16), (32, 32), (48, 48)], append_images=[im16, im32])

    # verify: every entry is present and pixel-identical to its source
    check = Image.open(ico)
    sizes = sorted(check.info["sizes"])
    assert sizes == [(16, 16), (32, 32), (48, 48)], sizes
    for src in (im16, im32, im48):
        check.size = src.size
        check.load()
        got = check.convert("RGBA")
        assert got.tobytes() == src.tobytes(), f"ICO entry {src.size} differs from its source"
    print(f"favicon.ico            sizes={sizes} {os.path.getsize(ico)} B (entries verified)")


if __name__ == "__main__":
    main()
