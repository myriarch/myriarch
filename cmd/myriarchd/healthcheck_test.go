// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/refdoc"
	"github.com/myriarch/myriarch/internal/testutil/clitest"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// healthcheckRun runs "myriarchd healthcheck" with the arguments and returns the exit code and what it wrote to stdout and stderr.
func healthcheckRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = execute(append([]string{"healthcheck"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// newTestToken returns an enrollment token of the documented form (not a real one).
func newTestToken(t *testing.T) string {
	t.Helper()
	tok, err := authn.NewToken(brand.EnrollmentTokenPrefix)
	require.NoError(t, err)
	return tok
}

// writeFile writes a file below dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// healthStub is a server with /readyz and an OpAMP endpoint: it answers like myriarchd does (401 without the token it wants,
// the upgrade with it) and records what it saw.
type healthStub struct {
	*httptest.Server

	wantToken string // the token the OpAMP endpoint accepts; empty: it accepts every connection
	readyz    int    // status of /readyz (default 200)
	opampPath string // default /v1/opamp
	opamp     func(http.ResponseWriter, *http.Request) bool

	mu         sync.Mutex
	ready      int      // requests of /readyz
	upgrades   int      // requests of the OpAMP endpoint
	auths      []string // Authorization headers of these requests
	userAgents []string // User-Agent headers of all requests
	overTLS    bool     // a request arrived over TLS
	messages   int      // data messages of upgraded connections
	closeErr   error    // the error that ended the read loop of the last upgraded connection
	finished   chan struct{}
	finishing  sync.Once
}

// seen is what the stub has recorded so far.
type seen struct {
	ready, upgrades, messages int
	auths, userAgents         []string
	overTLS                   bool
}

func newHealthStub(t *testing.T, tls bool, mod ...func(*healthStub)) *healthStub {
	t.Helper()
	s := &healthStub{readyz: http.StatusOK, opampPath: "/v1/opamp", finished: make(chan struct{})}
	for _, m := range mod {
		m(s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.ready++
		s.userAgents = append(s.userAgents, r.UserAgent())
		s.overTLS = s.overTLS || r.TLS != nil
		s.mu.Unlock()
		w.WriteHeader(s.readyz)
	})
	mux.HandleFunc(s.opampPath, s.serveOpAMP)
	if tls {
		s.Server = httptest.NewTLSServer(mux)
	} else {
		s.Server = httptest.NewServer(mux)
	}
	t.Cleanup(s.Close)
	return s
}

func (s *healthStub) serveOpAMP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.upgrades++
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.userAgents = append(s.userAgents, r.UserAgent())
	s.overTLS = s.overTLS || r.TLS != nil
	s.mu.Unlock()
	if s.opamp != nil && s.opamp(w, r) {
		return
	}
	if s.wantToken != "" && r.Header.Get("Authorization") != "Bearer "+s.wantToken {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			s.mu.Lock()
			s.closeErr = err
			s.mu.Unlock()
			s.finishing.Do(func() { close(s.finished) })
			return
		}
		s.mu.Lock()
		s.messages++
		s.mu.Unlock()
	}
}

// waitClosed waits until the upgraded connection has ended and returns the close code that the client sent (-1 if it did not close
// in the normal way).
func (s *healthStub) waitClosed(t *testing.T) int {
	t.Helper()
	select {
	case <-s.finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection of the check did not end")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ce *websocket.CloseError
	if errors.As(s.closeErr, &ce) {
		return ce.Code
	}
	return -1
}

func (s *healthStub) seen() seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seen{ready: s.ready, upgrades: s.upgrades, messages: s.messages,
		auths: append([]string(nil), s.auths...), userAgents: append([]string(nil), s.userAgents...), overTLS: s.overTLS}
}

// assertNoSecret fails when a secret appears in any of the outputs.
func assertNoSecret(t *testing.T, secret string, outputs ...string) {
	t.Helper()
	require.NotEmpty(t, secret)
	for _, o := range outputs {
		assert.NotContains(t, o, secret, "the output contains the secret")
	}
}

func TestHealthcheckWithoutATokenExpectsTheRefusal(t *testing.T) {
	stub := newHealthStub(t, false, func(s *healthStub) { s.wantToken = newTestToken(t) })
	code, stdout, stderr := healthcheckRun(t, "--url", stub.URL)
	assert.Equal(t, 0, code, stderr)
	assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n"+
		"opamp ok: the upgrade at /v1/opamp was refused with 401 (no token given, as expected)\n", stdout)
	assert.Empty(t, stderr)
	got := stub.seen()
	assert.Equal(t, [3]int{1, 1, 0}, [3]int{got.ready, got.upgrades, got.messages})
	assert.Equal(t, []string{""}, got.auths, "no Authorization header without a token")
	assert.Equal(t, []string{"myriarchd-healthcheck/0.0.0-dev", "myriarchd-healthcheck/0.0.0-dev"}, got.userAgents)
}

