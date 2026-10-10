// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package licenses

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myriarch/myriarch"
)

func get(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func checkHeaders(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	want := map[string]string{
		"Content-Type":            "text/plain; charset=utf-8",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"X-Frame-Options":         "DENY",
		"Cache-Control":           "no-cache",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: header %s = %q, want %q", what, k, got, v)
		}
	}
}

func TestServesTheEmbeddedTexts(t *testing.T) {
	h := Handler()
	files := map[string]string{
		"/licenses/LICENSE":             myriarch.License(),
		"/licenses/THIRD_PARTY_NOTICES": myriarch.ThirdPartyNotices(),
	}
	for path, text := range files {
		if text == "" {
			t.Fatalf("%s: the embedded text is empty", path)
		}
		rec := get(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, rec.Code)
		}
		if rec.Body.String() != text {
			t.Errorf("GET %s: the body differs from the embedded text", path)
		}
		checkHeaders(t, rec, "GET "+path)
		if etag := rec.Header().Get("ETag"); !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) < 10 {
			t.Errorf("GET %s: ETag %q is not a quoted validator", path, etag)
		}

		head := get(t, h, http.MethodHead, path, nil)
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			t.Errorf("HEAD %s: status %d, %d body bytes", path, head.Code, head.Body.Len())
		}
		checkHeaders(t, head, "HEAD "+path)
		if head.Header().Get("ETag") != rec.Header().Get("ETag") {
			t.Errorf("HEAD %s: ETag differs from GET", path)
		}
	}
	if files["/licenses/LICENSE"] == files["/licenses/THIRD_PARTY_NOTICES"] {
		t.Error("the two files must differ")
	}
}

func TestIndexNamesBothFiles(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/licenses", "/licenses/"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := get(t, h, method, path, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: status %d", method, path, rec.Code)
			}
			checkHeaders(t, rec, method+" "+path)
			if method == http.MethodHead {
				if rec.Body.Len() != 0 {
					t.Errorf("HEAD %s has a body", path)
				}
				continue
			}
			for _, want := range []string{"/licenses/LICENSE", "/licenses/THIRD_PARTY_NOTICES"} {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("GET %s: the index does not name %s:\n%s", path, want, rec.Body.String())
				}
			}
		}
	}
}

func TestIfNoneMatchReturns304(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/licenses", "/licenses/LICENSE", "/licenses/THIRD_PARTY_NOTICES"} {
		first := get(t, h, http.MethodGet, path, nil)
		etag := first.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", path)
		}
		rec := get(t, h, http.MethodGet, path, map[string]string{"If-None-Match": etag})
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("%s with a matching ETag: status %d, %d body bytes", path, rec.Code, rec.Body.Len())
		}
		rec = get(t, h, http.MethodGet, path, map[string]string{"If-None-Match": `"other"`})
		if rec.Code != http.StatusOK {
			t.Errorf("%s with another ETag: status %d", path, rec.Code)
		}
	}
	a := get(t, h, http.MethodGet, "/licenses/LICENSE", nil).Header().Get("ETag")
	b := get(t, h, http.MethodGet, "/licenses/THIRD_PARTY_NOTICES", nil).Header().Get("ETag")
	if a == b {
		t.Error("different texts must have different ETags")
	}
}

func TestUnknownNamesAre404(t *testing.T) {
	h := Handler()
	for _, path := range []string{
		"/licenses/NOTICE", "/licenses/license", "/licenses/LICENSE/", "/licenses/LICENSE/x",
		"/licenses/THIRD_PARTY_NOTICES.txt", "/licensesLICENSE", "/licenses2", "/other",
	} {
		rec := get(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("GET %s: Content-Type %q", path, ct)
		}
		if strings.Contains(rec.Body.String(), "Apache License") {
			t.Errorf("GET %s: a 404 must not carry license content", path)
		}
	}
}

func TestPathTraversalServesNothingElse(t *testing.T) {
	h := Handler()
	for _, path := range []string{
		"/licenses/../x", "/licenses/%2e%2e", "/licenses/%2e%2e/%2e%2e/etc/passwd", "/licenses/..%2fLICENSE",
		"/licenses/./LICENSE", "/licenses//LICENSE", "/licenses/LICENSE%00", "/licenses/%2fetc/passwd",
		"/licenses/..\\LICENSE", "/licenses/....//LICENSE",
	} {
		rec := get(t, h, http.MethodGet, path, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s: status 200 (body %.40q)", path, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "Apache License") || strings.Contains(rec.Body.String(), "Third-party software notices") {
			t.Errorf("GET %s: served license content", path)
		}
	}
}

func TestOtherMethodsAreRejected(t *testing.T) {
	h := Handler()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := get(t, h, m, "/licenses/LICENSE", nil)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, rec.Code)
		}
		if a := rec.Header().Get("Allow"); a != "GET, HEAD" {
			t.Errorf("%s: Allow %q", m, a)
		}
	}
}

func TestNewHandlerServesGivenTexts(t *testing.T) {
	h := newHandler("license text\n", "notices text\n")
	if got := get(t, h, http.MethodGet, "/licenses/LICENSE", nil).Body.String(); got != "license text\n" {
		t.Errorf("LICENSE: %q", got)
	}
	if got := get(t, h, http.MethodGet, "/licenses/THIRD_PARTY_NOTICES", nil).Body.String(); got != "notices text\n" {
		t.Errorf("THIRD_PARTY_NOTICES: %q", got)
	}
}
