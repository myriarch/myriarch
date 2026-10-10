# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Mock receiver for the e2e tests: accepts the signed myriarch webhook (executor type webhook,
# notification channel), AWX job launches (POST /api/v2/job_templates/<id>/launch/) and tells the
# myriarch-sim control API to flip the collector version (the update "happens" after the answer).
# Environment: PORT (default 9999), SECRET (HMAC secret, default devsecret), SIM_CONTROL (host:port[,host:port...] control APIs of the myriarch-sim processes).
import hmac, hashlib, json, os, urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
SECRET = os.environ.get('SECRET', 'devsecret').encode()
SIMS = ['http://%s/control/upgrade' % h for h in os.environ.get('SIM_CONTROL', '127.0.0.1:9465').split(',') if h]
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        ts = self.headers.get('X-Myriarch-Timestamp', '')
        sig = self.headers.get('X-Myriarch-Signature', '')
        ok = (not sig) or hmac.compare_digest(sig, 'sha256=' + hmac.new(SECRET, ts.encode() + b'.' + body, hashlib.sha256).hexdigest())
        data = json.loads(body or b'{}')
        print(self.path, 'signature_ok=%s' % ok, json.dumps(data)[:300], flush=True)
        if self.path.startswith('/api/v2/job_templates/'):           # AWX launch
            hosts = data.get('limit', '').split(',')
            version = data.get('extra_vars', {}).get('otelcol_version')
        else:                                                          # generic webhook
            hosts = [t['hostname'] for t in data.get('targets', [])]
            version = data.get('toVersion')
        if version and hosts:
            for sim in SIMS:                                              # one myriarch-sim process per entry
                try:
                    req = urllib.request.Request(sim, json.dumps({'hostnames': hosts, 'version': version}).encode(),
                                                 {'Content-Type': 'application/json'})
                    urllib.request.urlopen(req)
                except Exception as e:
                    print('sim control %s: %s' % (sim, e), flush=True)
        self.send_response(201 if 'job_templates' in self.path else 202); self.end_headers()
        self.wfile.write(b'{"job": 4711}')
HTTPServer(('127.0.0.1', int(os.environ.get('PORT', '9999'))), H).serve_forever()
