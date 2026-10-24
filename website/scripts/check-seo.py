#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Check of the metadata for search engines and AI crawlers in the built website (ADR-0031).

    WEBSITE_ENV=production python3 -I website/scripts/check-seo.py [--config-dir DIR] [--release FILE] [website/public]

The expected values come from the site configuration (config/_default/hugo.yaml and params.yaml) and from data/release.yaml, so the
pages cannot drift from them. The mode comes from the environment variable WEBSITE_ENV (production, the default, or preview;
ci/website.sh exports it). A preview names the production pages as the originals too, so the mode limits only the checks that depend on
the sitemap and on the policy of robots.txt, which differ in a preview (check-indexing.py checks those files for it). A build for another
host (WEBSITE_BASEURL, the root of that host) has its sitemap and its describedby link below that host; the canonical links stay on the
production origin.

Fails when

  - the configuration: params.seo.canonicalBase is not an origin (https://host[:port]/, no path: the site is served at the root of its host)
    or differs from baseURL of hugo.yaml, or a fact of the structured data (license, languages, keywords, repository, description) is
    missing,
  - a page (every HTML file but the 404 page and redirect stubs): has not exactly one canonical link, or its href is not the absolute URL of
    the page below the production origin; has not exactly one non-empty title and description; has no link rel="describedby" to /llms.txt;
    has an image without alt text; carries JSON-LD that is not valid JSON, has a raw < or > (a value that was not escaped could end the
    script element), is not an object of schema.org with a known type, has a property that the type does not define (the Schema Markup
    Validator would report it) or a value that differs from the configuration,
  - the structured data of a page is not the expected set: the home page has exactly one WebSite and one SoftwareSourceCode (the version is
    the latest release of data/release.yaml, without the leading v); a page with a visible breadcrumb has exactly one BreadcrumbList that
    equals it: the home page, then the items of the visible breadcrumb with the same names and URLs (a breadcrumb that Docsy hides with the
    class td-breadcrumbs--single, a trail of one item, is not visible: such a page has no list); every other page has no JSON-LD,
  - two pages have the same title (the 404 page is no page here), or a page outside docs/reference/ and docs/adr/ has a description of fewer
    than 70 or more than 160 characters (the generated references and the decision records are exempt: their descriptions come from the
    generators),
  - the sitemap has a URL without a <lastmod> or with one that is not a W3C date or date and time (the date comes from Git, never from the
    time of the build),
  - the title of the home page differs from params.seo.homeTitle, which must say more than the bare site title in at most 70 characters,
    or either is spelled against ADR-0015,
  - llms.txt is missing, has not the shape of the llms.txt format (one heading with the site title, a summary in a block quote, no HTML,
    and every list item below a level-2 heading a link of the form "- [name](url)" with optional ": notes": the format allows other lists
    only before the first level-2 heading), is larger than 64 KiB, links to a page that does not exist, to a placeholder page, to another
    host than the production origin and the repository, repeats a link, or spells the name against ADR-0015 ("Myriarch" at the start of a
    sentence, "myriarch" elsewhere; names of programs and paths are identifiers and always lower case),
  - the 404 page is missing, can be indexed (no robots meta with noindex), has a canonical link or structured data, has not exactly one h1
    or offers no link back to the home page, the documentation or the project in its content,
  - production only: robots.txt blocks a crawler (a Disallow rule with a path: the policy is that every crawler may read the site), or
    the sitemap and the pages differ (every URL of the sitemap is the URL of a page, and every page has its entry; a build with a relative
    base URL has no absolute sitemap and is not compared).

The output names the file and the problem and quotes the offending value (a title, a link, a property of the structured data, a setting of
the configuration): the site is public, and the value makes the finding actionable. Exit status 0 = clean, 1 = findings, 2 = usage or
environment error. Python 3.9 compatible, standard library only.
"""

import importlib.util
import json
import os
import re
import sys
from html.parser import HTMLParser
from urllib.parse import unquote, urlsplit

HERE = os.path.dirname(os.path.abspath(__file__))
WEBSITE = os.path.dirname(HERE)
DEFAULT_CONFIG_DIR = os.path.join(WEBSITE, "config", "_default")
DEFAULT_RELEASE = os.path.join(WEBSITE, "data", "release.yaml")

MODES = ("production", "preview")
SCHEMA_CONTEXT = "https://schema.org"
# The Apache-2.0 URLs that name the license of the code (the structured data of the home page names one of them).
APACHE_URLS = ("https://www.apache.org/licenses/LICENSE-2.0", "https://spdx.org/licenses/Apache-2.0.html")
HOME_TITLE_MAX = 70
DESCRIPTION_MIN, DESCRIPTION_MAX = 70, 160
DESCRIPTION_EXEMPT = ("docs/reference/", "docs/adr/")  # generated pages and decision records: the generators write their descriptions
LASTMOD = re.compile(r"^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2}))?$")
LLMS_MAX_BYTES = 64 * 1024
# The properties that the types of the structured data define in schema.org (https://schema.org/<Type>), restricted to the ones the site
# uses: a property that is not in the list is reported, because the Schema Markup Validator reports a property that the type does not
# define (softwareVersion on SoftwareSourceCode is the example). Extend the list only after you looked the property up on schema.org.
PROPERTIES = {
    "SoftwareSourceCode": {
        "@context", "@type", "@id", "name", "description", "url", "codeRepository", "license", "version", "programmingLanguage",
        "keywords", "isAccessibleForFree", "author", "copyrightHolder",
    },
    "WebSite": {"@context", "@type", "@id", "url", "name", "description", "inLanguage", "publisher"},
    "BreadcrumbList": {"@context", "@type", "itemListElement"},
    "ListItem": {"@type", "position", "name", "item"},
    "Organization": {"@type", "name", "url"},
}
REQUIRED = {
    "SoftwareSourceCode": ("@id", "name", "description", "url", "codeRepository", "license", "programmingLanguage"),
    "WebSite": ("@id", "name", "description", "url", "inLanguage", "publisher"),
    "BreadcrumbList": ("itemListElement",),
    "ListItem": ("position", "name", "item"),
    "Organization": ("name", "url"),
}


def load_sibling(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# ---- configuration -------------------------------------------------------------------------------------------------------------------


def strip_comment(raw):
    """Removes a trailing YAML comment (a # after white space, outside quotes) from the text of a value."""
    quote = ""
    for i, ch in enumerate(raw):
        if quote:
            if ch == quote:
                quote = ""
        elif ch in "\"'":
            quote = ch
        elif ch == "#" and (i == 0 or raw[i - 1] in " \t"):
            return raw[:i]
    return raw


def split_flow(body):
    """Splits the inside of a flow sequence at the commas that are outside quotes."""
    parts, cur, quote = [], [], ""
    for ch in body:
        if quote:
            cur.append(ch)
            if ch == quote:
                quote = ""
        elif ch in "\"'":
            quote = ch
            cur.append(ch)
        elif ch == ",":
            parts.append("".join(cur))
            cur = []
        else:
            cur.append(ch)
    parts.append("".join(cur))
    return parts


def unquote_scalar(raw):
    raw = raw.strip()
    if len(raw) >= 2 and raw[0] == raw[-1] and raw[0] in "\"'":
        return raw[1:-1]
    return raw


def parse_value(raw):
    """A scalar (quoted or plain) or a flow sequence of scalars, the two forms that the configuration files use."""
    raw = strip_comment(raw).strip()
    if raw.startswith("[") and raw.endswith("]"):
        return [unquote_scalar(p) for p in split_flow(raw[1:-1]) if p.strip()]
    return unquote_scalar(raw)


def read_keys(text, block=None):
    """The simple keys of a YAML file: those at the top level, or the children (indented by two spaces) of the top-level key `block`."""
    values = {}
    inside = block is None
    for line in text.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if block is not None:
            if re.match(r"^\S", line):
                inside = re.match(r"^%s:\s*(#.*)?$" % re.escape(block), line) is not None
                continue
            if not inside:
                continue
            m = re.match(r"^  ([A-Za-z0-9_]+):\s*(.*)$", line)
        else:
            m = re.match(r"^([A-Za-z0-9_]+):\s*(.*)$", line)
        if m and strip_comment(m.group(2)).strip():
            values[m.group(1)] = parse_value(m.group(2))
    return values


class Config:
    """The values the checks compare the pages with, and the problems found while reading them."""

    def __init__(self, config_dir, release_path):
        self.problems = []
        hugo = self._read(os.path.join(config_dir, "hugo.yaml"), "hugo.yaml")
        params = self._read(os.path.join(config_dir, "params.yaml"), "params.yaml")
        top = read_keys(hugo)
        ptop = read_keys(params)
        seo = read_keys(params, "seo")
        repo = read_keys(params, "repo")
        self.base_url = top.get("baseURL", "")
        self.title = top.get("title", "")
        self.locale = top.get("locale", "")
        self.description = ptop.get("description", "")
        self.copyright = ptop.get("copyright", "")
        self.repo_url = repo.get("url", "")
        self.canonical_base = seo.get("canonicalBase", "")
        self.home_title = seo.get("homeTitle", "")
        self.license = seo.get("license", "")
        self.languages = seo.get("programmingLanguage", [])
        self.keywords = seo.get("keywords", [])
        self.origin = self.canonical_base.rstrip("/")
        self.version = self._release(release_path)
        self._validate()

    def _read(self, path, name):
        try:
            with open(path, encoding="utf-8") as fh:
                return fh.read()
        except OSError:
            self.problems.append("%s: cannot be read" % name)
            return ""

    def _release(self, path):
        """The version of the latest release without the leading v ("" while there is none), as the structured data of the home page has it."""
        landing = load_sibling("check-landing")
        try:
            final, candidate, problems = landing.read_release(path)
        except OSError:
            self.problems.append("release.yaml: cannot be read")
            return ""
        self.problems.extend(problems)
        tag = final or candidate
        return tag[1:] if tag.startswith("v") else tag

    def _validate(self):
        problems = self.problems
        if not re.fullmatch(r"https://[a-z0-9.-]+(:[0-9]+)?/?", self.canonical_base):
            problems.append("params.yaml: seo.canonicalBase must be an origin (https://host[:port]/) without a path")
        elif self.base_url and self.canonical_base.rstrip("/") != self.base_url.rstrip("/"):
            problems.append("params.yaml: seo.canonicalBase (%s) differs from baseURL of hugo.yaml (%s)" % (self.canonical_base, self.base_url))
        if not self.base_url:
            problems.append("hugo.yaml: baseURL is missing")
        for key, value in (("title", self.title), ("locale", self.locale), ("description", self.description),
                           ("copyright", self.copyright), ("repo.url", self.repo_url), ("seo.homeTitle", self.home_title),
                           ("seo.license", self.license)):
            if not value:
                problems.append("%s: %s is missing" % ("hugo.yaml" if key in ("title", "locale") else "params.yaml", key))
        if self.home_title and (self.home_title == self.title or len(self.home_title) > HOME_TITLE_MAX):
            problems.append("params.yaml: seo.homeTitle must say what the project is (more than the site title %r) in at most %d characters"
                            % (self.title, HOME_TITLE_MAX))
        problems.extend(spelling_problems(self.home_title, "params.yaml seo.homeTitle"))
        if not self.languages:
            problems.append("params.yaml: seo.programmingLanguage is missing")
        if not self.keywords:
            problems.append("params.yaml: seo.keywords is missing")
        if self.license and self.license not in APACHE_URLS:
            problems.append("params.yaml: seo.license must be one of %s" % ", ".join(APACHE_URLS))


# ---- pages ---------------------------------------------------------------------------------------------------------------------------


class PageParser(HTMLParser):
    """Collects what the checks need from one page."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.lang = ""
        self.titles = []
        self.descriptions = []
        self.robots = []
        self.canonicals = []
        self.describedby = []
        self.ld = []
        self.images = []
        self.h1 = 0
        self.crumbs = None  # [(text, href or "")] of the visible breadcrumb navigation, None when the page has none
        self.crumbs_hidden = False  # the page has a breadcrumb navigation that Docsy hides (td-breadcrumbs--single, a trail of one item)
        self.content_links = []  # the hrefs of the links in div.td-content
        self._head = False
        self._title = None
        self._ld = None
        self._nav = 0
        self._item = None
        self._div = 0

    def handle_starttag(self, tag, attrs):
        a = {k.lower(): (v if v is not None else "") for k, v in attrs}
        classes = a.get("class", "").split()
        if tag == "html":
            self.lang = a.get("lang", "")
        elif tag == "head":
            self._head = True
        elif tag == "title" and self._head:  # the <title> of an inline SVG is no document title
            self._title = []
        elif tag == "meta":
            name = a.get("name", "").strip().lower()
            if name == "description":
                self.descriptions.append(a.get("content", ""))
            elif name == "robots":
                self.robots.append(a.get("content", ""))
        elif tag == "link":
            rels = a.get("rel", "").lower().split()
            if "canonical" in rels:
                self.canonicals.append(a.get("href", ""))
            if "describedby" in rels:
                self.describedby.append(a.get("href", ""))
        elif tag == "script" and a.get("type", "").strip().lower() == "application/ld+json":
            self._ld = []
        elif tag == "img":
            self.images.append(a)
        elif tag == "h1":
            self.h1 += 1
        elif tag == "nav" and a.get("aria-label", "").strip().lower() == "breadcrumb":
            if "td-breadcrumbs--single" in classes:
                self.crumbs_hidden = True  # the style sheet of the theme hides it, so the page shows no breadcrumb
            else:
                self._nav = 1
                self.crumbs = []
        elif self._nav and tag == "li" and "td-breadcrumbs__item" in classes:
            self._close_item()
            self._item = {"text": [], "href": ""}
        elif self._item is not None and tag == "a":
            self._item["href"] = a.get("href", "")
        if tag == "div":
            if self._div:
                self._div += 1
            elif "td-content" in classes:
                self._div = 1
        elif tag == "a" and self._div:
            self.content_links.append(a.get("href", ""))

    def handle_endtag(self, tag):
        if tag == "head":
            self._head = False
        elif tag == "title" and self._title is not None:
            self.titles.append("".join(self._title))
            self._title = None
        elif tag == "script" and self._ld is not None:
            self.ld.append("".join(self._ld))
            self._ld = None
        elif tag == "nav" and self._nav:
            self._close_item()
            self._nav = 0
        elif tag == "li" and self._item is not None:
            self._close_item()
        elif tag == "div" and self._div:
            self._div -= 1

    def handle_data(self, data):
        if self._title is not None:
            self._title.append(data)
        elif self._ld is not None:
            self._ld.append(data)
        elif self._item is not None:
            self._item["text"].append(data)

    def _close_item(self):
        if self._item is not None and self.crumbs is not None:
            self.crumbs.append(("".join(self._item["text"]).strip(), self._item["href"]))
        self._item = None


def read_text(path):
    with open(path, "rb") as fh:
        return fh.read().decode("utf-8", errors="replace")


def parse_page(text):
    parser = PageParser()
    parser.feed(text)
    parser.close()
    return parser


def page_path(rel):
    """The URL path that serves a built file ("docs/a/index.html" is "/docs/a/"), or None for a file that is no page of its own."""
    if rel == "index.html":
        return "/"
    if rel.endswith("/index.html"):
        return "/" + rel[: -len("index.html")]
    return None


def same_path(a, b):
    return unquote(a) == unquote(b)


def list_pages(root):
    out = []
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            if name.endswith(".html"):
                out.append(os.path.relpath(os.path.join(base, name), root).replace(os.sep, "/"))
    return out


# ---- spelling (ADR-0015) -------------------------------------------------------------------------------------------------------------


NAME = re.compile(r"\b[Mm]yriarch\b")


def spelling_problems(text, label):
    """Findings for the name of the project in running text: "Myriarch" starts a sentence, "myriarch" is written everywhere else.

    A name that is part of an identifier (myriarch-updater, myriarch.dev, ghcr.io/myriarch/myriarch, dev.myriarch.*, `myriarch` in a code span) is
    not meant: those are always lower case. A sentence starts at the beginning of a line (after the marks of a heading, a list item, a quote or
    emphasis) and after a full stop, an exclamation mark or a question mark.
    """
    out = []
    for number, line in enumerate(text.splitlines(), 1):
        for m in NAME.finditer(line):
            start, end = m.span()
            before = line[start - 1] if start else " "
            after = line[end] if end < len(line) else " "
            after2 = line[end + 1] if end + 1 < len(line) else " "
            if before in "/.-_@" or after in "-/_@" or (after == "." and after2.isalnum()):
                continue  # part of a path, a host name or a program name
            if line[:start].count("`") % 2:
                continue  # inside a code span
            lead = re.sub(r"^[\s>#*_\-+\[(\"']*", "", line[:start])
            lead = lead.rstrip(" \t*_\"'([")
            starts = lead == "" or lead[-1] in ".!?"
            word = m.group(0)
            if starts and word != "Myriarch":
                out.append("%s:%d: the name starts a sentence, write 'Myriarch' (ADR-0015)" % (label, number))
            elif not starts and word != "myriarch":
                out.append("%s:%d: the name is not at the start of a sentence, write 'myriarch' (ADR-0015)" % (label, number))
    return out


# ---- structured data -----------------------------------------------------------------------------------------------------------------


def build_base(cfg):
    """The base URL that the site was built for, without a final slash: WEBSITE_BASEURL (a build for another host), else baseURL of
    hugo.yaml. "" when it is relative (WEBSITE_BASEURL=/ in a preview): such a build has no absolute URL of its own."""
    base = (os.environ.get("WEBSITE_BASEURL", "").strip() or cfg.base_url).rstrip("/")
    return base if re.match(r"^https?://", base) else ""


def url_of(origin, href):
    """The absolute URL of a link of a page: a root-relative href lies below the production origin."""
    if href.startswith("/") and not href.startswith("//"):
        return origin + href
    return href


def check_node_shape(rel, node, kind, path):
    """The type, the properties and the required values of one JSON-LD node."""
    out = []
    where = "%s: JSON-LD %s" % (rel, path)
    if not isinstance(node, dict):
        return ["%s is not an object" % where]
    if node.get("@type") != kind:
        return ["%s has the type %r, expected %s" % (where, node.get("@type"), kind)]
    for key in sorted(set(node) - PROPERTIES[kind]):
        out.append("%s has the property %s, which %s does not define on schema.org (or the check does not list yet)" % (where, key, kind))
    for key in REQUIRED[kind]:
        value = node.get(key)
        if value in (None, "", [], {}):
            out.append("%s lacks %s" % (where, key))
    return out


def check_organization(rel, node, cfg, path):
    out = check_node_shape(rel, node, "Organization", path)
    if isinstance(node, dict) and not out:
        if node.get("name") != cfg.copyright:
            out.append("%s: JSON-LD %s has the name %r, expected %r (params.copyright)" % (rel, path, node.get("name"), cfg.copyright))
        if node.get("url") != cfg.origin + "/":
            out.append("%s: JSON-LD %s has the url %r, expected %r" % (rel, path, node.get("url"), cfg.origin + "/"))
    return out


def expect(rel, node, key, want, out, label):
    if node.get(key) != want:
        out.append("%s: JSON-LD %s has %s %r, expected %r" % (rel, label, key, node.get(key), want))


def check_software(rel, node, cfg):
    out = check_node_shape(rel, node, "SoftwareSourceCode", "SoftwareSourceCode")
    if out:
        return out
    label = "SoftwareSourceCode"
    expect(rel, node, "@id", cfg.origin + "/#software", out, label)
    expect(rel, node, "name", cfg.title, out, label)
    expect(rel, node, "description", cfg.description, out, label)
    expect(rel, node, "url", cfg.origin + "/", out, label)
    expect(rel, node, "codeRepository", cfg.repo_url, out, label)
    expect(rel, node, "license", cfg.license, out, label)
    expect(rel, node, "programmingLanguage", cfg.languages, out, label)
    if "keywords" in node:
        expect(rel, node, "keywords", ", ".join(cfg.keywords), out, label)
    if "isAccessibleForFree" in node and node["isAccessibleForFree"] is not True:
        out.append("%s: JSON-LD %s has isAccessibleForFree %r, expected true" % (rel, label, node["isAccessibleForFree"]))
    if cfg.version:
        expect(rel, node, "version", cfg.version, out, label)
    elif "version" in node:
        out.append("%s: JSON-LD %s names the version %r, but data/release.yaml names no release" % (rel, label, node["version"]))
    for key in ("author", "copyrightHolder"):
        if key in node:
            out.extend(check_organization(rel, node[key], cfg, "SoftwareSourceCode." + key))
    return out


def check_website(rel, node, cfg, lang):
    out = check_node_shape(rel, node, "WebSite", "WebSite")
    if out:
        return out
    label = "WebSite"
    expect(rel, node, "@id", cfg.origin + "/#website", out, label)
    expect(rel, node, "name", cfg.title, out, label)
    expect(rel, node, "description", cfg.description, out, label)
    expect(rel, node, "url", cfg.origin + "/", out, label)
    expect(rel, node, "inLanguage", lang or cfg.locale, out, label)
    out.extend(check_organization(rel, node.get("publisher"), cfg, "WebSite.publisher"))
    return out


def check_breadcrumbs(rel, node, cfg, parsed, canonical):
    """A BreadcrumbList is the home page plus the visible breadcrumb of the page, item by item."""
    out = check_node_shape(rel, node, "BreadcrumbList", "BreadcrumbList")
    if out:
        return out
    items = node.get("itemListElement")
    if not isinstance(items, list) or not items:
        return ["%s: JSON-LD BreadcrumbList has no items" % rel]
    want = [("Home", cfg.origin + "/")]
    for text, href in parsed.crumbs or []:
        want.append((text, url_of(cfg.origin, href) if href else canonical))
    if len(items) != len(want):
        out.append("%s: JSON-LD BreadcrumbList has %d items, the visible breadcrumb has %d (with the home page)" % (rel, len(items), len(want)))
    for number, (item, (name, url)) in enumerate(zip(items, want), 1):
        shape = check_node_shape(rel, item, "ListItem", "BreadcrumbList item %d" % number)
        if shape:
            out.extend(shape)
            continue
        if item.get("position") != number or isinstance(item.get("position"), bool):
            out.append("%s: JSON-LD BreadcrumbList item %d has the position %r" % (rel, number, item.get("position")))
        if item.get("name") != name:
            out.append("%s: JSON-LD BreadcrumbList item %d is named %r, the visible breadcrumb says %r" % (rel, number, item.get("name"), name))
        if item.get("item") != url:
            out.append("%s: JSON-LD BreadcrumbList item %d has the URL %r, expected %r" % (rel, number, item.get("item"), url))
    return out


def check_json_ld(rel, parsed, cfg, canonical, is_home):
    """Findings for the JSON-LD blocks of one page."""
    out = []
    nodes = []
    for body in parsed.ld:
        if "<" in body or ">" in body:
            out.append("%s: JSON-LD contains a raw < or >; jsonify writes them as \\u003c and \\u003e, and a raw one could end the script element" % rel)
        try:
            data = json.loads(body)
        except ValueError as exc:
            out.append("%s: JSON-LD is not valid JSON (%s)" % (rel, exc))
            continue
        if not isinstance(data, dict):
            out.append("%s: JSON-LD must be one object per script element" % rel)
            continue
        if data.get("@context") != SCHEMA_CONTEXT:
            out.append("%s: JSON-LD @context must be %s" % (rel, SCHEMA_CONTEXT))
        kind = data.get("@type")
        if kind not in ("SoftwareSourceCode", "WebSite", "BreadcrumbList"):
            out.append("%s: JSON-LD has the type %r, which the site does not use" % (rel, kind))
            continue
        nodes.append((kind, data))
    kinds = sorted(k for k, _ in nodes)
    if is_home:
        want = ["SoftwareSourceCode", "WebSite"]
    elif parsed.crumbs is not None:
        want = ["BreadcrumbList"]
    else:
        want = []
    if kinds != want and not any(o.startswith("%s: JSON-LD has the type" % rel) for o in out):
        note = ""
        if parsed.crumbs_hidden and "BreadcrumbList" in kinds:
            note = " (Docsy hides the breadcrumb of this page: td-breadcrumbs--single)"
        out.append("%s: JSON-LD has the types %s, expected %s%s" % (rel, kinds or "none", want or "none", note))
    for kind, data in nodes:
        if kind == "SoftwareSourceCode" and is_home:
            out.extend(check_software(rel, data, cfg))
        elif kind == "WebSite" and is_home:
            out.extend(check_website(rel, data, cfg, parsed.lang))
        elif kind == "BreadcrumbList" and parsed.crumbs is not None:
            out.extend(check_breadcrumbs(rel, data, cfg, parsed, canonical))
    return out


# ---- the checks ----------------------------------------------------------------------------------------------------------------------


def check_page(rel, parsed, cfg, base):
    """Findings for one page that search engines may index (no 404 page, no redirect stub). Returns (findings, canonical URL or "")."""
    out = []
    path = page_path(rel)
    canonical = ""
    if path is None:
        out.append("%s: is a page whose URL the check cannot tell; publish it as <path>/index.html or extend page_path()" % rel)
    elif len(parsed.canonicals) != 1:
        out.append("%s: has %d canonical links, expected exactly one" % (rel, len(parsed.canonicals)))
    else:
        href = parsed.canonicals[0]
        want = cfg.origin + path
        split = urlsplit(href)
        if split.query or split.fragment or not href.startswith(cfg.origin + "/") or not same_path(href, want):
            out.append("%s: the canonical link is %r, expected %r" % (rel, href, want))
        canonical = want
    if len(parsed.titles) != 1 or not parsed.titles[0].strip():
        out.append("%s: has %d titles, expected exactly one that is not empty" % (rel, len(parsed.titles)))
    if len(parsed.descriptions) != 1 or not parsed.descriptions[0].strip():
        out.append("%s: has %d descriptions, expected exactly one that is not empty" % (rel, len(parsed.descriptions)))
    out.extend(check_description_length(rel, parsed))
    out.extend(check_describedby(rel, parsed, cfg, base))
    out.extend(check_images(rel, parsed))
    is_home = rel == "index.html"
    if is_home:
        out.extend(check_home_title(rel, parsed, cfg))
    out.extend(check_json_ld(rel, parsed, cfg, canonical, is_home))
    return out, canonical


def check_description_length(rel, parsed):
    """A description of 70 to 160 characters says what the page answers and is not cut off in a result; references and ADRs are exempt."""
    if len(parsed.descriptions) != 1 or rel.startswith(DESCRIPTION_EXEMPT):
        return []
    text = " ".join(parsed.descriptions[0].split())
    if text and not DESCRIPTION_MIN <= len(text) <= DESCRIPTION_MAX:
        return ["%s: the description has %d characters, expected %d to %d: %r" % (rel, len(text), DESCRIPTION_MIN, DESCRIPTION_MAX, text)]
    return []


def check_unique_titles(titles):
    """titles: title -> list of built files. Two pages with one title cannot be told apart in a result list."""
    return ["%s: has the same title as %s: %r" % (rels[i], ", ".join(r for r in rels if r != rels[i]), title)
            for title, rels in sorted(titles.items()) if len(rels) > 1 for i in range(len(rels))]


def check_describedby(rel, parsed, cfg, base):
    """Every page links to /llms.txt of the host that serves it: relative, or below the production origin or the base URL of the build."""
    hrefs = parsed.describedby
    if len(hrefs) != 1:
        return ["%s: has %d links rel=describedby, expected exactly one to /llms.txt" % (rel, len(hrefs))]
    split = urlsplit(hrefs[0])
    hosts = {cfg.origin}
    m = re.match(r"^(https?://[^/]+)", base)
    if m:
        hosts.add(m.group(1))
    if split.path != "/llms.txt" or (split.netloc and split.scheme + "://" + split.netloc not in hosts):
        return ["%s: the link rel=describedby names %r, expected /llms.txt" % (rel, hrefs[0])]
    return []


def check_images(rel, parsed):
    out = []
    for img in parsed.images:
        src = img.get("src", "") or "(no src)"
        alt = img.get("alt")
        decorative = img.get("role", "").lower() in ("presentation", "none") or img.get("aria-hidden", "").lower() == "true"
        if alt is None and not decorative:
            out.append("%s: the image %s has no alt attribute" % (rel, src))
        elif alt is not None and not alt.strip() and not decorative:
            out.append("%s: the image %s has an empty alt text and is not marked as decoration (role=presentation or aria-hidden)" % (rel, src))
        elif alt and re.fullmatch(r"[\w.-]+\.(png|jpe?g|gif|svg|webp|avif)", alt.strip(), re.I):
            out.append("%s: the alt text of the image %s is a file name" % (rel, src))
    return out


def check_home_title(rel, parsed, cfg):
    """The title of the home page is params.seo.homeTitle (the configuration check says what a good one is)."""
    if len(parsed.titles) != 1:
        return []
    title = parsed.titles[0].strip()
    out = []
    if title != cfg.home_title:
        out.append("%s: the title of the home page is %r, params.seo.homeTitle says %r" % (rel, title, cfg.home_title))
    out.extend(spelling_problems(title, rel + " <title>"))
    return out


def sitemap_urls(root):
    try:
        text = read_text(os.path.join(root, "sitemap.xml"))
    except OSError:
        return None
    return [u.strip().replace("&amp;", "&") for u in re.findall(r"<loc>([^<]*)</loc>", text)]


def check_sitemap(root, paths, base):
    """The sitemap lists the URL of every page below the base URL of the build, and nothing else (the 404 page is no page)."""
    urls = sitemap_urls(root)
    if urls is None:
        return ["sitemap.xml: missing or unreadable"]
    out = []
    listed = set(urls)
    want = {base + path for path in paths}
    if len(listed) != len(urls):
        out.append("sitemap.xml: lists a URL more than once")
    for url in sorted(listed - want):
        out.append("sitemap.xml: lists %s, which is the URL of no page" % url)
    for url in sorted(want - listed):
        out.append("sitemap.xml: does not list %s" % url)
    return out


def check_lastmod(root):
    """Every URL of the sitemap has a lastmod in the format of the sitemap protocol. Hugo takes it from Git, so a page without one has no commit yet."""
    try:
        text = read_text(os.path.join(root, "sitemap.xml"))
    except OSError:
        return []  # check_sitemap reports a missing file in production
    out = []
    for block in re.findall(r"<url>(.*?)</url>", text, re.S):
        loc = re.search(r"<loc>([^<]*)</loc>", block)
        name = loc.group(1).strip() if loc else "(an entry without loc)"
        dates = re.findall(r"<lastmod>([^<]*)</lastmod>", block)
        if not dates:
            out.append("sitemap.xml: %s has no lastmod" % name)
        elif len(dates) > 1 or not LASTMOD.match(dates[0].strip()):
            out.append("sitemap.xml: %s has the lastmod %r, expected one W3C date or date and time" % (name, ", ".join(dates)))
    return out


def check_robots(root, mode):
    """In production every crawler may read the site: no Disallow rule with a path. The preview file is check-indexing.py's business."""
    if mode != "production":
        return []
    try:
        lines = read_text(os.path.join(root, "robots.txt")).splitlines()
    except OSError:
        return ["robots.txt: missing or unreadable"]
    out = []
    for number, line in enumerate(lines, 1):
        rule = line.split("#", 1)[0].strip()
        m = re.match(r"^disallow\s*:\s*(\S+)$", rule, re.I)
        if m:
            out.append("robots.txt:%d: production blocks %s; the policy is that every crawler may read the site" % (number, m.group(1)))
    return out


def resolve_site_file(root, path):
    """The file of the built site that serves a URL path (a directory serves its index.html), or None."""
    parts = [p for p in unquote(path).split("/") if p]
    if ".." in parts:
        return None
    full = os.path.join(root, *parts)
    if os.path.isdir(full):
        full = os.path.join(full, "index.html")
    if not os.path.isfile(full) or not os.path.realpath(full).startswith(os.path.realpath(root) + os.sep):
        return None
    return full


LINK = re.compile(r"\[([^\]]*)\]\(([^)\s]*)\)")
# A list item (any marker, any indentation) and the one form that llmstxt.org gives a list item below a heading: a link, then optional notes.
LIST_ITEM = re.compile(r"^\s*(?:[-*+]|\d+[.)])(?:\s|$)")
FILE_ITEM = re.compile(r"^- \[[^\]]+\]\([^)\s]+\)(?:: \S.*)?$")


def check_llms(root, cfg):
    """The shape and the links of llms.txt."""
    path = os.path.join(root, "llms.txt")
    try:
        with open(path, "rb") as fh:
            raw = fh.read()
    except OSError:
        return ["llms.txt: missing (the output format LLMS of the home page, config/_default/hugo.yaml)"]
    out = []
    if len(raw) > LLMS_MAX_BYTES:
        out.append("llms.txt: %d bytes, more than the limit of %d; curate data/llms.yaml" % (len(raw), LLMS_MAX_BYTES))
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return out + ["llms.txt: is not UTF-8"]
    if not text.endswith("\n") or text.endswith("\n\n"):
        out.append("llms.txt: must end with exactly one line break")
    lines = text.splitlines()
    headings = [n for n, ln in enumerate(lines, 1) if ln.startswith("# ")]
    if not lines or lines[0] != "# " + cfg.title:
        out.append("llms.txt: the first line must be the heading '# %s' (the title of the site)" % cfg.title)
    if len(headings) != 1:
        out.append("llms.txt: has %d level-1 headings, expected exactly one" % len(headings))
    following = next((ln for ln in lines[1:] if ln.strip()), "")
    if not following.startswith("> "):
        out.append("llms.txt: the heading must be followed by the summary in a block quote ('> ...')")
    if re.search(r"</?[A-Za-z][^>]*>", text):
        out.append("llms.txt: contains an HTML tag; the format is Markdown")
    seen = set()
    count = 0
    sectioned = False  # past the first level-2 heading, where the format keeps its lists of links
    for number, line in enumerate(lines, 1):
        if line.startswith("## "):
            sectioned = True
        elif sectioned and LIST_ITEM.match(line) and not FILE_ITEM.match(line):
            out.append("llms.txt:%d: a list item below a level-2 heading must be a link of the form '- [name](url)' or '- [name](url): notes'; "
                       "free-form lists belong before the first '## ' heading" % number)
        for title, url in LINK.findall(line):
            count += 1
            where = "llms.txt:%d" % number
            if not title.strip():
                out.append("%s: a link without text" % where)
            if url in seen:
                out.append("%s: the link %s appears twice" % (where, url))
            seen.add(url)
            out.extend(check_llms_link(root, cfg, where, url))
    if not count:
        out.append("llms.txt: has no link")
    out.extend(spelling_problems("\n".join([""] + lines[1:]), "llms.txt"))  # line 1 is the title of the site, which is checked above
    return out


def check_llms_link(root, cfg, where, url):
    if url.startswith(cfg.origin + "/"):
        split = urlsplit(url)
        target = resolve_site_file(root, split.path)
        if target is None:
            return ["%s: %s names no page or file of the site" % (where, url)]
        if target.endswith(".html") and "td-placeholder" in read_text(target):
            return ["%s: %s is a placeholder page; link it when the page is written" % (where, url)]
        return []
    if url == cfg.repo_url or url.startswith(cfg.repo_url + "/"):
        return []
    return ["%s: %s leaves the site and the repository; llms.txt links only to those" % (where, url)]


def check_404(root, cfg):
    path = os.path.join(root, "404.html")
    try:
        text = read_text(path)
    except OSError:
        return ["404.html: missing"]
    parsed = parse_page(text)
    out = []
    if not any("noindex" in [d.strip().lower() for d in c.split(",")] for c in parsed.robots):
        out.append("404.html: has no robots meta with noindex (a static host may serve the file under its own address with status 200)")
    if parsed.canonicals:
        out.append("404.html: has a canonical link; an error page has no original")
    if parsed.ld:
        out.append("404.html: has structured data")
    if parsed.h1 != 1:
        out.append("404.html: has %d h1 headings, expected exactly one" % parsed.h1)
    links = set(parsed.content_links)
    for target, name in (("/", "the home page"), ("/docs/", "the documentation"), ("/project/", "the project")):
        if not any(h == target or h == cfg.origin + target for h in links):
            out.append("404.html: the content has no link to %s (%s)" % (name, target))
    return out


def check_site(root, cfg, mode):
    """Runs every check. Returns (number of pages scanned, findings)."""
    findings = list(cfg.problems)
    indexing = load_sibling("check-indexing")
    base = build_base(cfg)
    paths = set()
    titles = {}
    scanned = 0
    for rel in list_pages(root):
        text = read_text(os.path.join(root, rel))
        scanned += 1
        if rel == "404.html" or indexing.is_redirect_stub(text):
            continue
        parsed = parse_page(text)
        page_findings, canonical = check_page(rel, parsed, cfg, base)
        findings.extend(page_findings)
        if len(parsed.titles) == 1 and parsed.titles[0].strip():
            titles.setdefault(" ".join(parsed.titles[0].split()), []).append(rel)
        if canonical:
            paths.add(page_path(rel))
    if not os.path.isfile(os.path.join(root, "index.html")):
        findings.append("index.html: missing")
    findings.extend(check_unique_titles(titles))
    findings.extend(check_lastmod(root))
    findings.extend(check_llms(root, cfg))
    findings.extend(check_404(root, cfg))
    findings.extend(check_robots(root, mode))
    if mode == "production" and base:
        findings.extend(check_sitemap(root, paths, base))
    return scanned, findings


def usage():
    print("usage: WEBSITE_ENV=production|preview python3 -I website/scripts/check-seo.py [--config-dir DIR] [--release FILE] [website/public]",
          file=sys.stderr)
    return 2


def main(argv):
    args = list(argv)
    config_dir, release = DEFAULT_CONFIG_DIR, DEFAULT_RELEASE
    for option in ("--config-dir", "--release"):
        if option in args:
            i = args.index(option)
            if i + 1 >= len(args):
                return usage()
            value = args[i + 1]
            del args[i:i + 2]
            if option == "--config-dir":
                config_dir = value
            else:
                release = value
    if len(args) > 1 or (args and args[0].startswith("-")):
        return usage()
    mode = os.environ.get("WEBSITE_ENV", "production") or "production"
    if mode not in MODES:
        print("check-seo: WEBSITE_ENV must be production or preview", file=sys.stderr)
        return 2
    root = args[0] if args else "website/public"
    if not os.path.isdir(root):
        print("check-seo: %s is not a directory (build the site first: make website)" % root, file=sys.stderr)
        return 2
    cfg = Config(config_dir, release)
    scanned, findings = check_site(root, cfg, mode)
    findings = sorted(set(findings))
    for line in findings:
        print(line)
    print("check-seo: mode %s, %d pages scanned, %d findings" % (mode, scanned, len(findings)))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