func TestHealthcheckWithATokenExpectsTheUpgradeAndSendsNoMessage(t *testing.T) {
	tok := newTestToken(t)
	stub := newHealthStub(t, false, func(s *healthStub) { s.wantToken = tok })
	// white space around the token does not matter (a file written by `echo` ends with a newline)
	file := writeFile(t, t.TempDir(), "token", "\n  "+tok+" \n")
	code, stdout, stderr := healthcheckRun(t, "--url", stub.URL+"/", "--token-file", file)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n"+
		"opamp ok: the upgrade at /v1/opamp was accepted with 101 (the server accepts the token)\n", stdout)
	assert.Empty(t, stderr)
	assertNoSecret(t, tok, stdout, stderr)

	// the connection ends with the normal close message and carries no OpAMP message
	assert.Equal(t, websocket.CloseNormalClosure, stub.waitClosed(t))
	got := stub.seen()
	assert.Equal(t, [3]int{1, 1, 0}, [3]int{got.ready, got.upgrades, got.messages})
	assert.Equal(t, []string{"Bearer " + tok}, got.auths)
}

func TestHealthcheckAcceptsAServerThatTakesConnectionsWithoutAToken(t *testing.T) {
	// the development mode: opamp.requireEnrollmentToken is off, so the upgrade without a token is accepted
	stub := newHealthStub(t, false)
	code, stdout, stderr := healthcheckRun(t, "--url", stub.URL)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "opamp ok: the upgrade at /v1/opamp was accepted with 101 (no token given; the server takes connections without one)\n")
	assert.Equal(t, websocket.CloseNormalClosure, stub.waitClosed(t))
	assert.Zero(t, stub.seen().messages)
}

func TestHealthcheckRefusedTokenIsUnhealthy(t *testing.T) {
	good, offered := newTestToken(t), newTestToken(t)
	stub := newHealthStub(t, false, func(s *healthStub) { s.wantToken = good })
	file := writeFile(t, t.TempDir(), "token", offered)
	code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--token-file", file)
	assert.Equal(t, exitUnhealthy, code)
	assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n", stdout)
	assert.Contains(t, stderr, "myriarchd: opamp failed: the upgrade at /v1/opamp was refused with 401 although a token was given")
	assertNoSecret(t, offered, stdout, stderr)
	assertNoSecret(t, good, stdout, stderr)
}

func TestHealthcheckReadyzMustAnswer200(t *testing.T) {
	for _, tc := range []struct {
		status int
		hint   string
	}{
		{http.StatusServiceUnavailable, "the server is not ready, its log names the cause"},
		{http.StatusInternalServerError, ""},
		{http.StatusNotFound, "there is no myriarchd at this address"},
		{http.StatusNoContent, ""},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			stub := newHealthStub(t, false, func(s *healthStub) { s.readyz = tc.status })
			code, stdout, stderr := healthcheckRun(t, "--url", stub.URL)
			assert.Equal(t, exitUnhealthy, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, fmt.Sprintf("myriarchd: readyz failed: GET %s/readyz answered %d, want 200", stub.URL, tc.status))
			assert.Contains(t, stderr, tc.hint)
			assert.Zero(t, stub.seen().upgrades, "the OpAMP endpoint is not asked while /readyz fails")
		})
	}
}

func TestHealthcheckFollowsNoRedirects(t *testing.T) {
	other := newHealthStub(t, false)
	tok := newTestToken(t)
	file := writeFile(t, t.TempDir(), "token", tok)

	t.Run("readyz", func(t *testing.T) {
		redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
		}))
		defer redirecting.Close()
		code, stdout, stderr := healthcheckRun(t, "--url", redirecting.URL)
		assert.Equal(t, exitUnhealthy, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, "answered 302, want 200: redirects are not followed")
		got := other.seen()
		assert.Zero(t, got.ready+got.upgrades, "the redirect target received a request")
	})

	t.Run("opamp, with a token", func(t *testing.T) {
		stub := newHealthStub(t, false, func(s *healthStub) {
			s.opamp = func(w http.ResponseWriter, r *http.Request) bool {
				http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
				return true
			}
		})
		code, _, stderr := healthcheckRun(t, "--url", stub.URL, "--token-file", file)
		assert.Equal(t, exitUnhealthy, code)
		assert.Contains(t, stderr, "the upgrade at /v1/opamp was answered with 307, want 101: redirects are not followed")
		assertNoSecret(t, tok, stderr)
		got := other.seen()
		assert.Zero(t, got.ready+got.upgrades, "the redirect target received a request")
		assert.Empty(t, got.auths, "the token went to the redirect target")
	})
}

