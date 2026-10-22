#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Unit tests of source-dates.py (the website gate runs them with the other tests in this directory).

    python3 -I -m unittest discover -s website/scripts -p 'test_*.py'

Each test builds a small Git repository in a temporary directory with fixed commit dates. Standard library only.
"""

import contextlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def load():
    spec = importlib.util.spec_from_file_location("source_dates", os.path.join(HERE, "source-dates.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class SourceDatesTest(unittest.TestCase):
    mod = load()

    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="source-dates-")
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.git("init", "-q")

    def git(self, *args, root=None):
        subprocess.run(["git", "-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@example.test", "-C", root or self.root] + list(args),
                       check=True, stdout=subprocess.PIPE)

    def write(self, rel, text="x\n"):
        path = os.path.join(self.root, *rel.split("/"))
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)

    def commit(self, message, date):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message, "--date", date)

    def run_main(self, *extra):
        out = os.path.join(self.root, "out.json")
        err = io.StringIO()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            rc = self.mod.main(["--root", self.root, "--output", out] + list(extra))
        data = None
        if rc == 0:
            with open(out, encoding="utf-8") as fh:
                data = json.load(fh)
        return rc, data, err.getvalue()

    def test_dates_are_the_last_commit_of_each_source_in_utc(self):
        self.write("website/data/repo_pages.yaml", "project:\n  - source: ROADMAP.md\n    slug: roadmap\n  - source: assets/brand/README.md\n")
        self.write("ROADMAP.md")
        self.write("assets/brand/README.md")
        self.write("docs/adr/0001-first.md")
        self.write("docs/adr/0000-template.md")
        self.write("docs/adr/README.md")
        self.commit("one", "2026-10-10T12:00:00+02:00")
        self.write("ROADMAP.md", "y\n")
        self.commit("two", "2026-10-15T01:30:00+02:00")
        rc, data, err = self.run_main()
        self.assertEqual(rc, 0, err)
        self.assertEqual(data, {
            "ROADMAP.md": "2026-10-14T23:30:00Z",
            "assets/brand/README.md": "2026-10-10T10:00:00Z",
            "docs/adr/0001-first.md": "2026-10-10T10:00:00Z",
        })

    def test_the_output_does_not_depend_on_the_time_of_the_run(self):
        self.write("website/data/repo_pages.yaml", "project:\n  - source: ROADMAP.md\n")
        self.write("ROADMAP.md")
        self.commit("one", "2026-10-10T12:00:00+02:00")
        first = self.run_main()[1]
        self.assertEqual(first, self.run_main()[1])

    def test_a_source_without_a_commit_has_no_entry(self):
        self.write("website/data/repo_pages.yaml", "project:\n  - source: ROADMAP.md\n")
        self.write("ROADMAP.md")
        self.commit("one", "2026-10-10T12:00:00+02:00")
        self.write("docs/adr/0002-new.md")
        rc, data, err = self.run_main()
        self.assertEqual(rc, 0, err)
        self.assertEqual(list(data), ["ROADMAP.md"])

    def test_a_shallow_clone_is_refused(self):
        self.write("website/data/repo_pages.yaml", "project:\n  - source: ROADMAP.md\n")
        self.write("ROADMAP.md")
        self.commit("one", "2026-10-10T12:00:00+02:00")
        self.write("ROADMAP.md", "y\n")
        self.commit("two", "2026-10-11T12:00:00+02:00")
        clone = tempfile.mkdtemp(prefix="source-dates-clone-")
        self.addCleanup(shutil.rmtree, clone, ignore_errors=True)
        target = os.path.join(clone, "c")
        subprocess.run(["git", "clone", "-q", "--depth", "1", "file://" + self.root, target], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        err = io.StringIO()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            rc = self.mod.main(["--root", target])
        self.assertEqual(rc, 2)
        self.assertIn("shallow clone", err.getvalue())

    def test_usage_errors(self):
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(self.mod.main(["--bogus"]), 2)
            self.assertEqual(self.mod.main(["--root"]), 2)


if __name__ == "__main__":
    unittest.main()
