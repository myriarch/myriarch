#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Unit tests of check-api-spec.py (ADR-0031): the generated API reference against the committed contract.

    python3 -I -m unittest discover -s website/scripts -p 'test_*.py'

Every test writes a small built site and a small repository into a temporary directory and asserts the findings of check(). Standard
library only.
"""

import contextlib
import importlib.util
import io
import os
import shutil
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))

SPEC_TEXT = (
    "openapi: 3.1.0\ninfo:\n  title: Test\n  version: 1.0.0\npaths:\n"
    "  /a:\n    get:\n      operationId: getA\n"
    "  /b:\n    post:\n      operationId: createB\n"
)
SPEC_URL = "/api/openapi.0123456789abcdef0123.yaml"


def load():
    spec = importlib.util.spec_from_file_location("check_api_spec", os.path.join(HERE, "check-api-spec.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def index_html(spec_url=SPEC_URL, extra=""):
    return (
        "<!doctype html><html><body><main>"
        '<a href="%s" download="openapi.yaml">Download</a>'
        '<a href="https://github.com/example/example/blob/main/api/openapi.yaml">source</a>%s'
        "</main></body></html>" % (spec_url, extra)
    )


def tag_html(*ops):
    return "<!doctype html><html><body>" + "".join("<h2>x</h2><p>Operation <code>%s</code>. Permission</p>" % o for o in ops) + "</body></html>"


class ApiSpecCheck(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="api-spec-check-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.mod = load()
        self.repo = os.path.join(self.tmp, "repo")
        self.site = os.path.join(self.tmp, "public")
        write(os.path.join(self.repo, "api", "openapi.yaml"), SPEC_TEXT)
        for name in ("_index.md", "data-types.md", "agents.md", "saved-filters.md"):
            write(os.path.join(self.repo, "docs", "reference", "api", name), "x\n")
        write(os.path.join(self.site, "api", "openapi.0123456789abcdef0123.yaml"), SPEC_TEXT)
        self.page("index.html", index_html())
        self.page("agents/index.html", tag_html("getA"))
        self.page("saved-filters/index.html", tag_html("createB"))
        self.page("data-types/index.html", "<html><body>types</body></html>")

    def page(self, rel, html):
        write(os.path.join(self.site, "docs", "reference", "api", rel), html)

    def findings(self):
        return self.mod.check(self.site, self.repo)[1]

    def assertFinding(self, text):
        found = self.findings()
        self.assertTrue(any(text in f for f in found), "no finding with %r in %r" % (text, found))

    def test_clean_site_has_no_findings(self):
        self.assertEqual(self.findings(), [])
        self.assertEqual(self.mod.check(self.site, self.repo)[0], 2)

    def test_missing_index(self):
        os.remove(os.path.join(self.site, "docs", "reference", "api", "index.html"))
        self.assertFinding("page is missing")

    def test_missing_tag_page(self):
        os.remove(os.path.join(self.site, "docs", "reference", "api", "agents", "index.html"))
        self.assertFinding("agents/index.html: the page is missing")

    def test_missing_data_types_page(self):
        os.remove(os.path.join(self.site, "docs", "reference", "api", "data-types", "index.html"))
        self.assertFinding("data-types/index.html: the page is missing")

    def test_spec_that_differs_from_the_contract(self):
        write(os.path.join(self.site, "api", "openapi.0123456789abcdef0123.yaml"), SPEC_TEXT + "# stale\n")
        self.assertFinding("differs from api/openapi.yaml")

    def test_spec_file_missing_from_the_site(self):
        os.remove(os.path.join(self.site, "api", "openapi.0123456789abcdef0123.yaml"))
        self.assertFinding("is not in the site")

    def test_no_download_link(self):
        self.page("index.html", "<html><body>none</body></html>")
        self.assertFinding("exactly one contract file, found 0")

    def test_two_contract_files_linked(self):
        write(os.path.join(self.site, "api", "openapi.fedcba9876543210fedc.yaml"), SPEC_TEXT)
        self.page("index.html", index_html(extra='<a href="/api/openapi.fedcba9876543210fedc.yaml">other</a>'))
        self.assertFinding("exactly one contract file, found 2")

    def test_remote_contract_is_not_a_site_file(self):
        self.page("index.html", index_html(spec_url="/other/api/openapi.yaml"))
        self.assertFinding("not a local fingerprinted file")

    def test_second_contract_file_in_the_site(self):
        write(os.path.join(self.site, "api", "openapi.fedcba9876543210fedc.yaml"), SPEC_TEXT)
        self.assertFinding("more than one contract file")

    def test_contract_without_fingerprint(self):
        write(os.path.join(self.site, "api", "openapi.yaml"), SPEC_TEXT)
        self.page("index.html", index_html(spec_url="/api/openapi.yaml"))
        self.assertFinding("not a local fingerprinted file")

    def test_viewer_script_on_any_page(self):
        write(os.path.join(self.site, "docs", "other", "index.html"), '<script src="/vendor/redoc/redoc.standalone.0123456789abcdef.js"></script>')
        self.assertFinding("loads the script")

    def test_viewer_script_on_the_index(self):
        self.page("index.html", index_html(extra='<script defer src="/js/api-reference.0123456789abcdef.js"></script><script src="/js/swagger-ui.js"></script>'))
        self.assertFinding("swagger-ui.js")

    def test_viewer_file_in_the_site(self):
        write(os.path.join(self.site, "vendor", "redoc", "redoc.standalone.js"), "x")
        self.assertFinding("a file of an API viewer")

    def test_operation_without_section(self):
        self.page("saved-filters/index.html", tag_html())
        self.assertFinding("operation createB of the contract has no section")

    def test_section_for_an_unknown_operation(self):
        self.page("agents/index.html", tag_html("getA", "ghost"))
        self.assertFinding("section for ghost")

    def test_operation_with_two_sections(self):
        self.page("saved-filters/index.html", tag_html("createB", "getA"))
        self.assertFinding("getA has more than one section")

    def test_operation_ids_are_read_from_the_contract_text(self):
        self.assertEqual(self.mod.contract_operations(self.repo), ["getA", "createB"])

    def test_main_usage_and_environment_errors(self):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(self.mod.main(["a", "b"]), 2)
            self.assertEqual(self.mod.main(["--help"]), 2)
            self.assertEqual(self.mod.main([os.path.join(self.tmp, "missing")]), 2)


if __name__ == "__main__":
    unittest.main()