func TestHealthcheckOpAMPPath(t *testing.T) {
	t.Run("another path of the server", func(t *testing.T) {
		stub := newHealthStub(t, false, func(s *healthStub) { s.opampPath = "/agents/opamp"; s.wantToken = newTestToken(t) })
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--opamp-path", "/agents/opamp")
		assert.Equal(t, 0, code, stderr)
		assert.Contains(t, stdout, "opamp ok: the upgrade at /agents/opamp was refused with 401")
	})
	t.Run("nothing is served at the path", func(t *testing.T) {
		stub := newHealthStub(t, false)
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--opamp-path", "/wrong")
		assert.Equal(t, exitUnhealthy, code)
		assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n", stdout)
		assert.Contains(t, stderr, "the upgrade at /wrong was answered with 404, want 401: nothing is served at this path, check --opamp-path")
	})
	for status, hint := range map[int]string{
		http.StatusServiceUnavailable:  "the server is shutting down, is full or cannot check the credentials",
		http.StatusTooManyRequests:     "the server limits the connections",
		http.StatusInternalServerError: "",
	} {
		t.Run("status "+strconv.Itoa(status), func(t *testing.T) {
			stub := newHealthStub(t, false, func(s *healthStub) {
				s.opamp = func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(status); return true }
			})
			code, _, stderr := healthcheckRun(t, "--url", stub.URL)
			assert.Equal(t, exitUnhealthy, code)
			assert.Contains(t, stderr, fmt.Sprintf("was answered with %d, want 401", status))
			assert.Contains(t, stderr, hint)
		})
	}
	t.Run("an answer that is no WebSocket handshake", func(t *testing.T) {
		stub := newHealthStub(t, false, func(s *healthStub) {
			s.opamp = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Connection", "Upgrade")
				w.Header().Set("Upgrade", "websocket")
				w.WriteHeader(http.StatusSwitchingProtocols) // without a valid Sec-WebSocket-Accept
				return true
			}
		})
		code, _, stderr := healthcheckRun(t, "--url", stub.URL)
		assert.Equal(t, exitUnhealthy, code)
		assert.Contains(t, stderr, "the answer is not a valid WebSocket handshake")
	})
}

func TestHealthcheckNoAnswerIsExitCode3(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		stub := newHealthStub(t, false)
		url := stub.URL
		stub.Close()
		code, stdout, stderr := healthcheckRun(t, "--url", url)
		assert.Equal(t, exitUnreachable, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, "myriarchd: readyz failed: cannot reach "+url+"/readyz: ")
		assert.NotContains(t, stderr, "no answer within", "a refused connection is not a time out")
	})
	t.Run("the OpAMP endpoint is gone after /readyz", func(t *testing.T) {
		stub := newHealthStub(t, false, func(s *healthStub) {
			s.opamp = func(w http.ResponseWriter, r *http.Request) bool {
				// the connection ends without an answer
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return true
			}
		})
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL)
		assert.Equal(t, exitUnreachable, code)
		assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n", stdout)
		assert.Contains(t, stderr, "myriarchd: opamp failed: cannot reach "+stub.URL+"/v1/opamp: ")
	})
}

func TestHealthcheckTimesOut(t *testing.T) {
	block := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}
	t.Run("readyz", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(block))
		defer ts.Close()
		start := time.Now()
		code, stdout, stderr := healthcheckRun(t, "--url", ts.URL, "--timeout", "300ms")
		assert.Equal(t, exitUnreachable, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, "readyz failed: cannot reach "+ts.URL+"/readyz: no answer within 300ms")
		assert.Less(t, time.Since(start), 5*time.Second)
	})
	t.Run("the upgrade", func(t *testing.T) {
		stub := newHealthStub(t, false, func(s *healthStub) {
			s.opamp = func(w http.ResponseWriter, r *http.Request) bool { block(w, r); return true }
		})
		start := time.Now()
		// the time is for both requests: /readyz must answer well within it, also on a busy machine
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--timeout", "2s")
		assert.Equal(t, exitUnreachable, code)
		assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n", stdout)
		assert.Contains(t, stderr, "opamp failed: cannot reach "+stub.URL+"/v1/opamp: no answer within 2s")
		assert.Less(t, time.Since(start), 5*time.Second)
	})
}

// testCA returns a self-signed CA certificate as PEM.
func testCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "healthcheck test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestHealthcheckTLS(t *testing.T) {
	tok := newTestToken(t)
	dir := t.TempDir()
	tokenFile := writeFile(t, dir, "token", tok)
	wrongCA := writeFile(t, dir, "wrong.pem", string(testCA(t)))
	caOf := func(stub *healthStub, name string) string {
		return writeFile(t, dir, name, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: stub.Certificate().Raw})))
	}

	t.Run("the CA that signed the certificate", func(t *testing.T) {
		stub := newHealthStub(t, true, func(s *healthStub) { s.wantToken = tok })
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--ca-file", caOf(stub, "right.pem"), "--token-file", tokenFile)
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, "readyz ok: GET "+stub.URL+"/readyz answered 200\n"+
			"opamp ok: the upgrade at /v1/opamp was accepted with 101 (the server accepts the token)\n", stdout)
		assert.True(t, stub.seen().overTLS, "the requests did not use TLS")
	})
	t.Run("without a token", func(t *testing.T) {
		stub := newHealthStub(t, true, func(s *healthStub) { s.wantToken = tok })
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--ca-file", caOf(stub, "right.pem"))
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, stdout, "opamp ok: the upgrade at /v1/opamp was refused with 401")
		assert.True(t, stub.seen().overTLS)
	})
	for name, args := range map[string][]string{
		"another CA":                          {"--ca-file", wrongCA},
		"the system roots do not know the CA": nil,
	} {
		t.Run(name, func(t *testing.T) {
			stub := newHealthStub(t, true, func(s *healthStub) { s.wantToken = tok })
			code, stdout, stderr := healthcheckRun(t, append([]string{"--url", stub.URL, "--token-file", tokenFile}, args...)...)
			assert.Equal(t, exitUnreachable, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, "myriarchd: readyz failed: cannot reach "+stub.URL+"/readyz: ")
			assert.Contains(t, strings.ToLower(stderr), "certificate")
			assertNoSecret(t, tok, stderr)
			got := stub.seen()
			assert.Zero(t, got.upgrades, "the token travelled to a server whose certificate was never trusted")
			assert.Empty(t, got.auths)
		})
	}
}

