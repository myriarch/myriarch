// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package webhookexec

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/netguard"
)

type recorded struct {
	method string
	path   string
	header http.Header
	body   []byte
}

type recorder struct {
	mu   sync.Mutex
	reqs []recorded
	// handler decides the response; default 200 {}.
	handler func(r *recorded, w http.ResponseWriter)
}

func (rc *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	rec := recorded{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: b}
	rc.mu.Lock()
	rc.reqs = append(rc.reqs, rec)
	h := rc.handler
	rc.mu.Unlock()
	if h != nil {
		h(&rec, w)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (rc *recorder) all() []recorded {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]recorded(nil), rc.reqs...)
}

// allowLoopback lists the host of the httptest servers (any port, http or https).
var allowLoopback = []string{"http://127.0.0.1"}

// failClosedMessage is the refusal for an empty executors.allowedWebhookHosts.
const failClosedMessage = "executors.allowedWebhookHosts is empty: webhook and AWX targets are refused (fail closed); list the permitted hosts (host, host:port, *.example.com, or http://host for plain HTTP)"

// testSecret is a valid HMAC secret (at least 32 bytes).
const testSecret = "0123456789abcdef0123456789abcdef-test"

func newExec(t *testing.T, typ domain.ExecutorType, cfg config.ExecutorsConfig, env map[string]string) *exec {
	t.Helper()
	e := newBase(typ, cfg, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.getenv = func(k string) string { return env[k] }
	fixed := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return fixed }
	return e
}

func targets(n int) []executor.Target {
	out := make([]executor.Target, n)
	for i := range out {
		out[i] = executor.Target{
			AgentID:  uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", i)),
			Hostname: fmt.Sprintf("host-%02d", i), OSType: "linux", Arch: "amd64", CurrentVersion: "0.161.0", Attempt: 1,
			Attributes: domain.AttributeView{"host.name": fmt.Sprintf("host-%02d.example.com", i), "site.id": fmt.Sprint(i)},
		}
	}
	return out
}

func webhookDef(url string, mut ...func(w *domain.WebhookExecutorSpec)) *domain.Executor {
	w := &domain.WebhookExecutorSpec{URL: url, SecretEnv: "MYRIARCH_EXEC_HOOK_SECRET", Headers: map[string]string{"X-Team": "observability"}}
	for _, m := range mut {
		m(w)
	}
	return &domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "hook"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook, Webhook: w}}
}

func updateReq(def *domain.Executor, ts []executor.Target) executor.UpdateRequest {
	return executor.UpdateRequest{
		RolloutID: uuid.MustParse("01a11121-e597-7b69-9d25-a80cc7b9c707"), Wave: 2, Fleet: "k8s-gateway",
		Distribution: "otelcol-contrib", ToVersion: "0.162.0", Targets: ts,
		Spec:       domain.UpdateSpec{Executor: domain.ExecutorWebhook, ExecutorRef: "hook", Params: map[string]any{"deployment": "otel-gateway"}},
		Definition: def,
	}
}

func verifySignature(t *testing.T, r recorded, secret string) {
	t.Helper()
	ts := r.header.Get("X-Myriarch-Timestamp")
	require.NotEmpty(t, ts)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(r.body)
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	assert.Equal(t, want, r.header.Get("X-Myriarch-Signature"), "HMAC-SHA256(secret, timestamp + \".\" + body)")
}

func TestWebhookStartSignedPayload(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	req := updateReq(webhookDef(srv.URL+"/myriarch/k8s-image-bump"), targets(3))

	res, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, res, 3)
	got := rc.all()
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, http.MethodPost, r.method)
	assert.Equal(t, "/myriarch/k8s-image-bump", r.path)
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, "observability", r.header.Get("X-Team"), "custom headers")
	assert.Equal(t, strconv.FormatInt(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).Unix(), 10), r.header.Get("X-Myriarch-Timestamp"))
	verifySignature(t, r, testSecret)

	var p executor.WebhookPayload
	require.NoError(t, json.Unmarshal(r.body, &p))
	assert.Equal(t, "collector.update", p.Event)
	assert.Equal(t, req.RolloutID, p.RolloutID)
	assert.Equal(t, 2, p.Wave)
	assert.Equal(t, "k8s-gateway", p.Fleet)
	assert.Equal(t, "otelcol-contrib", p.Distribution)
	assert.Equal(t, "0.162.0", p.ToVersion)
	assert.Equal(t, map[string]any{"deployment": "otel-gateway"}, p.Params)
	require.Len(t, p.Targets, 3)
	assert.Equal(t, "host-01", p.Targets[1].Hostname)
	assert.Equal(t, "linux", p.Targets[1].OSType)
	assert.Equal(t, "amd64", p.Targets[1].Arch)
	assert.Equal(t, "0.161.0", p.Targets[1].CurrentVersion)
	assert.Equal(t, "host-01.example.com", p.Targets[1].Attributes["host.name"])
	assert.Equal(t, r.header.Get("X-Myriarch-Delivery"), p.DeliveryID.String())

	for i, tr := range res {
		assert.True(t, tr.Started)
		assert.Empty(t, tr.Error)
		assert.Equal(t, req.Targets[i].AgentID, tr.AgentID, "same order as the request")
		assert.Equal(t, p.DeliveryID.String(), tr.ExternalRef, "ExternalRef = deliveryId")
	}
}

