#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Unit tests of check-seo.py (the website gate runs them with the other tests in this directory).

    python3 -I -m unittest discover -s website/scripts -p 'test_*.py'

Each test writes a small site, a configuration and a release file into a temporary directory, changes one thing that the check is
meant to catch (or nothing), runs the check and asserts the exit status and the output. Standard library only.
"""

import contextlib
import copy
import importlib.util
import io
import json
import os
import shutil
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))

ORIGIN = "https://example.test"
REPO = "https://git.example.test/myriarch/myriarch"
APACHE = "https://www.apache.org/licenses/LICENSE-2.0"
DESCRIPTION = "Fleet manager for OpenTelemetry Collectors, built on OpAMP."
HOME_TITLE = "Myriarch: GitOps fleet manager for OpenTelemetry Collectors"

HUGO = "baseURL: https://example.test/\ntitle: myriarch\nlocale: en\n"
PARAMS = """copyright: The Myriarch Authors
description: Fleet manager for OpenTelemetry Collectors, built on OpAMP.
repo:
  url: https://git.example.test/myriarch/myriarch
  branch: main
seo:
  canonicalBase: https://example.test/
  homeTitle: "Myriarch: GitOps fleet manager for OpenTelemetry Collectors"
  license: https://www.apache.org/licenses/LICENSE-2.0   # the license of the code
  programmingLanguage: [Go, TypeScript]
  keywords: [OpenTelemetry Collector, OpAMP, fleet management]
search:
  other: ignored
"""
RELEASE = '# release status\nfinal: "v0.1.0"\ncandidate: ""\n'

ORG = {"@type": "Organization", "name": "The Myriarch Authors", "url": ORIGIN + "/"}
GONE = object()  # in a mutation: remove the key


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def run_main(mod, argv):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = mod.main(argv)
    return rc, out.getvalue(), err.getvalue()


def mutate(data, **changes):
    """A copy of a JSON-LD node with keys replaced, or removed when the value is GONE."""
    out = copy.deepcopy(data)
    for key, value in changes.items():
        if value is GONE:
            out.pop(key, None)
        else:
            out[key] = value
    return out


def software(**changes):
    return mutate({
        "@context": "https://schema.org", "@type": "SoftwareSourceCode", "@id": ORIGIN + "/#software", "name": "myriarch",
        "description": DESCRIPTION, "url": ORIGIN + "/", "codeRepository": REPO, "license": APACHE,
        "programmingLanguage": ["Go", "TypeScript"], "keywords": "OpenTelemetry Collector, OpAMP, fleet management",
        "isAccessibleForFree": True, "author": ORG, "copyrightHolder": ORG, "version": "0.1.0",
    }, **changes)


def website(**changes):
    return mutate({
        "@context": "https://schema.org", "@type": "WebSite", "@id": ORIGIN + "/#website", "name": "myriarch",
        "description": DESCRIPTION, "url": ORIGIN + "/", "inLanguage": "en", "publisher": ORG,
    }, **changes)


def breadcrumbs(*items, **changes):
    """items: (name, path); the first item is the home page, the last the page itself."""
    return mutate({
        "@context": "https://schema.org", "@type": "BreadcrumbList",
        "itemListElement": [{"@type": "ListItem", "position": n, "name": name, "item": ORIGIN + path} for n, (name, path) in enumerate(items, 1)],
    }, **changes)


DOCS_TRAIL = (("Home", "/"), ("Documentation", "/docs/"), ("Concepts", "/docs/concepts/"), ("Git as the source of truth", "/docs/concepts/gitops/"))


def nav(*items, single=False):
    """The breadcrumb as Docsy draws it: items are (text, href), the last one has no link. single=True is the class that Docsy sets on a
    trail of one item (the pages at the first level), which its style sheet hides."""
    li = []
    for text, href in items:
        if href:
            li.append('<li class=td-breadcrumbs__item><a href="%s">%s</a></li>' % (href, text))
        else:
            li.append("<li class=td-breadcrumbs__item aria-current=page>%s</li>" % text)
    classes = "td-breadcrumbs td-breadcrumbs--single" if single else "td-breadcrumbs"
    return '<nav aria-label=breadcrumb class="%s"><ol class=td-breadcrumbs__list>%s</ol></nav>' % (classes, "".join(li))


def docs_index(canonical=ORIGIN + "/docs/"):
    """/docs/ as the build writes it: a trail of one item, which Docsy hides, and therefore no BreadcrumbList."""
    return page("Documentation | myriarch", canonical, body=nav(("Documentation", ""), single=True))


def page(title="A page | myriarch", canonical=None, ld=(), body="", description=DESCRIPTION, robots="index, follow", describedby="/llms.txt",
         lang="en", extra_head=""):
    head = '<meta charset=utf-8><meta name=robots content="%s">' % robots
    head += "<title>%s</title>" % title if title is not None else ""
    if description is not None:
        head += '<meta name=description content="%s">' % description
    if canonical is not None:
        head += '<link rel=canonical href="%s">' % canonical
    if describedby is not None:
        head += '<link rel=describedby href="%s">' % describedby
    for node in ld:
        head += '<script type="application/ld+json">%s</script>' % (node if isinstance(node, str) else json.dumps(node))
    return "<!doctype html><html lang=%s><head>%s%s</head><body>%s</body></html>" % (lang, head, extra_head, body)


LLMS = """# myriarch

> Myriarch is an open-source fleet manager for OpenTelemetry Collectors, built on OpAMP.

Status and license: the project is alpha. The server myriarchd renders the configuration. See myriarch.dev and the program myriarch-updater.

Key terms:

- **Blueprint**: A layered collector configuration that myriarch renders on the server.

## Start here