func TestHealthcheckUsageErrors(t *testing.T) {
	stub := newHealthStub(t, false)
	dir := t.TempDir()
	good := newTestToken(t)
	secretDSN := "postgres://myriarch:hunter2-secret@db.example.com:5432/myriarch"
	bootstrap := strings.Repeat("a1b2c3d4", 6)
	tooLong := strings.Repeat("a", maxTokenFileBytes+1)
	// the text of the operating system for a file that does not exist ("no such file or directory" on Unix)
	_, statErr := os.Stat(filepath.Join(dir, "missing"))
	require.Error(t, statErr)
	notFound := osError(statErr)
	files := map[string]string{
		"empty":     writeFile(t, dir, "empty", ""),
		"blank":     writeFile(t, dir, "blank", " \n\t\n"),
		"dsn":       writeFile(t, dir, "dsn", secretDSN+"\n"),
		"bootstrap": writeFile(t, dir, "bootstrap", bootstrap+"\n"),
		"two":       writeFile(t, dir, "two", good+"\n"+good+"\n"),
		"inner":     writeFile(t, dir, "inner", "myre_ "+good[len(brand.EnrollmentTokenPrefix):]),
		"prefix":    writeFile(t, dir, "prefix", brand.EnrollmentTokenPrefix+"short"),
		"long":      writeFile(t, dir, "long", tooLong),
		"nocert":    writeFile(t, dir, "nocert", "not a certificate\n"),
		"emptyca":   writeFile(t, dir, "emptyca", ""),
	}
	type usage struct {
		name   string
		args   []string
		want   string // part of the message
		secret string // must not appear in the output
	}
	cases := []usage{
		{"scheme ws", []string{"--url", "ws://127.0.0.1:8080"}, "--url: the scheme must be http or https", ""},
		{"scheme wss", []string{"--url", "wss://127.0.0.1:8080"}, "--url: the scheme must be http or https", ""},
		{"no scheme", []string{"--url", "127.0.0.1:8080"}, "--url: is not a valid URL (write it with its scheme, for example http://127.0.0.1:8080)", ""},
		{"host with a port and no scheme", []string{"--url", "localhost:8080"}, "--url: the scheme must be http or https", ""},
		{"empty", []string{"--url", ""}, "--url: the scheme must be http or https", ""},
		{"no host", []string{"--url", "http://:8080"}, "--url: has no host", ""},
		{"credentials", []string{"--url", "http://admin:hunter2-secret@127.0.0.1:8080"}, "--url: must not contain a user name or a password", "hunter2-secret"},
		{"credentials in an invalid URL", []string{"--url", "http://admin:hunter2-secret@127.0.0.1:80 80"}, "--url: is not a valid URL", "hunter2-secret"},
		{"path", []string{"--url", "http://127.0.0.1:8080/api"}, "--url: must not contain a path", ""},
		{"query", []string{"--url", "http://127.0.0.1:8080/?token=x"}, "--url: must not contain a query or a fragment", ""},
		{"fragment", []string{"--url", "http://127.0.0.1:8080/#x"}, "--url: must not contain a query or a fragment", ""},
		{"port out of range", []string{"--url", "http://127.0.0.1:99999"}, "--url: has a port outside 1 to 65535", ""},
		{"opamp path without a slash", []string{"--url", stub.URL, "--opamp-path", "v1/opamp"}, `--opamp-path: must start with "/"`, ""},
		{"opamp path with a query", []string{"--url", stub.URL, "--opamp-path", "/v1/opamp?x=1"}, `--opamp-path: must not contain "?" or "#"`, ""},
		{"opamp path with a space", []string{"--url", stub.URL, "--opamp-path", "/v1/op amp"}, "--opamp-path: must not contain spaces or control characters", ""},
		{"zero timeout", []string{"--url", stub.URL, "--timeout", "0"}, "--timeout: must be greater than 0", ""},
		{"negative timeout", []string{"--url", stub.URL, "--timeout", "-5s"}, "--timeout: must be greater than 0", ""},
		{"timeout without a unit", []string{"--url", stub.URL, "--timeout", "10"}, "invalid argument", ""},
		{"unknown flag", []string{"--url", stub.URL, "--nope"}, "unknown flag: --nope", ""},
		{"argument", []string{stub.URL}, "healthcheck takes no arguments", ""},
		{"token file that is missing", []string{"--url", stub.URL, "--token-file", filepath.Join(dir, "missing")}, "--token-file: cannot read " + filepath.Join(dir, "missing") + ": " + notFound, ""},
		{"token file that is a directory", []string{"--url", stub.URL, "--token-file", dir}, "--token-file: " + dir + " is not a regular file", ""},
		{"token file that is empty", []string{"--url", stub.URL, "--token-file", files["empty"]}, "--token-file: file " + files["empty"] + " is empty", ""},
		{"token file with white space only", []string{"--url", stub.URL, "--token-file", files["blank"]}, "--token-file: file " + files["blank"] + " is empty", ""},
		{"token file with the database DSN", []string{"--url", stub.URL, "--token-file", files["dsn"]}, "--token-file: file " + files["dsn"] + " must contain an enrollment token", "hunter2-secret"},
		{"token file with another secret", []string{"--url", stub.URL, "--token-file", files["bootstrap"]}, "--token-file: file " + files["bootstrap"] + " must contain an enrollment token", bootstrap},
		{"token file with two tokens", []string{"--url", stub.URL, "--token-file", files["two"]}, "must contain an enrollment token", good},
		{"token file with a space inside", []string{"--url", stub.URL, "--token-file", files["inner"]}, "must contain an enrollment token", good[len(brand.EnrollmentTokenPrefix):]},
		{"token file with a short token", []string{"--url", stub.URL, "--token-file", files["prefix"]}, "must contain an enrollment token", ""},
		{"token file that is too large", []string{"--url", stub.URL, "--token-file", files["long"]}, "is larger than 4096 bytes", tooLong},
		{"CA file that is missing", []string{"--url", stub.URL, "--ca-file", filepath.Join(dir, "missing.pem")}, "--ca-file: cannot read " + filepath.Join(dir, "missing.pem") + ": " + notFound, ""},
		{"CA file without a certificate", []string{"--url", stub.URL, "--ca-file", files["nocert"]}, "--ca-file: file " + files["nocert"] + " contains no certificate", ""},
		{"empty CA file", []string{"--url", stub.URL, "--ca-file", files["emptyca"]}, "--ca-file: file " + files["emptyca"] + " contains no certificate", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := healthcheckRun(t, tc.args...)
			assert.Equal(t, exitUsage, code, stderr)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, "myriarchd: ")
			assert.Contains(t, stderr, tc.want)
			if tc.secret != "" {
				assertNoSecret(t, tc.secret, stdout, stderr)
			}
		})
	}
	got := stub.seen()
	assert.Zero(t, got.ready+got.upgrades, "a request was made although the options were refused")
}

