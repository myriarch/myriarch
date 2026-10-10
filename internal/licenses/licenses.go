// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package licenses serves the license texts that are compiled into myriarchd:
// the project license and the notices of the third-party software in the
// binaries. The routes are public (no credentials): Apache-2.0 and the
// licenses of the bundled components require that the texts travel with every
// copy of the software, and a copy of the web UI has to be able to point to
// them without a session.
//
// Routes (the handler matches on the request path and works behind any mux):
//
//	/licenses                        index (text/plain) naming the two files
//	/licenses/LICENSE                the project license
//	/licenses/THIRD_PARTY_NOTICES    notices and license texts of third-party software
//	/licenses/<anything else>        404
//
// Only exact names are looked up in a fixed table; the path is never used to
// open a file, so there is no traversal surface.
package licenses

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/myriarch/myriarch"
)

// Names of the served files (the last path segment).
const (
	NameLicense = "LICENSE"
	NameNotices = "THIRD_PARTY_NOTICES"
)

// Base is the URL path prefix of the routes.
const Base = "/licenses"

const contentType = "text/plain; charset=utf-8"

type document struct {
	body string
	etag string
}

func newDocument(body string) document {
	sum := sha256.Sum256([]byte(body))
	return document{body: body, etag: `"` + hex.EncodeToString(sum[:])[:32] + `"`}
}

// Handler returns the handler for GET and HEAD of /licenses and everything
// below /licenses/, serving the embedded texts. Every other name is a 404.
func Handler() http.Handler {
	return newHandler(myriarch.License(), myriarch.ThirdPartyNotices())
}

func newHandler(license, notices string) http.Handler {
	docs := map[string]document{
		NameLicense: newDocument(license),
		NameNotices: newDocument(notices),
	}
	index := newDocument("Licenses of myriarch\n" +
		"\n" +
		Base + "/" + NameLicense + "\n" +
		"    The project license (Apache-2.0).\n" +
		Base + "/" + NameNotices + "\n" +
		"    Notices and license texts of the third-party software in the binaries and the web UI.\n")
	notFound := newDocument("not found\n")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		doc, ok := lookup(r.URL.Path, docs, index)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(notFound.body))
			}
			return
		}
		w.Header().Set("ETag", doc.etag)
		// A zero modification time: no Last-Modified, validation by ETag only.
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(doc.body))
	})
}

func lookup(path string, docs map[string]document, index document) (document, bool) {
	rest, ok := strings.CutPrefix(path, Base)
	if !ok {
		return document{}, false
	}
	switch rest {
	case "", "/":
		return index, true
	}
	name, ok := strings.CutPrefix(rest, "/")
	if !ok {
		return document{}, false
	}
	doc, ok := docs[name]
	return doc, ok
}

func setHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-cache")
}
