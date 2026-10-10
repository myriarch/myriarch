# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""WCAG 2.x contrast ratios.

Usage: python -I contrast.py FG:BG [FG:BG ...]     (hex colours, e.g. B3338A:FFFFFF)
       python -I contrast.py --json FG:BG ...      (machine-readable)
"""
import json
import sys


def channel(c):
    c /= 255
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def luminance(hexcol):
    h = hexcol.lstrip("#")
    r, g, b = (int(h[i:i + 2], 16) for i in (0, 2, 4))
    return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)


def ratio(fg, bg):
    a, b = luminance(fg), luminance(bg)
    hi, lo = max(a, b), min(a, b)
    return (hi + 0.05) / (lo + 0.05)


def verdict(r):
    text = "AAA" if r >= 7 else "AA" if r >= 4.5 else "AA large" if r >= 3 else "fail"
    graphics = "pass" if r >= 3 else "fail"
    return text, graphics


def main():
    args = sys.argv[1:]
    as_json = "--json" in args
    pairs = [a for a in args if a != "--json"]
    rows = []
    for p in pairs:
        fg, bg = p.split(":")
        r = ratio(fg, bg)
        t, g = verdict(r)
        rows.append({"fg": "#" + fg.lstrip("#").upper(), "bg": "#" + bg.lstrip("#").upper(),
                     "ratio": round(r, 2), "text": t, "graphics_1.4.11": g})
    if as_json:
        print(json.dumps(rows, indent=2))
    else:
        for row in rows:
            print(f'{row["fg"]} on {row["bg"]}: {row["ratio"]:5.2f}:1  text {row["text"]:8}  graphics {row["graphics_1.4.11"]}')


if __name__ == "__main__":
    main()