func TestWebhookDeliveryIDIsDeterministic(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	def := webhookDef(srv.URL)
	req := updateReq(def, targets(4))
	_, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	rev := req
	rev.Targets = []executor.Target{req.Targets[3], req.Targets[2], req.Targets[1], req.Targets[0]}
	_, err = e.Start(context.Background(), rev)
	require.NoError(t, err)
	got := rc.all()
	assert.Equal(t, got[0].header.Get("X-Myriarch-Delivery"), got[1].header.Get("X-Myriarch-Delivery"), "a retry carries the same delivery id (receiver can de-duplicate)")
	// another attempt or wave is a different delivery
	req.Targets[0].Attempt = 2
	_, _ = e.Start(context.Background(), req)
	assert.NotEqual(t, got[0].header.Get("X-Myriarch-Delivery"), rc.all()[2].header.Get("X-Myriarch-Delivery"))
}

func TestWebhookBatching(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	def := webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.BatchSize = 4 })
	res, err := e.Start(context.Background(), updateReq(def, targets(10)))
	require.NoError(t, err)
	got := rc.all()
	require.Len(t, got, 3, "10 targets in batches of 4")
	sizes := []int{}
	seen := map[string]bool{}
	for _, r := range got {
		var p executor.WebhookPayload
		require.NoError(t, json.Unmarshal(r.body, &p))
		sizes = append(sizes, len(p.Targets))
		seen[r.header.Get("X-Myriarch-Delivery")] = true
		verifySignature(t, r, testSecret)
	}
	assert.Equal(t, []int{4, 4, 2}, sizes)
	assert.Len(t, seen, 3, "every batch has its own delivery id")
	for _, tr := range res {
		assert.True(t, tr.Started)
	}
}

func TestWebhookDefaultBatchSizeIs100(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	_, err := e.Start(context.Background(), updateReq(webhookDef(srv.URL), targets(250)))
	require.NoError(t, err)
	assert.Len(t, rc.all(), 3)
}

func TestWebhookFailureModes(t *testing.T) {
	cases := []struct {
		name    string
		handler func(r *recorded, w http.ResponseWriter)
		want    string
	}{
		{"500", func(r *recorded, w http.ResponseWriter) { http.Error(w, "pipeline broken\nreally", 500) }, "webhook returned HTTP 500: pipeline broken really"},
		{"404", func(r *recorded, w http.ResponseWriter) { http.NotFound(w, nil) }, "webhook returned HTTP 404"},
		{"401", func(r *recorded, w http.ResponseWriter) { w.WriteHeader(401) }, "webhook returned HTTP 401"},
		{"redirect is not followed", func(r *recorded, w http.ResponseWriter) {
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
			w.WriteHeader(302)
		}, "webhook returned HTTP 302"},
		{"timeout", func(r *recorded, w http.ResponseWriter) { time.Sleep(300 * time.Millisecond) }, "webhook call failed: timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := &recorder{handler: c.handler}
			srv := httptest.NewServer(rc)
			defer srv.Close()
			e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
			def := webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.Timeout = domain.Duration(100 * time.Millisecond) })
			res, err := e.Start(context.Background(), updateReq(def, targets(2)))
			require.NoError(t, err)
			for _, tr := range res {
				assert.False(t, tr.Started)
				assert.Contains(t, tr.Error, c.want)
			}
			assert.Len(t, rc.all(), 1, "no redirect following, no retry")
		})
	}
	// connection refused: the error must not leak the URL
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	res, err := e.Start(context.Background(), updateReq(webhookDef("http://127.0.0.1:1/hook?token=SUPERSECRET"), targets(1)))
	require.NoError(t, err)
	assert.Contains(t, res[0].Error, "webhook call failed")
	assert.NotContains(t, res[0].Error, "SUPERSECRET")
}

