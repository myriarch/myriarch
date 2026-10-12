# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""HTTP server with scripted faults for the download retry tests (ci/tests/download-retry-test.sh, download-retry.ps1).

usage: python3 -I http-fault-server.py --rules FILE --log FILE --port-file FILE [--root DIR] [--port N]

Binds 127.0.0.1 only (port 0 = an ephemeral port by default) and writes the port into --port-file after the bind (atomically).
--rules is a JSON object {"<path>": [status, ...] | status}. A list answers the first requests to that path with these statuses
in order and serves the path normally afterwards; a number answers every request to the path with that status. Serving normally
means the file <root>/<path without its first segment> with 200 (the first segment is a label of the test case), otherwise 404.
Every request appends one line "<path> <status>" to --log. Fault answers have the body "fault <status>".
The test starts the server in the background and stops it again. Standard library only.
"""
import argparse
import http.server
import json
import os
import threading


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rules", required=True)
    ap.add_argument("--log", required=True)
    ap.add_argument("--port-file", required=True)
    ap.add_argument("--root", default=".")
    ap.add_argument("--port", type=int, default=0)
    args = ap.parse_args()

    with open(args.rules, encoding="utf-8") as f:
        rules = json.load(f)
    root = os.path.realpath(args.root)
    lock = threading.Lock()
    counts = {}
    log = open(args.log, "a", buffering=1, encoding="utf-8")

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self):
            path = self.path.split("?", 1)[0]
            with lock:
                n = counts.get(path, 0)
                counts[path] = n + 1
            rule = rules.get(path)
            body = b""
            if isinstance(rule, int):
                status = rule
            elif isinstance(rule, list) and n < len(rule):
                status = int(rule[n])
            else:
                status = 404
                parts = path.lstrip("/").split("/", 1)
                if len(parts) == 2:
                    target = os.path.realpath(os.path.join(root, parts[1]))
                    if target.startswith(root + os.sep) and os.path.isfile(target):
                        with open(target, "rb") as fh:
                            body = fh.read()
                        status = 200
            if status != 200:
                body = ("fault %d" % status).encode()
            with lock:
                log.write("%s %d\n" % (path, status))
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    tmp = args.port_file + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(str(server.server_address[1]))
    os.replace(tmp, args.port_file)
    server.serve_forever()


if __name__ == "__main__":
    main()
