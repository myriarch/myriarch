// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package webhookexec

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/netguard"
)

func awxDef(url string, mut ...func(a *domain.AWXExecutorSpec)) *domain.Executor {
	a := &domain.AWXExecutorSpec{
		URL: url, JobTemplateID: 42, TokenEnv: "MYRIARCH_EXEC_AWX_TOKEN",
		ExtraVars: map[string]any{
			"otelcol_version": "[[ .toVersion ]]",
			"rollout":         "[[ .rolloutId ]]",
			"wave":            "[[ .wave ]]",
			"hosts":           "[[ .hostnames ]]",
			"deployment":      "[[ .params.deployment ]]",
			"label":           "otelcol-[[ .toVersion ]]-[[ .distribution ]]",
			"static":          "unchanged",
			"nested":          map[string]any{"fleet": "[[ .fleet ]]"},
		},
	}
	for _, m := range mut {
		m(a)
	}
	return &domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "awx-linux"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorAWX, AWX: a}}
}

func awxReq(def *domain.Executor, ts []executor.Target) executor.UpdateRequest {
	r := updateReq(def, ts)
	r.Spec.Executor = domain.ExecutorAWX
	r.Spec.ExecutorRef = "awx-linux"
	return r
}

func launchHandler(job int) func(r *recorded, w http.ResponseWriter) {
	return func(r *recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"job": %d, "id": %d, "status": "pending"}`, job, job)
	}
}

func TestAWXLaunchBody(t *testing.T) {
	rc := &recorder{handler: launchHandler(777)}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "tok-abc"})
	req := awxReq(awxDef(srv.URL+"/"), targets(3))

	res, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	got := rc.all()
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, http.MethodPost, r.method)
	assert.Equal(t, "/api/v2/job_templates/42/launch/", r.path)
	assert.Equal(t, "Bearer tok-abc", r.header.Get("Authorization"))
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))

	var body struct {
		Limit     string         `json:"limit"`
		ExtraVars map[string]any `json:"extra_vars"`
	}
	require.NoError(t, json.Unmarshal(r.body, &body))
	assert.Equal(t, "host-00.example.com,host-01.example.com,host-02.example.com", body.Limit, "AWX limit: attribute host.name, comma separated")
	ev := body.ExtraVars
	assert.Equal(t, "0.162.0", ev["otelcol_version"])
	assert.Equal(t, req.RolloutID.String(), ev["rollout"])
	assert.EqualValues(t, 2, ev["wave"], "typed evaluation keeps numbers")
	assert.Equal(t, []any{"host-00.example.com", "host-01.example.com", "host-02.example.com"}, ev["hosts"])
	assert.Equal(t, "otel-gateway", ev["deployment"], ".params from fleet.spec.update.params")
	assert.Equal(t, "otelcol-0.162.0-otelcol-contrib", ev["label"])
	assert.Equal(t, "unchanged", ev["static"])
	assert.Equal(t, map[string]any{"fleet": "k8s-gateway"}, ev["nested"])

	// the WebhookPayload is passed as extra_vars.myriarch
	op, ok := ev["myriarch"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "collector.update", op["event"])
	assert.Equal(t, "k8s-gateway", op["fleet"])
	assert.Len(t, op["targets"], 3)

	for _, tr := range res {
		assert.True(t, tr.Started)
		assert.Equal(t, "awx-job:777", tr.ExternalRef)
	}
}

func TestAWXLimitAttributeAndBatches(t *testing.T) {
	job := 0
	var mu sync.Mutex
	rc := &recorder{handler: func(r *recorded, w http.ResponseWriter) {
		mu.Lock()
		job++
		j := job
		mu.Unlock()
		launchHandler(j)(r, w)
	}}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	ts := targets(5)
	ts[3].Attributes = domain.AttributeView{"site.id": "3"} // no host.name attribute: falls back to the hostname
	ts[4].Attributes, ts[4].Hostname = domain.AttributeView{}, ""
	req := awxReq(awxDef(srv.URL, func(a *domain.AWXExecutorSpec) { a.BatchSize = 2 }), ts)
	res, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	got := rc.all()
	require.Len(t, got, 2, "4 addressable hosts in batches of 2")
	var limits []string
	for _, g := range got {
		var b struct{ Limit string }
		require.NoError(t, json.Unmarshal(g.body, &b))
		limits = append(limits, b.Limit)
	}
	assert.Equal(t, "host-00.example.com,host-01.example.com", limits[0])
	assert.Equal(t, "host-02.example.com,host-03", limits[1])
	assert.Equal(t, "awx-job:1", res[0].ExternalRef)
	assert.Equal(t, "awx-job:2", res[2].ExternalRef)
	assert.False(t, res[4].Started)
	assert.Contains(t, res[4].Error, `agent has no attribute "host.name"`)

	// custom limit attribute
	rc2 := &recorder{handler: launchHandler(5)}
	srv2 := httptest.NewServer(rc2)
	defer srv2.Close()
	req = awxReq(awxDef(srv2.URL, func(a *domain.AWXExecutorSpec) { a.LimitAttribute = "site.id" }), targets(3))
	_, err = e.Start(context.Background(), req)
	require.NoError(t, err)
	var b struct{ Limit string }
	require.NoError(t, json.Unmarshal(rc2.all()[0].body, &b))
	assert.Equal(t, "0,1,2", b.Limit)
}

func TestAWXFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler func(r *recorded, w http.ResponseWriter)
		want    string
	}{
		{"401", func(r *recorded, w http.ResponseWriter) {
			http.Error(w, `{"detail":"Authentication credentials were not provided."}`, 401)
		}, "AWX returned HTTP 401: {\"detail\":\"Authentication credentials were not provided.\"}"},
		{"400 template", func(r *recorded, w http.ResponseWriter) { http.Error(w, `{"variables_needed_to_start":["x"]}`, 400) }, "AWX returned HTTP 400"},
		{"no job", func(r *recorded, w http.ResponseWriter) {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}, "no job id"},
		{"not json", func(r *recorded, w http.ResponseWriter) { w.WriteHeader(201); _, _ = w.Write([]byte(`<html>`)) }, "no job id"},
		{"limit ignored", func(r *recorded, w http.ResponseWriter) {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"job": 9, "ignored_fields": {"limit": "a,b"}}`))
		}, "AWX ignored the limit"},
		{"extra_vars ignored", func(r *recorded, w http.ResponseWriter) {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"job": 9, "ignored_fields": {"extra_vars": "{}"}}`))
		}, "AWX ignored extra_vars"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := &recorder{handler: c.handler}
			srv := httptest.NewServer(rc)
			defer srv.Close()
			e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
			res, err := e.Start(context.Background(), awxReq(awxDef(srv.URL), targets(2)))
			require.NoError(t, err)
			for _, tr := range res {
				assert.False(t, tr.Started)
				assert.Contains(t, tr.Error, c.want)
			}
		})
	}
}

func TestAWXIgnoredLimitCancelsTheRunawayJob(t *testing.T) {
	rc := &recorder{}
	rc.handler = func(r *recorded, w http.ResponseWriter) {
		if strings.HasSuffix(r.path, "/launch/") {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"job": 9, "ignored_fields": {"limit": "a"}}`))
			return
		}
		w.WriteHeader(202)
	}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	_, err := e.Start(context.Background(), awxReq(awxDef(srv.URL), targets(1)))
	require.NoError(t, err)
	got := rc.all()
	require.Len(t, got, 2)
	assert.Equal(t, "/api/v2/jobs/9/cancel/", got[1].path, "a job that would run on all hosts is cancelled immediately")
}

