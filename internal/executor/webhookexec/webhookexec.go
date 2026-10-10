// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package webhookexec implements executor types "webhook" (generic HTTP POST
// with HMAC signature, payload executor.WebhookPayload) and "awx" (Ansible
// AWX / Automation Platform job template launch:
// POST <url><apiPrefix>job_templates/<id>/launch/ with extra_vars and limit,
// cancel: POST <url><apiPrefix>jobs/<id>/cancel/; apiPrefix defaults to
// /api/v2/, Automation Platform 2.5 and later uses /api/controller/v2/).
//
// Both executors fail closed: every target host must be listed in
// executors.allowedWebhookHosts, an empty list refuses all targets.
package webhookexec

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/version"
)

const (
	defaultBatchSize = 100
	defaultTimeout   = 30 * time.Second
	maxResponseBytes = 64 << 10
	maxErrorBody     = 200
	// maxSecretFile bounds secret and CA files.
	maxSecretFile = 1 << 20
	// minSecretLen is the shortest accepted HMAC secret (bytes).
	minSecretLen = 32
)

// limitHostRE is the grammar of an AWX limit entry: a plain host name or
// address. It excludes every character with a meaning in the Ansible host
// pattern language (comma, colon, wildcard, negation, intersection, regex,
// ranges) and whitespace.
var limitHostRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,252})$`)

// reservedLimitNames are Ansible groups that would address more than one host.
var reservedLimitNames = map[string]bool{"all": true, "ungrouped": true, "localhost": false}

// deliveryNamespace derives deterministic delivery ids (UUIDv5): a repeated
// call for the same rollout/wave/attempt/targets carries the same id, so
// receivers can de-duplicate an at-least-once delivery.
var deliveryNamespace = uuid.MustParse("6f2f3f0e-7f0b-5f0e-9a5e-0b7d6c4a3e21")

// Signature returns the value of the X-Myriarch-Signature header:
// "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)). The generic
// webhook notifier uses the same scheme.
func Signature(secret []byte, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// NewWebhook returns the webhook executor.
func NewWebhook(cfg config.ExecutorsConfig, client *http.Client, logger *slog.Logger) executor.Executor {
	return newBase(domain.ExecutorWebhook, cfg, client, logger)
}

// NewAWX returns the AWX executor.
func NewAWX(cfg config.ExecutorsConfig, client *http.Client, logger *slog.Logger) executor.Executor {
	return newBase(domain.ExecutorAWX, cfg, client, logger)
}

type exec struct {
	typ    domain.ExecutorType
	cfg    config.ExecutorsConfig
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
	// getenv reads secrets from the myriarchd environment (tests replace it).
	getenv func(string) string
	// readFile reads a file below dir (relative name, no escape from dir).
	readFile func(dir, name string) ([]byte, error)
	renderer *blueprint.Renderer
}

func newBase(t domain.ExecutorType, cfg config.ExecutorsConfig, client *http.Client, logger *slog.Logger) *exec {
	if client == nil {
		client = &http.Client{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	defaults := config.Default().Executors
	if cfg.SecretEnvPrefix == "" {
		cfg.SecretEnvPrefix = defaults.SecretEnvPrefix
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = defaults.MaxTimeout
	}
	c := *client
	c.Timeout = 0 // per-request deadlines
	// Webhooks must not be redirected (SSRF): a redirect is a failure.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &exec{
		typ: t, cfg: cfg, client: &c, log: logger.With("component", "webhookexec", "executor", string(t)),
		now: func() time.Time { return time.Now().UTC() }, getenv: os.Getenv, readFile: readInRoot,
		renderer: blueprint.NewRenderer(blueprint.Options{}),
	}
}

func (e *exec) Type() domain.ExecutorType { return e.typ }

// ------------------------------------------------------------ allow list ----

// errNoAllowedHosts is returned for every webhook and AWX target while
// executors.allowedWebhookHosts has no entry.
const errNoAllowedHosts = "executors.allowedWebhookHosts is empty: webhook and AWX targets are refused (fail closed); list the permitted hosts (host, host:port, *.example.com, or http://host for plain HTTP)"

// hostAllowed checks host[:port] against executors.allowedWebhookHosts. The
// check fails closed in every mode: entries are trimmed, empty ones dropped,
// and without any entry every target is refused. List entries match the host,
// the host:port, or "*.example.com" suffixes.
func (e *exec) hostAllowed(u *url.URL) error {
	hostPort := strings.ToLower(u.Host)
	var entries []string
	for _, a := range e.cfg.AllowedWebhookHosts {
		if a = strings.TrimSpace(a); a != "" {
			entries = append(entries, a)
		}
	}
	if len(entries) == 0 {
		return errors.New(errNoAllowedHosts)
	}
	// Entries are "host", "host:port" or "*.suffix" (https only) and
	// "http://host[:port]" (explicit opt-in for plain http, e.g. an internal
	// AWX without TLS): credentials and signed payloads never go out in clear
	// text unless the operator allowed exactly that host.
	var plain, insecure []string
	for _, a := range entries {
		if rest, ok := strings.CutPrefix(strings.ToLower(a), "http://"); ok {
			insecure = append(insecure, rest)
		} else {
			plain = append(plain, strings.TrimPrefix(strings.ToLower(a), "https://"))
		}
	}
	switch {
	case u.Scheme == "http" && matchHost(insecure, u):
		return nil
	case u.Scheme != "http" && (matchHost(plain, u) || matchHost(insecure, u)):
		return nil
	case u.Scheme == "http" && matchHost(plain, u):
		return fmt.Errorf("host %s may only be called over https (plain http needs the explicit entry \"http://%s\" in executors.allowedWebhookHosts)", hostPort, hostPort)
	}
	return fmt.Errorf("host %s is not in executors.allowedWebhookHosts", hostPort)
}

// matchHost matches the host (or host:port) of u against a list of host,
// host:port and "*.suffix" entries.
func matchHost(list []string, u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	hostPort := strings.ToLower(u.Host)
	for _, a := range list {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "":
		case a == host || a == hostPort:
			return true
		case strings.HasPrefix(a, "*.") && netguard.HostListed([]string{a}, host):
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ secrets ----

// readInRoot reads name below dir; the name must be relative and cannot leave
// dir (also not through symbolic links).
func readInRoot(dir, name string) ([]byte, error) {
	f, err := os.OpenInRoot(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSecretFile))
}

// readOperatorFile reads a secret file whose path the operator chose (executors.secrets entry with an
// absolute path): a regular file of at most maxSecretFile bytes, like the files below executors.secretDir.
func readOperatorFile(path string) ([]byte, error) {
	// checked before opening: opening a FIFO for reading would block
	if pre, err := os.Stat(path); err != nil {
		return nil, err
	} else if !pre.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path) //nolint:gosec // the path comes from the operator's configuration
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSecretFile {
		return nil, fmt.Errorf("file is larger than %d bytes", maxSecretFile)
	}
	return b, nil
}

// secret resolves a secret reference of an Executor object. Git only names the
// secret; where it can come from is decided on the myriarchd side:
//
//   - a name listed in executors.secrets (registry) resolves to the configured
//     environment variable or file and is only released for the hosts listed
//     with it (checked against the destination u);
//   - otherwise env must start with executors.secretEnvPrefix and file must
//     be a relative name below executors.secretDir; such a reference is only
//     released for the hosts bound to it in executors.secretEnvHosts or
//     executors.secretFileHosts (production refuses unbound references).
//
// Arbitrary environment variables and paths are never readable from Git.
func (e *exec) secret(env, file, what string, u *url.URL) ([]byte, error) {
	ref := env
	if ref == "" {
		ref = file
	}
	if ref == "" {
		return nil, fmt.Errorf("%s: no secret reference configured", what)
	}
	if reg, ok := e.cfg.Secrets[ref]; ok {
		if u == nil || !matchHost(reg.AllowedHosts, u) {
			return nil, fmt.Errorf("%s: secret %q may not be sent to this host (see executors.secrets.%s.allowedHosts)", what, ref, ref)
		}
		switch {
		case reg.Env != "":
			return e.fromEnv(reg.Env, what)
		case reg.File != "":
			return e.fromFile(reg.File, what, filepath.IsAbs(reg.File))
		}
		return nil, fmt.Errorf("%s: secret %q is not configured", what, ref)
	}
	if env != "" {
		if !strings.HasPrefix(env, e.cfg.SecretEnvPrefix) {
			return nil, fmt.Errorf("%s: environment variable %s is not allowed (names must start with %s or be listed in executors.secrets)", what, env, e.cfg.SecretEnvPrefix)
		}
		if err := e.bound(e.cfg.SecretEnvHosts, "secretEnvHosts", env, what, u); err != nil {
			return nil, err
		}
		return e.fromEnv(env, what)
	}
	if err := e.bound(e.cfg.SecretFileHosts, "secretFileHosts", file, what, u); err != nil {
		return nil, err
	}
	return e.fromFile(file, what, false)
}

// bound checks that the secret reference ref may be sent to u: a binding in
// hosts must match the destination, and outside dev mode a binding is
// mandatory.
func (e *exec) bound(hosts map[string][]string, key, ref, what string, u *url.URL) error {
	list, ok := hosts[ref]
	switch {
	case !ok && !e.cfg.RequireSecretHosts:
		return nil
	case !ok:
		return fmt.Errorf("%s: secret %q is not bound to a destination (list its hosts in executors.%s or use executors.secrets)", what, ref, key)
	case u == nil || !matchHost(list, u):
		return fmt.Errorf("%s: secret %q may not be sent to this host (see executors.%s.%s)", what, ref, key, ref)
	}
	return nil
}

func (e *exec) fromEnv(name, what string) ([]byte, error) {
	v := e.getenv(name)
	if v == "" {
		return nil, fmt.Errorf("%s: environment variable %s is not set on the myriarchd host", what, name)
	}
	return checkSecretLen([]byte(v), what)
}

// fromFile reads a secret file. Files named in Git (operatorConfigured=false)
// must lie below executors.secretDir; registry entries may use an absolute
// path chosen by the operator.
func (e *exec) fromFile(name, what string, operatorConfigured bool) ([]byte, error) {
	var b []byte
	var err error
	switch {
	case operatorConfigured:
		b, err = readOperatorFile(name)
	case e.cfg.SecretDir == "":
		return nil, fmt.Errorf("%s: secret files are disabled (set executors.secretDir or use executors.secrets)", what)
	default:
		b, err = e.readFile(e.cfg.SecretDir, name)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: cannot read secret file %s: %v", what, filepath.Base(name), stripPath(err))
	}
	v := bytes.TrimSpace(b)
	if len(v) == 0 {
		return nil, fmt.Errorf("%s: secret file %s is empty", what, filepath.Base(name))
	}
	return checkSecretLen(v, what)
}

func checkSecretLen(v []byte, what string) ([]byte, error) {
	if len(v) < minSecretLen && strings.Contains(what, "webhook") {
		return nil, fmt.Errorf("%s: the HMAC secret must be at least %d bytes long", what, minSecretLen)
	}
	return v, nil
}

// limitHost validates one value for the AWX limit (strict host grammar).
func limitHost(v string) error {
	if !limitHostRE.MatchString(v) {
		return fmt.Errorf("%q is not a valid host name for the AWX limit", excerpt([]byte(v)))
	}
	if reservedLimitNames[strings.ToLower(v)] {
		return fmt.Errorf("%q is a reserved Ansible group name and cannot be used as limit", v)
	}
	return nil
}

func stripPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// ---------------------------------------------------------------- validate ----

// Validate performs the environment dependent checks: secret present, target
// host allowed (docs/concepts/executors.md, "Validation in two steps").
func (e *exec) Validate(spec domain.UpdateSpec, def *domain.Executor) domain.Issues {
	issue := func(msg string) domain.Issues {
		return domain.Issues{{Severity: domain.SeverityError, Code: "executor_invalid", Message: msg, Object: "Executor/" + spec.ExecutorRef}}
	}
	if def == nil {
		return issue(fmt.Sprintf("executor %q does not exist", spec.ExecutorRef))
	}
	switch e.typ {
	case domain.ExecutorWebhook:
		w := def.Webhook
		if def.Type != domain.ExecutorWebhook || w == nil {
			return issue(fmt.Sprintf("executor %q is not a webhook executor", def.Name))
		}
		u, err := url.Parse(w.URL)
		if err != nil || u.Host == "" {
			return issue("webhook URL is invalid")
		}
		if err := e.hostAllowed(u); err != nil {
			return issue(err.Error())
		}
		if _, err := e.secret(w.SecretEnv, w.SecretFile, "webhook secret", u); err != nil {
			return issue(err.Error())
		}
		if w.Timeout.D() > e.cfg.MaxTimeout {
			return issue(fmt.Sprintf("timeout %s exceeds executors.maxTimeout (%s)", w.Timeout.D(), e.cfg.MaxTimeout))
		}
	case domain.ExecutorAWX:
		a := def.AWX
		if def.Type != domain.ExecutorAWX || a == nil {
			return issue(fmt.Sprintf("executor %q is not an AWX executor", def.Name))
		}
		u, err := url.Parse(a.URL)
		if err != nil || u.Host == "" {
			return issue("AWX URL is invalid")
		}
		if err := e.hostAllowed(u); err != nil {
			return issue(err.Error())
		}
		if _, err := e.secret(a.TokenEnv, a.TokenFile, "AWX token", u); err != nil {
			return issue(err.Error())
		}
		if a.Timeout.D() > e.cfg.MaxTimeout {
			return issue(fmt.Sprintf("timeout %s exceeds executors.maxTimeout (%s)", a.Timeout.D(), e.cfg.MaxTimeout))
		}
		if a.CAFile != "" {
			if _, err := e.caPool(e.baseRoots(), a.CAFile); err != nil {
				return issue(err.Error())
			}
		}
	}
	return nil
}

// baseRoots returns the trust store of the shared HTTP client (the system
// roots plus outbound.caFile); nil = system roots.
func (e *exec) baseRoots() *x509.CertPool {
	if tr, ok := e.client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		return tr.TLSClientConfig.RootCAs
	}
	return nil
}

// caPool returns base (nil = system roots) plus the certificates of the AWX
// caFile: the file extends the trust store of the client, it does not replace
// it (an internal CA from outbound.caFile stays trusted).
func (e *exec) caPool(base *x509.CertPool, file string) (*x509.CertPool, error) {
	if e.cfg.SecretDir == "" {
		return nil, fmt.Errorf("AWX caFile: CA files are disabled (set executors.secretDir)")
	}
	pemBytes, err := e.readFile(e.cfg.SecretDir, file)
	if err != nil {
		return nil, fmt.Errorf("AWX caFile: %v", stripPath(err))
	}
	pool, err := netguard.AppendRoots(base, pemBytes)
	if err != nil {
		return nil, fmt.Errorf("AWX caFile %s contains no valid PEM certificate", file)
	}
	return pool, nil
}

// ------------------------------------------------------------------ helpers ----

func batches[T any](items []T, size int) [][]T {
	if size <= 0 {
		size = defaultBatchSize
	}
	var out [][]T
	for len(items) > 0 {
		n := min(size, len(items))
		out = append(out, items[:n])
		items = items[n:]
	}
	return out
}

func timeoutOr(d domain.Duration, fallback time.Duration) time.Duration {
	if d > 0 {
		return d.D()
	}
	if fallback > 0 {
		return fallback
	}
	return defaultTimeout
}

// timeout is the effective timeout of one call: the configured value, the
// default, never more than executors.maxTimeout (Git cannot lengthen it).
func (e *exec) timeout(d domain.Duration) time.Duration {
	t := timeoutOr(d, e.cfg.DefaultTimeout)
	if m := e.cfg.MaxTimeout; m > 0 && t > m {
		return m
	}
	return t
}

// deliveryID is deterministic for (kind, rollout, wave, targets).
func deliveryID(kind string, rollout uuid.UUID, wave int, version string, attempts []int, ids []domain.AgentID) uuid.UUID {
	sorted := make([]string, len(ids))
	for i, id := range ids {
		sorted[i] = id.String()
	}
	sort.Strings(sorted)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s|%s|%d|%s|", kind, rollout, wave, version)
	for _, a := range attempts {
		fmt.Fprintf(&sb, "%d,", a)
	}
	sb.WriteString("|")
	sb.WriteString(strings.Join(sorted, ","))
	return uuid.NewSHA1(deliveryNamespace, []byte(sb.String()))
}

func payloadFor(event string, req executor.UpdateRequest, targets []executor.Target, id uuid.UUID) executor.WebhookPayload {
	p := executor.WebhookPayload{
		Event: event, DeliveryID: id, RolloutID: req.RolloutID, Wave: req.Wave, Fleet: req.Fleet,
		Distribution: req.Distribution, ToVersion: req.ToVersion, Params: req.Spec.Params,
		Targets: make([]executor.WebhookTarget, len(targets)),
	}
	for i, t := range targets {
		attrs := map[string]string(t.Attributes)
		if attrs == nil {
			attrs = map[string]string{}
		}
		p.Targets[i] = executor.WebhookTarget{
			AgentID: t.AgentID, Hostname: t.Hostname, OSType: t.OSType, Arch: t.Arch,
			CurrentVersion: t.CurrentVersion, Attributes: attrs,
		}
	}
	return p
}

// excerpt returns a short single-line prefix of a response body.
func excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > maxErrorBody {
		s = s[:maxErrorBody] + "..."
	}
	return s
}

func netErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return err.Error()
}

// doJSON sends a JSON request and returns status and (limited) body.
func (e *exec) doJSON(ctx context.Context, client *http.Client, method, rawURL string, body []byte, header map[string]string, timeout time.Duration) (int, []byte, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent(brand.ServerBinary))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, errors.New(netErr(err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, b, nil
}

// clientFor returns the HTTP client for a definition (custom CA for AWX).
func (e *exec) clientFor(a *domain.AWXExecutorSpec) (*http.Client, error) {
	if a == nil || a.CAFile == "" {
		return e.client, nil
	}
	pool, err := e.caPool(e.baseRoots(), a.CAFile)
	if err != nil {
		return nil, err
	}
	var tr *http.Transport
	if base, ok := e.client.Transport.(*http.Transport); ok {
		tr = base.Clone()
	} else {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	c := *e.client
	c.Transport = tr
	return &c, nil
}

// failAll fills every result with the same error.
func failAll(results []executor.TargetResult, msg string) {
	for i := range results {
		results[i].Error = msg
	}
}

func newResults(targets []executor.Target) []executor.TargetResult {
	out := make([]executor.TargetResult, len(targets))
	for i, t := range targets {
		out[i].AgentID = t.AgentID
	}
	return out
}

func targetIDs(ts []executor.Target) []domain.AgentID {
	ids := make([]domain.AgentID, len(ts))
	for i, t := range ts {
		ids[i] = t.AgentID
	}
	return ids
}

func attempts(ts []executor.Target) []int {
	out := make([]int, len(ts))
	for i, t := range ts {
		out[i] = t.Attempt
	}
	return out
}

// marshal encodes without HTML escaping.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