- [Documentation](https://example.test/docs/): The map of the documentation.
- [Git as the source of truth](https://example.test/docs/concepts/gitops/): How a Git repository describes the state.

## Optional

- [Source code](https://git.example.test/myriarch/myriarch): The repository.
- [Releases](https://git.example.test/myriarch/myriarch/releases): Release notes.
"""

NOT_FOUND_BODY = (
    "<div class=td-content><h1>Page not found</h1><p>Try one of these places:</p><ul>"
    '<li><a href="/">The home page</a></li><li><a href="/docs/">The documentation</a></li><li><a href="/project/">The project</a></li></ul></div>'
)

PROD_ROBOTS = "# Crawler policy: every crawler may read the whole site.\nUser-agent: *\nDisallow:\n\nSitemap: https://example.test/sitemap.xml\n"


class SeoCase(unittest.TestCase):
    mod = load("check-seo")

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="seo-check-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        env = mock.patch.dict(os.environ, {"WEBSITE_ENV": "production"})
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("WEBSITE_BASEURL", None)  # the tests set it where they need it; patch.dict restores the environment
        self.public = os.path.join(self.tmp, "public")
        self.config = os.path.join(self.tmp, "config")
        self.release = os.path.join(self.tmp, "release.yaml")
        self.files = {}  # path below public -> text
        self.hugo, self.params, self.release_text = HUGO, PARAMS, RELEASE
        self.reset_site()

    def reset_site(self):
        """A site that passes the check."""
        self.files = {
            "index.html": page(HOME_TITLE, ORIGIN + "/", [software(), website()]),
            "docs/index.html": docs_index(),
            "docs/concepts/gitops/index.html": page(
                "Git as the source of truth | myriarch", ORIGIN + "/docs/concepts/gitops/", [breadcrumbs(*DOCS_TRAIL)],
                body=nav(("Documentation", "/docs/"), ("Concepts", "/docs/concepts/"), ("Git as the source of truth", ""))),
            "legal/third-party/index.html": page("Third-party | myriarch", ORIGIN + "/legal/third-party/",
                                                 body="<img src=/a.png alt='The logo of the project'>"),
            "404.html": page("404 Page not found | myriarch", None, robots="noindex, nofollow", body=NOT_FOUND_BODY),
            "llms.txt": LLMS,
            "robots.txt": PROD_ROBOTS,
            "sitemap.xml": self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party/"]),
        }

    @staticmethod
    def sitemap(paths):
        return ('<?xml version="1.0" encoding="utf-8" standalone="yes"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">%s</urlset>'
                % "".join("<url><loc>%s%s</loc></url>" % (ORIGIN, p) for p in paths))

    def write_site(self):
        shutil.rmtree(self.public, ignore_errors=True)
        for rel, text in self.files.items():
            path = os.path.join(self.public, *rel.split("/"))
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(text)
        os.makedirs(self.config, exist_ok=True)
        for name, text in (("hugo.yaml", self.hugo), ("params.yaml", self.params)):
            with open(os.path.join(self.config, name), "w", encoding="utf-8") as fh:
                fh.write(text)
        with open(self.release, "w", encoding="utf-8") as fh:
            fh.write(self.release_text)

    def run_check(self, *extra):
        self.write_site()
        return run_main(self.mod, ["--config-dir", self.config, "--release", self.release] + list(extra) + [self.public])

    def assert_clean(self):
        rc, out, err = self.run_check()
        self.assertEqual(rc, 0, out + err)
        self.assertIn(" 0 findings", out)

    def assert_finding(self, *needles):
        """The check fails and its output contains every needle."""
        rc, out, _ = self.run_check()
        self.assertEqual(rc, 1, out)
        for needle in needles:
            self.assertIn(needle, out)
        return out


class CleanSiteTest(SeoCase):
    def test_a_production_site_passes(self):
        rc, out, err = self.run_check()
        self.assertEqual(rc, 0, out + err)
        self.assertIn("check-seo: mode production, 5 pages scanned, 0 findings", out)

    def test_a_preview_site_passes_with_the_same_canonical_links(self):
        os.environ["WEBSITE_ENV"] = "preview"
        # a preview is built with the base URL "/": its sitemap has root-relative locations, its robots.txt blocks everything
        self.files["sitemap.xml"] = '<urlset><url><loc>/docs/</loc></url></urlset>'
        self.files["robots.txt"] = "User-agent: *\nDisallow: /\n"
        for rel in list(self.files):
            if rel.endswith("index.html") or rel == "404.html":
                self.files[rel] = self.files[rel].replace('content="index, follow"', 'content="noindex, nofollow"')
        self.files["llms.txt"] = LLMS
        rc, out, err = self.run_check()
        self.assertEqual(rc, 0, out + err)
        self.assertIn("mode preview", out)

    def test_a_preview_still_needs_the_production_canonical_links(self):
        os.environ["WEBSITE_ENV"] = "preview"
        self.files["docs/index.html"] = self.files["docs/index.html"].replace(ORIGIN + "/docs/", "/docs/", 1)
        out = self.assert_finding("docs/index.html: the canonical link is '/docs/', expected 'https://example.test/docs/'")
        self.assertNotIn("sitemap", out)

    def test_a_release_candidate_is_the_version_when_there_is_no_final_release(self):
        self.release_text = 'final: ""\ncandidate: "v0.2.0-rc.1"\n'
        self.files["index.html"] = page(HOME_TITLE, ORIGIN + "/", [software(version="0.2.0-rc.1"), website()])
        self.assert_clean()

    def test_without_a_release_the_home_page_names_no_version(self):
        self.release_text = 'final: ""\ncandidate: ""\n'
        self.files["index.html"] = page(HOME_TITLE, ORIGIN + "/", [software(version=GONE), website()])
        self.assert_clean()
        self.files["index.html"] = page(HOME_TITLE, ORIGIN + "/", [software(version="0.1.0"), website()])
        self.assert_finding("names the version '0.1.0', but data/release.yaml names no release")

    def test_a_redirect_stub_is_not_a_page(self):
        self.files["old/index.html"] = (
            '<!doctype html><html><head><title>/docs/</title><link rel=canonical href="%s/docs/"><meta name=robots content="noindex">'
            '<meta http-equiv=refresh content="0; url=%s/docs/"></head></html>' % (ORIGIN, ORIGIN))
        self.assert_clean()


class CanonicalTest(SeoCase):
    def test_a_missing_canonical_link_fails(self):
        self.files["docs/index.html"] = docs_index(None)
        self.assert_finding("docs/index.html: has 0 canonical links, expected exactly one")

    def test_two_canonical_links_fail(self):
        self.files["docs/index.html"] = self.files["docs/index.html"].replace(
            "</head>", '<link rel=canonical href="%s/docs/"></head>' % ORIGIN)
        self.assert_finding("docs/index.html: has 2 canonical links")

    def test_a_canonical_link_must_be_absolute_and_on_the_production_origin(self):
        for href in ("/docs/", "http://example.test/docs/", "https://other.test/docs/", "https://example.test/docs/?x=1", "https://example.test/docs/#a",
                     "https://example.test/docs", "https://example.test/", "https://example.test/Docs/"):
            self.files["docs/index.html"] = docs_index(href)
            self.assert_finding("docs/index.html: the canonical link is %r, expected 'https://example.test/docs/'" % href)

    def test_the_canonical_link_of_a_page_names_the_page_itself(self):
        # a canonical link to the home page from a page below it would hand the page over to the home page
        self.files["legal/third-party/index.html"] = page("Third-party | myriarch", ORIGIN + "/", body="")
        self.assert_finding("legal/third-party/index.html: the canonical link is 'https://example.test/', expected 'https://example.test/legal/third-party/'")

    def test_the_canonical_origin_must_equal_the_base_url_of_the_site(self):
        self.params = self.params.replace("canonicalBase: https://example.test/", "canonicalBase: https://www.example.test/")
        self.assert_finding("params.yaml: seo.canonicalBase (https://www.example.test/) differs from baseURL of hugo.yaml (https://example.test/)")

    def test_the_canonical_origin_must_be_https(self):
        self.params = self.params.replace("canonicalBase: https://example.test/", "canonicalBase: http://example.test/")
        self.assert_finding("params.yaml: seo.canonicalBase must be an absolute https URL")

    def test_a_page_that_the_check_cannot_map_to_a_url_fails(self):
        self.files["verification.html"] = page("Verification | myriarch", None)
        self.assert_finding("verification.html: is a page whose URL the check cannot tell")


class PageBasicsTest(SeoCase):
    def test_a_page_needs_one_title_and_one_description(self):
        self.files["legal/third-party/index.html"] = page(None, ORIGIN + "/legal/third-party/")
        self.assert_finding("legal/third-party/index.html: has 0 titles, expected exactly one that is not empty")
        self.reset_site()
        self.files["legal/third-party/index.html"] = page("   ", ORIGIN + "/legal/third-party/")
        self.assert_finding("legal/third-party/index.html: has 1 titles")
        self.reset_site()
        self.files["legal/third-party/index.html"] = page("T | myriarch", ORIGIN + "/legal/third-party/", description=None)
        self.assert_finding("legal/third-party/index.html: has 0 descriptions")
        self.reset_site()
        self.files["legal/third-party/index.html"] = page("T | myriarch", ORIGIN + "/legal/third-party/", description="  ")
        self.assert_finding("legal/third-party/index.html: has 1 descriptions")

    def test_the_title_of_an_inline_svg_is_no_document_title(self):
        self.files["legal/third-party/index.html"] = page(
            "Third-party | myriarch", ORIGIN + "/legal/third-party/", body="<svg role=img><title id=t>A diagram</title></svg>")
        self.assert_clean()

    def test_every_page_links_to_llms_txt(self):
        self.files["legal/third-party/index.html"] = page("T | myriarch", ORIGIN + "/legal/third-party/", describedby=None)
        self.assert_finding("legal/third-party/index.html: has 0 links rel=describedby, expected exactly one to /llms.txt")
        for href in ("/other.txt", "https://other.test/llms.txt"):
            self.files["legal/third-party/index.html"] = page("T | myriarch", ORIGIN + "/legal/third-party/", describedby=href)
            self.assert_finding("the link rel=describedby names %r, expected /llms.txt" % href)
        self.files["legal/third-party/index.html"] = page("T | myriarch", ORIGIN + "/legal/third-party/", describedby=ORIGIN + "/llms.txt")
        self.assert_clean()

    def test_images_need_alt_text(self):
        def body(img):
            self.files["legal/third-party/index.html"] = page("T | myriarch", ORIGIN + "/legal/third-party/", body=img)

        body("<img src=/a.png>")
        self.assert_finding("the image /a.png has no alt attribute")
        body("<img src=/a.png alt=>")
        self.assert_finding("the image /a.png has an empty alt text and is not marked as decoration")
        body("<img src=/a.png alt='logo.png'>")
        self.assert_finding("the alt text of the image /a.png is a file name")
        for decorative in ("<img src=/a.png alt= role=presentation>", "<img src=/a.png alt= aria-hidden=true>", "<img src=/a.png alt='An overview'>"):
            body(decorative)
            self.assert_clean()


class StructuredDataTest(SeoCase):
    def page_with(self, rel, *nodes, canonical=None, title="Page | myriarch", body=""):
        self.files[rel] = page(title, canonical or ORIGIN + "/" + rel[: -len("index.html")], list(nodes), body=body)

    def home(self, *nodes):
        self.files["index.html"] = page(HOME_TITLE, ORIGIN + "/", list(nodes))

    def test_invalid_json_fails(self):
        self.home(software(), website(), '{"@context": "https://schema.org", "@type": ')
        self.assert_finding("index.html: JSON-LD is not valid JSON")

    def test_a_raw_angle_bracket_in_the_json_fails(self):
        # jsonify escapes < and >; a value that reached the page unescaped could end the script element
        self.home(software(description="1 < 2 and 3 > 2"), website())
        self.assert_finding("index.html: JSON-LD contains a raw < or >")
        # a value that does end the script element also leaves the JSON broken
        self.home(software(description="</script><script>alert(1)</script>"), website())
        self.assert_finding("index.html: JSON-LD is not valid JSON")
        # the escaped form is what jsonify writes: valid, and no finding about the brackets (the description differs from the configuration)
        escaped = json.dumps(software(description="1 < 2")).replace("<", "\\u003c").replace(">", "\\u003e")
        self.home(escaped, website())
        out = self.assert_finding("has description")
        self.assertNotIn("raw < or >", out)

    def test_the_context_must_be_schema_org(self):
        self.home(software(**{"@context": "http://schema.org"}), website())
        self.assert_finding("index.html: JSON-LD @context must be https://schema.org")

    def test_an_array_instead_of_an_object_fails(self):
        self.home('[%s]' % json.dumps(software()), website())
        self.assert_finding("index.html: JSON-LD must be one object per script element")

    def test_a_type_the_site_does_not_use_fails(self):
        self.home(software(), website(), mutate(website(), **{"@type": "SoftwareApplication"}))
        self.assert_finding("index.html: JSON-LD has the type 'SoftwareApplication', which the site does not use")

    def test_the_home_page_has_exactly_one_software_and_one_website(self):
        self.home(software())
        self.assert_finding("index.html: JSON-LD has the types ['SoftwareSourceCode'], expected ['SoftwareSourceCode', 'WebSite']")
        self.home(software(), website(), website())
        self.assert_finding("expected ['SoftwareSourceCode', 'WebSite']")
        self.home()
        self.assert_finding("index.html: JSON-LD has the types none, expected ['SoftwareSourceCode', 'WebSite']")

    def test_a_property_that_the_type_does_not_define_fails(self):
        # softwareVersion is defined for SoftwareApplication, not for SoftwareSourceCode
        self.home(software(softwareVersion="0.1.0"), website())
        self.assert_finding("index.html: JSON-LD SoftwareSourceCode has the property softwareVersion, which SoftwareSourceCode does not define")
        self.home(software(), website(potentialAction={"@type": "SearchAction"}))
        self.assert_finding("JSON-LD WebSite has the property potentialAction")

    def test_the_facts_of_the_software_node_come_from_the_configuration(self):
        for key, value, label in (
            ("name", "other", "name"), ("description", "Another text.", "description"), ("codeRepository", "https://other.test/x", "codeRepository"),
            ("license", "https://example.test/license", "license"), ("programmingLanguage", ["Go"], "programmingLanguage"),
            ("keywords", "gitops", "keywords"), ("url", ORIGIN + "/docs/", "url"), ("@id", ORIGIN + "/#sw", "@id"),
            ("version", "0.0.9", "version"), ("isAccessibleForFree", False, "isAccessibleForFree"),
        ):
            self.home(software(**{key: value}), website())
            self.assert_finding("index.html: JSON-LD SoftwareSourceCode has %s %r" % (label, value))

    def test_the_required_values_of_the_software_node_must_not_be_empty(self):
        for key in ("name", "codeRepository", "license", "programmingLanguage", "description"):
            self.home(software(**{key: GONE}), website())
            self.assert_finding("index.html: JSON-LD SoftwareSourceCode lacks %s" % key)
        self.home(software(version=GONE), website())
        self.assert_finding("has version None, expected '0.1.0'")

    def test_the_organization_is_the_copyright_holder(self):
        self.home(software(author=mutate(ORG, name="Somebody")), website())
        self.assert_finding("JSON-LD SoftwareSourceCode.author has the name 'Somebody', expected 'The Myriarch Authors'")
        self.home(software(), website(publisher=mutate(ORG, url="https://other.test/")))
        self.assert_finding("JSON-LD WebSite.publisher has the url 'https://other.test/'")
        self.home(software(copyrightHolder=mutate(ORG, email="x@example.test")), website())
        self.assert_finding("JSON-LD SoftwareSourceCode.copyrightHolder has the property email")

    def test_the_website_node_comes_from_the_configuration(self):
        for key, value in (("name", "other"), ("url", "https://example.test"), ("inLanguage", "de"), ("@id", "x"), ("description", "Another text.")):
            self.home(software(), website(**{key: value}))
            self.assert_finding("JSON-LD WebSite has %s %r" % (key, value))
        self.home(software(), website(publisher=GONE))
        self.assert_finding("index.html: JSON-LD WebSite lacks publisher")

    def test_only_the_home_page_carries_software_and_website(self):
        self.page_with("legal/third-party/index.html", software(), website())
        self.assert_finding("legal/third-party/index.html: JSON-LD has the types ['SoftwareSourceCode', 'WebSite'], expected none")

    def test_a_page_with_a_breadcrumb_has_exactly_one_matching_breadcrumb_list(self):
        trail = (("Documentation", "/docs/"), ("Concepts", "/docs/concepts/"), ("Git as the source of truth", ""))
        rel = "docs/concepts/gitops/index.html"
        self.page_with(rel, breadcrumbs(*DOCS_TRAIL), body=nav(*trail))
        self.assert_clean()
        # no structured data although the breadcrumb is visible
        self.page_with(rel, body=nav(*trail))
        self.assert_finding("%s: JSON-LD has the types none, expected ['BreadcrumbList']" % rel)
        # structured data although there is no visible breadcrumb
        self.page_with(rel, breadcrumbs(*DOCS_TRAIL), body="")
        self.assert_finding("%s: JSON-LD has the types ['BreadcrumbList'], expected none" % rel)
        # two lists
        self.page_with(rel, breadcrumbs(*DOCS_TRAIL), breadcrumbs(*DOCS_TRAIL), body=nav(*trail))
        self.assert_finding("expected ['BreadcrumbList']")

    def test_the_breadcrumb_list_must_equal_the_visible_breadcrumb(self):
        trail = (("Documentation", "/docs/"), ("Concepts", "/docs/concepts/"), ("Git as the source of truth", ""))
        rel = "docs/concepts/gitops/index.html"
        cases = (
            (breadcrumbs(*DOCS_TRAIL[:3]), "has 3 items, the visible breadcrumb has 4 (with the home page)"),
            (breadcrumbs(("Start", "/"), *DOCS_TRAIL[1:]), "BreadcrumbList item 1 is named 'Start', the visible breadcrumb says 'Home'"),
            (breadcrumbs(DOCS_TRAIL[0], DOCS_TRAIL[1], ("Ideas", "/docs/concepts/"), DOCS_TRAIL[3]),
             "BreadcrumbList item 3 is named 'Ideas', the visible breadcrumb says 'Concepts'"),
            (breadcrumbs(DOCS_TRAIL[0], DOCS_TRAIL[1], DOCS_TRAIL[2], ("Git as the source of truth", "/docs/concepts/other/")),
             "BreadcrumbList item 4 has the URL 'https://example.test/docs/concepts/other/', expected 'https://example.test/docs/concepts/gitops/'"),
            (breadcrumbs(DOCS_TRAIL[0], DOCS_TRAIL[1], ("Concepts", "/docs/other/"), DOCS_TRAIL[3]),
             "BreadcrumbList item 3 has the URL 'https://example.test/docs/other/', expected 'https://example.test/docs/concepts/'"),
        )
        for node, needle in cases:
            self.page_with(rel, node, body=nav(*trail))
            self.assert_finding(needle)

    def test_the_positions_and_the_shape_of_the_items_are_checked(self):
        trail = (("Documentation", "/docs/"), ("Concepts", "/docs/concepts/"), ("Git as the source of truth", ""))
        rel = "docs/concepts/gitops/index.html"
        node = breadcrumbs(*DOCS_TRAIL)
        node["itemListElement"][1]["position"] = 5
        self.page_with(rel, node, body=nav(*trail))
        self.assert_finding("BreadcrumbList item 2 has the position 5")
        node = breadcrumbs(*DOCS_TRAIL)
        node["itemListElement"][2]["itemListElement"] = []
        self.page_with(rel, node, body=nav(*trail))
        self.assert_finding("BreadcrumbList item 3 has the property itemListElement, which ListItem does not define")
        node = breadcrumbs(*DOCS_TRAIL)
        del node["itemListElement"][0]["item"]
        self.page_with(rel, node, body=nav(*trail))
        self.assert_finding("BreadcrumbList item 1 lacks item")
        self.page_with(rel, breadcrumbs(*DOCS_TRAIL, itemListElement=[]), body=nav(*trail))
        self.assert_finding("JSON-LD BreadcrumbList lacks itemListElement")

    def test_a_first_level_page_has_no_breadcrumb_list(self):
        # Docsy hides a trail of one item (td-breadcrumbs--single) with its style sheet: the page shows no breadcrumb, so a list would
        # equal nothing
        rel = "docs/index.html"
        self.assert_clean()  # docs/index.html: the hidden breadcrumb and no JSON-LD
        for trail in (DOCS_TRAIL[:2], DOCS_TRAIL[1:2]):
            self.files[rel] = page("Documentation | myriarch", ORIGIN + "/docs/", [breadcrumbs(*trail)], body=nav(("Documentation", ""), single=True))
            self.assert_finding("%s: JSON-LD has the types ['BreadcrumbList'], expected none "
                                "(Docsy hides the breadcrumb of this page: td-breadcrumbs--single)" % rel)

    def test_the_class_of_the_navigation_decides_whether_the_breadcrumb_is_visible(self):
        # the same page without the class would show its breadcrumb, so it would need the list
        rel = "docs/index.html"
        self.files[rel] = page("Documentation | myriarch", ORIGIN + "/docs/", body=nav(("Documentation", "")))
        self.assert_finding("%s: JSON-LD has the types none, expected ['BreadcrumbList']" % rel)


class HomeTitleTest(SeoCase):
    def test_the_home_title_is_the_title_of_the_configuration(self):
        self.files["index.html"] = page("myriarch", ORIGIN + "/", [software(), website()])
        self.assert_finding("index.html: the title of the home page is 'myriarch', params.seo.homeTitle says '%s'" % HOME_TITLE)

    def test_the_configured_title_must_say_what_the_project_is(self):
        self.params = PARAMS.replace('homeTitle: "%s"' % HOME_TITLE, "homeTitle: myriarch")
        self.files["index.html"] = page("myriarch", ORIGIN + "/", [software(), website()])
        self.assert_finding("params.yaml: seo.homeTitle must say what the project is (more than the site title 'myriarch') in at most 70 characters")
        long_title = "Myriarch: " + "x" * 70
        self.params = PARAMS.replace('homeTitle: "%s"' % HOME_TITLE, 'homeTitle: "%s"' % long_title)
        self.files["index.html"] = page(long_title, ORIGIN + "/", [software(), website()])
        self.assert_finding("in at most 70 characters")

    def test_the_home_title_follows_the_spelling_rule(self):
        bad = "myriarch: GitOps fleet manager for OpenTelemetry Collectors"
        self.params = PARAMS.replace('homeTitle: "%s"' % HOME_TITLE, 'homeTitle: "%s"' % bad)
        self.files["index.html"] = page(bad, ORIGIN + "/", [software(), website()])
        self.assert_finding("index.html <title>:1: the name starts a sentence, write 'Myriarch' (ADR-0015)",
                            "params.yaml seo.homeTitle:1: the name starts a sentence, write 'Myriarch' (ADR-0015)")

    def test_other_titles_are_not_linted_for_the_spelling(self):
        self.files["legal/third-party/index.html"] = page("What is Myriarch | myriarch", ORIGIN + "/legal/third-party/")
        self.assert_clean()


class LlmsTest(SeoCase):
    def llms(self, text):
        self.files["llms.txt"] = text

    def test_a_missing_llms_txt_fails(self):
        del self.files["llms.txt"]
        self.assert_finding("llms.txt: missing")

    def test_the_shape_of_the_file(self):
        self.llms(LLMS.replace("# myriarch\n", "# Something else\n", 1))
        self.assert_finding("llms.txt: the first line must be the heading '# myriarch' (the title of the site)")
        self.llms(LLMS.replace("> Myriarch is an open-source fleet manager for OpenTelemetry Collectors, built on OpAMP.\n", "Myriarch is a fleet manager.\n"))
        self.assert_finding("the heading must be followed by the summary in a block quote")
        self.llms(LLMS + "\n# Another heading\n")
        self.assert_finding("llms.txt: has 2 level-1 headings, expected exactly one")
        self.llms(LLMS.replace("Key terms:", "<h2>Key terms</h2>"))
        self.assert_finding("llms.txt: contains an HTML tag")
        self.llms(LLMS + "\n")
        self.assert_finding("llms.txt: must end with exactly one line break")
        self.llms(LLMS.rstrip("\n"))
        self.assert_finding("llms.txt: must end with exactly one line break")
        self.llms("")
        self.assert_finding("llms.txt: the first line must be the heading")
        self.llms(LLMS + "x" * 70000 + "\n")
        self.assert_finding("llms.txt: 7")

    def test_links_go_to_pages_of_the_site_or_the_repository(self):
        self.llms(LLMS + "- [Elsewhere](https://other.test/page/): A page of another host.\n")
        self.assert_finding("https://other.test/page/ leaves the site and the repository")
        self.llms(LLMS + "- [Look-alike](https://example.test.other.test/docs/): A page of another host.\n")
        self.assert_finding("https://example.test.other.test/docs/ leaves the site and the repository")
        self.llms(LLMS + "- [Other repository](https://git.example.test/myriarch/other): Another repository.\n")
        self.assert_finding("https://git.example.test/myriarch/other leaves the site and the repository")
        self.llms(LLMS + "- [Relative](/docs/): A relative link.\n")
        self.assert_finding("/docs/ leaves the site and the repository")
        self.llms(LLMS + "- [Anchor](https://example.test/docs/#section): An anchor of an existing page.\n")
        self.assert_clean()

    def test_a_link_to_a_page_that_does_not_exist_fails(self):
        self.llms(LLMS + "- [Gone](https://example.test/docs/gone/): A page that was removed.\n")
        self.assert_finding("https://example.test/docs/gone/ names no page or file of the site")
        self.llms(LLMS + "- [Escape](https://example.test/docs/../../etc/passwd): A path outside of the site.\n")
        self.assert_finding("names no page or file of the site")

    def test_a_link_to_a_placeholder_page_fails(self):
        self.files["docs/planned/index.html"] = page("Planned | myriarch", ORIGIN + "/docs/planned/",
                                                     body='<div class="alert td-placeholder" role=note>This page is being written.</div>')
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party/", "/docs/planned/"])
        self.llms(LLMS + "- [Planned](https://example.test/docs/planned/): Not written yet.\n")
        self.assert_finding("https://example.test/docs/planned/ is a placeholder page")

    def test_a_list_item_below_a_heading_must_be_a_file_link(self):
        # llmstxt.org: free-form content, lists included, stands before the first level-2 heading; the sections below hold lists of links
        # (the key terms were a section of their own once, which strict readers drop)
        self.llms(LLMS.replace("Key terms:\n", "## Key terms\n"))
        self.assert_finding("llms.txt:9: a list item below a level-2 heading must be a link of the form '- [name](url)'")
        link = "https://example.test/docs/#a"
        for item in ("- **Blueprint**: A layered collector configuration.", "* [Documentation](%s): A star as the marker." % link,
                     "  - [Documentation](%s): An indented item." % link, "1. [Documentation](%s): A numbered item." % link,
                     "- [Documentation](%s) without the colon." % link, "- [Documentation](%s):" % link,
                     "- Documentation: [the map](%s)" % link):
            self.llms(LLMS + item + "\n")
            out = self.assert_finding("a list item below a level-2 heading must be a link of the form '- [name](url)'")
            self.assertEqual(out.count("a list item below"), 1, item)
        # the two forms of the format, with and without notes, and a list before the first heading
        for item in ("- [Documentation](%s)" % link, "- [Documentation](%s): The notes may have a colon: here." % link):
            self.llms(LLMS + item + "\n")
            self.assert_clean()
        self.llms(LLMS.replace("Key terms:\n", "Key terms:\n\n- A term without a definition.\n- [A link](%s): As a free-form item." % link))
        self.assert_clean()

    def test_a_link_appears_once_and_has_a_text(self):
        self.llms(LLMS + "- [Documentation again](https://example.test/docs/): The same page.\n")
        self.assert_finding("the link https://example.test/docs/ appears twice")
        self.llms(LLMS + "- [](https://example.test/docs/concepts/gitops/#a): No text.\n")
        self.assert_finding("a link without text")

    def test_a_file_without_a_link_fails(self):
        self.llms("# myriarch\n\n> Myriarch is a fleet manager.\n")
        self.assert_finding("llms.txt: has no link")

    def test_the_spelling_rule_of_the_name(self):
        self.llms(LLMS.replace("that myriarch renders", "that Myriarch renders"))
        self.assert_finding("llms.txt:9: the name is not at the start of a sentence, write 'myriarch' (ADR-0015)")
        self.llms(LLMS.replace("Status and license: the project is alpha.", "Status and license: the project is alpha. myriarch is independent."))
        self.assert_finding("the name starts a sentence, write 'Myriarch' (ADR-0015)")

    def test_identifiers_are_not_part_of_the_spelling_rule(self):
        # in the free-form part, before the first heading, where lists and quotes may stand
        text = LLMS.replace("## Start here", (
            "Use `myriarch` in code, the command myriarch-updater, the host myriarch.dev, the path ghcr.io/myriarch/myriarch and dev.myriarch.name.\n"
            "A new sentence. Myriarch starts it, and the (myriarch) in brackets and \"myriarch\" in quotes do not.\n"
            "- **Myriarch** starts a list item.\n> Myriarch starts a quote.\n\n## Start here"), 1)
        self.llms(text)
        self.assert_clean()


class NotFoundPageTest(SeoCase):
    def test_a_missing_404_page_fails(self):
        del self.files["404.html"]
        self.assert_finding("404.html: missing")

    def test_the_404_page_must_not_be_indexable_in_either_mode(self):
        self.files["404.html"] = page("404 | myriarch", None, robots="index, follow", body=NOT_FOUND_BODY)
        self.assert_finding("404.html: has no robots meta with noindex")
        os.environ["WEBSITE_ENV"] = "preview"
        self.assert_finding("404.html: has no robots meta with noindex")

    def test_the_404_page_has_neither_a_canonical_link_nor_structured_data(self):
        self.files["404.html"] = page("404 | myriarch", ORIGIN + "/404.html", [website()], robots="noindex, nofollow", body=NOT_FOUND_BODY)
        self.assert_finding("404.html: has a canonical link; an error page has no original", "404.html: has structured data")

    def test_the_404_page_has_one_heading_and_ways_back_in_its_content(self):
        self.files["404.html"] = page("404 | myriarch", None, robots="noindex", body=NOT_FOUND_BODY.replace("<h1>Page not found</h1>", ""))
        self.assert_finding("404.html: has 0 h1 headings, expected exactly one")
        # the navigation bar is not content: links in it do not count
        nav_bar = '<nav><a href="/">myriarch</a><a href="/docs/">Documentation</a><a href="/project/">Project</a></nav>'
        self.files["404.html"] = page("404 | myriarch", None, robots="noindex", body=nav_bar + "<div class=td-content><h1>Page not found</h1></div>")
        self.assert_finding("404.html: the content has no link to the home page (/)",
                            "404.html: the content has no link to the documentation (/docs/)",
                            "404.html: the content has no link to the project (/project/)")
        self.files["404.html"] = page("404 | myriarch", None, robots="noindex", body=NOT_FOUND_BODY.replace('<li><a href="/docs/">The documentation</a></li>', ""))
        self.assert_finding("404.html: the content has no link to the documentation (/docs/)")

    def test_nested_divs_in_the_content_are_followed(self):
        body = ('<div class=td-content><h1>Page not found</h1><div class="d-lg-none my-4"><div></div></div>'
                '<a href="/">Home</a><a href="/docs/">Docs</a><a href="/project/">Project</a></div><a href="/elsewhere/">Outside</a>')
        self.files["404.html"] = page("404 | myriarch", None, robots="noindex", body=body)
        self.assert_clean()


class RobotsAndSitemapTest(SeoCase):
    def test_production_robots_txt_must_not_block_a_crawler(self):
        self.files["robots.txt"] = PROD_ROBOTS + "\nUser-agent: ExampleBot\nDisallow: /\n"
        self.assert_finding("robots.txt:8: production blocks /; the policy is that every crawler may read the site")
        self.files["robots.txt"] = PROD_ROBOTS + "\nUser-agent: ExampleBot\nDisallow: /private/ # not public\n"
        self.assert_finding("robots.txt:8: production blocks /private/")
        # an empty Disallow, an Allow rule and a commented rule block nothing
        self.files["robots.txt"] = PROD_ROBOTS + "\nUser-agent: ExampleBot\nAllow: /\n# Disallow: /old/\n"
        self.assert_clean()

    def test_a_missing_robots_txt_fails_in_production(self):
        del self.files["robots.txt"]
        self.assert_finding("robots.txt: missing or unreadable")

    def test_the_robots_policy_is_not_checked_in_a_preview(self):
        os.environ["WEBSITE_ENV"] = "preview"
        self.files["robots.txt"] = "User-agent: *\nDisallow: /\n"
        for rel in list(self.files):
            if rel.endswith("index.html"):
                self.files[rel] = self.files[rel].replace('content="index, follow"', 'content="noindex, nofollow"')
        self.assert_clean()

    def test_the_sitemap_and_the_canonical_links_must_agree(self):
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/"])
        self.assert_finding("sitemap.xml: does not list https://example.test/legal/third-party/")
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party/", "/404.html"])
        self.assert_finding("sitemap.xml: lists https://example.test/404.html, which is the URL of no page")
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party/", "/docs/"])
        self.assert_finding("sitemap.xml: lists a URL more than once")
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party"])
        self.assert_finding("sitemap.xml: lists https://example.test/legal/third-party, which is the URL of no page",
                            "sitemap.xml: does not list https://example.test/legal/third-party/")
        del self.files["sitemap.xml"]
        self.assert_finding("sitemap.xml: missing or unreadable")


class BuildForAnotherHostTest(SeoCase):
    """WEBSITE_BASEURL builds the site for another host (ci/website.sh): the canonical links stay on the production origin."""

    HOST = "https://preview.example.test"

    def test_the_sitemap_and_the_llms_link_follow_the_base_url_of_the_build(self):
        os.environ["WEBSITE_BASEURL"] = self.HOST + "/"
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party/"]).replace(ORIGIN, self.HOST)
        for rel in list(self.files):
            if rel.endswith("index.html") or rel == "404.html":
                self.files[rel] = self.files[rel].replace('href="/llms.txt"', 'href="%s/llms.txt"' % self.HOST)
        self.assert_clean()

    def test_a_sitemap_below_the_production_origin_is_wrong_for_such_a_build(self):
        os.environ["WEBSITE_BASEURL"] = self.HOST + "/"
        out = self.assert_finding("sitemap.xml: does not list https://preview.example.test/docs/",
                                  "sitemap.xml: lists https://example.test/docs/, which is the URL of no page")
        self.assertNotIn("canonical link", out)

    def test_a_describedby_link_to_a_third_host_fails(self):
        os.environ["WEBSITE_BASEURL"] = self.HOST + "/"
        self.files["docs/index.html"] = self.files["docs/index.html"].replace('href="/llms.txt"', 'href="https://other.test/llms.txt"')
        self.files["sitemap.xml"] = self.sitemap(["/", "/docs/", "/docs/concepts/gitops/", "/legal/third-party/"]).replace(ORIGIN, self.HOST)
        self.assert_finding("docs/index.html: the link rel=describedby names 'https://other.test/llms.txt'")

    def test_a_relative_base_url_has_no_sitemap_to_compare(self):
        os.environ["WEBSITE_BASEURL"] = "/"
        self.files["sitemap.xml"] = '<urlset><url><loc>/docs/</loc></url></urlset>'
        self.assert_clean()


class ConfigurationTest(SeoCase):
    def test_missing_facts_fail(self):
        for needle, key in (("seo.homeTitle is missing", "homeTitle"), ("seo.license is missing", "license"),
                            ("seo.programmingLanguage is missing", "programmingLanguage"), ("seo.keywords is missing", "keywords")):
            self.params = "\n".join(line for line in PARAMS.splitlines() if not line.strip().startswith(key + ":")) + "\n"
            self.assert_finding("params.yaml: " + needle)

    def test_the_license_must_be_an_apache_url(self):
        self.params = PARAMS.replace("LICENSE-2.0   #", "LICENSE-1.1   #").replace("LICENSE-2.0", "LICENSE-1.1")
        self.assert_finding("params.yaml: seo.license must be one of https://www.apache.org/licenses/LICENSE-2.0, https://spdx.org/licenses/Apache-2.0.html")

    def test_unreadable_configuration_files_are_findings(self):
        self.write_site()
        os.remove(os.path.join(self.config, "params.yaml"))
        rc, out, _ = run_main(self.mod, ["--config-dir", self.config, "--release", self.release, self.public])
        self.assertEqual(rc, 1)
        self.assertIn("params.yaml: cannot be read", out)

    def test_a_malformed_release_file_is_a_finding(self):
        self.release_text = 'final: "v0.1"\ncandidate: ""\n'
        self.assert_finding("final 'v0.1' is not a tag of the form vX.Y.Z")

    def test_the_configuration_of_the_site_is_valid(self):
        cfg = self.mod.Config(self.mod.DEFAULT_CONFIG_DIR, self.mod.DEFAULT_RELEASE)
        self.assertEqual(cfg.problems, [])
        self.assertEqual(cfg.canonical_base.rstrip("/"), cfg.base_url.rstrip("/"))
        self.assertTrue(cfg.home_title.startswith("Myriarch"), cfg.home_title)
        self.assertLessEqual(len(cfg.home_title), self.mod.HOME_TITLE_MAX)
        self.assertEqual(self.mod.spelling_problems(cfg.home_title, "home title"), [])


class ParsingTest(unittest.TestCase):
    mod = load("check-seo")

    def test_read_keys_reads_the_top_level_and_one_block(self):
        text = "# comment\nbaseURL: https://x.test/   # inline\ntitle: \"a # b\"\nempty:\nseo:\n  one: 1\n  two: [a, 'b, c', \"d\"]\n  # note\n  three: x # y\nnext:\n  one: wrong\n"
        top = self.mod.read_keys(text)
        self.assertEqual(top["baseURL"], "https://x.test/")
        self.assertEqual(top["title"], "a # b")
        self.assertNotIn("empty", top)
        seo = self.mod.read_keys(text, "seo")
        self.assertEqual(seo, {"one": "1", "two": ["a", "b, c", "d"], "three": "x"})
        self.assertEqual(self.mod.read_keys(text, "absent"), {})

    def test_page_path(self):
        page_path = self.mod.page_path
        self.assertEqual(page_path("index.html"), "/")
        self.assertEqual(page_path("docs/index.html"), "/docs/")
        self.assertEqual(page_path("docs/a/b/index.html"), "/docs/a/b/")
        self.assertIsNone(page_path("404.html"))
        self.assertIsNone(page_path("docs/page.html"))

    def test_spelling_problems(self):
        lint = self.mod.spelling_problems
        self.assertEqual(lint("Myriarch is a tool. It is called myriarch.", "t"), [])
        self.assertEqual(lint("# Myriarch\n\n- myriarch-updater and `myriarch` and myriarch.dev\n", "t"), [])
        self.assertEqual(len(lint("It is called Myriarch.", "t")), 1)
        self.assertEqual(len(lint("myriarch is a tool.", "t")), 1)
        self.assertEqual(len(lint("Fine. myriarch starts here.", "t")), 1)
        self.assertEqual(len(lint("What? Myriarch answers. And Myriarch again", "t")), 1)
        self.assertEqual(lint("line one\nline two Myriarch", "x"), ["x:2: the name is not at the start of a sentence, write 'myriarch' (ADR-0015)"])


class UsageTest(SeoCase):
    def test_usage_errors(self):
        for argv in (["--bogus"], ["a", "b"], ["--config-dir"], ["--release"]):
            rc, _, err = run_main(self.mod, argv)
            self.assertEqual(rc, 2, argv)
            self.assertIn("usage:", err)

    def test_a_missing_site_directory_is_an_environment_error(self):
        rc, _, err = run_main(self.mod, [self.path("does-not-exist")])
        self.assertEqual(rc, 2)
        self.assertIn("not a directory", err)

    def test_an_unknown_environment_is_refused(self):
        os.environ["WEBSITE_ENV"] = "staging"
        rc, _, err = run_main(self.mod, [self.tmp])
        self.assertEqual(rc, 2)
        self.assertIn("production or preview", err)

    def path(self, *parts):
        return os.path.join(self.tmp, *parts)


if __name__ == "__main__":
    unittest.main()