func TestWebhookOneFailingBatchDoesNotFailTheOthers(t *testing.T) {
	var n int
	var mu sync.Mutex
	rc := &recorder{handler: func(r *recorded, w http.ResponseWriter) {
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		if k == 2 {
			http.Error(w, "boom", 503)
			return
		}
		w.WriteHeader(200)
	}}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	res, err := e.Start(context.Background(), updateReq(webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.BatchSize = 2 }), targets(6)))
	require.NoError(t, err)
	started := 0
	for i, tr := range res {
		if tr.Started {
			started++
		} else {
			assert.Contains(t, tr.Error, "HTTP 503")
			assert.True(t, i == 2 || i == 3, "the second batch failed")
		}
	}
	assert.Equal(t, 4, started)
}

func TestWebhookValidate(t *testing.T) {
	srv := httptest.NewServer(&recorder{})
	defer srv.Close()
	spec := domain.UpdateSpec{Executor: domain.ExecutorWebhook, ExecutorRef: "hook"}
	tmp := t.TempDir()
	emptyFile := filepath.Join(tmp, "empty")
	require.NoError(t, os.WriteFile(emptyFile, nil, 0o600))
	goodFile := filepath.Join(tmp, "good")
	require.NoError(t, os.WriteFile(goodFile, []byte(testSecret+"\n"), 0o600))

	cases := []struct {
		name string
		cfg  config.ExecutorsConfig
		env  map[string]string
		def  *domain.Executor
		want string // "" = ok
	}{
		{"ok env", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef(srv.URL), ""},
		{"ok file", config.ExecutorsConfig{SecretDir: tmp, AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "good" }), ""},
		{"file outside the secret dir", config.ExecutorsConfig{SecretDir: tmp, AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "../good" }), "cannot read secret file"},
		{"absolute file path from Git", config.ExecutorsConfig{SecretDir: tmp, AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", goodFile }), "cannot read secret file"},
		{"files disabled without secretDir", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "good" }), "secret files are disabled"},
		{"arbitrary environment variable", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_DATABASE_DSN": "postgres://secret"}, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv = "MYRIARCH_DATABASE_DSN" }), "is not allowed"},
		{"short secret", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": "short"}, webhookDef(srv.URL), "at least 32 bytes"},
		{"registry secret for an allowed host", config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com", "other.example.com"}, Secrets: map[string]config.ExecutorSecret{"deploy": {Env: "ANY_NAME", AllowedHosts: []string{"ci.example.com"}}}}, map[string]string{"ANY_NAME": testSecret}, webhookDef("https://ci.example.com/hook", func(w *domain.WebhookExecutorSpec) { w.SecretEnv = "deploy" }), ""},
		{"registry secret for another host", config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com", "other.example.com"}, Secrets: map[string]config.ExecutorSecret{"deploy": {Env: "ANY_NAME", AllowedHosts: []string{"ci.example.com"}}}}, map[string]string{"ANY_NAME": testSecret}, webhookDef("https://other.example.com/hook", func(w *domain.WebhookExecutorSpec) { w.SecretEnv = "deploy" }), "may not be sent to this host"},
		{"timeout above the maximum", config.ExecutorsConfig{MaxTimeout: time.Minute, AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.Timeout = domain.Duration(time.Hour) }), "exceeds executors.maxTimeout"},
		{"no definition", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil, nil, `executor "hook" does not exist`},
		{"env unset", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL), "environment variable MYRIARCH_EXEC_HOOK_SECRET is not set"},
		{"file missing", config.ExecutorsConfig{SecretDir: tmp, AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "nope" }), "cannot read secret file"},
		{"file empty", config.ExecutorsConfig{SecretDir: tmp, AllowedWebhookHosts: allowLoopback}, nil, webhookDef(srv.URL, func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "empty" }), "is empty"},
		{"host not allowed", config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef(srv.URL), "is not in executors.allowedWebhookHosts"},
		{"host allowed exact", config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://ci.example.com/hook"), ""},
		{"host allowed with port", config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com:8443"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://ci.example.com:8443/hook"), ""},
		{"wrong port", config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com:8443"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://ci.example.com/hook"), "not in executors.allowedWebhookHosts"},
		{"wildcard", config.ExecutorsConfig{AllowedWebhookHosts: []string{"*.example.internal"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://hooks.example.internal/x"), ""},
		{"wildcard does not match the apex or lookalikes", config.ExecutorsConfig{AllowedWebhookHosts: []string{"*.example.internal"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://evil-example.internal/x"), "not in executors.allowedWebhookHosts"},
		{"apex", config.ExecutorsConfig{AllowedWebhookHosts: []string{"*.example.internal"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://example.internal/x"), "not in executors.allowedWebhookHosts"},
		{"empty list refuses cloud metadata", config.ExecutorsConfig{}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("http://169.254.169.254/latest"), failClosedMessage},
		{"empty list refuses the unspecified address", config.ExecutorsConfig{}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("http://0.0.0.0:8080/x"), failClosedMessage},
		{"empty list refuses loopback", config.ExecutorsConfig{}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("http://[::1]:8080/x"), failClosedMessage},
		{"link-local allowed explicitly", config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://169.254.169.254"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("http://169.254.169.254/x"), ""},
		{"loopback allowed explicitly", config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://127.0.0.1:9999"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("http://127.0.0.1:9999/x"), ""},
		{"plain http needs the explicit opt-in", config.ExecutorsConfig{AllowedWebhookHosts: []string{"127.0.0.1:9999"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("http://127.0.0.1:9999/x"), "only be called over https"},
		{"https is fine for an opt-in entry", config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://ci.example.com"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, webhookDef("https://ci.example.com/x"), ""},
		{"wrong type", config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}, &domain.Executor{ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorAWX}}, "is not a webhook executor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newExec(t, domain.ExecutorWebhook, c.cfg, c.env)
			issues := e.Validate(spec, c.def)
			if c.want == "" {
				assert.Empty(t, issues)
				return
			}
			require.NotEmpty(t, issues)
			assert.Equal(t, domain.SeverityError, issues[0].Severity)
			assert.Equal(t, "executor_invalid", issues[0].Code)
			assert.Contains(t, issues[0].Message, c.want)
		})
	}
}

func TestStartRefusesDisallowedHostAndMissingSecretWithoutCalling(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: []string{"ci.example.com"}}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	res, err := e.Start(context.Background(), updateReq(webhookDef(srv.URL), targets(2)))
	require.NoError(t, err)
	for _, tr := range res {
		assert.Contains(t, tr.Error, "not in executors.allowedWebhookHosts")
	}
	e2 := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil)
	res, _ = e2.Start(context.Background(), updateReq(webhookDef(srv.URL), targets(1)))
	assert.Contains(t, res[0].Error, "MYRIARCH_EXEC_HOOK_SECRET is not set")
	assert.Empty(t, rc.all(), "no request is sent")
}

func TestWebhookCancel(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	rid := uuid.MustParse("01a11121-e597-7b69-9d25-a80cc7b9c707")
	ts := targets(3)
	req := executor.CancelRequest{
		RolloutID: rid, AgentIDs: []domain.AgentID{ts[0].AgentID, ts[1].AgentID, ts[2].AgentID},
		Spec:       domain.UpdateSpec{Executor: domain.ExecutorWebhook, Params: map[string]any{"deployment": "x"}},
		Definition: webhookDef(srv.URL),
	}
	require.NoError(t, e.Cancel(context.Background(), req))
	got := rc.all()
	require.Len(t, got, 1)
	verifySignature(t, got[0], testSecret)
	var p executor.WebhookPayload
	require.NoError(t, json.Unmarshal(got[0].body, &p))
	assert.Equal(t, "collector.update.cancel", p.Event)
	assert.Equal(t, rid, p.RolloutID)
	require.Len(t, p.Targets, 3)
	assert.Equal(t, map[string]any{"deployment": "x"}, p.Params, "engine internals are not sent")

	// best effort: errors are reported, nothing panics
	rc.handler = func(r *recorded, w http.ResponseWriter) { http.Error(w, "nope", 500) }
	assert.Error(t, e.Cancel(context.Background(), req))
	assert.NoError(t, e.Cancel(context.Background(), executor.CancelRequest{}))
	assert.NoError(t, e.Cancel(context.Background(), executor.CancelRequest{AgentIDs: req.AgentIDs}), "no definition: nothing to do")
}

func TestSignatureFunction(t *testing.T) {
	// Known value: HMAC-SHA256("secret", "1760000000.{\"a\":1}")
	m := hmac.New(sha256.New, []byte("secret"))
	m.Write([]byte(`1760000000.{"a":1}`))
	assert.Equal(t, "sha256="+hex.EncodeToString(m.Sum(nil)), Signature([]byte("secret"), 1760000000, []byte(`{"a":1}`)))
	assert.NotEqual(t, Signature([]byte("secret"), 1760000000, []byte(`{"a":1}`)), Signature([]byte("secret"), 1760000001, []byte(`{"a":1}`)), "the timestamp is signed (replay protection)")
}

func TestTypes(t *testing.T) {
	assert.Equal(t, domain.ExecutorWebhook, NewWebhook(config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil, nil).Type())
	assert.Equal(t, domain.ExecutorAWX, NewAWX(config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil, nil).Type())
	assert.Equal(t, []int{2, 2, 1}, func() []int {
		var out []int
		for _, b := range batches([]int{1, 2, 3, 4, 5}, 2) {
			out = append(out, len(b))
		}
		return out
	}())
	assert.Empty(t, batches([]int{}, 2))
	assert.Equal(t, "a b c...", excerpt([]byte(strings.Repeat(" a\n b c", 1))[:0])+"a b c...")
}

// SIG-2: Git only names secrets; arbitrary environment variables and files of the myriarchd host are not readable
// and a registry secret is never sent to a host outside its allowedHosts.
func TestStartNeverReadsArbitrarySecretsAndBindsRegistrySecretsToHosts(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	secretDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(secretDir, "hook"), []byte(testSecret), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(secretDir), "outside-"+filepath.Base(secretDir)), []byte(testSecret), 0o600))
	srvHost := strings.TrimPrefix(srv.URL, "http://")

	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{SecretDir: secretDir, AllowedWebhookHosts: []string{"http://" + srvHost},
		Secrets: map[string]config.ExecutorSecret{
			"deploy": {Env: "UNPREFIXED_DEPLOY_SECRET", AllowedHosts: []string{"ci.example.com"}},
			"local":  {File: "hook", AllowedHosts: []string{srvHost}},
		}},
		map[string]string{"MYRIARCH_DATABASE_DSN": "postgres://admin:pw@db/x", "UNPREFIXED_DEPLOY_SECRET": testSecret})

	try := func(mut func(w *domain.WebhookExecutorSpec)) executor.TargetResult {
		res, err := e.Start(context.Background(), updateReq(webhookDef(srv.URL, mut), targets(1)))
		require.NoError(t, err)
		return res[0]
	}
	// arbitrary environment variable of the myriarchd process
	r := try(func(w *domain.WebhookExecutorSpec) { w.SecretEnv = "MYRIARCH_DATABASE_DSN" })
	assert.False(t, r.Started)
	assert.Contains(t, r.Error, "is not allowed")
	// file outside the secret directory (relative escape and absolute path)
	r = try(func(w *domain.WebhookExecutorSpec) {
		w.SecretEnv, w.SecretFile = "", "../outside-"+filepath.Base(secretDir)
	})
	assert.False(t, r.Started)
	r = try(func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "/etc/passwd" })
	assert.False(t, r.Started)
	// a registry secret for another destination
	r = try(func(w *domain.WebhookExecutorSpec) { w.SecretEnv = "deploy" })
	assert.False(t, r.Started)
	assert.Contains(t, r.Error, "may not be sent to this host")
	assert.Empty(t, rc.all(), "no request may be made in any of these cases")

	// the registry entry for this host works, and so does a file inside the secret directory
	r = try(func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "local", "" })
	assert.True(t, r.Started, r.Error)
	r = try(func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "hook" })
	assert.True(t, r.Started, r.Error)
	for _, got := range rc.all() {
		verifySignature(t, got, testSecret)
	}
	assert.Len(t, rc.all(), 2)
}

