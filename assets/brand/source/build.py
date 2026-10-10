# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Generate the myriarch logo SVGs (unoptimized) from the final geometry.

Usage: python -I build.py <fontdir> <outdir>

<fontdir> must contain Inter-SemiBold.ttf and Inter-Regular.ttf (Inter 4.1).
Writes every deliverable SVG plus the internal raster sources
(favicon-32.svg, favicon-48.svg, apple-touch-icon.svg, icon-512.svg) and
geometry.json with the construction numbers.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from brandlib import (BLACK, MIST, ORCHID, PLUM, TYRIAN, WHITE, Mark,  # noqa: E402
                      PixelMark, Typesetter, circle_d, fmt, svg_doc)

# ---------------------------------------------------------------------------
# Construction (design units; the files use SCALE x design units)
# ---------------------------------------------------------------------------
SCALE = 2
MARK = Mark(D=12, P=15, S=13, ro=6.5, ri=2.75, apex_step=13.5)
UNIT = 2 * MARK.ro            # clear-space unit: outer diameter of the open ring
TRACKING = -0.02              # em, wordmark
WORD = "myriarch"
TAGLINE = ("Command ten thousand OpenTelemetry Collectors",
           "from one GitOps-governed control plane.")
SOCIAL_BG = "#FBF6F9"
TAGLINE_INK = "#5C4656"


class Brand:
    def __init__(self, fontdir):
        self.semi = Typesetter(os.path.join(fontdir, "Inter-SemiBold.ttf"))
        self.regular = Typesetter(os.path.join(fontdir, "Inter-Regular.ttf"))
        f = self.semi
        c = f.glyph_bounds(f.shape("c")[0][0])
        self.round_top, self.round_bottom = c[3], c[1]          # 1132, -23
        self.lsb_m = f.glyph_bounds(f.shape("m")[0][0])[0]      # 138

    # -- horizontal lockup -------------------------------------------------
    def horizontal(self):
        """The round letters (c, a, y bowl) span exactly the two lower rows of
        the mark: their tops meet the tops of the 3-dot row, their bottoms the
        bottoms of the 4-dot row, so the dots and the round letters overshoot
        the x-height and the baseline by the same amount."""
        f = self.semi
        span = MARK.D + MARK.S                                    # rows 2 and 3
        size = span * f.upm / (self.round_top - self.round_bottom)
        k = size / f.upm
        baseline = MARK.height + self.round_bottom * k           # bottom - overshoot
        x = MARK.width + UNIT - self.lsb_m * k
        d, bbox, _ = f.set(WORD, size * SCALE, x=x * SCALE, baseline=baseline * SCALE,
                           tracking_em=TRACKING)
        width = bbox[2]
        height = max(MARK.height * SCALE, bbox[3])
        return {"size": size, "baseline": baseline, "x": x, "word": d,
                "mark": MARK.path(SCALE), "width": width, "height": height,
                "x_height": f.x_height * k, "cap_height": f.cap_height * k,
                "word_bbox": [b / SCALE for b in bbox]}

    # -- stacked lockup ----------------------------------------------------
    def stacked(self):
        """Wordmark exactly twice as wide as the mark, one unit below it."""
        f = self.semi
        _, bb, _ = f.set(WORD, 1000, tracking_em=TRACKING)
        size = 2 * MARK.width * 1000 / (bb[2] - bb[0])
        k = size / f.upm
        baseline = MARK.height + UNIT + f.cap_height * k
        x = -self.lsb_m * k
        d, bbox, _ = f.set(WORD, size * SCALE, x=x * SCALE, baseline=baseline * SCALE,
                           tracking_em=TRACKING)
        ink_w = bbox[2] - bbox[0]
        dx = (ink_w - MARK.width * SCALE) / 2 + bbox[0]
        return {"size": size, "baseline": baseline, "word": d,
                "mark": MARK.path(SCALE, dx, 0), "width": bbox[2], "height": bbox[3],
                "word_bbox": [b / SCALE for b in bbox], "mark_dx": dx / SCALE}

    # -- tagline -----------------------------------------------------------
    def tagline(self, size, cx, top_baseline, leading, tracking=-0.01):
        f = self.regular
        paths, boxes = [], []
        for i, line in enumerate(TAGLINE):
            _, bb, _ = f.set(line, size, tracking_em=tracking)
            w = bb[2] - bb[0]
            x = cx - w / 2 - bb[0]
            d, bb2, _ = f.set(line, size, x=x, baseline=top_baseline + i * leading,
                              tracking_em=tracking)
            paths.append(d)
            boxes.append(bb2)
        return "".join(paths), boxes


