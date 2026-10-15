// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/version"
)

// Exit codes of "myriarchd healthcheck" besides 0 (healthy) and exitUsage (2, wrong usage).
const (
	exitUnhealthy   = 1 // the server answered, but not as expected
	exitUnreachable = 3 // no usable answer: connection refused, name not resolved, TLS verification failed, time out
)

const (
	// defaultHealthcheckTimeout is the time allowed for both requests together.
	defaultHealthcheckTimeout = 10 * time.Second
	// maxTokenFileBytes and maxCAFileBytes bound what the command reads from a file: an enrollment token has at most 125
	// characters, a CA bundle is a few kilobytes.
	maxTokenFileBytes = 4 << 10
	maxCAFileBytes    = 1 << 20
	// closeWait is how long the command waits for the answer to its close message after a successful upgrade.
	closeWait = 2 * time.Second
	// maxReplyBytes bounds what the command reads from a reply that it only discards.
	maxReplyBytes = 4 << 10
)

// healthcheckOptions are the flags of "myriarchd healthcheck".
type healthcheckOptions struct {
	url       string
	opampPath string
	tokenFile string
	caFile    string
	timeout   time.Duration
}

// newHealthcheckCommand returns "myriarchd healthcheck".
func newHealthcheckCommand() *cobra.Command {
	defaultURL := healthcheckDefaultURL()
	var o healthcheckOptions
	cmd := &cobra.Command{
		Use:   "healthcheck [--url URL] [--opamp-path PATH] [--token-file FILE] [--ca-file FILE] [--timeout DURATION]",
		Short: "Check that a running myriarchd is ready and serves its OpAMP endpoint",
		Long: `Checks a running myriarchd and tells the result by the exit code. The container image has neither a shell nor curl, so
this command is the health check of the container and of "helm test". Example for a container health check, in the form
that runs the program without a shell:

  ["CMD", "/usr/local/bin/myriarchd", "healthcheck"]

Two requests, one after the other, within --timeout:

1. GET /readyz must answer 200. The server answers 503 while its database does not answer, the schema does not fit or its event
   bus is not connected.
2. A WebSocket upgrade at --opamp-path: the request of an OpAMP agent, without its first message. Without --token-file the
   server must refuse it with 401, which shows that the route and the authentication are in place; a server that takes
   connections without a token (the development mode) may accept it with 101. With --token-file the server must accept it
   with 101. After 101 the command closes the connection with a normal WebSocket close. It sends no OpAMP message, so no
   agent is registered and nothing is stored.

The token is read from the file only, never from an argument or from the environment, and it is never printed. The file must
hold one enrollment token (the prefix myre_, then 32 to 120 letters, digits, underscores or hyphens); anything else is
refused before a request is made. Use a token that exists for this check only. The server counts a check without a token as an unauthorized
connect (myriarch_opamp_connect_rejected_total, reason unauthorized) and a check with a token as a use of that token.

The command follows no redirects and uses no proxy; it needs no loopback address. With --ca-file the certificate of an https
server must be signed by one of the CAs of that PEM file and by no other; without it the system roots apply.

Output: one line per successful request on stdout ("readyz ok: ...", "opamp ok: ..."), the reason of a failure on stderr.
Exit codes: 0 healthy, 1 not healthy (the server answered, but not as expected), 2 wrong usage (an invalid value, a token file
or CA file that cannot be used), 3 no answer (connection refused, name not resolved, TLS verification failed, time out).`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &exitError{code: exitUsage, msg: "healthcheck takes no arguments; the address of the server is --url"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runHealthcheck(ctx, cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.url, "url", defaultURL, "base URL of the server: http or https, a host and an optional port, no path (the path of the OpAMP endpoint is --opamp-path)")
	cmd.Flags().StringVar(&o.opampPath, "opamp-path", config.Default().OpAMP.Path, "path of the OpAMP endpoint (opamp.path of the server)")
	cmd.Flags().StringVar(&o.tokenFile, "token-file", "", "file with an enrollment token: the check then expects the server to accept the upgrade (101); without it the server must refuse it (401). The token is read from the file only")
	cmd.Flags().StringVar(&o.caFile, "ca-file", "", "PEM file with the CAs that may have signed the certificate of an https server (only these; default: the system roots)")
	cmd.Flags().DurationVar(&o.timeout, "timeout", defaultHealthcheckTimeout, "time allowed for both requests together")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &exitError{code: exitUsage, msg: err.Error()} })
	return cmd
}

// healthcheckDefaultURL is the address of a server with the default listen address, seen from its own host or container.
func healthcheckDefaultURL() string {
	_, port, err := net.SplitHostPort(config.Default().Server.ListenAddr)
	if err != nil || port == "" {
		port = "8080"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port)
}

