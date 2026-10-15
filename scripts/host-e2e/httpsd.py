#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Static HTTPS file server of the host end-to-end test (scripts/host-e2e/lib.sh, he_https_start).

    python3 -I scripts/host-e2e/httpsd.py --directory DIR --port PORT --cert FILE --key FILE [--bind 127.0.0.1]

It serves the files below DIR over TLS with the throwaway certificate of the test and nothing else: no directory listing and
only GET and HEAD. It binds to a loopback IPv4 address and refuses any other bind address. The installers need an https:// URL
for the release files, and myriarchd reads its artifact source the same way; a plain "python3 -m http.server" speaks HTTP
only. Each request becomes one line "<time> <client> <request line> <status>" on standard error, without query strings or
headers, so the log can be uploaded as an artifact.
"""

import argparse
import functools
import http.server
import ipaddress
import re
import ssl
import sys


class Handler(http.server.SimpleHTTPRequestHandler):
    server_version = "host-e2e-httpd"
    timeout = 60  # a client that stalls does not hold a thread for ever

    def list_directory(self, path):  # no directory listings
        self.send_error(404, "Not Found")
        return None

    def do_POST(self):  # GET and HEAD only
        self.send_error(405, "Method Not Allowed")

    do_PUT = do_DELETE = do_PATCH = do_POST

    def log_message(self, fmt, *args):
        # the request line without its query string
        line = re.sub(r"\?[^ \"]*", "", fmt % args)
        sys.stderr.write("%s %s %s\n" % (self.log_date_time_string(), self.client_address[0], line))
        sys.stderr.flush()


class Server(http.server.ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address, handler, context):
        super().__init__(address, handler)
        self.context = context

    def get_request(self):
        # The handshake happens in the thread of the request, not in the accept loop: a client that connects and says
        # nothing must not stop the server from serving the others.
        sock, address = super().get_request()
        return self.context.wrap_socket(sock, server_side=True, do_handshake_on_connect=False), address

    def handle_error(self, request, client_address):
        exc = sys.exc_info()[1]
        sys.stderr.write("%s: connection error: %s\n" % (client_address[0], exc))
        sys.stderr.flush()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--directory", required=True, help="document root")
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--cert", required=True, help="PEM file with the server certificate")
    parser.add_argument("--key", required=True, help="PEM file with the private key")
    parser.add_argument("--bind", default="127.0.0.1", help="loopback IPv4 address to listen on (default 127.0.0.1)")
    args = parser.parse_args(argv)

    try:
        if not ipaddress.IPv4Address(args.bind).is_loopback:
            raise ValueError("not a loopback address")
    except ValueError:
        parser.error("--bind must be a loopback IPv4 address (127.0.0.0/8), got %r" % args.bind)

    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(args.cert, args.key)
    handler = functools.partial(Handler, directory=args.directory)
    server = Server((args.bind, args.port), handler, context)
    sys.stderr.write("serving %s on https://%s:%d/\n" % (args.directory, args.bind, args.port))
    sys.stderr.flush()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
