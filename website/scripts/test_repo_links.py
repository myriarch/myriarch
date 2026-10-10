#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Unit tests of check-repo-links.py (the website gate runs them with the other tests in this directory).

    python3 -I -m unittest discover -s website/scripts -p 'test_*.py'

Each test writes a small repository and a small built site into a temporary directory, runs the check and asserts the findings.
Standard library only.
"""

import contextlib
import importlib.util
import io
import os
import shutil
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
URL = "https://github.com/example/project"


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


class RepoLinksTest(unittest.TestCase):
    mod = load("check-repo-links")

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="repo-links-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.repo = os.path.join(self.tmp, "repo")
        self.public = os.path.join(self.tmp, "public")
        write(os.path.join(self.repo, "docs", "adr", "0001-x.md"), "# x\n")
        write(os.path.join(self.repo, "api", "openapi.yaml"), "openapi: 3.1.0\n")

    def page(self, name, *hrefs):
        write(os.path.join(self.public, name), "".join('<a href="%s">x</a>\n' % h for h in hrefs))

    def test_existing_targets_pass(self):
        self.page(
            "index.html",
            URL + "/blob/main/docs/adr/0001-x.md#status",
            URL + "/tree/main/docs/adr",
            URL + "/edit/main/api/openapi.yaml",
            "https://example.com/other",
            URL + "/issues/new?title=x",
        )
        checked, findings = self.mod.scan(self.public, self.repo, URL, "main")
        self.assertEqual((checked, findings), (3, []))

    def test_a_missing_path_is_found(self):
        self.page("a/index.html", URL + "/blob/main/website/content/0024-container-registry.md")
        _, findings = self.mod.scan(self.public, self.repo, URL, "main")
        self.assertEqual(findings, [("a/index.html", "blob", "website/content/0024-container-registry.md", "does not exist")])

    def test_blob_of_a_directory_and_tree_of_a_file_are_found(self):
        self.page("index.html", URL + "/blob/main/docs/adr", URL + "/tree/main/api/openapi.yaml")
        _, findings = self.mod.scan(self.public, self.repo, URL, "main")
        self.assertEqual(
            sorted(f[3] for f in findings),
            ["is a directory (blob link)", "is a file (tree link)"],
        )

    def test_links_to_other_branches_are_not_ours(self):
        self.page("index.html", URL + "/blob/v1/nothing.md")
        checked, findings = self.mod.scan(self.public, self.repo, URL, "main")
        self.assertEqual((checked, findings), (0, []))

    def test_percent_encoding_and_entities(self):
        write(os.path.join(self.repo, "docs", "a b.md"), "x\n")
        self.page("index.html", URL + "/blob/main/docs/a%20b.md", URL + "/blob/main/docs/a%20b.md?x=1&amp;y=2")
        _, findings = self.mod.scan(self.public, self.repo, URL, "main")
        self.assertEqual(findings, [])

    def test_repo_settings_come_from_params(self):
        text = "title: x\n\nrepo:\n  url: https://github.com/a/b/\n  branch: main\n\nother: 1\n"
        self.assertEqual(self.mod.repo_settings(text), ("https://github.com/a/b", "main"))
        self.assertEqual(self.mod.repo_settings("title: x\n"), (None, None))

    def test_usage_errors(self):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(self.mod.main(["a", "b"]), 2)
            self.assertEqual(self.mod.main(["--bogus"]), 2)
            self.assertEqual(self.mod.main([os.path.join(self.tmp, "missing")]), 2)
        self.assertIn("not a directory", err.getvalue())


if __name__ == "__main__":
    unittest.main()
