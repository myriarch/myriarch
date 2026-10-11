#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Check of the landing page of the built website (ADR-0031).

    python3 -I website/scripts/check-landing.py [website/public]

Fails when

  - a screenshot that website/data/landing.yaml lists is missing in the light or in the dark appearance in
    website/assets/images/screenshots/ (<name>-light.png, <name>-dark.png), or is not a PNG of 1440x900 pixels,
  - an image of index.html has no numeric width and height, no alt text, or a source that is not a file of the site,
  - an image has fetchpriority="high" and loading="lazy" at the same time (the image above the fold must not be lazy),
  - index.html loads a resource from another host (the same rules as check-third-party.py),
  - a section of the landing page is missing (hero heading, features, architecture, screenshots, quickstart, community), the page
    does not have exactly one h1, the architecture diagram has no role="img" and title, or the interim home page class is left over,
  - the mark in the hero is not exactly one inline svg (class landing-hero__mark) that is hidden from assistive technology (aria-hidden="true",
    focusable="false", no title) and consists of one path.ring and nine path.dot with the classes r1 to r3 and c0 to c3 in their rows, or the
    circles of these paths are not the circles of assets/brand/myriarch-icon.svg (same centres and radii within 0.01),
  - the mark is not a plain image in the markup (it must have no role, no tabindex and no aria-pressed: the script makes it the pause control
    only when it can animate), the page still has a visible pause button (any element with the class landing-hero__motion), or the page
    does not load the script landing-mark (the loop of the mark and its role as the pause control need it) from the site,
  - index.html or a stylesheet that it links requests one of the files /brand/myriarch-icon*.svg (the mark is inline, so that it takes the
    colour of the page and no file of the other appearance is fetched),
  - website/data/release.yaml (the single source of the release status) does not have exactly the keys final and candidate, sets both,
    or holds a tag in another form than vX.Y.Z (final) or vX.Y.Z-rc.N (candidate), or the status chip of the hero does not say what the
    file says ("Latest release <tag>", "Release candidate <tag> (pre-release)" or "No release has been published yet").