// A server that does not answer the close message, or that talks after the upgrade, does not keep the check waiting: the upgrade is
// what the check needs, and the end of the connection is best effort.
func TestHealthcheckEndsTheConnectionInBoundedTime(t *testing.T) {
	upgrade := func(w http.ResponseWriter, r *http.Request) *websocket.Conn {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return nil
		}
		return conn
	}
	t.Run("the server never reads, so it never answers the close message", func(t *testing.T) {
		release := make(chan struct{})
		stub := newHealthStub(t, false, func(s *healthStub) {
			s.opamp = func(w http.ResponseWriter, r *http.Request) bool {
				if conn := upgrade(w, r); conn != nil {
					defer conn.Close()
					select {
					case <-release:
					case <-time.After(30 * time.Second):
					}
				}
				return true
			}
		})
		defer close(release)
		start := time.Now()
		code, stdout, stderr := healthcheckRun(t, "--url", stub.URL)
		assert.Equal(t, 0, code, stderr)
		assert.Contains(t, stdout, "accepted with 101")
		assert.Less(t, time.Since(start), closeWait+5*time.Second)
	})
	t.Run("the server sends more than the check reads", func(t *testing.T) {
		stub := newHealthStub(t, false, func(s *healthStub) {
			s.opamp = func(w http.ResponseWriter, r *http.Request) bool {
				if conn := upgrade(w, r); conn != nil {
					defer conn.Close()
					_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 1<<20))
					_, _, _ = conn.ReadMessage() // until the check goes away
				}
				return true
			}
		})
		start := time.Now()
		code, _, stderr := healthcheckRun(t, "--url", stub.URL)
		assert.Equal(t, 0, code, stderr)
		assert.Less(t, time.Since(start), closeWait+5*time.Second)
	})
}

// The clients ask the server of --url and nobody else: no proxy, no redirect, no compression, no cookies.
func TestHealthcheckClientsUseNoProxyAndFollowNoRedirect(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	client, done := newHealthcheckClient(cfg)
	defer done()
	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, tr.Proxy)
	assert.True(t, tr.DisableKeepAlives)
	assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
	assert.False(t, tr.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, http.ErrUseLastResponse, client.CheckRedirect(nil, nil))

	d := newHealthcheckDialer(cfg)
	assert.Nil(t, d.Proxy)
	assert.False(t, d.EnableCompression)
	assert.Nil(t, d.Jar)
	assert.Equal(t, uint16(tls.VersionTLS12), d.TLSClientConfig.MinVersion)
	assert.False(t, d.TLSClientConfig.InsecureSkipVerify)
}