// healthcheck is one run of the command: the checked server, what to expect from it and the clients to ask it.
type healthcheck struct {
	base      *url.URL // scheme and host of the server, no path
	opampPath string
	token     string // never printed or logged
	tlsConfig *tls.Config
	timeout   time.Duration
	userAgent string
	stdout    io.Writer
}

// runHealthcheck checks the options, asks the server and returns an *exitError for everything but a healthy server.
func runHealthcheck(ctx context.Context, stdout io.Writer, o healthcheckOptions) error {
	base, err := parseHealthcheckURL(o.url)
	if err != nil {
		return usageError("--url: %v", err)
	}
	if err := checkOpAMPPath(o.opampPath); err != nil {
		return usageError("--opamp-path: %v", err)
	}
	if o.timeout <= 0 {
		return usageError("--timeout: must be greater than 0")
	}
	h := &healthcheck{
		base: base, opampPath: o.opampPath, timeout: o.timeout, stdout: stdout,
		userAgent: version.UserAgent(brand.ServerBinary + "-healthcheck"),
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if o.caFile != "" {
		pool, err := readCAFile(o.caFile)
		if err != nil {
			return usageError("--ca-file: %v", err)
		}
		h.tlsConfig.RootCAs = pool
	}
	if o.tokenFile != "" {
		if h.token, err = readEnrollmentTokenFile(o.tokenFile); err != nil {
			return usageError("--token-file: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	if err := h.checkReadyz(ctx); err != nil {
		return err
	}
	return h.checkOpAMP(ctx)
}

func usageError(format string, args ...any) error {
	return &exitError{code: exitUsage, msg: fmt.Sprintf(format, args...)}
}

// parseHealthcheckURL checks the base URL of the server and returns it without a path.
func parseHealthcheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// the message of the parser quotes the whole value, which can carry a password: name the reason only
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("is not a valid URL (write it with its scheme, for example http://127.0.0.1:8080): %v", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "http" && scheme != "https":
		return nil, errors.New("the scheme must be http or https")
	case u.Hostname() == "":
		return nil, errors.New("has no host")
	case u.User != nil:
		return nil, errors.New("must not contain a user name or a password")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, errors.New("must not contain a query or a fragment")
	case u.Path != "" && u.Path != "/":
		return nil, errors.New("must not contain a path; it is the base URL of the server, and the path of the OpAMP endpoint is --opamp-path")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, errors.New("has a port outside 1 to 65535")
		}
	}
	return &url.URL{Scheme: scheme, Host: u.Host}, nil
}

// checkOpAMPPath applies the rules of opamp.path that matter for a request.
func checkOpAMPPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return errors.New(`must start with "/"`)
	case strings.ContainsAny(p, "?#"):
		return errors.New(`must not contain "?" or "#"`)
	case strings.ContainsFunc(p, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return errors.New("must not contain spaces or control characters")
	}
	return nil
}

// readSmallFile reads a regular file of at most limit bytes. A FIFO or a device is refused before it is opened, because opening a
// FIFO would wait for a writer. The error names the file and the reason, never the content.
func readSmallFile(path string, limit int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %s", path, osError(err))
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %s", path, osError(err))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %s", path, osError(err))
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file %s is larger than %d bytes", path, limit)
	}
	return b, nil
}

// readEnrollmentTokenFile returns the enrollment token in the file. Anything else is refused, so that a file with another secret
// (the database DSN, the token of the bootstrap admin) is never sent to a server.
func readEnrollmentTokenFile(path string) (string, error) {
	b, err := readSmallFile(path, maxTokenFileBytes)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("file %s is empty", path)
	}
	if msg := domain.EnrollmentTokenProblem(tok); msg != "" {
		return "", fmt.Errorf("file %s %s", path, msg)
	}
	return tok, nil
}

// readCAFile returns the CAs of a PEM file.
func readCAFile(path string) (*x509.CertPool, error) {
	b, err := readSmallFile(path, maxCAFileBytes)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("file %s contains no certificate", path)
	}
	return pool, nil
}

// checkReadyz asks GET /readyz and expects 200.
func (h *healthcheck) checkReadyz(ctx context.Context) error {
	target := *h.base
	target.Path = "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return usageError("--url: %v", err)
	}
	req.Header.Set("User-Agent", h.userAgent)

	client, done := newHealthcheckClient(h.tlsConfig)
	defer done()
	resp, err := client.Do(req)
	if err != nil {
		return h.noAnswer("readyz", &target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBytes))
	if resp.StatusCode != http.StatusOK {
		hint := ""
		switch {
		case resp.StatusCode == http.StatusServiceUnavailable:
			hint = ": the server is not ready, its log names the cause"
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			hint = ": redirects are not followed, --url must name the server itself"
		case resp.StatusCode == http.StatusNotFound:
			hint = ": there is no myriarchd at this address"
		}
		return &exitError{code: exitUnhealthy, msg: fmt.Sprintf("readyz failed: GET %s answered %d, want 200%s", &target, resp.StatusCode, hint)}
	}
	fmt.Fprintf(h.stdout, "readyz ok: GET %s answered 200\n", &target)
	return nil
}

