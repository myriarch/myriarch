#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Static file server for the production build of the website (make website-preview).

It behaves like a static host (ADR-0031): directory index files, /404.html with the 404 status and gzip for text files, which the
Python standard server does not compress. Lighthouse and the request check therefore measure what a visitor gets from a real host.
It binds to a loopback address only.

usage: python3 -I website/scripts/preview-server.py --directory website/public [--port 1313] [--bind 127.0.0.1]
"""

import argparse
import functools
import gzip
import http.server
import mimetypes
import os
import sys
import urllib.parse

COMPRESSIBLE = (
    "text/",
    "application/javascript",
    "application/json",
    "application/xml",
    "application/yaml",
    "application/x-yaml",
    "image/svg+xml",
    "application/manifest+json",
    "font/ttf",
    "application/x-font-ttf",
)
MIN_COMPRESS_BYTES = 512

mimetypes.add_type("application/javascript", ".mjs")
mimetypes.add_type("application/manifest+json", ".webmanifest")


def resolve(root, url_path):
    """Maps a URL path to a file below root. Returns None for a path that escapes root or does not exist."""
    path = urllib.parse.unquote(urllib.parse.urlsplit(url_path).path)
    parts = [p for p in path.split("/") if p not in ("", ".")]
    if any(p == ".." or "\x00" in p or "\\" in p for p in parts):
        return None
    candidate = os.path.join(root, *parts)
    if os.path.isdir(candidate):
        candidate = os.path.join(candidate, "index.html")
    if not os.path.isfile(candidate):
        return None
    return candidate


def compressible(content_type):
    return content_type.startswith(COMPRESSIBLE)


class Handler(http.server.BaseHTTPRequestHandler):
    server_version = "myriarch-preview"

    def __init__(self, *args, root, **kwargs):
        self.root = os.path.realpath(root)
        super().__init__(*args, **kwargs)

    def log_message(self, fmt, *args):  # quiet: the preview is a tool, not a service
        pass

    def do_HEAD(self):
        self.serve(head=True)

    def do_GET(self):
        self.serve(head=False)

    def serve(self, head):
        file = resolve(self.root, self.path)
        status = 200
        if file is None or not os.path.realpath(file).startswith(self.root + os.sep):
            file = os.path.join(self.root, "404.html")
            status = 404
            if not os.path.isfile(file):
                self.send_error(404)
                return
        content_type = mimetypes.guess_type(file)[0] or "application/octet-stream"
        with open(file, "rb") as f:
            body = f.read()
        if content_type.startswith("text/") or content_type in ("application/javascript", "application/json"):
            content_type += "; charset=utf-8"
        encoded = False
        if len(body) >= MIN_COMPRESS_BYTES and compressible(content_type) and "gzip" in self.headers.get("Accept-Encoding", ""):
            body = gzip.compress(body, compresslevel=6, mtime=0)
            encoded = True
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Vary", "Accept-Encoding")
        self.send_header("Cache-Control", "no-cache")
        if encoded:
            self.send_header("Content-Encoding", "gzip")
        self.end_headers()
        if not head:
            self.wfile.write(body)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--directory", required=True)
    parser.add_argument("--port", type=int, default=1313)
    parser.add_argument("--bind", default="127.0.0.1")
    args = parser.parse_args(argv)
    if args.bind not in ("127.0.0.1", "::1", "localhost"):
        print("preview-server: only loopback addresses are allowed", file=sys.stderr)
        return 2
    if not os.path.isdir(args.directory):
        print(f"preview-server: {args.directory} is not a directory; build the site first", file=sys.stderr)
        return 2
    handler = functools.partial(Handler, root=args.directory)
    with http.server.ThreadingHTTPServer((args.bind, args.port), handler) as httpd:
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            pass
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
