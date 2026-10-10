// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HDR-1: every UI response carries the browser hardening headers; the policy allows the SPA but no inline scripts,
// no framing and no foreign connections.
func TestUIHeaders(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/agents", "/rollouts/123", "/favicon.svg"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		hdr := rec.Header()
		if hdr.Get("X-Frame-Options") != "DENY" || hdr.Get("Referrer-Policy") != "no-referrer" || hdr.Get("Cross-Origin-Opener-Policy") != "same-origin" {
			t.Errorf("%s: %v", path, hdr)
		}
		csp := hdr.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "font-src 'self' data:", "object-src 'none'", "base-uri 'self'", "connect-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP lacks %q: %s", path, want, csp)
			}
		}
		if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "*") {
			t.Errorf("%s: CSP too permissive: %s", path, csp)
		}
	}
}

// A missing hashed asset is an error (404), not the application shell.
func TestMissingAssetIs404(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/does-not-exist-abc123.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: %d", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("headers are also set on errors")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/some/client/side/route", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("client side route must get the shell: %d", rec.Code)
	}
}