// checkOpAMP asks for a WebSocket upgrade at the OpAMP path, without a message after it.
func (h *healthcheck) checkOpAMP(ctx context.Context) error {
	target := *h.base
	target.Path = h.opampPath
	wsURL := target
	wsURL.Scheme = "ws"
	if target.Scheme == "https" {
		wsURL.Scheme = "wss"
	}
	header := http.Header{"User-Agent": {h.userAgent}}
	if h.token != "" {
		header.Set("Authorization", "Bearer "+h.token)
	}
	conn, resp, err := newHealthcheckDialer(h.tlsConfig).DialContext(ctx, wsURL.String(), header)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	switch {
	case conn != nil:
		// the upgrade is proven; end the connection in the normal way, so that the server logs no warning about it
		closeWebSocket(ctx, conn)
		if h.token == "" {
			fmt.Fprintf(h.stdout, "opamp ok: the upgrade at %s was accepted with 101 (no token given; the server takes connections without one)\n", h.opampPath)
		} else {
			fmt.Fprintf(h.stdout, "opamp ok: the upgrade at %s was accepted with 101 (the server accepts the token)\n", h.opampPath)
		}
		return nil
	case status == 0 || !errors.Is(err, websocket.ErrBadHandshake):
		if err == nil {
			err = errors.New("no answer")
		}
		return h.noAnswer("opamp", &target, err)
	case status == http.StatusUnauthorized && h.token == "":
		fmt.Fprintf(h.stdout, "opamp ok: the upgrade at %s was refused with 401 (no token given, as expected)\n", h.opampPath)
		return nil
	case status == http.StatusUnauthorized:
		return h.unhealthy("the upgrade at %s was refused with 401 although a token was given: the server does not accept the token (revoked, expired or unknown), or it also requires a client certificate", h.opampPath)
	}
	want, hint := "401", ""
	if h.token != "" {
		want = "101"
	}
	switch {
	case status == http.StatusSwitchingProtocols:
		hint = ": the answer is not a valid WebSocket handshake"
	case status >= 300 && status < 400:
		hint = ": redirects are not followed"
	case status == http.StatusNotFound:
		hint = ": nothing is served at this path, check --opamp-path (opamp.path of the server)"
	case status == http.StatusTooManyRequests:
		hint = ": the server limits the connections of this client address or token"
	case status == http.StatusServiceUnavailable:
		hint = ": the server is shutting down, is full or cannot check the credentials"
	}
	return h.unhealthy("the upgrade at %s was answered with %d, want %s%s", h.opampPath, status, want, hint)
}

// newHealthcheckClient returns the HTTP client of the /readyz request and the function that releases it: one request on a connection
// of its own, no redirect and no proxy (whatever HTTP_PROXY and HTTPS_PROXY say), so that nothing but the server of --url is asked.
func newHealthcheckClient(tlsConfig *tls.Config) (*http.Client, func()) {
	tr := &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig.Clone(), DisableKeepAlives: true}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, tr.CloseIdleConnections
}

// newHealthcheckDialer returns the WebSocket dialer of the upgrade request: no proxy, no compression, no cookies. The dialer follows
// no redirect; it hands the answer to the caller.
func newHealthcheckDialer(tlsConfig *tls.Config) *websocket.Dialer {
	return &websocket.Dialer{Proxy: nil, TLSClientConfig: tlsConfig.Clone(), EnableCompression: false, Jar: nil}
}

// unhealthy is a server that answered, but not as expected.
func (h *healthcheck) unhealthy(format string, args ...any) error {
	return &exitError{code: exitUnhealthy, msg: "opamp failed: " + fmt.Sprintf(format, args...)}
}

// noAnswer is a request that got no answer: the reason without the URL that the error repeats.
func (h *healthcheck) noAnswer(step string, target *url.URL, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	reason := err.Error()
	if isTimeout(err) {
		reason = fmt.Sprintf("no answer within %s", h.timeout)
	}
	return &exitError{code: exitUnreachable, msg: fmt.Sprintf("%s failed: cannot reach %s: %s", step, target, reason)}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// closeWebSocket sends the normal close message and waits briefly for the answer. A failure does not matter: the upgrade has
// succeeded, which is all the check needs.
func closeWebSocket(ctx context.Context, conn *websocket.Conn) {
	defer conn.Close()
	wait := closeWait
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
		wait = time.Until(dl)
	}
	if wait <= 0 {
		return
	}
	deadline := time.Now().Add(wait)
	conn.SetReadLimit(maxReplyBytes)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline)
	_ = conn.SetReadDeadline(deadline)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}
