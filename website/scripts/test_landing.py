#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Unit tests of check-landing.py (the website gate runs them with the other tests in this directory).

    python3 -I -m unittest discover -s website/scripts -p 'test_*.py'

Each test writes a small site and a screenshot directory into a temporary directory, runs the check and asserts the exit status and
the output. Standard library only.
"""

import contextlib
import importlib.util
import io
import os
import shutil
import struct
import tempfile
import unittest
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def png(width, height):
    """A valid PNG of one grey line repeated: only the header matters to the check, but the file is well formed."""

    def chunk(kind, data):
        body = kind + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    header = struct.pack(">IIBBBBB", width, height, 8, 0, 0, 0, 0)
    raw = b"".join(b"\x00" + b"\x80" * width for _ in range(1))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


DATA = """hero:
  title: x
screenshots:
  title: The web UI
  items:
    - name: dashboard
      title: Overview
    - name: agents
      title: Agents
quickstart:
  title: y
"""

def circle(cx, cy, r, ccw=False):
    """A full circle as two arcs, in the form of the brand files."""
    sweep = 0 if ccw else 1
    return "M%s %sa%s %s 0 1 %d %s 0 %s %s 0 1 %d%s 0" % (cx - r, cy, r, r, sweep, 2 * r, r, r, sweep, -2 * r)


RING_D = circle(57, 13, 13) + circle(57, 13, 5.5, ccw=True)
DOTS = [(row, col, 42 - 15 * (row - 1) + 30 * col, 14 + 26 * row, 12) for row in (1, 2, 3) for col in range(row + 1)]
ICON_SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="114" height="104" viewBox="0 0 114 104"><path fill="#b3338a" d="%s"/></svg>' % (
    "".join(circle(cx, cy, r) for _, _, cx, cy, r in DOTS) + RING_D
)


def mark_svg(extra="", ring_d=RING_D, dots=DOTS, svg_attrs=' aria-hidden="true" focusable="false"'):
    paths = '<path fill="currentColor" d="%s" class="ring"/>' % ring_d
    for row, col, cx, cy, r in dots:
        paths += '<path fill="currentColor" d="%s" class="dot r%d c%d"/>' % (circle(cx, cy, r), row, col)
    return '<svg width="114" height="104" class="landing-hero__mark"%s viewBox="0 0 114 104">%s%s</svg>' % (svg_attrs, extra, paths)


BUTTON = '<button type="button" class="landing-hero__motion" aria-label="Pause logo animation" aria-pressed="false" hidden></button>'  # the removed one

GOOD_PAGE = """<!doctype html><html><head><title>t</title><link rel="stylesheet" href="/css/site.css"></head><body class="td-home">
<h1>Title</h1>
@MARK@
<section id="features"></section>
<section id="architecture"><svg role="img" aria-labelledby="arch-title arch-desc" viewBox="0 0 1 1"><title id="arch-title">a</title></svg></section>
<section id="screenshots">
<img src="/images/a.webp" srcset="/images/a_640.webp 640w, /images/a.webp 960w" width="1440" height="900" alt="Overview" loading="lazy">
</section>
<section id="quickstart"></section>
<section id="community"></section>
<script src="/js/landing-mark.min.0123.js" integrity="sha256-x" defer></script>
</body></html>
""".replace("@MARK@", mark_svg())


class LandingTest(unittest.TestCase):
    mod = load("check-landing")

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="landing-check-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.public = os.path.join(self.tmp, "public")
        self.shots = os.path.join(self.tmp, "shots")
        self.data = os.path.join(self.tmp, "landing.yaml")
        os.makedirs(os.path.join(self.public, "images"))
        os.makedirs(self.shots)
        for name in ("a.webp", "a_640.webp"):
            self.write(os.path.join(self.public, "images", name), b"x")
        self.write(self.data, DATA.encode())
        self.icon = os.path.join(self.tmp, "myriarch-icon.svg")
        self.write(self.icon, ICON_SVG.encode())
        os.makedirs(os.path.join(self.public, "css"))
        self.write(os.path.join(self.public, "css", "site.css"), b".landing-hero__mark{display:none}")
        for name in ("dashboard", "agents"):
            for theme in ("light", "dark"):
                self.write(os.path.join(self.shots, "%s-%s.png" % (name, theme)), png(1440, 900))
        self.page(GOOD_PAGE)

    def write(self, path, content):
        with open(path, "wb") as fh:
            fh.write(content)

    def page(self, html):
        self.write(os.path.join(self.public, "index.html"), html.encode())

    def run_check(self, *extra):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = self.mod.main([self.public, "--screenshots", self.shots, "--data", self.data, "--icon", self.icon] + list(extra))
        return rc, out.getvalue(), err.getvalue()

    def test_clean_page_passes(self):
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 0, out)
        self.assertIn("0 problems", out)

    def test_missing_screenshot_in_one_theme(self):
        os.remove(os.path.join(self.shots, "agents-dark.png"))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("assets/images/screenshots/agents-dark.png: the screenshot is missing", out)
        self.assertNotIn("dashboard", out)

    def test_wrong_size_and_not_a_png(self):
        self.write(os.path.join(self.shots, "agents-light.png"), png(800, 600))
        self.write(os.path.join(self.shots, "dashboard-dark.png"), b"not a png at all, not at all")
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("agents-light.png: size is 800x600, expected 1440x900", out)
        self.assertIn("dashboard-dark.png: not a PNG file", out)

    def test_image_without_dimensions_or_alt(self):
        self.page(GOOD_PAGE.replace(' width="1440" height="900"', "").replace('alt="Overview"', 'alt=""'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("has no numeric width", out)
        self.assertIn("has no numeric height", out)
        self.assertIn("has no alt text", out)

    def test_image_without_alt_attribute(self):
        self.page(GOOD_PAGE.replace(' alt="Overview"', ""))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("has no alt text", out)

    def test_lazy_image_with_high_priority(self):
        self.page(GOOD_PAGE.replace('loading="lazy"', 'loading="lazy" fetchpriority="high"'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("is lazy and has fetchpriority=high", out)

    def test_image_source_missing_from_the_site(self):
        os.remove(os.path.join(self.public, "images", "a_640.webp"))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("image source /images/a_640.webp is not a file of the site", out)

    def test_external_resource_is_reported(self):
        self.page(GOOD_PAGE.replace("</head>", '<link rel="stylesheet" href="https://fonts.example.com/a.css"></head>'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("loads https://fonts.example.com/a.css from another host", out)

    def test_external_image_is_reported_once_as_foreign(self):
        self.page(GOOD_PAGE.replace('src="/images/a.webp"', 'src="https://img.example.com/a.png"'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("img src loads https://img.example.com/a.png from another host", out)
        self.assertNotIn("is not a file of the site", out)

    def test_plain_anchor_to_another_host_is_fine(self):
        self.page(GOOD_PAGE.replace("<h1>", '<a href="https://github.com/myriarch/myriarch">x</a><h1>'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 0, out)

    def test_missing_section_and_heading_count(self):
        self.page(GOOD_PAGE.replace('<section id="community"></section>', "").replace("<h1>Title</h1>", "<h1>A</h1><h1>B</h1>"))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("the section #community is missing", out)
        self.assertIn("2 h1 elements, expected exactly 1", out)

    def test_interim_home_class_is_reported(self):
        self.page(GOOD_PAGE.replace('class="td-home"', 'class="td-home interim-home"'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("interim-home", out)

    def test_diagram_needs_role_and_title(self):
        self.page(GOOD_PAGE.replace('role="img" ', "").replace('<title id="arch-title">a</title>', ""))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("role=img and a <title>", out)

    def test_page_without_diagram(self):
        start = GOOD_PAGE.index('<svg role="img"')
        end = GOOD_PAGE.index("</svg>", start) + len("</svg>")
        self.page(GOOD_PAGE[:start] + GOOD_PAGE[end:])
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("the architecture diagram", out)

    def page_with_mark(self, mark):
        self.page(GOOD_PAGE.replace(mark_svg(), mark))

    def test_page_without_the_mark(self):
        self.page_with_mark("")
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("exactly one inline svg with the class landing-hero__mark, found 0", out)

    def test_two_marks(self):
        self.page_with_mark(mark_svg() + mark_svg())
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("found 2", out)

    def test_mark_must_be_decoration(self):
        self.page_with_mark(mark_svg(svg_attrs=' role="img" aria-label="myriarch"'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn('aria-hidden="true"', out)
        self.assertIn("no title, aria-label or role", out)

    def test_mark_with_a_title(self):
        self.page_with_mark(mark_svg(extra="<title>myriarch</title>"))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("no title, aria-label or role", out)

    def test_mark_with_eight_dots(self):
        self.page_with_mark(mark_svg(dots=DOTS[:-1]))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("needs 1 path.ring and 9 path.dot, found 1 and 8", out)

    def test_dot_with_a_wrong_class(self):
        self.page(GOOD_PAGE.replace('class="dot r3 c3"', 'class="dot r3 c9"'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("expected dot r<1-3> c<0-3>", out)

    def test_circles_that_differ_from_the_static_mark(self):
        moved = [(row, col, cx + (1 if (row, col) == (2, 1) else 0), cy, r) for row, col, cx, cy, r in DOTS]
        self.page_with_mark(mark_svg(dots=moved))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("the circles of the mark are not those of myriarch-icon.svg", out)

    def test_page_with_the_removed_pause_button(self):
        self.page(GOOD_PAGE.replace("</h1>", "</h1>" + BUTTON, 1))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("still has a visible pause control (<button> with the class landing-hero__motion)", out)

    def test_page_with_any_other_pause_control(self):
        self.page(GOOD_PAGE.replace("</h1>", '</h1><div class="landing-hero__motion"></div>', 1))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("still has a visible pause control (<div> with the class landing-hero__motion)", out)

    def test_mark_that_is_a_control_in_the_markup(self):
        cases = (
            (' role="button"', "no title, aria-label or role"),
            (' tabindex="0"', "no tabindex or aria-pressed"),
            (' aria-pressed="false"', "no tabindex or aria-pressed"),
        )
        for attrs, message in cases:
            with self.subTest(attrs=attrs):
                self.page_with_mark(mark_svg(svg_attrs=' aria-hidden="true" focusable="false"' + attrs))
                rc, out, _ = self.run_check()
                self.assertEqual(rc, 1)
                self.assertIn(message, out)

    def test_page_without_the_script_of_the_mark(self):
        self.page(GOOD_PAGE.replace("/js/landing-mark.min.0123.js", "/js/other.js"))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("must load exactly one script landing-mark from the site", out)

    def test_script_of_the_mark_from_another_host(self):
        self.page(GOOD_PAGE.replace("/js/landing-mark.min.0123.js", "https://cdn.example.com/landing-mark.js"))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("must load exactly one script landing-mark from the site", out)
        self.assertIn("loads https://cdn.example.com/landing-mark.js from another host", out)

    def test_mark_in_the_minified_form_of_hugo(self):
        # arc flags glued to the next number and numbers such as 5.5.0, as the HTML minifier writes them
        d = "M44 13a13 13 0 1126 0 13 13 0 11-26 0m7.5.0a5.5 5.5.0 1011 0 5.5 5.5.0 10-11 0"
        self.assertEqual(self.mod.path_circles(d), [(57.0, 13.0, 13.0), (57.0, 13.0, 5.5)])
        with self.assertRaises(ValueError):
            self.mod.path_circles("M0 0L5 5")
        with self.assertRaises(ValueError):
            self.mod.path_circles("M0 0a5 5 0 1 1 10 0z")

    def test_page_requesting_the_icon_file(self):
        self.page(GOOD_PAGE.replace("<h1>", '<img src="/brand/myriarch-icon.svg" width="1" height="1" alt="x"><h1>'))
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("requests /brand/myriarch-icon.svg", out)

    def test_stylesheet_requesting_the_icon_file(self):
        self.write(os.path.join(self.public, "css", "site.css"), b".m{background:url(/brand/myriarch-icon-dark.svg)}")
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1)
        self.assertIn("/css/site.css: requests /brand/myriarch-icon-dark.svg", out)

    def test_other_brand_files_are_fine(self):
        self.write(os.path.join(self.public, "css", "site.css"), b".logo{background:url(/brand/myriarch-horizontal.svg)}")
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 0, out)

    def test_usage_errors(self):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(self.mod.main(["a", "b"]), 2)
            self.assertEqual(self.mod.main(["--bogus"]), 2)
            self.assertEqual(self.mod.main([os.path.join(self.tmp, "does-not-exist")]), 2)
        self.assertIn("usage:", err.getvalue())
        self.assertIn("is not a directory", err.getvalue())

    def test_screenshot_names_come_from_the_data_file(self):
        self.assertEqual(self.mod.screenshot_names(self.data), ["dashboard", "agents"])

    def test_real_data_file_lists_four_screenshots(self):
        names = self.mod.screenshot_names(self.mod.LANDING_DATA)
        self.assertEqual(names, ["dashboard", "agents", "fleet-version", "rollout"])


if __name__ == "__main__":
    unittest.main()
