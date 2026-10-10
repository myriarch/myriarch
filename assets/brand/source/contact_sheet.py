# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Compose a contact sheet SVG showing every logo variant on white and on
#0f172a, small-size checks, the raster outputs and the social images.

Usage: python -I contact_sheet.py <brand_dir> <favicon-48.png> <out.svg>

Labels use <text> (Inter, supplied to the renderer) and rasters are embedded
as data URIs: the sheet is a review artefact, not a logo file.
"""
import base64
import os
import re
import sys

NIGHT = "#0f172a"
SVG_OPEN = re.compile(r"<svg\b[^>]*>", re.S)
PANEL_H = 1060


def load(path):
    src = open(path, encoding="utf-8").read()
    m = SVG_OPEN.search(src)
    head = m.group(0)
    vb = re.search(r'viewBox="([^"]+)"', head).group(1)
    inner = src[m.end():src.rindex("</svg>")]
    inner = re.sub(r"<title>.*?</title>", "", inner, flags=re.S)
    inner = re.sub(r"<style>.*?</style>", "", inner, flags=re.S)
    vbw, vbh = (float(v) for v in vb.split()[2:4])
    return vb, inner, vbw, vbh


def place(path, x, y, w=None, h=None):
    vb, inner, vbw, vbh = load(path)
    if w is None:
        w = h * vbw / vbh
    if h is None:
        h = w * vbh / vbw
    return (f'<svg x="{x:.2f}" y="{y:.2f}" width="{w:.2f}" height="{h:.2f}" viewBox="{vb}">{inner}</svg>',
            w, h)


def png(path, x, y, w, h, pixelated=False):
    data = base64.b64encode(open(path, "rb").read()).decode()
    mode = "optimizeSpeed" if pixelated else "optimizeQuality"
    return (f'<image x="{x}" y="{y}" width="{w}" height="{h}" image-rendering="{mode}" '
            f'href="data:image/png;base64,{data}"/>')


def label(x, y, text, color):
    return f'<text x="{x:.1f}" y="{y:.1f}" font-size="13" fill="{color}">{text}</text>'


def panel(bdir, fav48, y0, bg, W):
    fg = "#64748b" if bg == "#ffffff" else "#94a3b8"
    out = [f'<rect x="0" y="{y0}" width="{W}" height="{PANEL_H}" fill="{bg}"/>',
           label(40, y0 + 34, f"background {bg}", fg)]
    # icons
    x, y = 40, y0 + 60
    for name in ("myriarch-icon", "myriarch-icon-dark", "myriarch-icon-black", "myriarch-icon-white"):
        svg, w, h = place(os.path.join(bdir, name + ".svg"), x, y + 18, h=104)
        out += [label(x, y + 8, name, fg), svg]
        x += w + 60
    sx = x + 20
    out.append(label(sx, y + 8, "icon at 48 / 32 / 24 px, favicon.svg at 16 px, dark icon at 32 px", fg))
    cx = sx
    for hpx in (48, 32, 24):
        svg, w, h = place(os.path.join(bdir, "myriarch-icon.svg"), cx, y + 18 + 104 - hpx, h=hpx)
        out.append(svg)
        cx += w + 24
    svg, w, h = place(os.path.join(bdir, "favicon.svg"), round(cx), y + 18 + 104 - 16, w=16)
    out.append(svg)
    cx += 40
    svg, w, h = place(os.path.join(bdir, "myriarch-icon-dark.svg"), cx, y + 18 + 104 - 32, h=32)
    out.append(svg)
    # horizontal lockups
    y = y0 + 230
    for i, name in enumerate(("myriarch-horizontal", "myriarch-horizontal-dark",
                              "myriarch-horizontal-black", "myriarch-horizontal-white")):
        col, row = i % 2, i // 2
        px, py = 40 + col * 560, y + row * 150
        svg, w, h = place(os.path.join(bdir, name + ".svg"), px, py + 22, w=440)
        out += [label(px, py + 8, name, fg), svg]
    sx = 1160
    out.append(label(sx, y + 8, "horizontal and horizontal-dark at 240 / 160 / 120 / 100 px wide", fg))
    yy = y + 24
    for wpx in (240, 160, 120, 100):
        svg, w, h = place(os.path.join(bdir, "myriarch-horizontal.svg"), sx, yy, w=wpx)
        svg2, _, _ = place(os.path.join(bdir, "myriarch-horizontal-dark.svg"), sx + 270, yy, w=wpx)
        out += [svg, svg2]
        yy += h + 22
    # stacked
    y = y0 + 560
    x = 40
    for name in ("myriarch-stacked", "myriarch-stacked-dark"):
        svg, w, h = place(os.path.join(bdir, name + ".svg"), x, y + 24, w=230)
        out += [label(x, y + 8, name, fg), svg]
        x += 320
    out.append(label(700, y + 8, "social-preview.svg (1280x640) and og-image.svg (1200x630)", fg))
    svg, w, h = place(os.path.join(bdir, "social-preview.svg"), 700, y + 24, w=520)
    out.append(svg)
    svg, w, h = place(os.path.join(bdir, "og-image.svg"), 1250, y + 24, w=490)
    out.append(svg)
    # rasters
    y = y0 + 880
    out.append(label(40, y + 8, "PNG / ICO: favicon-16 (1x, 6x), favicon-32 (1x, 3x), favicon.ico 48 entry (1x, 2x), "
                                "apple-touch-icon (180 px at 2/3), icon-192 (at 1/2), icon-512 (at 1/4)", fg))
    ry = y + 24
    out.append(png(os.path.join(bdir, "favicon-16.png"), 40, ry, 16, 16))
    out.append(png(os.path.join(bdir, "favicon-16.png"), 66, ry, 96, 96, pixelated=True))
    out.append(png(os.path.join(bdir, "favicon-32.png"), 190, ry, 32, 32))
    out.append(png(os.path.join(bdir, "favicon-32.png"), 232, ry, 96, 96, pixelated=True))
    out.append(png(fav48, 356, ry, 48, 48))
    out.append(png(fav48, 414, ry, 96, 96, pixelated=True))
    out.append(png(os.path.join(bdir, "apple-touch-icon.png"), 540, ry, 120, 120))
    out.append(png(os.path.join(bdir, "icon-192.png"), 690, ry, 96, 96))
    out.append(png(os.path.join(bdir, "icon-512.png"), 816, ry, 128, 128))
    return out


def main():
    bdir, fav48, out = sys.argv[1], sys.argv[2], sys.argv[3]
    W = 1780
    parts = panel(bdir, fav48, 0, "#ffffff", W) + panel(bdir, fav48, PANEL_H, NIGHT, W)
    H = 2 * PANEL_H
    svg = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">'
           + "".join(parts) + "</svg>")
    open(out, "w", encoding="utf-8").write(svg)
    print(out, W, H)


if __name__ == "__main__":
    main()
