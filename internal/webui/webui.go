// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package webui serves the embedded single page application.
//
// The React app in web/ is built by Vite into internal/webui/dist (see
// web/vite.config.ts: build.outDir = "../internal/webui/dist"). The real
// embedding is enabled with the build tag "embedui":
//
//	make web-build && go build -tags embedui ./cmd/myriarchd
//
// Without the tag (plain `go build ./...`, unit tests) a placeholder page is
// served, so the Go code compiles before the UI exists.
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Handler serves the SPA: existing files from the embedded FS (with long
// cache headers for hashed assets under /assets/), and index.html for every
// other path (client-side routing). Paths under /api/, /v1/opamp, /healthz,
// /readyz and /metrics are never routed here (the server mounts them first).
func Handler() http.Handler {
	files := FS()
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "." {
			serveIndex(w, r, files)
			return
		}
		if f, err := fs.Stat(files, p); err == nil && !f.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			// a missing hashed asset is an error, not the application shell
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, files)
	})
}

// ContentSecurityPolicy of the UI: scripts only from the application itself
// (the build has no inline scripts), inline styles for the component library,
// fonts and images from the application or embedded data: URIs (the build
// inlines small font files), no framing, no plugins, connections only to the
// own origin (REST, SSE).
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// setSecurityHeaders adds the browser hardening headers of every UI response.
func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

func serveIndex(w http.ResponseWriter, r *http.Request, files fs.FS) {
	b, err := fs.ReadFile(files, "index.html")
	if err != nil {
		http.Error(w, "UI not available", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