// A proxy in the environment is not used: a name that only a proxy could reach is not reached, and the proxy sees no request.
func TestHealthcheckIgnoresTheProxyOfTheEnvironment(t *testing.T) {
	var mu sync.Mutex
	asked := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked++
		mu.Unlock()
		w.WriteHeader(http.StatusOK) // as a proxy would answer for /readyz
	}))
	defer proxy.Close()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	code, stdout, stderr := healthcheckRun(t, "--url", "http://healthcheck-proxy-test.invalid:8080", "--timeout", "3s")
	assert.Equal(t, exitUnreachable, code, stderr)
	assert.Empty(t, stdout, "the proxy answered for the server")
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, asked, "the check sent a request to the proxy of the environment")
}

// The exit codes and the streams as a process sees them (the real main, the real stdout and stderr).
func TestHealthcheckProcessLevelContract(t *testing.T) {
	good := newHealthStub(t, false, func(s *healthStub) { s.wantToken = newTestToken(t) })
	broken := newHealthStub(t, false, func(s *healthStub) { s.readyz = http.StatusServiceUnavailable })
	gone := newHealthStub(t, false)
	goneURL := gone.URL
	gone.Close()

	r := clitest.Exec(t, "healthcheck", "--url", good.URL)
	assert.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, "readyz ok: GET "+good.URL+"/readyz answered 200\nopamp ok: the upgrade at /v1/opamp was refused with 401 (no token given, as expected)\n", r.Stdout)
	assert.Empty(t, r.Stderr)

	r = clitest.Exec(t, "healthcheck", "--url", broken.URL)
	assert.Equal(t, exitUnhealthy, r.Code)
	assert.Empty(t, r.Stdout)
	assert.True(t, strings.HasPrefix(r.Stderr, "myriarchd: readyz failed: "), r.Stderr)

	r = clitest.Exec(t, "healthcheck", "--url", goneURL)
	assert.Equal(t, exitUnreachable, r.Code)
	assert.Empty(t, r.Stdout)
	assert.True(t, strings.HasPrefix(r.Stderr, "myriarchd: readyz failed: cannot reach "), r.Stderr)

	r = clitest.Exec(t, "healthcheck", "--url", good.URL, "--token-file", filepath.Join(t.TempDir(), "missing"))
	assert.Equal(t, exitUsage, r.Code)
	assert.Empty(t, r.Stdout)
	assert.True(t, strings.HasPrefix(r.Stderr, "myriarchd: --token-file: cannot read "), r.Stderr)

	r = clitest.Exec(t, "healthcheck", "--nope")
	assert.Equal(t, exitUsage, r.Code)
	assert.Empty(t, r.Stdout)
	assert.True(t, strings.HasPrefix(r.Stderr, "myriarchd: unknown flag: --nope"), r.Stderr)

	r = clitest.Exec(t, "healthcheck", "--help")
	assert.Equal(t, 0, r.Code)
	assert.Empty(t, r.Stderr)
	assert.Contains(t, r.Stdout, "myriarchd healthcheck [--url URL]")
}

func TestHealthcheckTokenFileThroughASymlink(t *testing.T) {
	// the files of a Secret volume are symbolic links
	tok := newTestToken(t)
	stub := newHealthStub(t, false, func(s *healthStub) { s.wantToken = tok })
	dir := t.TempDir()
	target := writeFile(t, dir, "real-token", tok)
	link := filepath.Join(dir, "token")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	code, stdout, stderr := healthcheckRun(t, "--url", stub.URL, "--token-file", link)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "accepted with 101")
}

func TestHealthcheckOutputStreams(t *testing.T) {
	stub := newHealthStub(t, false, func(s *healthStub) { s.wantToken = newTestToken(t) })
	// a healthy server writes to stdout only
	code, stdout, stderr := healthcheckRun(t, "--url", stub.URL)
	assert.Equal(t, 0, code)
	assert.Equal(t, 2, strings.Count(stdout, "\n"))
	assert.Empty(t, stderr)
	// a failure writes the reason to stderr, as `myriarchd: ...`, one line
	bad := newHealthStub(t, false, func(s *healthStub) { s.readyz = http.StatusServiceUnavailable })
	code, stdout, stderr = healthcheckRun(t, "--url", bad.URL)
	assert.Equal(t, exitUnhealthy, code)
	assert.Empty(t, stdout)
	assert.True(t, strings.HasPrefix(stderr, "myriarchd: readyz failed: "), stderr)
	assert.Equal(t, 1, strings.Count(stderr, "\n"))
}