def lockup_svg(lk, mark_color, word_color, title="myriarch"):
    body = (f'<path fill="{mark_color}" d="{lk["mark"]}"/>'
            f'<path fill="{word_color}" d="{lk["word"]}"/>')
    return svg_doc(lk["width"], lk["height"], (0, 0, lk["width"], lk["height"]), body, title)


def icon_svg(color):
    w, h = MARK.width * SCALE, MARK.height * SCALE
    return svg_doc(w, h, (0, 0, w, h), f'<path fill="{color}" d="{MARK.path(SCALE)}"/>')


def icon_parts_svg():
    """The mark as ten separate shapes, for animation (assets/brand/README.md, "Motion").

    The geometry is the one of icon_svg(): the same circles, only not merged into one path. The ring is
    one path (outer circle clockwise, inner circle counter-clockwise: the same nonzero hole as in the
    static mark), every dot is a path of its own with the classes `dot`, `r<row>` (1 to 3, top to
    bottom) and `c<column>` (0 to row), so a style sheet can address a row or a column. The fill is
    `currentColor`: the page decides the colour. Not for static use: use myriarch-icon.svg there.
    """
    x, y, ro, ri = MARK.ring
    ring = (circle_d(x * SCALE, y * SCALE, ro * SCALE, 2, cw=True)
            + circle_d(x * SCALE, y * SCALE, ri * SCALE, 2, cw=False))
    body = [f'<path class="ring" fill="currentColor" d="{ring}"/>']
    dots = iter(MARK.dots)
    for row in (1, 2, 3):
        for col in range(row + 1):
            cx, cy, r = next(dots)
            body.append(f'<path class="dot r{row} c{col}" fill="currentColor" '
                        f'd="{circle_d(cx * SCALE, cy * SCALE, r * SCALE, 2, cw=True)}"/>')
    w, h = MARK.width * SCALE, MARK.height * SCALE
    return svg_doc(w, h, (0, 0, w, h), "".join(body))


# Pixel-snapped favicon geometries: canvas, dot, gap, row step, ring, offset
FAV16 = PixelMark(16, d=3, g=1, s=4, ro=1.5, ri=0.6, ox=0, oy=0)
FAV32 = PixelMark(32, d=6, g=2, s=7, ro=3.0, ri=1.25, ox=1, oy=2)
FAV48 = PixelMark(48, d=10, g=2, s=11, ro=5.0, ri=2.1, ox=1, oy=3)


def favicon_svg():
    style = f"@media (prefers-color-scheme:dark){{path{{fill:{ORCHID}}}}}"
    return svg_doc(16, 16, (0, 0, 16, 16), f'<path fill="{TYRIAN}" d="{FAV16.path()}"/>',
                   style=style)


def pixel_svg(pm):
    n = pm.N
    return svg_doc(n, n, (0, 0, n, n), f'<path fill="{TYRIAN}" d="{pm.path()}"/>')


def optical_offset():
    """How far to lift the mark above geometric centring (design units).

    The ink centroid of the tetractys sits 7 units below the middle of its
    bounding box, so a geometrically centred mark looks low. Lifting it by a
    sixth of that distance looks centred; a third already floats when the
    margins are tight (compared visually at 512 px)."""
    r = MARK.D / 2
    ring_ink = MARK.ro ** 2 - MARK.ri ** 2
    weights = [ring_ink] + [r * r] * len(MARK.dots)
    ys = [MARK.ring[1]] + [d[1] for d in MARK.dots]
    centroid = sum(w * y for w, y in zip(weights, ys)) / sum(weights)
    return (centroid - MARK.height / 2) / 6


def square_icon_svg(n, mark_width, background=None, color=TYRIAN):
    s = mark_width / MARK.width
    dx = (n - MARK.width * s) / 2
    dy = (n - MARK.height * s) / 2 - optical_offset() * s
    if abs(s - round(s)) < 1e-9:
        # whole-number scale: keep every dot edge on whole pixels
        dx, dy = round(dx), round(dy)
    body = ""
    if background:
        body += f'<rect width="{n}" height="{n}" fill="{background}"/>'
    body += f'<path fill="{color}" d="{MARK.path(s, dx, dy, 3)}"/>'
    return svg_doc(n, n, (0, 0, n, n), body)


