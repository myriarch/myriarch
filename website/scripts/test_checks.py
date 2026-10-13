#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Unit tests of the checks in this directory (ci/tests/website-test.sh, the website gate).

    python3 -I -m unittest discover -s website/scripts -p 'test_*.py'

Every check is imported from its file (the names contain a hyphen), exercised on fixtures in testdata/ or on small trees
that the test writes into a temporary directory, and its exit status and output are asserted. Standard library only.
"""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
import shutil
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
TESTDATA = os.path.join(HERE, "testdata")
WEBSITE = os.path.dirname(HERE)


def load(name):
    """Imports website/scripts/<name>.py under a module name without the hyphen."""
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def run_main(mod, argv):
    """Runs mod.main(argv) and returns (exit status, stdout, stderr)."""
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = mod.main(argv)
    return rc, out.getvalue(), err.getvalue()


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def copy_fixture(rel, dest):
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    shutil.copyfile(os.path.join(TESTDATA, rel), dest)


class TempCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="website-checks-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)

    def path(self, *parts):
        return os.path.join(self.tmp, *parts)


class ThirdPartyTest(TempCase):
    mod = load("check-third-party")

    def test_clean_site_passes(self):
        copy_fixture("third-party/good.html.fixture", self.path("public", "index.html"))
        write(self.path("public", "scss", "main.css"), ".a{background:url(/brand/a.svg)}\n")
        write(self.path("public", "js", "main.js"), "const m = await import('/vendor/x.mjs');\n")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 0, out)
        self.assertIn("0 resources from other hosts", out)

    def test_html_findings(self):
        copy_fixture("third-party/bad.html.fixture", self.path("public", "docs", "index.html"))
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        for url in (
            "https://unpkg.com/lunr@2.3.9/lunr.min.js",
            "//cdn.example.com/site.css",
            "https://fonts.gstatic.com/x.woff2",
            "https://fonts.googleapis.com",
            "HTTP://example.com/favicon.ico",
            "https://example.com/site.webmanifest",
            "https://fonts.googleapis.com/css?family=Open+Sans",
            "https://example.com/x.png",
            "https://example.com/bg.png",
            "https://example.com/a.png",
            "https://example.com/b.png",
            "https://example.com/embed",
            "https://example.com/p.png",
            "https://cdn.example.com/m.mjs",
            "https://cdn.example.com/w.js",
            "https://cdn.example.com/x.js",
            "https://cdn.example.com/worker.js",
        ):
            self.assertIn(url, out, url)
        self.assertNotIn("/local.png", out)
        self.assertNotIn("/ok.mp4", out)
        self.assertTrue(all(line.startswith("docs/index.html: ") for line in out.splitlines()[:-1]), out)

    def test_css_and_js_findings(self):
        copy_fixture("third-party/bad.css.fixture", self.path("public", "scss", "main.css"))
        copy_fixture("third-party/bad.js.fixture", self.path("public", "js", "main.mjs"))
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertIn("scss/main.css: css @import loads https://fonts.example.com/a.css", out)
        self.assertIn("scss/main.css: css url() loads https://example.com/a.png", out)
        self.assertIn("scss/main.css: css url() loads //example.com/f.woff2", out)
        self.assertNotIn("/local.png", out)
        self.assertIn("js/main.mjs: script import loads https://cdn.example.com/m.mjs", out)
        self.assertIn("js/main.mjs: script import loads //cdn.example.com/y.js", out)
        self.assertNotIn("/vendor/ok.mjs", out)

    def test_usage_errors(self):
        rc, _, err = run_main(self.mod, [self.path("missing")])
        self.assertEqual(rc, 2)
        self.assertIn("not a directory", err)
        rc, _, _ = run_main(self.mod, ["--bogus"])
        self.assertEqual(rc, 2)
        rc, _, _ = run_main(self.mod, ["a", "b"])
        self.assertEqual(rc, 2)


class LocalPathsTest(TempCase):
    mod = load("check-local-paths")

    def test_clean_site_passes(self):
        write(self.path("public", "index.html"), "<p>/var/lib/myriarch and /opt/myriarch</p>\n")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 0, out)

    def test_scan_finds_the_repository_root_and_user_directories(self):
        repo = "/work/build/myriarch"
        write(self.path("public", "a", "index.html"), '<a href="https://github.com/x/new/main/%s/docs/x.md">x</a>\n' % repo)
        write(self.path("public", "b", "index.html"), "<p>/Users/alice/Development/x</p>\n")
        write(self.path("public", "c", "index.html"), "<p>/home/bob/x</p>\n")
        write(self.path("public", "d", "index.html"), "<p>C:\\Users\\carol\\x</p>\n")
        write(self.path("public", "e.json"), '{"path": "%s"}\n' % repo)
        scanned, findings = self.mod.scan(self.path("public"), repo)
        self.assertEqual(scanned, 5)
        self.assertEqual(
            sorted(findings),
            [
                ("a/index.html", "contains the absolute path of the repository"),
                ("b/index.html", "contains a user directory path"),
                ("c/index.html", "contains a user directory path"),
                ("d/index.html", "contains a user directory path"),
                ("e.json", "contains the absolute path of the repository"),
            ],
        )

    def test_vendor_libraries_may_contain_example_paths_but_not_the_repository_root(self):
        repo = "/work/build/myriarch"
        write(self.path("public", "vendor", "lib.js"), "var example = '/home/user/project';\n")
        scanned, findings = self.mod.scan(self.path("public"), repo)
        self.assertEqual((scanned, findings), (1, []))
        write(self.path("public", "vendor", "leak.js"), "var p = '%s';\n" % repo)
        _, findings = self.mod.scan(self.path("public"), repo)
        self.assertEqual(findings, [("vendor/leak.js", "contains the absolute path of the repository")])

    def test_output_has_no_matched_text(self):
        write(self.path("public", "index.html"), "<p>/Users/alice/secret</p>\n")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertNotIn("alice", out)
        self.assertIn("index.html: contains a user directory path", out)

    def test_usage_errors(self):
        rc, _, err = run_main(self.mod, [self.path("missing")])
        self.assertEqual(rc, 2)
        self.assertIn("not a directory", err)


class OverridesTest(TempCase):
    mod = load("check-overrides")

    def make(self, upstream, mine, listed):
        """Writes a theme with the upstream files, a site with its layouts and overrides.txt; returns (site, theme)."""
        site, theme = self.path("site"), self.path("theme")
        for rel, text in upstream.items():
            write(os.path.join(theme, rel), text)
        for rel, text in mine.items():
            write(os.path.join(site, rel), text)
        lines = ["# comment\n", "\n"]
        for rel, text in listed.items():
            lines.append("%s %s\n" % (rel, hashlib.sha256(text.encode()).hexdigest()))
        write(os.path.join(site, "overrides.txt"), "".join(lines))
        return site, theme

    def test_pinned_override_passes(self):
        site, theme = self.make({"layouts/_partials/head.html": "old\n"}, {"layouts/_partials/head.html": "mine\n", "layouts/new.html": "x\n"},
                                {"layouts/_partials/head.html": "old\n"})
        count, findings = self.mod.check(site, theme)
        self.assertEqual((count, findings), (1, []))

    def test_reuse_annotation_must_match_the_listed_overrides(self):
        site, theme = self.make({"layouts/a.html": "old\n", "layouts/b.html": "old\n"}, {"layouts/a.html": "mine\n", "layouts/b.html": "mine\n"},
                                {"layouts/a.html": "old\n", "layouts/b.html": "old\n"})
        reuse = os.path.join(os.path.dirname(site), "REUSE.toml")
        block = ('[[annotations]]\npath = [\n  "website/layouts/a.html",\n  %s]\nprecedence = "aggregate"\n'
                 'SPDX-FileCopyrightText = "Copyright The Docsy Authors"\n')
        other = '[[annotations]]\npath = ["website/layouts/zzz.html"]\nSPDX-FileCopyrightText = "Copyright The Myriarch Authors"\n'
        write(reuse, other + block % '"website/layouts/b.html",\n')
        self.assertEqual(self.mod.check(site, theme), (2, []))
        # an override that the annotation misses, and an annotation path without an override
        write(reuse, other + block % "")
        _, findings = self.mod.check(site, theme)
        self.assertEqual(findings, ["website/layouts/b.html: listed in overrides.txt but not in the Docsy annotation of REUSE.toml (add it there)"])
        write(reuse, other + block % '"website/layouts/b.html",\n  "website/layouts/c.html",\n')
        _, findings = self.mod.check(site, theme)
        self.assertEqual(findings, ["website/layouts/c.html: in the Docsy annotation of REUSE.toml but not listed in overrides.txt"])

    def test_the_real_reuse_annotation_matches_overrides_txt(self):
        with open(os.path.join(WEBSITE, "overrides.txt"), "r", encoding="utf-8") as fh:
            entries, errors = self.mod.parse_overrides(fh.read())
        self.assertEqual(errors, [])
        self.assertGreater(len(entries), 0)
        self.assertEqual(self.mod.check_reuse(WEBSITE, entries), [])

    def test_changed_upstream_file_is_reported(self):
        site, theme = self.make({"layouts/_partials/head.html": "old\n"}, {"layouts/_partials/head.html": "mine\n"},
                                {"layouts/_partials/head.html": "old\n"})
        write(os.path.join(theme, "layouts/_partials/head.html"), "new\n")
        _, findings = self.mod.check(site, theme)
        self.assertEqual(len(findings), 1)
        self.assertIn("layouts/_partials/head.html: the upstream file changed", findings[0])

    def test_missing_override_and_missing_upstream(self):
        site, theme = self.make({"layouts/a.html": "a\n"}, {}, {"layouts/a.html": "a\n", "layouts/b.html": "b\n"})
        _, findings = self.mod.check(site, theme)
        self.assertIn("layouts/a.html: listed in overrides.txt but missing in website/", findings)
        self.assertIn("layouts/b.html: listed in overrides.txt but missing in website/", findings)
        self.assertTrue(any(f.startswith("layouts/b.html: the upstream file no longer exists") for f in findings), findings)

    def test_unlisted_shadowing_file_is_reported(self):
        site, theme = self.make({"layouts/_partials/footer.html": "u\n"}, {"layouts/_partials/footer.html": "mine\n"}, {})
        _, findings = self.mod.check(site, theme)
        self.assertEqual(findings, ["layouts/_partials/footer.html: replaces a Docsy file but is not listed in overrides.txt"])

    def test_pinned_asset_override_passes(self):
        # a script of website/assets that shadows a theme file is pinned like a layout; a file without a counterpart in the theme is free
        site, theme = self.make({"assets/js/offline-search.js": "old\n"},
                                {"assets/js/offline-search.js": "mine\n", "assets/js/landing-mark.js": "x\n", "assets/images/a.png": "x"},
                                {"assets/js/offline-search.js": "old\n"})
        self.assertEqual(self.mod.check(site, theme), (1, []))

    def test_unlisted_asset_shadowing_a_theme_file_is_reported(self):
        site, theme = self.make({"assets/js/offline-search.js": "old\n", "assets/scss/td/_nav.scss": "u\n"},
                                {"assets/js/offline-search.js": "mine\n", "assets/scss/td/_nav.scss": "mine\n", "assets/js/landing-mark.js": "x\n"}, {})
        _, findings = self.mod.check(site, theme)
        self.assertEqual(sorted(findings), [
            "assets/js/offline-search.js: replaces a Docsy file but is not listed in overrides.txt",
            "assets/scss/td/_nav.scss: replaces a Docsy file but is not listed in overrides.txt",
        ])

    def test_changed_upstream_asset_and_missing_asset_override_are_reported(self):
        site, theme = self.make({"assets/js/offline-search.js": "old\n"}, {"assets/js/offline-search.js": "mine\n"},
                                {"assets/js/offline-search.js": "old\n"})
        write(os.path.join(theme, "assets/js/offline-search.js"), "new\n")
        _, findings = self.mod.check(site, theme)
        self.assertEqual(len(findings), 1)
        self.assertIn("assets/js/offline-search.js: the upstream file changed", findings[0])
        os.remove(os.path.join(site, "assets/js/offline-search.js"))
        _, findings = self.mod.check(site, theme)
        self.assertIn("assets/js/offline-search.js: listed in overrides.txt but missing in website/", findings)

    def test_project_style_files_replace_placeholders_without_a_pin(self):
        placeholder = "/*\nProjects can override this file. For details, see:\nhttps://www.docsy.dev/docs/content/lookandfeel/#project-style-files\n*/\n"
        hooks = sorted(self.mod.PROJECT_HOOKS)
        self.assertEqual(len(hooks), 3)
        site, theme = self.make({rel: placeholder for rel in hooks}, {rel: "$brand: #fff;\n" for rel in hooks}, {})
        self.assertEqual(self.mod.check(site, theme), (0, []))
        # a Docsy update puts rules into a placeholder: the file of the project would hide them
        write(os.path.join(theme, hooks[0]), placeholder + "body { margin: 0; }\n")
        _, findings = self.mod.check(site, theme)
        self.assertEqual(len(findings), 1)
        self.assertTrue(findings[0].startswith(hooks[0] + ": the upstream file is no longer an empty placeholder"), findings)
        # another file of the theme's scss directory is no exception
        shutil.rmtree(site)
        shutil.rmtree(theme)
        site, theme = self.make({"assets/scss/_styles_project_other.scss": placeholder}, {"assets/scss/_styles_project_other.scss": "a {}\n"}, {})
        _, findings = self.mod.check(site, theme)
        self.assertEqual(findings, ["assets/scss/_styles_project_other.scss: replaces a Docsy file but is not listed in overrides.txt"])

    def test_has_code(self):
        for text in ("", "\n", "/* c */", "/*\nmulti\nline https://example.test/x\n*/\n", "// line\n", "/* a */ // b\n"):
            self.assertFalse(self.mod.has_code(text), repr(text))
        for text in ("a { b: c }", "/* c */ a {}", "$x: 1; // c", "@import 'x';"):
            self.assertTrue(self.mod.has_code(text), repr(text))

    def test_the_real_project_style_files_are_not_pinned(self):
        # the three hooks exist in the project and are no entries of overrides.txt (a pin would also demand the Docsy attribution)
        with open(os.path.join(WEBSITE, "overrides.txt"), encoding="utf-8") as fh:
            entries, _ = self.mod.parse_overrides(fh.read())
        for rel in self.mod.PROJECT_HOOKS:
            self.assertTrue(os.path.isfile(os.path.join(WEBSITE, rel)), rel)
            self.assertNotIn(rel, entries)

    def test_parse_overrides(self):
        good = "a" * 64
        entries, errors = self.mod.parse_overrides("# c\n\nlayouts/a.html %s\nlayouts/a.html %s\nbad line\nlayouts/b.html XYZ\n" % (good, good))
        self.assertEqual(entries, {"layouts/a.html": good})
        self.assertEqual(len(errors), 3)

    def test_theme_dir_from_the_mounts_stream(self):
        stream = json.dumps({"path": "example.com/site", "dir": "/a"}) + "\n" + json.dumps({"path": "github.com/docsy/docsy/theme", "dir": "/b/theme@v1/"})
        self.assertEqual(self.mod.theme_dir_from_mounts(stream), "/b/theme@v1/")
        self.assertIsNone(self.mod.theme_dir_from_mounts(json.dumps({"path": "example.com/site", "dir": "/a"})))

    def test_the_real_overrides_file_is_well_formed(self):
        with open(os.path.join(WEBSITE, "overrides.txt"), encoding="utf-8") as fh:
            entries, errors = self.mod.parse_overrides(fh.read())
        self.assertEqual(errors, [])
        self.assertGreaterEqual(len(entries), 1)
        for rel in entries:
            self.assertTrue(os.path.isfile(os.path.join(WEBSITE, rel)), rel)


class PlaceholdersTest(TempCase):
    mod = load("check-placeholders")

    PAGE = '---\ntitle: "Install"\nweight: 10\ndescription: "D."\nparams:\n  placeholder:\n    tracking: "operations-manual"\n    replaces: "%s"\n---\n\n{{< placeholder >}}\n'

    def site(self, pages, repo_files=()):
        repo = self.path("repo")
        site = os.path.join(repo, "website")
        for rel, text in pages.items():
            write(os.path.join(site, "content", rel), text)
        for rel in repo_files:
            write(os.path.join(repo, rel), "x\n")
        return site, repo

    def test_open_placeholder_passes(self):
        site, repo = self.site({"docs/operations/install-helm.md": self.PAGE % "docs/operations/install-helm.md",
                                "docs/other.md": '---\ntitle: "T"\ndescription: "D."\n---\ntext\n'})
        self.assertEqual(self.mod.check(site, repo), (1, []))

    def test_existing_replaced_file_is_reported(self):
        site, repo = self.site({"docs/operations/install-helm.md": self.PAGE % "docs/operations/install-helm.md"}, ["docs/operations/install-helm.md"])
        _, findings = self.mod.check(site, repo)
        self.assertEqual(findings, ["website/content/docs/operations/install-helm.md: docs/operations/install-helm.md exists now; delete the placeholder"])

    def test_wrong_location_wrong_path_missing_shortcode_missing_tracking(self):
        site, repo = self.site({
            "docs/elsewhere.md": self.PAGE % "docs/operations/air-gapped.md",
            "docs/operations/a.md": self.PAGE % "operations/a.md",
            "docs/operations/b.md": (self.PAGE % "docs/operations/b.md").replace("{{< placeholder >}}", "text"),
            "docs/operations/c.md": (self.PAGE % "docs/operations/c.md").replace('    tracking: "operations-manual"\n', ""),
        })
        _, findings = self.mod.check(site, repo)
        self.assertIn("website/content/docs/elsewhere.md: a placeholder for docs/operations/air-gapped.md must live at website/content/docs/operations/air-gapped.md", findings)
        self.assertIn("website/content/docs/operations/a.md: placeholder.replaces must be a repository path below docs/ ending in .md", findings)
        self.assertIn("website/content/docs/operations/b.md: the body must use the placeholder shortcode", findings)
        self.assertIn("website/content/docs/operations/c.md: placeholder.tracking is missing", findings)
        self.assertEqual(len(findings), 4)

    def test_tracking_must_be_a_slug_or_an_issue_reference_and_planned_a_release(self):
        site, repo = self.site({
            "docs/operations/a.md": (self.PAGE % "docs/operations/a.md").replace("operations-manual", "Follow up"),
            "docs/operations/b.md": (self.PAGE % "docs/operations/b.md").replace('    replaces:', '    planned: "soon"\n    replaces:'),
            "docs/operations/c.md": (self.PAGE % "docs/operations/c.md").replace('    replaces:', '    planned: "v0.2.0"\n    replaces:'),
        })
        _, findings = self.mod.check(site, repo)
        self.assertEqual(findings, [
            "website/content/docs/operations/a.md: placeholder.tracking must be a lowercase topic slug such as operations-manual or a GitHub issue reference such as #123",
            "website/content/docs/operations/b.md: placeholder.planned must be a release such as v0.2.0",
        ])

    def test_issue_reference_must_be_quoted(self):
        site, repo = self.site({
            "docs/operations/a.md": (self.PAGE % "docs/operations/a.md").replace('"operations-manual"', '"#123"'),
            "docs/operations/b.md": (self.PAGE % "docs/operations/b.md").replace('"operations-manual"', "'#123'"),
            "docs/operations/c.md": (self.PAGE % "docs/operations/c.md").replace('"operations-manual"', "#123"),
        })
        count, findings = self.mod.check(site, repo)
        self.assertEqual(count, 3)
        self.assertEqual(findings, [
            'website/content/docs/operations/c.md: placeholder.tracking must be quoted when it is an issue reference (tracking: "#123"); an unquoted # starts a YAML comment',
        ])

    def test_the_real_site_is_clean(self):
        count, findings = self.mod.check(WEBSITE, os.path.dirname(WEBSITE))
        self.assertEqual(findings, [])
        self.assertGreaterEqual(count, 0)


class NoticesTest(TempCase):
    mod = load("check-notices")

    def site(self, deps=None, dev=None, gomod=None, entries=None, mermaid="11.17.2", page=None):
        deps = {"bootstrap": "5.3.8", "mermaid": "11.17.2"} if deps is None else deps
        dev = {"markdownlint-cli2": "0.23.3"} if dev is None else dev
        site = self.path("site")
        write(os.path.join(site, "package.json"), json.dumps({"dependencies": deps, "devDependencies": dev}))
        write(os.path.join(site, "go.mod"), gomod or "module x\n\ngo 1.24\n\nrequire github.com/docsy/docsy/theme v0.18.0\n")
        if entries is None:
            entries = [("Docsy", "github.com/docsy/docsy/theme", "0.18.0"), ("Bootstrap", "bootstrap", "5.3.8"), ("Mermaid", "mermaid", "11.17.2")]
        text = "# comment\n"
        for name, pkg, version in entries:
            text += '- name: %s\n  package: "%s"\n  version: %s\n  license: MIT\n  url: https://example.com\n  use: something\n  source: package.json\n  license_file: /licenses/%s/LICENSE.txt\n' % (name, pkg, version, name.lower())
        write(os.path.join(site, "data", "third_party.yaml"), text)
        write(os.path.join(site, "config", "_default", "params.yaml"), "docsy:\n  plugins:\n    mermaid: { enable: true, version: %s }\n" % mermaid)
        write(os.path.join(site, "content", "legal", "third-party.md"), page or "Build tools: markdownlint-cli2, Hugo.\n")
        return site

    def test_consistent_notices_pass(self):
        self.assertEqual(self.mod.check(self.site()), (3, []))

    def test_license_texts_must_be_published(self):
        site = self.site()
        public = self.path("public")
        for name in ("docsy", "bootstrap"):
            write(os.path.join(public, "licenses", name, "LICENSE.txt"), "MIT License\n")
        write(os.path.join(public, "licenses", "mermaid", "LICENSE.txt"), "")
        _, findings = self.mod.check(site, public)
        self.assertEqual(findings, ["third_party.yaml: the license text of Mermaid is not published at /licenses/mermaid/LICENSE.txt"])
        write(os.path.join(public, "licenses", "mermaid", "LICENSE.txt"), "MIT License\n")
        self.assertEqual(self.mod.check(site, public), (3, []))
        # without a built site only the sources are checked
        self.assertEqual(self.mod.check(site, self.path("missing")), (3, []))

    def test_missing_entry_and_version_mismatch(self):
        site = self.site(entries=[("Docsy", "github.com/docsy/docsy/theme", "0.17.0"), ("Mermaid", "mermaid", "11.17.2")])
        _, findings = self.mod.check(site)
        self.assertIn("third_party.yaml: no notice for bootstrap 5.3.8", findings)
        self.assertIn("third_party.yaml: github.com/docsy/docsy/theme is 0.17.0 in the notices but 0.18.0 in the pins", findings)

    def test_stale_entry_and_missing_field(self):
        site = self.site(entries=[("Docsy", "github.com/docsy/docsy/theme", "0.18.0"), ("Bootstrap", "bootstrap", "5.3.8"),
                                  ("Mermaid", "mermaid", "11.17.2"), ("Old", "old-package", "1.0.0")])
        _, findings = self.mod.check(site)
        self.assertEqual(findings, ["third_party.yaml: old-package is not pinned in package.json or go.mod"])
        path = os.path.join(site, "data", "third_party.yaml")
        with open(path, encoding="utf-8") as fh:
            text = fh.read().replace("  license: MIT\n", "", 1)
        write(path, text)
        _, findings = self.mod.check(site)
        self.assertTrue(any("lacks the field license" in f for f in findings), findings)

    def test_range_versions_are_refused(self):
        site = self.site(deps={"bootstrap": "^5.3.8", "mermaid": "11.17.2"})
        _, findings = self.mod.check(site)
        self.assertTrue(any("bootstrap is pinned as ^5.3.8" in f for f in findings), findings)

    def test_mermaid_plugin_version_must_match(self):
        _, findings = self.mod.check(self.site(mermaid="11.0.0"))
        self.assertEqual(findings, ["params.yaml: the Mermaid plugin version (11.0.0) differs from package.json (11.17.2)"])

    def test_dev_dependency_must_be_named_on_the_page(self):
        _, findings = self.mod.check(self.site(page="Nothing here.\n"))
        self.assertEqual(findings, ["content/legal/third-party.md: the build tool markdownlint-cli2 is not named"])

    def test_docsy_module_must_be_required(self):
        _, findings = self.mod.check(self.site(gomod="module x\n\ngo 1.24\n"))
        self.assertTrue(any("go.mod: the Docsy module" in f for f in findings), findings)

    def test_parse_entries_rejects_nested_yaml(self):
        with self.assertRaises(ValueError):
            self.mod.parse_entries("- name: A\n    deep: nested\n")

    def test_the_real_site_is_clean(self):
        count, findings = self.mod.check(WEBSITE)
        self.assertEqual(findings, [])
        self.assertGreaterEqual(count, 5)


class LighthouseTest(TempCase):
    mod = load("lighthouse-scores")

    def test_scores_at_or_above_the_minimum_pass(self):
        rc, out, _ = run_main(self.mod, [os.path.join(TESTDATA, "lighthouse", "good.json")])
        self.assertEqual(rc, 0, out)
        self.assertIn("performance=99 accessibility=100 best-practices=100 seo=92", out)

    def test_a_score_below_the_minimum_fails(self):
        rc, out, _ = run_main(self.mod, ["--min", "90", os.path.join(TESTDATA, "lighthouse", "slow.json")])
        self.assertEqual(rc, 1)
        self.assertIn("BELOW 90: performance", out)
        rc, out, _ = run_main(self.mod, ["--min", "85", os.path.join(TESTDATA, "lighthouse", "slow.json")])
        self.assertEqual(rc, 0, out)

    def test_a_missing_category_fails_and_categories_can_be_limited(self):
        report = os.path.join(TESTDATA, "lighthouse", "missing.json")
        rc, out, _ = run_main(self.mod, [report])
        self.assertEqual(rc, 1)
        self.assertIn("seo=missing", out)
        self.assertIn("accessibility=missing", out)
        rc, out, _ = run_main(self.mod, ["--categories", "performance,best-practices", report])
        self.assertEqual(rc, 0, out)

    def test_directories_and_unreadable_reports(self):
        rc, out, _ = run_main(self.mod, [os.path.join(TESTDATA, "lighthouse")])
        self.assertEqual(rc, 1)
        self.assertIn("3 reports", out)
        write(self.path("broken.json"), "{not json")
        rc, out, _ = run_main(self.mod, [self.path("broken.json")])
        self.assertEqual(rc, 1)
        self.assertIn("cannot read the report", out)

    def test_usage_errors(self):
        for argv in ([], ["--min"], ["--min", "x", "a.json"], ["--bogus"]):
            rc, _, _ = run_main(self.mod, argv)
            self.assertEqual(rc, 2, argv)
        rc, out, _ = run_main(self.mod, [self.path("does-not-exist.json")])
        self.assertEqual(rc, 1)
        self.assertIn("cannot read the report", out)


class IndexingTest(TempCase):
    mod = load("check-indexing")
    PAGE = '<!doctype html><title>x</title><meta name=robots content="%s"><p>The directive noindex in text is no finding.</p>'

    def setUp(self):
        super().setUp()
        self.env = mock.patch.dict(os.environ, {"WEBSITE_ENV": "production", "WEBSITE_BASEURL": "https://example.test/"})
        self.env.start()
        self.addCleanup(self.env.stop)

    def site(self, robots, content):
        write(self.path("public", "index.html"), self.PAGE % content)
        write(self.path("public", "docs", "index.html"), self.PAGE % content)
        if robots is not None:
            write(self.path("public", "robots.txt"), robots)

    PROD_ROBOTS = "User-agent: *\nDisallow:\n\nSitemap: https://example.test/sitemap.xml\n"
    PREVIEW_ROBOTS = "User-agent: *\nDisallow: /\n"

    def test_production_site_passes(self):
        self.site(self.PROD_ROBOTS, "index, follow")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 0, out)
        self.assertIn("mode production, 2 pages scanned, 0 findings", out)

    def test_preview_site_passes(self):
        os.environ["WEBSITE_ENV"] = "preview"
        self.site(self.PREVIEW_ROBOTS, "noindex, nofollow")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 0, out)
        self.assertIn("mode preview", out)

    def test_a_preview_that_can_be_indexed_fails(self):
        self.site(self.PROD_ROBOTS, "index, follow")
        os.environ["WEBSITE_ENV"] = "preview"
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertIn('does not contain the line "Disallow: /"', out)
        self.assertIn("names a sitemap", out)
        self.assertIn("index.html: the preview page has no robots meta tag with noindex", out)
        self.assertIn("docs/index.html", out)

    def test_a_production_site_that_hides_itself_fails(self):
        self.site(self.PREVIEW_ROBOTS, "noindex, nofollow")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertIn('production contains the line "Disallow: /"', out)
        self.assertIn("does not contain the line 'Sitemap: https://example.test/sitemap.xml'", out)
        self.assertIn("index.html: the production page carries a robots meta tag with noindex", out)

    def test_an_alias_redirect_stub_is_no_finding_in_production(self):
        self.site(self.PROD_ROBOTS, "index, follow")
        write(
            self.path("public", "old", "index.html"),
            '<!doctype html><html><head><title>/docs/</title><link rel=canonical href="https://example.test/docs/">'
            '<meta name=robots content="noindex"><meta charset=utf-8><meta http-equiv=refresh content="0; url=https://example.test/docs/">'
            "</head></html>",
        )
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 0, out)
        self.assertIn("3 pages scanned, 0 findings", out)
        # A noindex page without the redirect markers is still a finding.
        write(self.path("public", "hidden", "index.html"), self.PAGE % "noindex")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertIn("hidden/index.html", out)

    def test_the_sitemap_must_use_the_base_url(self):
        self.site("User-agent: *\nDisallow:\n\nSitemap: https://other.test/sitemap.xml\n", "index, follow")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertIn("https://example.test/sitemap.xml", out)

    def test_a_missing_robots_txt_fails(self):
        self.site(None, "index, follow")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 1)
        self.assertIn("robots.txt: missing", out)

    def test_the_base_url_defaults_to_the_site_configuration(self):
        del os.environ["WEBSITE_BASEURL"]
        self.site("User-agent: *\nDisallow:\n\nSitemap: https://myriarch.dev/sitemap.xml\n", "index, follow")
        rc, out, _ = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 0, out)

    def test_usage_and_environment_errors(self):
        for argv in (["--bogus"], ["a", "b"]):
            rc, _, _ = run_main(self.mod, argv)
            self.assertEqual(rc, 2, argv)
        rc, _, _ = run_main(self.mod, [self.path("does-not-exist")])
        self.assertEqual(rc, 2)
        os.environ["WEBSITE_ENV"] = "staging"
        self.site(self.PROD_ROBOTS, "index, follow")
        rc, _, err = run_main(self.mod, [self.path("public")])
        self.assertEqual(rc, 2)
        self.assertIn("production or preview", err)


if __name__ == "__main__":
    unittest.main()