func TestAWXExtraVarsTemplateErrorFailsTheBatchWithoutLaunching(t *testing.T) {
	rc := &recorder{handler: launchHandler(1)}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	def := awxDef(srv.URL, func(a *domain.AWXExecutorSpec) { a.ExtraVars = map[string]any{"x": "[[ .nope.deeper ]]"} })
	res, err := e.Start(context.Background(), awxReq(def, targets(2)))
	require.NoError(t, err)
	assert.Contains(t, res[0].Error, "extraVars")
	assert.Empty(t, rc.all())
}

func TestAWXValidate(t *testing.T) {
	srv := httptest.NewServer(&recorder{})
	defer srv.Close()
	spec := domain.UpdateSpec{Executor: domain.ExecutorAWX, ExecutorRef: "awx-linux"}
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	assert.Empty(t, e.Validate(spec, awxDef(srv.URL)))

	e2 := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, nil)
	issues := e2.Validate(spec, awxDef(srv.URL))
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "environment variable MYRIARCH_EXEC_AWX_TOKEN is not set")

	e3 := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: []string{"awx.example.internal"}}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	issues = e3.Validate(spec, awxDef(srv.URL))
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "allowedWebhookHosts")
	assert.Empty(t, e3.Validate(spec, awxDef("https://awx.example.internal")))

	caDir := t.TempDir()
	e = newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{SecretDir: caDir, AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	bad := awxDef(srv.URL, func(a *domain.AWXExecutorSpec) { a.CAFile = "missing.pem" })
	issues = e.Validate(spec, bad)
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "caFile")
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "garbage.pem"), []byte("not pem"), 0o600))
	issues = e.Validate(spec, awxDef(srv.URL, func(a *domain.AWXExecutorSpec) { a.CAFile = "garbage.pem" }))
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "no valid PEM")
}

