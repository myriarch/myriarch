// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/myriarch/myriarch/internal/config"
)

// sourceAuth adds the credentials of artifacts.sourceAuth (bearer token or
// basic authentication) to the requests to the artifact mirror.
//
// The credentials go ONLY to requests whose origin (scheme, host, port) equals
// the origin of artifacts.sourceBaseUrl. That is decided for every single
// request at the transport, so it also holds for the requests that follow a
// redirect: a mirror that redirects to a blob store on another host never
// receives the credentials there, and the Sigstore TUF repository only if it
// is on the same origin as the mirror. The files are read again when they
// change (rotation without restart). Values never appear in errors or logs.
type sourceAuth struct {
	origin  string
	token   *secretFile
	user    string
	passwd  *secretFile
	bearer  bool
	checked bool
}

// secretFile is a file with a secret that is read again when it changes.
type secretFile struct {
	path string

	mu    sync.Mutex
	value string
	stamp struct {
		mod  time.Time
		size int64
	}
	loaded bool
}

// get returns the trimmed content of the file. A file that cannot be read any
// more keeps the last good value (a rotation may replace the file in two steps),
// an empty file is an error.
func (f *secretFile) get() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, err := os.Stat(f.path)
	if err != nil {
		if f.loaded {
			return f.value, nil
		}
		return "", fmt.Errorf("cannot read file: %w", pathErr(err))
	}
	if f.loaded && st.ModTime().Equal(f.stamp.mod) && st.Size() == f.stamp.size {
		return f.value, nil
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		if f.loaded {
			return f.value, nil
		}
		return "", fmt.Errorf("cannot read file: %w", pathErr(err))
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		if f.loaded {
			return f.value, nil
		}
		return "", errors.New("file is empty")
	}
	f.value, f.loaded = v, true
	f.stamp.mod, f.stamp.size = st.ModTime(), st.Size()
	return v, nil
}

func pathErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// originOf returns "scheme://host:port" of a URL with the default port made
// explicit.
func originOf(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// newSourceAuth validates the settings and reads the files once (so a wrong
// path stops myriarchd at startup). It returns nil if no credentials are set.
func newSourceAuth(cfg config.ArtifactSourceAuth, baseURL string) (*sourceAuth, error) {
	if !cfg.Configured() {
		return nil, nil
	}
	switch {
	case cfg.TokenFile != "" && (cfg.Username != "" || cfg.PasswordFile != ""):
		return nil, errors.New("artifacts.sourceAuth: use either tokenFile or username + passwordFile, not both")
	case cfg.TokenFile == "" && (cfg.Username == "" || cfg.PasswordFile == ""):
		return nil, errors.New("artifacts.sourceAuth: needs tokenFile (bearer) or username together with passwordFile (basic)")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("artifacts.sourceBaseUrl %q is invalid", redactURL(baseURL))
	}
	a := &sourceAuth{origin: originOf(u)}
	if cfg.TokenFile != "" {
		a.bearer, a.token = true, &secretFile{path: cfg.TokenFile}
		if _, err := a.token.get(); err != nil {
			return nil, fmt.Errorf("artifacts.sourceAuth.tokenFile: %w", err)
		}
	} else {
		a.user, a.passwd = cfg.Username, &secretFile{path: cfg.PasswordFile}
		if _, err := a.passwd.get(); err != nil {
			return nil, fmt.Errorf("artifacts.sourceAuth.passwordFile: %w", err)
		}
	}
	return a, nil
}

// header returns the Authorization header value.
func (a *sourceAuth) header() (string, error) {
	if a.bearer {
		t, err := a.token.get()
		if err != nil {
			return "", fmt.Errorf("artifacts.sourceAuth.tokenFile: %w", err)
		}
		return "Bearer " + t, nil
	}
	p, err := a.passwd.get()
	if err != nil {
		return "", fmt.Errorf("artifacts.sourceAuth.passwordFile: %w", err)
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(a.user+":"+p)), nil
}

// wrap returns a RoundTripper that adds the credentials to the requests to the
// origin of the mirror and passes everything else through unchanged.
func (a *sourceAuth) wrap(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &authTransport{rt: rt, auth: a}
}

type authTransport struct {
	rt   http.RoundTripper
	auth *sourceAuth
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if originOf(req.URL) != t.auth.origin || req.Header.Get("Authorization") != "" {
		return t.rt.RoundTrip(req)
	}
	h, err := t.auth.header()
	if err != nil {
		return nil, err
	}
	// a RoundTripper must not modify the request it is given
	r2 := req.Clone(req.Context())
	r2.Header.Set("Authorization", h)
	return t.rt.RoundTrip(r2)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the
// underlying transport.
func (t *authTransport) CloseIdleConnections() {
	type closer interface{ CloseIdleConnections() }
	if c, ok := t.rt.(closer); ok {
		c.CloseIdleConnections()
	}
}

// statusError describes an unsuccessful HTTP status of the mirror; for 401 and
// 403 it points to artifacts.sourceAuth.
func (c *Cache) statusError(rawURL string, code int) error {
	msg := fmt.Sprintf("GET %s: HTTP %d", redactURL(rawURL), code)
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		if c.auth != nil && originOf(mustURL(rawURL)) == c.auth.origin {
			msg += " (the mirror rejected the credentials of artifacts.sourceAuth: check the token or password and the permissions of the account)"
		} else if c.auth == nil {
			msg += " (the mirror requires authentication: set artifacts.sourceAuth)"
		}
	}
	return errors.New(msg)
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{}
	}
	return u
}
