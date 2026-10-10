# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Validate the logo SVGs: well-formed XML, explicit size, viewBox, title,
no text, raster, scripts or external references. Prints byte sizes.

A file named *-parts.svg (the mark as separate shapes for animation) must also be the mark: the union
of its circles equals the circles of the static mark (build.MARK at build.SCALE, same centres and radii
within 0.01), the ring is one path with a counter-clockwise hole, and there are exactly one ring and nine
dots with the classes r1 to r3 and c0 to c3 in their rows.

Usage: python -I validate.py <file.svg>...
Exit status 1 if any check fails.
"""
import os
import re
import sys
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(__file__))

SVG_NS = "{http://www.w3.org/2000/svg}"
FORBIDDEN = {"text", "tspan", "textPath", "image", "script", "foreignObject", "use",
             "a", "iframe", "feImage", "audio", "video", "font", "font-face"}


NUMBER = re.compile(r"[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?")


def path_circles(d):
    """The circles of a path made of subpaths that are full circles drawn as two arcs (M/m, a/A, optional z).

    Returns a list of (cx, cy, r, sweep) with sweep 1 for clockwise. Raises ValueError for anything else. Accepts the forms
    that svgo writes: relative commands, the arc command repeated implicitly, flags glued to the next number, no closing z."""
    circles = []
    x = y = 0.0
    start = None
    arcs = []
    pos = 0
    cmd = None

    def skip(p):
        while p < len(d) and d[p] in " \t\r\n,":
            p += 1
        return p

    def number(p):
        m = NUMBER.match(d, skip(p))
        if not m:
            raise ValueError("a number is expected at offset %d" % p)
        return float(m.group()), m.end()

    def close():
        nonlocal start, arcs
        if start is None:
            return
        if len(arcs) != 2:
            raise ValueError("a subpath is not a circle of two arcs")
        (x1, y1), r, sweep = arcs[0]
        (x2, y2), r2, sweep2 = arcs[1]
        if abs(x2 - start[0]) > 0.01 or abs(y2 - start[1]) > 0.01 or sweep != sweep2 or abs(r - r2) > 0.01:
            raise ValueError("the two arcs do not make a circle")
        circles.append(((start[0] + x1) / 2, (start[1] + y1) / 2, r, sweep))
        start, arcs = None, []

    while True:
        pos = skip(pos)
        if pos >= len(d):
            break
        if d[pos] in "MmAaZz":
            cmd, pos = d[pos], pos + 1
            if cmd in "Zz":
                close()
                continue
        elif cmd is None or cmd in "Mm":
            raise ValueError("unexpected data at offset %d" % pos)
        if cmd in "Mm":
            close()
            nx, pos = number(pos)
            ny, pos = number(pos)
            x, y = (x + nx, y + ny) if cmd == "m" else (nx, ny)
            start, arcs = (x, y), []
            cmd = "L"  # a further pair would be a lineto: refused by the check above
        elif cmd in "Aa":
            if start is None:
                raise ValueError("an arc without a moveto")
            rx, pos = number(pos)
            ry, pos = number(pos)
            _, pos = number(pos)
            flags = []
            for _ in range(2):  # one character each, possibly glued to the next number
                pos = skip(pos)
                if d[pos:pos + 1] not in ("0", "1"):
                    raise ValueError("bad arc flag at offset %d" % pos)
                flags.append(int(d[pos]))
                pos += 1
            dx, pos = number(pos)
            dy, pos = number(pos)
            if abs(rx - ry) > 1e-6:
                raise ValueError("an arc is not circular")
            x, y = (x + dx, y + dy) if cmd == "a" else (dx, dy)
            arcs.append(((x, y), rx, flags[1]))
        else:
            raise ValueError("unsupported command %s" % cmd)
    close()
    return circles


def check_parts(root):
    """Checks of *-parts.svg: the parts are the mark."""
    import build  # the construction (needs fontTools through brandlib, like the rest of the build)

    problems = []
    paths = [el for el in root.iter(SVG_NS + "path")]
    if len(paths) != 10:
        return [f"expected 10 paths (ring and nine dots), found {len(paths)}"]
    ring = [p for p in paths if p.get("class") == "ring"]
    dots = [p for p in paths if (p.get("class") or "").split()[:1] == ["dot"]]
    if len(ring) != 1 or len(dots) != 9:
        problems.append(f"expected 1 ring and 9 dots, found {len(ring)} and {len(dots)}")
        return problems
    rows = {}
    for p in dots:
        classes = p.get("class").split()
        if len(classes) != 3 or not re.fullmatch(r"r[123]", classes[1]) or not re.fullmatch(r"c[0-3]", classes[2]):
            problems.append(f"bad dot classes {p.get('class')!r}")
            continue
        rows.setdefault(classes[1], []).append(classes[2])
    for row, want in (("r1", 2), ("r2", 3), ("r3", 4)):
        if sorted(rows.get(row, [])) != [f"c{i}" for i in range(want)]:
            problems.append(f"row {row} has the columns {sorted(rows.get(row, []))}, expected {want} dots c0..c{want - 1}")
    for p in paths:
        if p.get("fill") != "currentColor":
            problems.append(f"{p.get('class')}: fill must be currentColor")
    try:
        got = []
        for p in paths:
            got += path_circles(p.get("d", ""))
        ring_sweeps = sorted(c[3] for c in path_circles(ring[0].get("d", "")))
        if ring_sweeps != [0, 1]:
            problems.append("the ring must be one clockwise circle with one counter-clockwise hole")
        want = path_circles(build.MARK.path(build.SCALE))
    except ValueError as e:
        return problems + [f"path data: {e}"]
    key = lambda c: (round(c[1], 1), round(c[0], 1), round(c[2], 1), c[3])  # noqa: E731
    got, want = sorted(got, key=key), sorted(want, key=key)
    if len(got) != len(want):
        problems.append(f"{len(got)} circles, the mark has {len(want)}")
    else:
        for g, w in zip(got, want):
            if any(abs(a - b) > 0.01 for a, b in zip(g[:3], w[:3])) or g[3] != w[3]:
                problems.append(f"circle {tuple(round(v, 2) for v in g[:3])} does not match the mark circle {tuple(round(v, 2) for v in w[:3])}")
    return problems


def check(path):
    problems = []
    raw = open(path, "rb").read()
    try:
        root = ET.fromstring(raw)
    except ET.ParseError as e:
        return [f"XML parse error: {e}"], {}
    if root.tag != SVG_NS + "svg":
        problems.append(f"root is {root.tag}")
    w, h, vb = root.get("width"), root.get("height"), root.get("viewBox")
    if not (w and h and vb):
        problems.append("missing width/height/viewBox")
    else:
        vx, vy, vw, vh = (float(v) for v in vb.split())
        if abs(float(w) - vw) > 1e-6 or abs(float(h) - vh) > 1e-6:
            problems.append(f"width/height {w}x{h} differ from viewBox {vb}")
    if root.find(SVG_NS + "title") is None:
        problems.append("no <title>")
    fills = set()
    for el in root.iter():
        tag = el.tag.replace(SVG_NS, "")
        if tag in FORBIDDEN:
            problems.append(f"forbidden element <{tag}>")
        for k, v in el.attrib.items():
            if k.endswith("href"):
                problems.append(f"href attribute on <{tag}>")
            if "url(" in v:
                problems.append(f"url() in {k}")
            if k.startswith("on"):
                problems.append(f"event handler {k}")
        if el.get("fill"):
            fills.add(el.get("fill").lower())
        if tag == "style" and el.text and re.search(r"@import|url\(", el.text):
            problems.append("external reference in <style>")
    if b"<!DOCTYPE" in raw or b"<!ENTITY" in raw:
        problems.append("DOCTYPE/ENTITY present")
    if os.path.basename(path).endswith("-parts.svg"):
        problems += check_parts(root)
    return problems, {"bytes": len(raw), "fills": sorted(fills), "size": f"{w}x{h}"}


def main():
    bad = 0
    for p in sys.argv[1:]:
        problems, info = check(p)
        status = "ok" if not problems else "FAIL: " + "; ".join(problems)
        bad += bool(problems)
        print(f"{os.path.basename(p):34} {info.get('bytes', 0):6} B  {info.get('size', ''):>16}  "
              f"fills={','.join(info.get('fills', []))}  {status}")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