func TestAWXCustomCA(t *testing.T) {
	rc := &recorder{handler: launchHandler(31)}
	srv := httptest.NewTLSServer(rc)
	defer srv.Close()
	caDir := t.TempDir()
	caFile := "ca.pem"
	require.NoError(t, os.WriteFile(filepath.Join(caDir, caFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{SecretDir: caDir, AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})

	// without the CA the handshake fails (and the error does not leak the URL)
	res, err := e.Start(context.Background(), awxReq(awxDef(srv.URL), targets(1)))
	require.NoError(t, err)
	assert.False(t, res[0].Started)
	assert.Contains(t, res[0].Error, "AWX launch failed")

	// with caFile it works
	res, err = e.Start(context.Background(), awxReq(awxDef(srv.URL, func(a *domain.AWXExecutorSpec) { a.CAFile = caFile }), targets(1)))
	require.NoError(t, err)
	assert.True(t, res[0].Started, res[0].Error)
	assert.Equal(t, "awx-job:31", res[0].ExternalRef)
}

func TestAWXCancel(t *testing.T) {
	rc := &recorder{}
	rc.handler = func(r *recorded, w http.ResponseWriter) {
		switch {
		case strings.HasSuffix(r.path, "/jobs/12/cancel/"):
			w.WriteHeader(202)
		case strings.HasSuffix(r.path, "/jobs/13/cancel/"):
			http.Error(w, `{"detail":"The cancel action is not available"}`, 405)
		default:
			w.WriteHeader(404)
		}
	}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
	ts := targets(4)
	req := executor.CancelRequest{
		RolloutID: awxReq(nil, nil).RolloutID,
		AgentIDs:  []domain.AgentID{ts[0].AgentID, ts[1].AgentID, ts[2].AgentID, ts[3].AgentID},
		Spec:      domain.UpdateSpec{Executor: domain.ExecutorAWX},
		Refs: map[domain.AgentID]string{
			ts[0].AgentID: "awx-job:12", ts[1].AgentID: "awx-job:12", // same job for a batch
			ts[2].AgentID: "awx-job:13", ts[3].AgentID: "started",
		},
		Definition: awxDef(srv.URL),
	}
	err := e.Cancel(context.Background(), req)
	require.Error(t, err, "job 13 could not be cancelled")
	assert.Contains(t, err.Error(), "cancel AWX job 13")
	paths := []string{}
	for _, r := range rc.all() {
		paths = append(paths, r.path)
		assert.Equal(t, "Bearer t", r.header.Get("Authorization"))
	}
	assert.Equal(t, []string{"/api/v2/jobs/12/cancel/", "/api/v2/jobs/13/cancel/"}, paths, "each job once; refs without job ids are ignored")

	// nothing to cancel without refs
	req.Refs = nil
	rc2 := len(rc.all())
	assert.NoError(t, e.Cancel(context.Background(), req))
	assert.Len(t, rc.all(), rc2)

	// already finished jobs (409/404) are fine
	rc.handler = func(r *recorded, w http.ResponseWriter) { w.WriteHeader(http.StatusConflict) }
	req.Refs = map[domain.AgentID]string{ts[0].AgentID: "awx-job:12"}
	assert.NoError(t, e.Cancel(context.Background(), req))
}

func TestContextCancellationStopsStart(t *testing.T) {
	rc := &recorder{handler: func(r *recorded, w http.ResponseWriter) { time.Sleep(200 * time.Millisecond) }}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.Start(ctx, updateReq(webhookDef(srv.URL), targets(2)))
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 150*time.Millisecond)
}

// EXEC-1: values from agent attributes become AWX limit entries only if they are plain host names.
func TestAWXLimitValuesAreValidated(t *testing.T) {
	rc := &recorder{handler: launchHandler(5)}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})

	hostile := []string{
		"all", "ALL", "ungrouped", // groups that address more than one host
		"web*", "*", "!prod", "a,b", "a:b", "a&b", "~regex", "h[1:3]", "a b", " a", "a\nb", "", "-leading-dash", ".leading-dot",
		strings.Repeat("a", 300), "host;rm -rf /", "host`id`", "$(id)", "a/../b",
	}
	ts := targets(len(hostile) + 2)
	for i, h := range hostile {
		ts[i].Attributes = domain.AttributeView{"host.name": h}
		ts[i].Hostname = ""
	}
	good1, good2 := len(hostile), len(hostile)+1
	ts[good1].Attributes = domain.AttributeView{"host.name": "edge-1001.sites.example.com"}
	ts[good2].Attributes = domain.AttributeView{"host.name": "edge_1002"}

	res, err := e.Start(context.Background(), awxReq(awxDef(srv.URL), ts))
	require.NoError(t, err)
	for i := range hostile {
		assert.False(t, res[i].Started, "%q must not be used as limit", hostile[i])
		assert.NotEmpty(t, res[i].Error)
	}
	assert.True(t, res[good1].Started, res[good1].Error)
	assert.True(t, res[good2].Started, res[good2].Error)

	got := rc.all()
	require.Len(t, got, 1, "one launch for the valid hosts only")
	var body struct {
		Limit string `json:"limit"`
	}
	require.NoError(t, json.Unmarshal(got[0].body, &body))
	assert.Equal(t, "edge-1001.sites.example.com,edge_1002", body.Limit)
}