func TestHealthcheckDefaults(t *testing.T) {
	cmd := newHealthcheckCommand()
	assert.Equal(t, "http://127.0.0.1:8080", cmd.Flag("url").DefValue)
	assert.Equal(t, config.Default().OpAMP.Path, cmd.Flag("opamp-path").DefValue)
	assert.Equal(t, "/v1/opamp", cmd.Flag("opamp-path").DefValue)
	assert.Equal(t, "", cmd.Flag("token-file").DefValue)
	assert.Equal(t, "", cmd.Flag("ca-file").DefValue)
	assert.Equal(t, "10s", cmd.Flag("timeout").DefValue)
	// the token has no flag and no environment variable of its own: it comes from a file only
	for _, name := range []string{"token", "password", "secret", "bearer"} {
		assert.Nil(t, cmd.Flag(name), "flag --%s", name)
	}
}

func TestParseHealthcheckURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080":            "http://127.0.0.1:8080",
		"http://127.0.0.1:8080/":           "http://127.0.0.1:8080",
		"HTTPS://Fleet.Example.com":        "https://Fleet.Example.com",
		"https://fleet.example.com:8443":   "https://fleet.example.com:8443",
		"http://[2001:db8::1]:8080":        "http://[2001:db8::1]:8080",
		"http://myriarch.myriarch.svc:80/": "http://myriarch.myriarch.svc:80",
	} {
		u, err := parseHealthcheckURL(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, u.String(), in)
	}
}

// TestHealthcheckCountsAsUnauthorizedAndRegistersNoAgent runs the check against the real server, with the production setting
// opamp.requireEnrollmentToken: the check without a token is an unauthorized connect in the metrics, the check with a token is
// accepted, and neither registers an agent or counts a message.
func TestHealthcheckCountsAsUnauthorizedAndRegistersNoAgent(t *testing.T) {
	dsn := pgtest.NewPool(t).Config().ConnString()
	dir := t.TempDir()
	enroll := newTestToken(t)
	enrollFile := writeFile(t, dir, "enrollment-probe", enroll+"\n")
	bootstrapFile := writeFile(t, dir, "bootstrap-token", randomSecret(t, 24)+"\n")
	cfg, addr := devConfigDSN(t, dsn)
	cfg.Auth.DevLogin = config.DevLoginConfig{}
	cfg.OpAMP.RequireEnrollmentToken = true
	cfg.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin", TokenFile: bootstrapFile}
	cfg.OpAMP.EnrollmentTokens = []config.EnrollmentTokenConfig{{Name: "probe", TokenFile: enrollFile, Labels: map[string]string{"purpose": "healthcheck"}}}
	startRunWith(t, cfg, addr, runOptions{})
	base := "http://" + addr

	// the series of the counters: absent until the first increment
	metric := func(series string) float64 {
		t.Helper()
		resp, err := http.Get(base + "/metrics")
		require.NoError(t, err)
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, err = buf.ReadFrom(resp.Body)
		require.NoError(t, err)
		for _, line := range strings.Split(buf.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, series+" "); ok {
				v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
				require.NoError(t, err, line)
				return v
			}
		}
		return 0
	}
	const unauthorized = `myriarch_opamp_connect_rejected_total{reason="unauthorized"}`
	const messagesIn = `myriarch_opamp_messages_total{direction="in"}`
	dbc, err := pgx.Connect(context.Background(), cfg.Database.DSN)
	require.NoError(t, err)
	defer dbc.Close(context.Background())
	agents := func() int {
		t.Helper()
		var n int
		require.NoError(t, dbc.QueryRow(context.Background(), `SELECT count(*) FROM agents`).Scan(&n))
		return n
	}
	before := metric(unauthorized)
	require.Zero(t, agents())

	// without a token: the route and the authentication are in place; the server counts one unauthorized connect
	code, stdout, stderr := healthcheckRun(t, "--url", base)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "readyz ok: GET "+base+"/readyz answered 200\n"+
		"opamp ok: the upgrade at /v1/opamp was refused with 401 (no token given, as expected)\n", stdout)
	assert.Equal(t, before+1, metric(unauthorized))

	// with the token: accepted, counted as an authenticated connect, no agent, no message
	for i := 0; i < 2; i++ {
		code, stdout, stderr = healthcheckRun(t, "--url", base, "--token-file", enrollFile)
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, stdout, "opamp ok: the upgrade at /v1/opamp was accepted with 101 (the server accepts the token)\n")
		assertNoSecret(t, enroll, stdout, stderr)
	}
	assert.Equal(t, before+1, metric(unauthorized), "a check with a valid token counted as unauthorized")
	assert.Zero(t, metric(messagesIn), "the check sent an OpAMP message")
	assert.Zero(t, agents(), "the check registered an agent")

	// a token of the right form that the server does not know is refused, and that counts as unauthorized
	other := writeFile(t, dir, "other", newTestToken(t))
	code, stdout, stderr = healthcheckRun(t, "--url", base, "--token-file", other)
	assert.Equal(t, exitUnhealthy, code)
	assert.Equal(t, "readyz ok: GET "+base+"/readyz answered 200\n", stdout)
	assert.Contains(t, stderr, "refused with 401 although a token was given")
	assert.Equal(t, before+2, metric(unauthorized))
	assert.Zero(t, agents())

}

