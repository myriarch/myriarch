# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Tests of preview-server.py: path resolution, the 404 page and gzip."""

import gzip
import http.client
import importlib.util
import os
import pathlib
import tempfile
import threading
import unittest
import functools
import http.server

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("preview_server", HERE / "preview-server.py")
preview = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preview)


class PreviewServerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        root = pathlib.Path(cls.tmp.name) / "public"
        (root / "docs").mkdir(parents=True)
        (root / "index.html").write_text("<h1>home</h1>" + "x" * 1000)
        (root / "docs" / "index.html").write_text("<h1>docs</h1>")
        (root / "404.html").write_text("<h1>missing</h1>")
        (root / "app.mjs").write_text("export const a = 1;" + " " * 1000)
        (pathlib.Path(cls.tmp.name) / "secret.txt").write_text("secret")
        cls.root = str(root)
        handler = functools.partial(preview.Handler, root=cls.root)
        cls.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        cls.port = cls.httpd.server_address[1]
        threading.Thread(target=cls.httpd.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.tmp.cleanup()

    def get(self, path, headers=None):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        conn.request("GET", path, headers=headers or {})
        resp = conn.getresponse()
        body = resp.read()
        conn.close()
        return resp, body

    def test_directory_index(self):
        resp, body = self.get("/docs/")
        self.assertEqual(resp.status, 200)
        self.assertIn(b"docs", body)

    def test_missing_page_gets_404_page(self):
        resp, body = self.get("/nope/")
        self.assertEqual(resp.status, 404)
        self.assertIn(b"missing", body)

    def test_no_path_escape(self):
        for p in ("/../secret.txt", "/%2e%2e/secret.txt", "/docs/../../secret.txt"):
            resp, body = self.get(p)
            self.assertEqual(resp.status, 404, p)
            self.assertNotIn(b"secret", body)

    def test_gzip_when_accepted(self):
        resp, body = self.get("/", {"Accept-Encoding": "gzip"})
        self.assertEqual(resp.getheader("Content-Encoding"), "gzip")
        self.assertIn(b"home", gzip.decompress(body))

    def test_plain_without_accept_encoding(self):
        resp, body = self.get("/", {"Accept-Encoding": "identity"})
        self.assertIsNone(resp.getheader("Content-Encoding"))
        self.assertIn(b"home", body)

    def test_module_type(self):
        resp, _ = self.get("/app.mjs")
        self.assertTrue(resp.getheader("Content-Type").startswith("application/javascript"))

    def test_resolve_rejects_dotdot(self):
        self.assertIsNone(preview.resolve(self.root, "/../secret.txt"))
        self.assertIsNotNone(preview.resolve(self.root, "/"))


if __name__ == "__main__":
    unittest.main()
