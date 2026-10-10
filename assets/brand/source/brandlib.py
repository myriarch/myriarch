# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Geometry and typesetting helpers for the myriarch logo (tetractys mark).

Everything is generated as plain filled paths: no <text>, no strokes, no
external references, so every file renders identically everywhere and can be
recoloured by changing a single fill.
"""
import math

import uharfbuzz as hb
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

# ---------------------------------------------------------------------------
# Palette
# ---------------------------------------------------------------------------
TYRIAN = "#B3338A"   # main colour of the mark
ORCHID = "#F28CCB"   # mark on dark backgrounds
PLUM = "#2B0A1F"     # wordmark ink
MIST = "#F1F5F9"     # wordmark on dark backgrounds
NIGHT = "#0f172a"    # reference dark background
BLACK = "#000000"
WHITE = "#FFFFFF"


def fmt(v, nd=2):
    """Format a number compactly (no trailing zeros, no '-0')."""
    s = f"{v:.{nd}f}".rstrip("0").rstrip(".")
    if s in ("-0", ""):
        s = "0"
    return s


# ---------------------------------------------------------------------------
# Mark geometry
# ---------------------------------------------------------------------------
class Mark:
    """Tetractys: rows of 1, 2, 3, 4 on an equilateral lattice.

    Units: dot diameter D, pitch P (centre to centre within a row), row step S.
    The apex (row 0) is an open ring with outer radius ro and inner radius ri;
    it is lifted by `apex_step` above row 1 instead of S so that the gap
    between the (slightly larger) ring and the two dots of row 1 stays equal
    to the dot gap.
    """

    def __init__(self, D=12.0, P=15.0, S=13.0, ro=6.0, ri=3.0, apex_step=None):
        self.D, self.P, self.S, self.ro, self.ri = D, P, S, ro, ri
        r = D / 2
        if apex_step is None:
            gap = math.hypot(P / 2, S) - D          # dot-to-dot diagonal gap
            apex_step = math.sqrt((ro + r + gap) ** 2 - (P / 2) ** 2)
        self.apex_step = apex_step
        self.width = 3 * P + D
        cx = self.width / 2
        top = max(ro, r)
        y_apex = top
        y1 = y_apex + apex_step
        self.rows_y = [y_apex, y1, y1 + S, y1 + 2 * S]
        self.height = self.rows_y[3] + r
        self.dots = []
        for k in (1, 2, 3):
            for j in range(k + 1):
                self.dots.append((cx + (j - k / 2) * P, self.rows_y[k], r))
        self.ring = (cx, y_apex, ro, ri)

    # metrics used for alignment
    @property
    def bottom_row_top(self):
        return self.rows_y[3] - self.D / 2

    def row_top(self, k):
        return self.rows_y[k] - (self.ro if k == 0 else self.D / 2)

    def gaps(self):
        r = self.D / 2
        horiz = self.P - self.D
        diag = math.hypot(self.P / 2, self.S) - self.D
        ring = math.hypot(self.P / 2, self.apex_step) - self.ro - r
        return horiz, diag, ring

    def ink_ratio(self):
        r = self.D / 2
        return (self.ro ** 2 - self.ri ** 2) / r ** 2

    def path(self, scale=1.0, dx=0.0, dy=0.0, nd=2):
        parts = []
        for (x, y, r) in self.dots:
            parts.append(circle_d(x * scale + dx, y * scale + dy, r * scale, nd, cw=True))
        x, y, ro, ri = self.ring
        parts.append(circle_d(x * scale + dx, y * scale + dy, ro * scale, nd, cw=True))
        parts.append(circle_d(x * scale + dx, y * scale + dy, ri * scale, nd, cw=False))
        return "".join(parts)


def circle_d(cx, cy, r, nd=2, cw=True):
    """A full circle as two arcs. cw=False draws it counter-clockwise, which
    cuts a hole under the nonzero fill rule (used for the open apex)."""
    sweep = 1 if cw else 0
    return (f"M{fmt(cx - r, nd)} {fmt(cy, nd)}"
            f"a{fmt(r, nd)} {fmt(r, nd)} 0 1 {sweep} {fmt(2 * r, nd)} 0"
            f"a{fmt(r, nd)} {fmt(r, nd)} 0 1 {sweep} {fmt(-2 * r, nd)} 0z")


class PixelMark:
    """Pixel-snapped tetractys for tiny sizes (favicons).

    All dot bounding boxes sit on whole pixels of an N x N canvas.
    d: dot diameter (px), g: gap (px), s: row step (px), ro/ri: ring radii,
    ox/oy: canvas offset of the mark's bounding box (px).
    """

    def __init__(self, N, d, g, s, ro, ri, ox, oy, apex_lift=0.0):
        self.N = N
        p = d + g
        self.width = 3 * p + d
        cx = ox + self.width / 2
        r = d / 2
        ys = [oy + r + k * s for k in range(4)]
        ys[0] -= apex_lift
        self.dots = []
        for k in (1, 2, 3):
            for j in range(k + 1):
                self.dots.append((cx + (j - k / 2) * p, ys[k], r))
        self.ring = (cx, ys[0], ro, ri)
        self.height = ys[3] + r - (ys[0] - ro)

    def path(self, nd=3):
        parts = [circle_d(x, y, r, nd) for (x, y, r) in self.dots]
        x, y, ro, ri = self.ring
        parts.append(circle_d(x, y, ro, nd, cw=True))
        parts.append(circle_d(x, y, ri, nd, cw=False))
        return "".join(parts)


# ---------------------------------------------------------------------------
# Text to outlines
# ---------------------------------------------------------------------------
class Typesetter:
    def __init__(self, font_path):
        self.path = font_path
        self.font = TTFont(font_path)
        self.upm = self.font["head"].unitsPerEm
        self.glyphset = self.font.getGlyphSet()
        os2 = self.font["OS/2"]
        self.x_height = os2.sxHeight
        self.cap_height = os2.sCapHeight
        blob = hb.Blob.from_file_path(font_path)
        self.hb_font = hb.Font(hb.Face(blob))

    def shape(self, text, features=None):
        buf = hb.Buffer()
        buf.add_str(text)
        buf.guess_segment_properties()
        feats = {"kern": True, "liga": True, "calt": True}
        if features:
            feats.update(features)
        hb.shape(self.hb_font, buf, feats)
        return [(self.font.getGlyphName(i.codepoint), p.x_advance, p.x_offset, p.y_offset)
                for i, p in zip(buf.glyph_infos, buf.glyph_positions)]

    def set(self, text, size, x=0.0, baseline=0.0, tracking_em=0.0, nd=2, features=None):
        """Return (path_d, ink_bbox, advance) for `text` set at `size` units
        with its origin at (x, baseline). Tracking is added between glyphs
        only (not after the last one)."""
        k = size / self.upm
        track = tracking_em * self.upm
        pen_x = 0.0
        parts = []
        xs, ys = [], []
        glyphs = self.shape(text, features)
        for idx, (gname, adv, xoff, yoff) in enumerate(glyphs):
            ntos = lambda v: fmt(v, nd)  # noqa: E731
            spen = SVGPathPen(self.glyphset, ntos=ntos)
            ox = x + (pen_x + xoff) * k
            oy = baseline - yoff * k
            tpen = TransformPen(spen, (k, 0, 0, -k, ox, oy))
            self.glyphset[gname].draw(tpen)
            d = spen.getCommands()
            if d:
                parts.append(d)
                b = self.glyph_bounds(gname)
                if b:
                    xs += [ox + b[0] * k, ox + b[2] * k]
                    ys += [oy - b[3] * k, oy - b[1] * k]
            pen_x += adv
            if idx < len(glyphs) - 1:
                pen_x += track
        bbox = (min(xs), min(ys), max(xs), max(ys)) if xs else None
        return "".join(parts), bbox, pen_x * k

    def glyph_bounds(self, gname):
        from fontTools.pens.boundsPen import BoundsPen
        bp = BoundsPen(self.glyphset)
        self.glyphset[gname].draw(bp)
        return bp.bounds

    def lsb(self, text):
        gname = self.shape(text)[0][0]
        return self.glyph_bounds(gname)[0]

    def metric(self, name, size):
        return getattr(self, name) * size / self.upm


# ---------------------------------------------------------------------------
# SVG document helper
# ---------------------------------------------------------------------------
def ceil2(v):
    """Round up to 2 decimals (after removing float noise), so a tight
    viewBox never clips ink when optimizers shorten the numbers."""
    v = round(v, 6)
    return math.ceil(v * 100 - 1e-6) / 100


def svg_doc(width, height, view_box, body, title="myriarch", style=None, extra_attrs=""):
    width, height = ceil2(width), ceil2(height)
    x, y, w, h = view_box
    vb = " ".join(fmt(v, 2) for v in (x, y, ceil2(w), ceil2(h)))
    head = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{fmt(width, 2)}" '
            f'height="{fmt(height, 2)}" viewBox="{vb}" role="img" aria-label="{title}"{extra_attrs}>')
    out = [head, f"<title>{title}</title>"]
    if style:
        out.append(f"<style>{style}</style>")
    out.append(body)
    out.append("</svg>")
    return "".join(out) + "\n"