The output names the file and the problem. Exit status 0 = clean, 1 = findings, 2 = usage error.
Python 3.9 compatible, standard library only.
"""

import importlib.util
import os
import re
import struct
import sys
from html.parser import HTMLParser

HERE = os.path.dirname(os.path.abspath(__file__))
WEBSITE = os.path.dirname(HERE)
LANDING_DATA = os.path.join(WEBSITE, "data", "landing.yaml")
RELEASE_DATA = os.path.join(WEBSITE, "data", "release.yaml")
SCREENSHOT_DIR = os.path.join(WEBSITE, "assets", "images", "screenshots")
ICON = os.path.join(os.path.dirname(WEBSITE), "assets", "brand", "myriarch-icon.svg")
EXPECTED_SIZE = (1440, 900)
THEMES = ("light", "dark")
SECTIONS = ("features", "architecture", "screenshots", "quickstart", "community")
PNG_MAGIC = b"\x89PNG\r\n\x1a\n"


def load_sibling(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def screenshot_names(data_path):
    """The names under `screenshots: items:` in landing.yaml (a plain text scan, so that no YAML library is needed)."""
    names = []
    in_block = False
    with open(data_path, encoding="utf-8") as fh:
        for line in fh:
            if re.match(r"^screenshots:\s*$", line):
                in_block = True
                continue
            if in_block and re.match(r"^\S", line):
                break
            m = re.match(r"^\s+-\s+name:\s*(\S+)\s*$", line) if in_block else None
            if m:
                names.append(m.group(1).strip("\"'"))
    return names


FINAL_TAG = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+$")
CANDIDATE_TAG = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$")
NO_RELEASE_NOTE = "No release has been published yet"


def read_release(path):
    """(final, candidate, problems) of release.yaml, read as plain text: two top-level keys with a quoted or bare scalar."""
    name = "website/data/" + os.path.basename(path)
    values = {}
    problems = []
    with open(path, encoding="utf-8") as fh:
        for number, line in enumerate(fh, 1):
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            m = re.match(r"^(\w+):[ \t]*(?:\"([^\"]*)\"|'([^']*)'|([^\s#\"']*))[ \t]*(?:#.*)?$", line.rstrip("\n"))
            if not m:
                problems.append("%s:%d: expected 'final: \"\"' or 'candidate: \"\"'" % (name, number))
                continue
            key = m.group(1)
            if key in values:
                problems.append("%s:%d: the key %s appears twice" % (name, number, key))
            values[key] = next((g for g in m.groups()[1:] if g is not None), "")
    for key in ("final", "candidate"):
        if key not in values:
            problems.append("%s: the key %s is missing" % (name, key))
    for key in sorted(set(values) - {"final", "candidate"}):
        problems.append("%s: unknown key %s (only final and candidate)" % (name, key))
    final, candidate = values.get("final", ""), values.get("candidate", "")
    if final and not FINAL_TAG.match(final):
        problems.append("%s: final %r is not a tag of the form vX.Y.Z" % (name, final))
    if candidate and not CANDIDATE_TAG.match(candidate):
        problems.append("%s: candidate %r is not a tag of the form vX.Y.Z-rc.N" % (name, candidate))
    if final and candidate:
        problems.append("%s: final and candidate are both set; set only one (the latest final release wins, docs/releasing.md)" % name)
    return final, candidate, problems


def expected_note(final, candidate):
    if final:
        return "Latest release %s" % final
    if candidate:
        return "Release candidate %s (pre-release)" % candidate
    return NO_RELEASE_NOTE


def png_size(path):
    """(width, height) of a PNG file, or None when the file is not a PNG."""
    with open(path, "rb") as fh:
        head = fh.read(24)
    if len(head) < 24 or head[:8] != PNG_MAGIC or head[12:16] != b"IHDR":
        return None
    return struct.unpack(">II", head[16:24])


class LandingParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.images = []  # attribute dicts of every <img>
        self.ids = set()
        self.h1 = 0
        self.marks = []  # attribute dicts of every <svg> with the class landing-hero__mark
        self.mark_paths = []  # (class, d) of the paths inside the first mark
        self.mark_has_title = False
        self._mark_depth = 0
        self.stylesheets = []  # href of every stylesheet link
        self.motion_controls = []  # tags of the elements with the class landing-hero__motion (the removed pause button)
        self.scripts = []  # src of every <script src>
        self.body_class = ""
        self.svg_roles = []  # (role, has_title) per inline <svg> with the diagram id
        self._svg_depth = 0
        self._svg_has_title = False
        self._svg_role = None
        self._in_diagram_svg = False
        self.chip_notes = []  # text of every element with the class landing-chip__note
        self._chip_depth = 0
        self._chip_text = []

    def handle_starttag(self, tag, attrs):
        a = {k: (v if v is not None else "") for k, v in attrs}
        if self._chip_depth > 0:
            self._chip_depth += 1
        elif "landing-chip__note" in a.get("class", "").split():
            self._chip_depth = 1
            self._chip_text = []
        if a.get("id"):
            self.ids.add(a["id"])
        if "landing-hero__motion" in a.get("class", "").split():
            self.motion_controls.append(tag)
        if tag == "h1":
            self.h1 += 1
        elif tag == "body":
            self.body_class = a.get("class", "")
        elif tag == "img":
            self.images.append(a)
        elif tag == "script" and a.get("src"):
            self.scripts.append(a["src"])
        elif tag == "link" and "stylesheet" in a.get("rel", "").lower().split():
            self.stylesheets.append(a.get("href", ""))
        elif tag == "svg" and "landing-hero__mark" in a.get("class", "").split():
            self.marks.append(a)
            self._mark_depth += 1
        elif self._mark_depth > 0 and tag == "path":
            self.mark_paths.append((a.get("class", ""), a.get("d", "")))
        elif self._mark_depth > 0 and tag == "title":
            self.mark_has_title = True
        elif tag == "svg":
            if self._svg_depth == 0 and a.get("aria-labelledby") == "arch-title arch-desc":
                self._in_diagram_svg = True
                self._svg_role = a.get("role")
                self._svg_has_title = False
            if self._in_diagram_svg:
                self._svg_depth += 1
        elif tag == "title" and self._in_diagram_svg:
            self._svg_has_title = True


    def handle_data(self, data):
        if self._chip_depth > 0:
            self._chip_text.append(data)

    def handle_endtag(self, tag):
        if self._chip_depth > 0:
            self._chip_depth -= 1
            if self._chip_depth == 0:
                self.chip_notes.append(" ".join("".join(self._chip_text).split()))
        if tag == "svg" and self._mark_depth > 0:
            self._mark_depth -= 1
        if tag == "svg" and self._in_diagram_svg:
            self._svg_depth -= 1
            if self._svg_depth == 0:
                self.svg_roles.append((self._svg_role, self._svg_has_title))
                self._in_diagram_svg = False


def image_sources(img):
    out = []
    if img.get("src"):
        out.append(img["src"])
    for part in img.get("srcset", "").split(","):
        part = part.strip()
        if part:
            out.append(part.split()[0])
    return out


def check_screenshots(data_path, shot_dir):
    problems = []
    names = screenshot_names(data_path)
    if not names:
        problems.append("%s: no screenshot is listed under screenshots.items" % os.path.relpath(data_path, WEBSITE))
    for name in names:
        for theme in THEMES:
            rel = "assets/images/screenshots/%s-%s.png" % (name, theme)
            path = os.path.join(shot_dir, "%s-%s.png" % (name, theme))
            if not os.path.isfile(path):
                problems.append("%s: the screenshot is missing (node website/scripts/screenshots.mjs)" % rel)
                continue
            size = png_size(path)
            if size is None:
                problems.append("%s: not a PNG file" % rel)
            elif size != EXPECTED_SIZE:
                problems.append("%s: size is %dx%d, expected %dx%d" % ((rel,) + size + EXPECTED_SIZE))
    return problems


NUMBER = re.compile(r"[-+]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][-+]?[0-9]+)?")


def path_circles(d):
    """The circles (cx, cy, r) of a path whose subpaths are full circles drawn as two arcs (M/m, a/A, optional z).

    Understands the forms that svgo and the HTML minifier write: relative commands, the arc command repeated implicitly, arc flags glued
    to the next number, numbers such as 5.5.0 and no closing z. Raises ValueError for anything else."""
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
        (x1, y1), r = arcs[0]
        (x2, y2), r2 = arcs[1]
        if abs(x2 - start[0]) > 0.01 or abs(y2 - start[1]) > 0.01 or abs(r - r2) > 0.01:
            raise ValueError("the two arcs do not make a circle")
        circles.append(((start[0] + x1) / 2, (start[1] + y1) / 2, r))
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
            cmd = "L"
        elif cmd in "Aa":
            if start is None:
                raise ValueError("an arc without a moveto")
            rx, pos = number(pos)
            ry, pos = number(pos)
            _, pos = number(pos)
            for _flag in range(2):
                pos = skip(pos)
                if d[pos:pos + 1] not in ("0", "1"):
                    raise ValueError("bad arc flag at offset %d" % pos)
                pos += 1
            dx, pos = number(pos)
            dy, pos = number(pos)
            if abs(rx - ry) > 1e-6:
                raise ValueError("an arc is not circular")
            x, y = (x + dx, y + dy) if cmd == "a" else (dx, dy)
            arcs.append(((x, y), rx))
        else:
            raise ValueError("unsupported command %s" % cmd)
    close()
    return circles


def icon_circles(icon_path):
    """The circles of the static mark (every path of the file)."""
    with open(icon_path, encoding="utf-8") as fh:
        text = fh.read()
    out = []
    for d in re.findall(r"<path[^>]* d=\"([^\"]+)\"", text):
        out += path_circles(d)
    return out


def check_mark(parser, icon_path):
    problems = []
    if len(parser.marks) != 1:
        return ["index.html: the hero must contain exactly one inline svg with the class landing-hero__mark, found %d" % len(parser.marks)]
    svg = parser.marks[0]
    if svg.get("aria-hidden") != "true" or svg.get("focusable") != "false":
        problems.append('index.html: the mark must have aria-hidden="true" and focusable="false" (it is decoration)')
    if parser.mark_has_title or svg.get("aria-label") or svg.get("role"):
        problems.append("index.html: the mark must have no title, aria-label or role (the name is carried by the heading and the logo)")
    if "tabindex" in svg or "aria-pressed" in svg:
        problems.append("index.html: the mark must have no tabindex or aria-pressed in the markup (without JavaScript and with reduced motion it is not a control; the script adds them)")
    rings = [p for p in parser.mark_paths if p[0].split() == ["ring"]]
    dots = [p for p in parser.mark_paths if p[0].split()[:1] == ["dot"]]
    if len(rings) != 1 or len(dots) != 9 or len(parser.mark_paths) != 10:
        problems.append("index.html: the mark needs 1 path.ring and 9 path.dot, found %d and %d (%d paths)" % (len(rings), len(dots), len(parser.mark_paths)))
        return problems
    rows = {}
    for cls, _ in dots:
        parts = cls.split()
        if len(parts) != 3 or not re.fullmatch(r"r[123]", parts[1]) or not re.fullmatch(r"c[0-3]", parts[2]):
            problems.append("index.html: a dot of the mark has the classes %r, expected dot r<1-3> c<0-3>" % cls)
            continue
        rows.setdefault(parts[1], []).append(parts[2])
    for row, count in (("r1", 2), ("r2", 3), ("r3", 4)):
        if sorted(rows.get(row, [])) != ["c%d" % i for i in range(count)]:
            problems.append("index.html: the dots of row %s are %s, expected %d dots c0..c%d" % (row, sorted(rows.get(row, [])), count, count - 1))
    try:
        got = []
        for _, d in parser.mark_paths:
            got += path_circles(d)
        want = icon_circles(icon_path)
    except (ValueError, OSError) as exc:
        problems.append("index.html: cannot compare the mark with %s: %s" % (os.path.basename(icon_path), exc))
        return problems
    key = lambda c: (round(c[1], 1), round(c[0], 1), round(c[2], 1))  # noqa: E731
    got, want = sorted(got, key=key), sorted(want, key=key)
    if len(got) != len(want) or any(abs(a - b) > 0.01 for g, w in zip(got, want) for a, b in zip(g, w)):
        problems.append("index.html: the circles of the mark are not those of %s" % os.path.basename(icon_path))
    return problems


def check_motion_control(parser):
    """The mark is its own pause control (WCAG 2.2.2), made by the script: no visible pause button, and the script is loaded."""
    problems = []
    if parser.motion_controls:
        problems.append(
            "index.html: the page still has a visible pause control (%s with the class landing-hero__motion); the mark itself is the control"
            % ", ".join("<%s>" % t for t in parser.motion_controls)
        )
    scripts = [src for src in parser.scripts if "landing-mark" in src]
    if len(scripts) != 1 or re.match(r"^(https?:)?//", scripts[0]):
        problems.append("index.html: the page must load exactly one script landing-mark from the site, found %s" % (scripts or "none"))
    return problems


def check_icon_requests(public, text, stylesheets):
    """The mark is inline: neither index.html nor a stylesheet that it links names /brand/myriarch-icon*.svg."""
    problems = []
    pattern = re.compile(r"/brand/myriarch-icon[^\"')\s]*\.svg")
    if pattern.search(text):
        problems.append("index.html: requests %s; the mark in the hero is inline" % pattern.search(text).group(0))
    for href in stylesheets:
        if re.match(r"^(https?:)?//", href):
            continue
        path = os.path.join(public, href.split("?")[0].split("#")[0].lstrip("/"))
        if os.path.isfile(path):
            with open(path, "rb") as fh:
                css = fh.read().decode("utf-8", errors="replace")
            m = pattern.search(css)
            if m:
                problems.append("%s: requests %s; the mark in the hero is inline" % (href, m.group(0)))
    return problems


def check_status_chip(parser, release_path):
    final, candidate, problems = read_release(release_path)
    problems = list(problems)
    if problems:
        return problems
    want = expected_note(final, candidate)
    if len(parser.chip_notes) != 1:
        problems.append("index.html: the hero must contain exactly one status chip note (landing-chip__note), found %d" % len(parser.chip_notes))
    elif parser.chip_notes[0] != want:
        problems.append("index.html: the status chip says %r, website/data/release.yaml says %r" % (parser.chip_notes[0], want))
    return problems


def check_page(public, icon_path=ICON, release_path=RELEASE_DATA):
    problems = []
    index = os.path.join(public, "index.html")
    if not os.path.isfile(index):
        return ["index.html: the page does not exist in %s" % public]
    with open(index, "rb") as fh:
        text = fh.read().decode("utf-8", errors="replace")
    parser = LandingParser()
    parser.feed(text)
    parser.close()

    if parser.h1 != 1:
        problems.append("index.html: %d h1 elements, expected exactly 1" % parser.h1)
    for section in SECTIONS:
        if section not in parser.ids:
            problems.append("index.html: the section #%s is missing" % section)
    if "interim-home" in parser.body_class.split():
        problems.append("index.html: the body still has the class interim-home (the landing page replaces the interim page)")
    if not parser.svg_roles:
        problems.append('index.html: the architecture diagram (svg with aria-labelledby="arch-title arch-desc") is missing')
    for role, has_title in parser.svg_roles:
        if role != "img" or not has_title:
            problems.append("index.html: the architecture diagram needs role=img and a <title>")

    problems += check_mark(parser, icon_path)
    problems += check_motion_control(parser)
    problems += check_icon_requests(public, text, parser.stylesheets)
    problems += check_status_chip(parser, release_path)

    if not parser.images:
        problems.append("index.html: the page has no image")
    for img in parser.images:
        label = img.get("src", "") or "(no src)"
        for attr in ("width", "height"):
            if not re.fullmatch(r"[1-9][0-9]*", img.get(attr, "").strip()):
                problems.append("index.html: image %s has no numeric %s" % (label, attr))
        if not img.get("alt", "").strip():
            problems.append("index.html: image %s has no alt text" % label)
        if img.get("fetchpriority", "").lower() == "high" and img.get("loading", "").lower() == "lazy":
            problems.append("index.html: image %s is lazy and has fetchpriority=high" % label)
        for src in image_sources(img):
            if src.startswith("data:"):
                continue
            if re.match(r"^(https?:)?//", src):
                continue  # reported by the third-party scan below
            rel = src.split("?")[0].split("#")[0].lstrip("/")
            if not os.path.isfile(os.path.join(public, rel)):
                problems.append("index.html: image source %s is not a file of the site" % src)

    third = load_sibling("check-third-party")
    scanner = third.PageScanner()
    scanner.feed(text)
    scanner.close()
    for what, url in sorted(set(scanner.findings)):
        problems.append("index.html: %s loads %s from another host" % (what, url))
    return problems


def main(argv):
    args = list(argv)
    shot_dir = SCREENSHOT_DIR
    data_path = LANDING_DATA
    if "--screenshots" in args:
        i = args.index("--screenshots")
        if i + 1 >= len(args):
            print("usage: python3 -I website/scripts/check-landing.py [website/public]", file=sys.stderr)
            return 2
        shot_dir = args[i + 1]
        del args[i:i + 2]
    icon_path = ICON
    if "--icon" in args:
        i = args.index("--icon")
        if i + 1 >= len(args):
            print("usage: python3 -I website/scripts/check-landing.py [website/public]", file=sys.stderr)
            return 2
        icon_path = args[i + 1]
        del args[i:i + 2]
    release_path = RELEASE_DATA
    if "--release" in args:
        i = args.index("--release")
        if i + 1 >= len(args):
            print("usage: python3 -I website/scripts/check-landing.py [website/public]", file=sys.stderr)
            return 2
        release_path = args[i + 1]
        del args[i:i + 2]
    if "--data" in args:
        i = args.index("--data")
        if i + 1 >= len(args):
            print("usage: python3 -I website/scripts/check-landing.py [website/public]", file=sys.stderr)
            return 2
        data_path = args[i + 1]
        del args[i:i + 2]
    if len(args) > 1 or (args and args[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-landing.py [website/public]", file=sys.stderr)
        return 2
    public = args[0] if args else "website/public"
    if not os.path.isdir(public):
        print("check-landing: %s is not a directory (build the site first: make website)" % public, file=sys.stderr)
        return 2
    for path in (data_path, release_path):
        if not os.path.isfile(path):
            print("check-landing: %s does not exist" % path, file=sys.stderr)
            return 2

    problems = check_screenshots(data_path, shot_dir) + check_page(public, icon_path, release_path)
    for p in problems:
        print(p)
    print("check-landing: %d problems" % len(problems))
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