// OUT-1: the destination policy checks the resolved address of every executor connection.
func TestExecutorCallsGoThroughTheDestinationPolicy(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	cfg := config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://" + host}}
	run := func(p *netguard.Policy) executor.TargetResult {
		e := newBase(domain.ExecutorWebhook, cfg, p.Client(0), slog.New(slog.NewTextHandler(io.Discard, nil)))
		e.getenv = func(k string) string { return map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}[k] }
		res, err := e.Start(context.Background(), updateReq(webhookDef(srv.URL), targets(1)))
		require.NoError(t, err)
		return res[0]
	}
	// the host list allows the target by name, but the address is loopback: refused at connection time
	denied, _ := netguard.New(nil)
	r := run(denied)
	assert.False(t, r.Started)
	assert.Contains(t, r.Error, "outbound policy")
	assert.Empty(t, rc.all())
	// an operator opens the range explicitly
	ip, _, _ := net.SplitHostPort(host)
	allowed, _ := netguard.New([]string{ip + "/32"})
	assert.True(t, run(allowed).Started)
	assert.Len(t, rc.all(), 1)
}

// SIG-2: a secret referenced by its prefixed environment name or as a file is only released for the hosts bound to it;
// production (RequireSecretHosts) refuses references without a binding.
func TestPrefixedSecretsAreBoundToDestinations(t *testing.T) {
	rc1, rc2 := &recorder{}, &recorder{}
	srv1, srv2 := httptest.NewServer(rc1), httptest.NewServer(rc2)
	defer srv1.Close()
	defer srv2.Close()
	h1, h2 := strings.TrimPrefix(srv1.URL, "http://"), strings.TrimPrefix(srv2.URL, "http://")
	secretDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(secretDir, "hook"), []byte(testSecret), 0o600))
	env := map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret}
	base := func(mut func(c *config.ExecutorsConfig)) *exec {
		c := config.ExecutorsConfig{SecretDir: secretDir, AllowedWebhookHosts: []string{"http://" + h1, "http://" + h2}}
		mut(&c)
		return newExec(t, domain.ExecutorWebhook, c, env)
	}
	start := func(e *exec, url string, mut func(w *domain.WebhookExecutorSpec)) executor.TargetResult {
		if mut == nil {
			mut = func(*domain.WebhookExecutorSpec) {}
		}
		res, err := e.Start(context.Background(), updateReq(webhookDef(url, mut), targets(1)))
		require.NoError(t, err)
		return res[0]
	}
	useFile := func(w *domain.WebhookExecutorSpec) { w.SecretEnv, w.SecretFile = "", "hook" }

	// production without any binding: closed
	prod := base(func(c *config.ExecutorsConfig) { c.RequireSecretHosts = true })
	r := start(prod, srv1.URL, nil)
	assert.False(t, r.Started)
	assert.Contains(t, r.Error, "not bound to a destination")
	r = start(prod, srv1.URL, useFile)
	assert.False(t, r.Started)
	assert.Contains(t, r.Error, "not bound to a destination")
	assert.Empty(t, rc1.all())

	// bound: allowed for the bound host, refused for another allowed host (Start and Validate)
	bound := base(func(c *config.ExecutorsConfig) {
		c.RequireSecretHosts = true
		c.SecretEnvHosts = map[string][]string{"MYRIARCH_EXEC_HOOK_SECRET": {h1}}
		c.SecretFileHosts = map[string][]string{"hook": {h1}}
	})
	assert.True(t, start(bound, srv1.URL, nil).Started)
	assert.True(t, start(bound, srv1.URL, useFile).Started)
	for _, mut := range []func(w *domain.WebhookExecutorSpec){nil, useFile} {
		r = start(bound, srv2.URL, mut)
		assert.False(t, r.Started)
		assert.Contains(t, r.Error, "may not be sent to this host")
	}
	assert.Empty(t, rc2.all(), "the secret must not reach the second host")
	def := webhookDef(srv2.URL)
	issues := bound.Validate(domain.UpdateSpec{ExecutorRef: def.Name}, def)
	require.Len(t, issues, 1)
	assert.Contains(t, issues[0].Message, "may not be sent to this host")

	// dev mode keeps unbound references working
	dev := base(func(c *config.ExecutorsConfig) {})
	assert.True(t, start(dev, srv2.URL, nil).Started)
}

// Secret files named by the operator in the registry are read with the same bound as files below
// executors.secretDir and must be regular files.
func TestRegistryFilesAreBoundedAndRegular(t *testing.T) {
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{}, nil)
	dir := t.TempDir()

	small := filepath.Join(dir, "small")
	if err := os.WriteFile(small, []byte(testSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := e.fromFile(small, "webhook secret", true); err != nil || string(b) != testSecret {
		t.Fatalf("small registry file: %q %v", b, err)
	}

	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, bytes.Repeat([]byte("a"), 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fromFile(big, "webhook secret", true); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a 2 MiB registry file must be refused: %v", err)
	}

	if _, err := e.fromFile(dir, "webhook secret", true); err == nil {
		t.Fatal("a directory must be refused")
	}
}