// TestHealthcheckAgainstTheDevelopmentServer: the Compose quickstart runs `myriarchd dev`, which takes connections without a
// token, and uses the check without flags. The server in this test listens on another port, so the address is given.
func TestHealthcheckAgainstTheDevelopmentServer(t *testing.T) {
	cfg, addr := devConfig(t)
	startRun(t, cfg, addr)
	code, stdout, stderr := healthcheckRun(t, "--url", "http://"+addr)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "opamp ok: the upgrade at /v1/opamp was accepted with 101 (no token given; the server takes connections without one)\n")
	dbc, err := pgx.Connect(context.Background(), cfg.Database.DSN)
	require.NoError(t, err)
	defer dbc.Close(context.Background())
	var n int
	require.NoError(t, dbc.QueryRow(context.Background(), `SELECT count(*) FROM agents`).Scan(&n))
	assert.Zero(t, n, "the check registered an agent")
}

// The design document (docs/design is normative) lists the flags with their defaults, quotes the output lines and the one-line form of a
// failure, and describes the exit codes. A change of the code has to change the document in the same pull request.
func TestHealthcheckIsDocumentedInTheDesignDocument(t *testing.T) {
	doc := section(t, readDoc(t, "docs/design/opamp-server.md"), "Health check")
	flat := oneLine(doc)

	// the flags and their defaults (a flag without a default says "none")
	cmd := newHealthcheckCommand()
	for _, name := range []string{"url", "opamp-path", "token-file", "ca-file", "timeout"} {
		f := cmd.Flag(name)
		require.NotNil(t, f, name)
		def := "none"
		if f.DefValue != "" {
			def = "`" + f.DefValue + "`"
		}
		assert.Contains(t, doc, "| `--"+name+"` | "+def+" | ", "the flag table does not list --%s with the default %s", name, def)
	}

	// the output lines: run the command against stubs and find what it printed, with <url> for the address, in the document
	tok := newTestToken(t)
	file := writeFile(t, t.TempDir(), "token", tok)
	closed := newHealthStub(t, false, func(s *healthStub) { s.wantToken = tok })
	open := newHealthStub(t, false)
	broken := newHealthStub(t, false, func(s *healthStub) { s.readyz = http.StatusServiceUnavailable })
	var lines []string
	for _, run := range []struct {
		stub *healthStub
		args []string
	}{
		{closed, nil},
		{closed, []string{"--token-file", file}},
		{open, nil},
	} {
		code, stdout, stderr := healthcheckRun(t, append([]string{"--url", run.stub.URL}, run.args...)...)
		require.Equal(t, 0, code, stderr)
		lines = append(lines, strings.Split(strings.TrimRight(strings.ReplaceAll(stdout, run.stub.URL, "<url>"), "\n"), "\n")...)
	}
	require.Len(t, lines, 6)
	for _, line := range lines {
		assert.Contains(t, doc, "\n"+line+"\n", "the document does not quote the output line %q", line)
	}
	code, _, stderr := healthcheckRun(t, "--url", broken.URL)
	require.Equal(t, exitUnhealthy, code)
	assert.Contains(t, flat, "`"+strings.TrimRight(strings.ReplaceAll(stderr, broken.URL, "<url>"), "\n")+"`", "the document does not quote the one-line form of a failure")
	assert.Contains(t, flat, "`myriarchd: <request> failed: <reason>`")

	// the exit codes
	for _, row := range []struct {
		code int
		text string
	}{
		{0, "Healthy"}, {exitUnhealthy, "Not healthy"}, {exitUsage, "Wrong usage"}, {exitUnreachable, "No answer"},
	} {
		assert.Contains(t, doc, fmt.Sprintf("| %d | %s", row.code, row.text), "the table of exit codes")
	}

	// the numbers and names in the text
	for _, want := range []string{
		"`GET /readyz` must be answered with 200", "`Authorization: Bearer <token>`", "(code 1000)", "waits at most two seconds",
		"at most 4 KiB", "exit code 2 before a request is made", "`myriarch_opamp_connect_rejected_total{reason=\"unauthorized\"}`",
		"`myriarch_auth_agent_requests_total{result=\"unauthorized\"}`", "`MyriarchUnauthorizedConnects`",
	} {
		assert.Contains(t, flat, want)
	}
	assert.Equal(t, 4<<10, maxTokenFileBytes)
	assert.Equal(t, 2*time.Second, closeWait)
}

// The metrics that the documentation names exist, with the labels it names.
func TestHealthcheckDocumentationNamesRealMetrics(t *testing.T) {
	metrics, err := refdoc.ScanMetrics(filepath.Join("..", ".."))
	require.NoError(t, err)
	labels := map[string][]string{}
	for _, m := range metrics {
		labels[m.Name] = m.Labels
	}
	assert.Equal(t, []string{"reason"}, labels["myriarch_opamp_connect_rejected_total"])
	assert.Equal(t, []string{"result"}, labels["myriarch_auth_agent_requests_total"])
}