def social_svg(brand, W, H, lockup_w, tag_size):
    lk = brand.horizontal()
    s = lockup_w / lk["width"]                     # lockup units (SCALE) -> px
    # optical block: mark top .. baseline of the wordmark, tagline cap line ..
    # last baseline (descenders ignored)
    lock_h = lk["baseline"] * SCALE * s
    leading = tag_size * 1.38
    gap = 2 * UNIT * SCALE * s                    # baseline to tagline cap line
    tag_cap = brand.regular.cap_height * tag_size / brand.regular.upm
    tag_h = tag_cap + leading
    block = lock_h + gap + tag_h
    top = (H - block) / 2 - H * 0.01
    lx = (W - lockup_w) / 2
    tag_baseline = top + lock_h + gap + tag_cap
    tag_d, boxes = brand.tagline(tag_size, W / 2, tag_baseline, leading)
    body = (f'<rect width="{W}" height="{H}" fill="{SOCIAL_BG}"/>'
            f'<g transform="translate({fmt(lx)} {fmt(top)}) scale({fmt(s, 5)})">'
            f'<path fill="{TYRIAN}" d="{lk["mark"]}"/><path fill="{PLUM}" d="{lk["word"]}"/></g>'
            f'<path fill="{TAGLINE_INK}" d="{tag_d}"/>')
    content = {
        "left": min([lx] + [b[0] for b in boxes]),
        "right": max([lx + lockup_w] + [b[2] for b in boxes]),
        "top": top,
        "bottom": max(b[3] for b in boxes),
    }
    title = "myriarch: " + " ".join(TAGLINE)
    return svg_doc(W, H, (0, 0, W, H), body, title=title), content


def main():
    fontdir, out = sys.argv[1], sys.argv[2]
    os.makedirs(out, exist_ok=True)
    brand = Brand(fontdir)
    h = brand.horizontal()
    st = brand.stacked()
    files = {
        "myriarch-icon.svg": icon_svg(TYRIAN),
        "myriarch-icon-dark.svg": icon_svg(ORCHID),
        "myriarch-icon-black.svg": icon_svg(BLACK),
        "myriarch-icon-white.svg": icon_svg(WHITE),
        "myriarch-icon-parts.svg": icon_parts_svg(),
        "myriarch-horizontal.svg": lockup_svg(h, TYRIAN, PLUM),
        "myriarch-horizontal-dark.svg": lockup_svg(h, ORCHID, MIST),
        "myriarch-horizontal-black.svg": lockup_svg(h, BLACK, BLACK),
        "myriarch-horizontal-white.svg": lockup_svg(h, WHITE, WHITE),
        "myriarch-stacked.svg": lockup_svg(st, TYRIAN, PLUM),
        "myriarch-stacked-dark.svg": lockup_svg(st, ORCHID, MIST),
        "favicon.svg": favicon_svg(),
    }
    social, social_box = social_svg(brand, 1280, 640, lockup_w=760, tag_size=42)
    og, og_box = social_svg(brand, 1200, 630, lockup_w=720, tag_size=40)
    files["social-preview.svg"] = social
    files["og-image.svg"] = og
    # raster sources (not shipped as SVG)
    raster = {
        "favicon-32.svg": pixel_svg(FAV32),
        "favicon-48.svg": pixel_svg(FAV48),
        "apple-touch-icon.svg": square_icon_svg(180, MARK.width * 2, background=WHITE),
        "icon-512.svg": square_icon_svg(512, 512 * 0.8),
        "icon-192.svg": square_icon_svg(192, 192 * 0.8),
    }
    os.makedirs(os.path.join(out, "raster-src"), exist_ok=True)
    for name, svg in files.items():
        with open(os.path.join(out, name), "w") as fh:
            fh.write(svg)
    for name, svg in raster.items():
        with open(os.path.join(out, "raster-src", name), "w") as fh:
            fh.write(svg)
    geo = {
        "design_units": {"dot_diameter": MARK.D, "pitch": MARK.P, "row_step": MARK.S,
                         "ring_outer_diameter": 2 * MARK.ro, "ring_inner_diameter": 2 * MARK.ri,
                         "apex_step": MARK.apex_step, "mark_width": MARK.width,
                         "mark_height": MARK.height, "unit": UNIT,
                         "gaps_h_diag_ring": MARK.gaps(), "ring_ink_ratio": MARK.ink_ratio(),
                         "file_scale": SCALE},
        "horizontal": {k: v for k, v in h.items() if k not in ("word", "mark")},
        "stacked": {k: v for k, v in st.items() if k not in ("word", "mark")},
        "tracking_em": TRACKING,
        "social_content_box": social_box,
        "og_content_box": og_box,
        "optical_lift_units": optical_offset(),
    }
    with open(os.path.join(out, "geometry.json"), "w") as fh:
        json.dump(geo, fh, indent=2)
    print(json.dumps(geo, indent=2))


if __name__ == "__main__":
    main()
