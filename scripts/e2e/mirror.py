# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Local release mirror for the e2e tests (artifacts.sourceBaseUrl). Serves, for any version, the
# layout of the upstream releases:  /v<ver>/otelcol-contrib_<ver>_<os>_<arch>.tar.gz  and  ....sha256
# with a small synthetic archive, so that the artifact cache, the download tokens and the
# myriarch-updater/myriarch-sim download + SHA-256 check run without any network access.
# Environment: PORT (default 19998).
import gzip, hashlib, io, os, re, tarfile
from http.server import BaseHTTPRequestHandler, HTTPServer

ASSET = re.compile(r'^/v(\d+\.\d+\.\d+)/(otelcol-contrib_\d+\.\d+\.\d+_(?:linux|windows|darwin)_[a-z0-9]+\.(?:tar\.gz|zip))(\.sha256)?$')


def archive(name: str) -> bytes:
    # deterministic bytes (mtime 0): the checksum file and the archive are separate requests
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode='w') as tf:
        data = (b'synthetic otelcol-contrib for e2e: ' + name.encode() + b'\n') * 2000
        info = tarfile.TarInfo('otelcol-contrib')
        info.size, info.mode = len(data), 0o755
        tf.addfile(info, io.BytesIO(data))
    out = io.BytesIO()
    with gzip.GzipFile(fileobj=out, mode='wb', mtime=0) as gz:
        gz.write(raw.getvalue())
    return out.getvalue()


class H(BaseHTTPRequestHandler):
    def do_GET(self):
        m = ASSET.match(self.path)
        if not m:
            self.send_response(404); self.end_headers(); return
        name = m.group(2)
        body = archive(name)
        if m.group(3):
            body = ('%s  %s\n' % (hashlib.sha256(body).hexdigest(), name)).encode()
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


HTTPServer(('127.0.0.1', int(os.environ.get('PORT', '19998'))), H).serve_forever()
