#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks that the built website tells search engines the right thing for its environment (ADR-0031).

    WEBSITE_ENV=production python3 -I website/scripts/check-indexing.py [website/public]

The expected mode comes from the environment variable WEBSITE_ENV (production, the default, or preview; ci/website.sh exports
it). A preview is built with the production asset pipeline and differs only in what it tells crawlers, so a mix-up in either
direction is a defect: a preview that can be indexed, or a production site that hides itself. The check fails when

  - mode preview:    robots.txt is missing, lacks the line "Disallow: /", or has a Sitemap line, or an HTML page does not carry
                     the robots meta tag with noindex,
  - mode production: robots.txt is missing, has the line "Disallow: /", or lacks the line "Sitemap: <base URL>sitemap.xml"
                     (the base URL is WEBSITE_BASEURL or the baseURL of website/config/_default/hugo.yaml), or an HTML page
                     carries a robots meta tag with noindex.

Only meta tags are evaluated; the word noindex in the text of a page is no finding. The output names the file and the problem,
never page content. Exit status 0 = clean, 1 = findings, 2 = usage or environment error. Python 3.9 compatible, standard
library only.
"""

import os
import re
import sys
from html.parser import HTMLParser

MODES = ("production", "preview")


class RobotsMeta(HTMLParser):
    """Collects the content of every <meta name="robots"> of a page."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.contents = []

    def handle_starttag(self, tag, attrs):
        if tag != "meta":
            return
        attr = {k.lower(): (v or "") for k, v in attrs}
        if attr.get("name", "").strip().lower() == "robots":
            self.contents.append(attr.get("content", ""))


def has_noindex(html):
    """Returns (number of robots meta tags, whether one of them contains the directive noindex)."""
    parser = RobotsMeta()
    parser.feed(html)
    parser.close()
    found = any("noindex" in [d.strip().lower() for d in c.split(",")] for c in parser.contents)
    return len(parser.contents), found


def configured_base_url():
    """The base URL of the build: WEBSITE_BASEURL, else the baseURL of the site configuration."""
    base = os.environ.get("WEBSITE_BASEURL", "").strip()
    if not base:
        conf = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "config", "_default", "hugo.yaml")
        try:
            with open(conf, encoding="utf-8") as fh:
                for line in fh:
                    m = re.match(r"^baseURL:\s*(\S+)\s*$", line)
                    if m:
                        base = m.group(1).strip("\"'")
                        break
        except OSError:
            return ""
    return base if base.endswith("/") or not base else base + "/"


def check_robots(root, mode, base_url):
    """Returns the findings about robots.txt."""
    path = os.path.join(root, "robots.txt")
    try:
        with open(path, encoding="utf-8") as fh:
            lines = [ln.strip() for ln in fh.read().splitlines()]
    except OSError:
        return ["robots.txt: missing or unreadable"]
    disallow_all = "Disallow: /" in lines
    sitemaps = [ln for ln in lines if ln.lower().startswith("sitemap:")]
    findings = []
    if mode == "preview":
        if not disallow_all:
            findings.append('robots.txt: the preview does not contain the line "Disallow: /"')
        if sitemaps:
            findings.append("robots.txt: the preview names a sitemap")
    else:
        if disallow_all:
            findings.append('robots.txt: production contains the line "Disallow: /"')
        want = "Sitemap: " + base_url + "sitemap.xml"
        if want not in sitemaps:
            findings.append("robots.txt: production does not contain the line %r" % want)
    return findings


def check_pages(root, mode):
    """Returns (pages scanned, findings) for the robots meta tags of every HTML page."""
    findings = []
    scanned = 0
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            if not name.endswith(".html"):
                continue
            path = os.path.join(base, name)
            rel = os.path.relpath(path, root).replace(os.sep, "/")
            with open(path, "rb") as fh:
                html = fh.read().decode("utf-8", errors="replace")
            scanned += 1
            _, noindex = has_noindex(html)
            if mode == "preview" and not noindex:
                findings.append("%s: the preview page has no robots meta tag with noindex" % rel)
            if mode == "production" and noindex:
                findings.append("%s: the production page carries a robots meta tag with noindex" % rel)
    return scanned, findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: WEBSITE_ENV=production|preview python3 -I website/scripts/check-indexing.py [website/public]", file=sys.stderr)
        return 2
    mode = os.environ.get("WEBSITE_ENV", "production") or "production"
    if mode not in MODES:
        print("check-indexing: WEBSITE_ENV must be production or preview", file=sys.stderr)
        return 2
    root = argv[0] if argv else "website/public"
    if not os.path.isdir(root):
        print("check-indexing: %s is not a directory (build the site first: make website)" % root, file=sys.stderr)
        return 2
    base_url = configured_base_url()
    if mode == "production" and not base_url:
        print("check-indexing: no base URL (WEBSITE_BASEURL or baseURL in website/config/_default/hugo.yaml)", file=sys.stderr)
        return 2
    findings = check_robots(root, mode, base_url)
    scanned, page_findings = check_pages(root, mode)
    findings += page_findings
    for line in findings:
        print(line)
    print("check-indexing: mode %s, %d pages scanned, %d findings" % (mode, scanned, len(findings)))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
