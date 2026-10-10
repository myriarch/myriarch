#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Throw-away mirror for contrib/enterprise-mirror/tests/publish-mirror-test.sh: stores PUT files in a directory and serves them with GET.

  put-server.py --dir DIR --port-file FILE --type artifactory|nexus [--token T] [--user U --password P]

Checks the authentication like Artifactory (Bearer) or Nexus (Basic), does not reject a PUT without X-Checksum-Sha256
for Artifactory, but overwrites existing files (so the immutability comes from the script, not from the server).
For tests only.
"""
import argparse, base64, hashlib, http.server, os, sys

ap = argparse.ArgumentParser()
ap.add_argument("--dir", required=True)
ap.add_argument("--port-file", required=True)
ap.add_argument("--type", choices=["artifactory", "nexus"], required=True)
ap.add_argument("--token", default="")
ap.add_argument("--user", default="")
ap.add_argument("--password", default="")
ap.add_argument("--log", default="")
args = ap.parse_args()


class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *a):
        if args.log:
            with open(args.log, "a") as f:
                f.write("%s %s\n" % (self.command, self.path))

    def authorized(self):
        h = self.headers.get("Authorization", "")
        if args.type == "artifactory":
            return h == "Bearer " + args.token
        want = "Basic " + base64.b64encode((args.user + ":" + args.password).encode()).decode()
        return h == want

    def target(self):
        rel = os.path.normpath(self.path.split("?")[0].lstrip("/"))
        if rel.startswith(".."):
            return None
        return os.path.join(args.dir, rel)

    def do_GET(self):
        if not self.authorized():
            self.send_response(401); self.end_headers(); return
        p = self.target()
        if not p or not os.path.isfile(p):
            self.send_response(404); self.end_headers(); return
        data = open(p, "rb").read()
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_PUT(self):
        if not self.authorized():
            self.send_response(401); self.end_headers(); return
        p = self.target()
        n = int(self.headers.get("Content-Length", "0"))
        data = self.rfile.read(n)
        want = self.headers.get("X-Checksum-Sha256")
        if want and want != hashlib.sha256(data).hexdigest():
            self.send_response(400); self.end_headers(); return
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "wb") as f:
            f.write(data)
        self.send_response(201)
        self.send_header("Content-Length", "0")
        self.end_headers()


srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
with open(args.port_file, "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
