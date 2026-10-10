#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Static check of the built website: no resource is loaded from another host (ADR-0031, privacy rule).

    python3 -I website/scripts/check-third-party.py [website/public]

Scans the built site and fails when a page, style sheet or script refers to a resource through an absolute (http:, https:)
or protocol-relative (//host) URL:

  - HTML: script src; link href when rel is stylesheet, preload, modulepreload, icon, shortcut icon, apple-touch-icon,
    manifest, prefetch, preconnect or dns-prefetch; img, source, video, audio, track, iframe, embed src; srcset and poster;
    object data; url() and @import in <style> elements and in style attributes. Plain anchors (<a href>) are fine: a
    link is not a request.
  - CSS files: url() and @import with an absolute URL.
  - JavaScript files and inline scripts: import("https://..."), from "https://...", importScripts("https://..."),
    new Worker("https://...") and new SharedWorker("https://...").

The output names the file, the element and the URL; nothing else. Exit status 0 = clean, 1 = findings, 2 = usage error.
Python 3.9 compatible, standard library only.
"""

import os
import re
import sys
from html.parser import HTMLParser

LINK_RELS = {
    "stylesheet", "preload", "modulepreload", "icon", "shortcut", "apple-touch-icon", "apple-touch-icon-precomposed",
    "manifest", "prefetch", "preconnect", "dns-prefetch", "mask-icon",
}
SRC_TAGS = {"script", "img", "source", "video", "audio", "track", "iframe", "embed", "input"}
CSS_URL = re.compile(r"url\(\s*(?:\"([^\"]*)\"|'([^']*)'|([^)\s\"']*))\s*\)", re.I)
CSS_IMPORT = re.compile(r"@import\s+(?:url\(\s*)?(?:\"([^\"]*)\"|'([^']*)')", re.I)
JS_PATTERNS = [
    re.compile(r"\bimport\s*\(\s*[\"'`](\s*(?:https?:)?//[^\"'`]*)[\"'`]"),
    re.compile(r"\bfrom\s*[\"'](\s*(?:https?:)?//[^\"']*)[\"']"),
    re.compile(r"\bimportScripts\s*\(\s*[\"'](\s*(?:https?:)?//[^\"']*)[\"']"),
    re.compile(r"\bnew\s+(?:Shared)?Worker\s*\(\s*[\"'](\s*(?:https?:)?//[^\"']*)[\"']"),
]


def is_remote(url):
    """True for an absolute http(s) URL or a protocol-relative URL."""
    u = (url or "").strip().lower()
    return u.startswith("http://") or u.startswith("https://") or u.startswith("//")


def css_findings(text):
    out = []
    for m in CSS_URL.finditer(text):
        url = next((g for g in m.groups() if g), "")
        if is_remote(url):
            out.append(("css url()", url.strip()))
    for m in CSS_IMPORT.finditer(text):
        url = next((g for g in m.groups() if g), "")
        if is_remote(url):
            out.append(("css @import", url.strip()))
    return out


def js_findings(text):
    out = []
    for pat in JS_PATTERNS:
        for m in pat.finditer(text):
            out.append(("script import", m.group(1).strip()))
    return out


class PageScanner(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.findings = []
        self._open = None  # "style" or "script" while inside such an element
        self._buf = []

    def add(self, what, url):
        self.findings.append((what, url))

    def handle_starttag(self, tag, attrs):
        a = {k: (v or "") for k, v in attrs}
        if tag == "link":
            rels = set(a.get("rel", "").lower().split())
            if rels & LINK_RELS and is_remote(a.get("href", "")):
                self.add("link rel=%s" % a.get("rel", ""), a["href"].strip())
        elif tag == "object" and is_remote(a.get("data", "")):
            self.add("object data", a["data"].strip())
        elif tag == "script" and is_remote(a.get("src", "")):
            self.add("script src", a["src"].strip())
        elif tag in SRC_TAGS and tag != "script" and is_remote(a.get("src", "")):
            if tag == "input" and a.get("type", "").lower() != "image":
                pass
            else:
                self.add("%s src" % tag, a["src"].strip())
        for attr in ("srcset", "poster"):
            value = a.get(attr, "")
            if not value:
                continue
            parts = [p.split()[0] for p in value.split(",") if p.strip()] if attr == "srcset" else [value]
            for p in parts:
                if is_remote(p):
                    self.add("%s %s" % (tag, attr), p.strip())
        if a.get("style"):
            for what, url in css_findings(a["style"]):
                self.add("style attribute %s" % what, url)
        if tag in ("style", "script") and not (tag == "script" and a.get("src")):
            self._open = tag
            self._buf = []
            if tag == "script" and a.get("type", "").lower() in ("application/json", "application/ld+json"):
                self._open = None

    def handle_endtag(self, tag):
        if self._open == tag:
            text = "".join(self._buf)
            if tag == "style":
                for what, url in css_findings(text):
                    self.add("<style> %s" % what, url)
            else:
                for what, url in js_findings(text):
                    self.add("inline script %s" % what, url)
            self._open = None
            self._buf = []

    def handle_data(self, data):
        if self._open:
            self._buf.append(data)


def read_text(path):
    with open(path, "rb") as fh:
        return fh.read().decode("utf-8", errors="replace")


def scan(root):
    """Returns (files scanned, findings as (relative path, what, url))."""
    findings = []
    scanned = 0
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            ext = os.path.splitext(name)[1].lower()
            if ext not in (".html", ".css", ".js", ".mjs"):
                continue
            path = os.path.join(base, name)
            rel = os.path.relpath(path, root).replace(os.sep, "/")
            text = read_text(path)
            scanned += 1
            if ext == ".html":
                parser = PageScanner()
                parser.feed(text)
                parser.close()
                items = parser.findings
            elif ext == ".css":
                items = css_findings(text)
            else:
                items = js_findings(text)
            for what, url in items:
                findings.append((rel, what, url))
    return scanned, findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-third-party.py [website/public]", file=sys.stderr)
        return 2
    root = argv[0] if argv else "website/public"
    if not os.path.isdir(root):
        print("check-third-party: %s is not a directory (build the site first: make website)" % root, file=sys.stderr)
        return 2
    scanned, findings = scan(root)
    for rel, what, url in sorted(set(findings)):
        print("%s: %s loads %s" % (rel, what, url))
    print("check-third-party: %d files scanned, %d resources from other hosts" % (scanned, len(set(findings))))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