// SIG-2 / EXEC-1: a timeout from Git cannot exceed the configured maximum.
func TestExecutorTimeoutIsCapped(t *testing.T) {
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{MaxTimeout: 2 * time.Second, AllowedWebhookHosts: allowLoopback}, nil)
	assert.Equal(t, 2*time.Second, e.timeout(domain.Duration(time.Hour)))
	assert.Equal(t, time.Second, e.timeout(domain.Duration(time.Second)))
	assert.Equal(t, 2*time.Second, e.timeout(0), "default 30s is capped, too")
}

// FX-5: the caFile of an AWX executor ADDS to the trust store of the shared
// client (outbound.caFile), it does not replace it.
func TestAWXCAFileExtendsTheOutboundTrustStore(t *testing.T) {
	// (httptest servers share one certificate: these two have their own)
	internal := newTLSServerWithOwnCert(t, &recorder{handler: launchHandler(1)}) // trusted through outbound.caFile
	awx := newTLSServerWithOwnCert(t, &recorder{handler: launchHandler(2)})      // trusted through the caFile of the executor

	caDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "awx.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: awx.Certificate().Raw}), 0o600))
	policy, err := netguard.New([]string{"127.0.0.1"})
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(internal.Certificate())
	policy.SetRootCAs(pool)

	e := newBase(domain.ExecutorAWX, config.ExecutorsConfig{SecretDir: caDir, AllowedWebhookHosts: allowLoopback}, policy.Client(10*time.Second), nil)
	e.getenv = func(k string) string { return map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"}[k] }
	start := func(url, caFile string) []executor.TargetResult {
		res, err := e.Start(context.Background(), awxReq(awxDef(url, func(a *domain.AWXExecutorSpec) { a.CAFile = caFile }), targets(1)))
		require.NoError(t, err)
		return res
	}

	assert.True(t, start(internal.URL, "")[0].Started, "the CA of outbound.caFile is trusted")
	assert.False(t, start(awx.URL, "")[0].Started, "a server of a CA nobody configured is refused")
	assert.True(t, start(awx.URL, "awx.pem")[0].Started, "the caFile of the executor is trusted")
	res := start(internal.URL, "awx.pem")
	assert.True(t, res[0].Started, "with a caFile the CA of outbound.caFile stays trusted: %s", res[0].Error)
}

// newTLSServerWithOwnCert starts an HTTPS server on 127.0.0.1 with a fresh self-signed certificate.
func newTLSServerWithOwnCert(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "test server"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// An empty or blank allowlist refuses webhook and AWX targets in Validate,
// Start and Cancel without sending a request (fail closed).
func TestEmptyAllowlistRefusesEveryTarget(t *testing.T) {
	for name, list := range map[string][]string{"nil": nil, "empty": {}, "blank entries": {"", "  ", "\t"}} {
		t.Run(name, func(t *testing.T) {
			rc := &recorder{handler: launchHandler(5)}
			srv := httptest.NewServer(rc)
			defer srv.Close()
			cfg := config.ExecutorsConfig{AllowedWebhookHosts: list}
			ts := targets(2)

			// webhook
			hook := newExec(t, domain.ExecutorWebhook, cfg, map[string]string{"MYRIARCH_EXEC_HOOK_SECRET": testSecret})
			hspec := domain.UpdateSpec{Executor: domain.ExecutorWebhook, ExecutorRef: "hook"}
			issues := hook.Validate(hspec, webhookDef(srv.URL))
			require.NotEmpty(t, issues)
			assert.Equal(t, "executor_invalid", issues[0].Code)
			assert.Equal(t, failClosedMessage, issues[0].Message)
			res, err := hook.Start(context.Background(), updateReq(webhookDef(srv.URL), ts))
			require.NoError(t, err)
			for _, tr := range res {
				assert.False(t, tr.Started)
				assert.Equal(t, failClosedMessage, tr.Error)
			}
			err = hook.Cancel(context.Background(), executor.CancelRequest{
				RolloutID: awxReq(nil, nil).RolloutID, AgentIDs: []domain.AgentID{ts[0].AgentID},
				Spec: hspec, Definition: webhookDef(srv.URL),
			})
			require.Error(t, err)
			assert.Equal(t, failClosedMessage, err.Error())

			// AWX
			awx := newExec(t, domain.ExecutorAWX, cfg, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
			aspec := domain.UpdateSpec{Executor: domain.ExecutorAWX, ExecutorRef: "awx-linux"}
			issues = awx.Validate(aspec, awxDef(srv.URL))
			require.NotEmpty(t, issues)
			assert.Equal(t, "executor_invalid", issues[0].Code)
			assert.Equal(t, failClosedMessage, issues[0].Message)
			res, err = awx.Start(context.Background(), awxReq(awxDef(srv.URL), ts))
			require.NoError(t, err)
			for _, tr := range res {
				assert.False(t, tr.Started)
				assert.Equal(t, failClosedMessage, tr.Error)
			}
			err = awx.Cancel(context.Background(), executor.CancelRequest{
				RolloutID: awxReq(nil, nil).RolloutID, AgentIDs: []domain.AgentID{ts[0].AgentID},
				Spec: aspec, Refs: map[domain.AgentID]string{ts[0].AgentID: "awx-job:5"}, Definition: awxDef(srv.URL),
			})
			require.Error(t, err)
			assert.Equal(t, failClosedMessage, err.Error())

			assert.Empty(t, rc.all(), "a refused target never receives a request")
		})
	}
}

// spec.awx.apiPrefix moves the launch and cancel endpoints; the default stays
// /api/v2/ and the prefix is normalized to one trailing slash.
func TestAWXAPIPrefix(t *testing.T) {
	cases := []struct {
		name, prefix, launch, cancel string
	}{
		{"default", "", "/api/v2/job_templates/42/launch/", "/api/v2/jobs/9/cancel/"},
		{"controller prefix", "/api/controller/v2/", "/api/controller/v2/job_templates/42/launch/", "/api/controller/v2/jobs/9/cancel/"},
		{"missing trailing slash", "/api/controller/v2", "/api/controller/v2/job_templates/42/launch/", "/api/controller/v2/jobs/9/cancel/"},
		{"repeated trailing slashes", "/api/controller/v2//", "/api/controller/v2/job_templates/42/launch/", "/api/controller/v2/jobs/9/cancel/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// launch that ignores the limit: the job is cancelled at once
			rc := &recorder{}
			rc.handler = func(r *recorded, w http.ResponseWriter) {
				if strings.HasSuffix(r.path, "/launch/") {
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"job": 9, "ignored_fields": {"limit": "a"}}`))
					return
				}
				w.WriteHeader(http.StatusAccepted)
			}
			srv := httptest.NewServer(rc)
			defer srv.Close()
			e := newExec(t, domain.ExecutorAWX, config.ExecutorsConfig{AllowedWebhookHosts: allowLoopback}, map[string]string{"MYRIARCH_EXEC_AWX_TOKEN": "t"})
			def := awxDef(srv.URL+"/", func(a *domain.AWXExecutorSpec) { a.APIPrefix = c.prefix })
			_, err := e.Start(context.Background(), awxReq(def, targets(1)))
			require.NoError(t, err)
			got := rc.all()
			require.Len(t, got, 2)
			assert.Equal(t, c.launch, got[0].path)
			assert.Equal(t, c.cancel, got[1].path, "cancel after an ignored limit uses the prefix")

			// explicit cancel
			rc2 := &recorder{}
			srv2 := httptest.NewServer(rc2)
			defer srv2.Close()
			def = awxDef(srv2.URL, func(a *domain.AWXExecutorSpec) { a.APIPrefix = c.prefix })
			ts := targets(1)
			require.NoError(t, e.Cancel(context.Background(), executor.CancelRequest{
				RolloutID: awxReq(nil, nil).RolloutID, AgentIDs: []domain.AgentID{ts[0].AgentID},
				Spec: domain.UpdateSpec{Executor: domain.ExecutorAWX}, Refs: map[domain.AgentID]string{ts[0].AgentID: "awx-job:9"},
				Definition: def,
			}))
			got = rc2.all()
			require.Len(t, got, 1)
			assert.Equal(t, c.cancel, got[0].path)
		})
	}
}

func TestEffectiveAPIPrefix(t *testing.T) {
	var nilSpec *domain.AWXExecutorSpec
	assert.Equal(t, domain.DefaultAWXAPIPrefix, nilSpec.EffectiveAPIPrefix())
	assert.Equal(t, "/api/v2/", (&domain.AWXExecutorSpec{}).EffectiveAPIPrefix())
	assert.Equal(t, "/api/controller/v2/", (&domain.AWXExecutorSpec{APIPrefix: "/api/controller/v2"}).EffectiveAPIPrefix())
	assert.Equal(t, "/api/controller/v2/", (&domain.AWXExecutorSpec{APIPrefix: "/api/controller/v2/"}).EffectiveAPIPrefix())
}
