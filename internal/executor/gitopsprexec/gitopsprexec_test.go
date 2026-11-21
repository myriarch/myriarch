// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/executor/gitopsprexec/gitopsprtest"
)

const (
	valuesYAML   = "# gateway values\nreplicaCount: 2 # keep\nimage:\n  repository: registry.example.com/otel/collector\n  tag: 0.161.0 # the version\nresources:\n  limits: {cpu: 1}\n"
	manifestYAML = "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - name: collector\n          image: registry.example.com:5000/otel/col:0.161.0\n"
	tokenEnvName = "MYRIARCH_EXEC_PR_TOKEN"
)

type double interface {
	URL() string
	Host() string
	Token() string
	SetFile(branch, path, content string)
	File(branch, path string) (string, bool)
	Branches() []string
	Requests() []gitopsprtest.Request
	Merge(id int)
	CloseWithoutMerge(id int)
	RateLimitNext(n int)
	EchoAuthInErrors(on bool)
	Calls() []string
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)} }

type fixture struct {
	name    string
	d       double
	def     *domain.Executor
	file    string
	content string
	clk     *clock
}

func (f *fixture) path(cluster string) string { return "clusters/" + cluster + "/" + f.file }

func newDef(provider, apiURL, file, field, format string) *domain.Executor {
	return &domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "pr"}, ExecutorSpec: domain.ExecutorSpec{
		Type: domain.ExecutorGitOpsPR,
		GitOpsPR: &domain.GitOpsPRExecutorSpec{
			Provider: provider, APIURL: apiURL, Repository: "example/deploy", BaseBranch: "main",
			Path: `clusters/[[ attr "k8s.cluster.name" ]]/` + file, Field: field, ValueFormat: format, TokenEnv: tokenEnvName,
			Labels: []string{"collector"},
		},
	}}
}

func newFixture(t *testing.T, name string) *fixture {
	t.Helper()
	f := &fixture{name: name, clk: newClock()}
	switch name {
	case "github":
		f.d = gitopsprtest.NewGitHub(t)
		f.file, f.content = "values.yaml", valuesYAML
		f.def = newDef("github", f.d.URL(), f.file, "image.tag", "tag")
	default:
		f.d = gitopsprtest.NewGitLab(t)
		f.file, f.content = "deploy.yaml", manifestYAML
		f.def = newDef("gitlab", f.d.URL(), f.file, "spec.template.spec.containers[0].image", "image")
	}
	for _, c := range []string{"edge-1", "edge-2"} {
		f.d.SetFile("main", f.path(c), f.content)
	}
	return f
}

func eachProvider(t *testing.T, fn func(t *testing.T, f *fixture)) {
	for _, name := range []string{"github", "gitlab"} {
		t.Run(name, func(t *testing.T) { fn(t, newFixture(t, name)) })
	}
}

func (f *fixture) exec(t *testing.T, logs io.Writer, mod ...func(*config.ExecutorsConfig)) *Executor {
	t.Helper()
	cfg := config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://" + f.d.Host()}}
	for _, m := range mod {
		m(&cfg)
	}
	if logs == nil {
		logs = io.Discard
	}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := map[string]string{tokenEnvName: f.d.Token()}
	return New(cfg, nil, log, Options{PublicURL: "https://myriarch.example.com", Now: f.clk.Now, Getenv: func(k string) string { return env[k] }})
}

func target(cluster string) executor.Target {
	return executor.Target{AgentID: uuid.New(), Hostname: cluster, Attributes: domain.AttributeView{"k8s.cluster.name": cluster}}
}

func (f *fixture) req(rid uuid.UUID, ts ...executor.Target) executor.UpdateRequest {
	return executor.UpdateRequest{RolloutID: rid, Fleet: "edge", ToVersion: "0.162.0", Targets: ts, Definition: f.def,
		Spec: domain.UpdateSpec{Executor: domain.ExecutorGitOpsPR, ExecutorRef: "pr"}}
}

func lineDiff(a, b string) []string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(la) != len(lb) {
		return []string{"different number of lines"}
	}
	var d []string
	for i := range la {
		if la[i] != lb[i] {
			d = append(d, lb[i])
		}
	}
	return d
}

func count(calls []string, method, suffix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, method+" ") && strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

func TestStartGitHubChangesExactlyOneLine(t *testing.T) {
	testStartOneLine(t, newFixture(t, "github"), "  tag: 0.162.0 # the version")
}

func TestStartGitLabChangesExactlyOneLine(t *testing.T) {
	testStartOneLine(t, newFixture(t, "gitlab"), "          image: registry.example.com:5000/otel/col:0.162.0")
}

func testStartOneLine(t *testing.T, f *fixture, wantLine string) {
	rid := uuid.New()
	tg := target("edge-1")
	res, err := f.exec(t, nil).Start(context.Background(), f.req(rid, tg))
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.True(t, res[0].Started)
	assert.Empty(t, res[0].Error)
	assert.True(t, domain.IsChangeRequestRef(res[0].ExternalRef))
	assert.NotEmpty(t, res[0].ExternalURL)

	branch := brand.GitOpsPRBranchPrefix + rid.String() + "/wave-1"
	reqs := f.d.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, branch, reqs[0].Head)
	assert.Equal(t, "main", reqs[0].Base)
	assert.Contains(t, reqs[0].Body, rid.String())
	assert.Contains(t, reqs[0].Body, "https://myriarch.example.com/rollouts/"+rid.String())
	assert.Contains(t, reqs[0].Title, "0.162.0")

	changed := 0
	for _, c := range []string{"edge-1", "edge-2"} {
		base, _ := f.d.File("main", f.path(c))
		head, ok := f.d.File(branch, f.path(c))
		require.True(t, ok)
		if base == head {
			continue
		}
		changed++
		assert.Equal(t, []string{wantLine}, lineDiff(base, head))
	}
	assert.Equal(t, 1, changed, "exactly one file differs from the base branch")
	assert.Equal(t, valuesOrManifest(f), mustFile(t, f, "main", "edge-1"), "the base branch is untouched")
}

func valuesOrManifest(f *fixture) string { return f.content }

func mustFile(t *testing.T, f *fixture, branch, cluster string) string {
	t.Helper()
	c, ok := f.d.File(branch, f.path(cluster))
	require.True(t, ok)
	return c
}

func TestStartGroupsTargetsByFile(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		a, a2, b, c := target("edge-1"), target("edge-1"), target("edge-2"), executor.Target{AgentID: uuid.New(), Attributes: domain.AttributeView{}}
		res, err := f.exec(t, nil).Start(context.Background(), f.req(uuid.New(), a, b, c, a2))
		require.NoError(t, err)
		assert.True(t, res[0].Started && res[1].Started && res[3].Started)
		assert.Equal(t, res[0].ExternalRef, res[1].ExternalRef)
		assert.Equal(t, res[0].ExternalRef, res[3].ExternalRef)
		assert.False(t, res[2].Started)
		assert.Contains(t, res[2].Error, "is missing")
		assert.Len(t, f.d.Requests(), 1)
		assert.Equal(t, 1, count(f.d.Calls(), "POST", "/commits"), "one commit for both files")
		assert.Len(t, f.d.Branches(), 2)
	})
}

func TestStartAlreadyAtVersionOpensNoRequest(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		upd, _, _, err := setImageScalar([]byte(f.content), fieldOf(f), formatOf(f), "0.162.0")
		require.NoError(t, err)
		f.d.SetFile("main", f.path("edge-1"), string(upd))
		res, err := f.exec(t, nil).Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.NoError(t, err)
		assert.True(t, res[0].Started)
		assert.Empty(t, res[0].ExternalRef)
		assert.Empty(t, f.d.Requests())
		assert.Equal(t, []string{"main"}, f.d.Branches())
	})
}

func fieldOf(f *fixture) string  { return f.def.GitOpsPR.Field }
func formatOf(f *fixture) string { return f.def.GitOpsPR.ValueFormat }

func TestStartIsIdempotent(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		e := f.exec(t, nil)
		req := f.req(uuid.New(), target("edge-1"))
		r1, err := e.Start(context.Background(), req)
		require.NoError(t, err)
		r2, err := e.Start(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, r1[0].ExternalRef, r2[0].ExternalRef)
		assert.Len(t, f.d.Requests(), 1)
		assert.Len(t, f.d.Branches(), 2)
		assert.Equal(t, 1, count(f.d.Calls(), "POST", "/commits"))
	})
}

func TestStartRollbackUsesOwnBranch(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		rid := uuid.New()
		req := f.req(rid, target("edge-1"))
		req.Rollback = true
		req.ToVersion = "0.160.0"
		res, err := f.exec(t, nil).Start(context.Background(), req)
		require.NoError(t, err)
		require.True(t, res[0].Started)
		assert.Equal(t, brand.GitOpsPRBranchPrefix+rid.String()+"/wave-1-rollback", f.d.Requests()[0].Head)
		assert.Contains(t, f.d.Requests()[0].Title, "Roll back")
	})
}

func TestStartUsesNextSuffixAfterMerge(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		e := f.exec(t, nil)
		rid := uuid.New()
		_, err := e.Start(context.Background(), f.req(rid, target("edge-1")))
		require.NoError(t, err)
		f.d.CloseWithoutMerge(1)
		res, err := e.Start(context.Background(), f.req(rid, target("edge-1")))
		require.NoError(t, err)
		require.True(t, res[0].Started)
		reqs := f.d.Requests()
		require.Len(t, reqs, 2)
		assert.True(t, strings.HasSuffix(reqs[1].Head, "/wave-1-2"))
	})
}

func pullCount(f *fixture) int {
	n := count(f.d.Calls(), "GET", "/pulls/1")
	return n + count(f.d.Calls(), "GET", "/merge_requests/1")
}

func TestRequestStatesPollsAtMostEvery30s(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		e := f.exec(t, nil)
		res, err := e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.NoError(t, err)
		sr := executor.StatusRequest{Refs: []string{res[0].ExternalRef}, Definition: f.def}
		for range 3 {
			st, err := e.RequestStates(context.Background(), sr)
			require.NoError(t, err)
			assert.Equal(t, domain.ChangeRequestOpen, st[res[0].ExternalRef].State)
		}
		assert.Equal(t, 1, pullCount(f))
		f.clk.Advance(31 * time.Second)
		f.d.Merge(1)
		st, err := e.RequestStates(context.Background(), sr)
		require.NoError(t, err)
		assert.Equal(t, 2, pullCount(f))
		assert.Equal(t, domain.ChangeRequestMerged, st[res[0].ExternalRef].State)
		assert.NotEmpty(t, st[res[0].ExternalRef].MergeCommit)
		f.clk.Advance(time.Hour)
		_, err = e.RequestStates(context.Background(), sr)
		require.NoError(t, err)
		assert.Equal(t, 2, pullCount(f), "a merged request is not asked again")
	})
}

func TestRequestStatesBacksOffOnRateLimit(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		e := f.exec(t, nil)
		res, err := e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.NoError(t, err)
		sr := executor.StatusRequest{Refs: []string{res[0].ExternalRef}, Definition: f.def}
		f.d.RateLimitNext(1)
		st, err := e.RequestStates(context.Background(), sr)
		require.NoError(t, err)
		assert.Empty(t, st)
		assert.Equal(t, 1, pullCount(f))
		f.clk.Advance(10 * time.Second)
		_, err = e.RequestStates(context.Background(), sr)
		require.NoError(t, err)
		assert.Equal(t, 1, pullCount(f), "no call during the wait of the API")
		f.clk.Advance(2 * time.Minute)
		st, err = e.RequestStates(context.Background(), sr)
		require.NoError(t, err)
		assert.Equal(t, 2, pullCount(f))
		assert.Equal(t, domain.ChangeRequestOpen, st[res[0].ExternalRef].State)
	})
}

func TestCancelClosesOpenRequestsAndDeletesOwnBranches(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		e := f.exec(t, nil)
		ridA, ridB := uuid.New(), uuid.New()
		a, err := e.Start(context.Background(), f.req(ridA, target("edge-1")))
		require.NoError(t, err)
		b, err := e.Start(context.Background(), f.req(ridB, target("edge-2")))
		require.NoError(t, err)
		f.d.SetFile("feature/other", "README.md", "x")
		f.d.Merge(2)

		closed, err := e.CloseRequests(context.Background(), executor.CancelRequest{RolloutID: ridA, Definition: f.def,
			Refs: map[domain.AgentID]string{uuid.New(): a[0].ExternalRef}})
		require.NoError(t, err)
		require.Len(t, closed, 1)
		assert.Equal(t, domain.ChangeRequestClosed, closed[0].State)
		reqs := f.d.Requests()
		assert.Equal(t, "closed", reqs[0].State)
		require.Len(t, reqs[0].Comments, 1)
		assert.Contains(t, reqs[0].Comments[0], "aborted")
		assert.NotContains(t, f.d.Branches(), reqs[0].Head, "the own branch is deleted")
		assert.Contains(t, f.d.Branches(), "feature/other", "a foreign branch stays")

		// Cancel of the merged request does not revert it
		require.NoError(t, e.Cancel(context.Background(), executor.CancelRequest{RolloutID: ridB, Definition: f.def,
			Refs: map[domain.AgentID]string{uuid.New(): b[0].ExternalRef}}))
		assert.Equal(t, "merged", f.d.Requests()[1].State)
		got, _ := f.d.File("main", f.path("edge-2"))
		assert.NotEqual(t, f.content, got, "the merged change stays")
	})
}

func TestCloseRequestsNeverDeletesForeignBranch(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		e := f.exec(t, nil)
		res, err := e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.NoError(t, err)
		// the request belongs to another rollout: its branch carries another id
		_, err = e.CloseRequests(context.Background(), executor.CancelRequest{RolloutID: uuid.New(), Definition: f.def,
			Refs: map[domain.AgentID]string{uuid.New(): res[0].ExternalRef}})
		require.NoError(t, err)
		assert.Contains(t, f.d.Branches(), f.d.Requests()[0].Head)
	})
}

func TestValidateFailsClosed(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		spec := domain.UpdateSpec{Executor: domain.ExecutorGitOpsPR, ExecutorRef: "pr"}
		assert.Empty(t, f.exec(t, nil).Validate(spec, f.def))
		cases := map[string]func(*config.ExecutorsConfig){
			"empty allow list": func(c *config.ExecutorsConfig) { c.AllowedWebhookHosts = nil },
			"other host":       func(c *config.ExecutorsConfig) { c.AllowedWebhookHosts = []string{"api.example.com"} },
			"http not allowed": func(c *config.ExecutorsConfig) { c.AllowedWebhookHosts = []string{f.d.Host()} },
		}
		for name, mod := range cases {
			is := f.exec(t, nil, mod).Validate(spec, f.def)
			require.Len(t, is, 1, name)
			assert.Equal(t, domain.SeverityError, is[0].Severity, name)
		}
		noTok := *f.def
		g := *f.def.GitOpsPR
		g.TokenEnv = "MYRIARCH_EXEC_MISSING"
		noTok.GitOpsPR = &g
		is := f.exec(t, nil).Validate(spec, &noTok)
		require.Len(t, is, 1)
		assert.Contains(t, is[0].Message, "MYRIARCH_EXEC_MISSING")
		assert.NotEmpty(t, f.exec(t, nil).Validate(spec, nil))
		_, err := f.exec(t, nil, cases["empty allow list"]).Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.Error(t, err, "Start fails closed as well")
		assert.Empty(t, f.d.Calls())
	})
}

func TestStartDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()
	for _, provider := range []string{"github", "gitlab"} {
		def := newDef(provider, redirector.URL, "values.yaml", "image.tag", "tag")
		cfg := config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://" + strings.TrimPrefix(redirector.URL, "http://")}}
		e := New(cfg, nil, nil, Options{Getenv: func(string) string { return "tok-redirect-0123456789" }})
		_, err := e.Start(context.Background(), executor.UpdateRequest{RolloutID: uuid.New(), ToVersion: "0.162.0", Definition: def,
			Targets: []executor.Target{target("edge-1")}})
		require.Error(t, err, provider)
		assert.Contains(t, err.Error(), "302")
	}
	assert.Zero(t, hits.Load(), "no redirect is followed")
}

func TestCredentialsNeverLeak(t *testing.T) {
	eachProvider(t, func(t *testing.T, f *fixture) {
		var logs bytes.Buffer
		secret := f.d.Token()
		var seen []string
		note := func(s string) { seen = append(seen, s) }

		e := f.exec(t, &logs)
		f.d.EchoAuthInErrors(true)
		f.d.RateLimitNext(1)
		_, err := e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.Error(t, err)
		note(err.Error())
		f.clk.Advance(time.Hour)

		res, err := e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.NoError(t, err)
		b, _ := json.Marshal(res)
		note(string(b))
		st, err := e.RequestStates(context.Background(), executor.StatusRequest{Refs: []string{res[0].ExternalRef}, Definition: f.def})
		require.NoError(t, err)
		b, _ = json.Marshal(st)
		note(string(b))
		b, _ = json.Marshal(f.d.Requests())
		note(string(b))

		// a wrong credential: the answer of the double repeats it
		wrong := "wrong-token-value-0123456789"
		bad := New(config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://" + f.d.Host()}}, nil, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			Options{Now: f.clk.Now, Getenv: func(string) string { return wrong }})
		_, err = bad.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
		require.Error(t, err)
		note(err.Error())
		assert.Contains(t, err.Error(), "[redacted]")
		assert.NotContains(t, err.Error(), wrong)
		_, err = bad.CloseRequests(context.Background(), executor.CancelRequest{RolloutID: uuid.New(), Definition: f.def,
			Refs: map[domain.AgentID]string{uuid.New(): res[0].ExternalRef}})
		if err != nil {
			note(err.Error())
		}
		is := bad.Validate(domain.UpdateSpec{ExecutorRef: "pr"}, f.def)
		b, _ = json.Marshal(is)
		note(string(b))

		_, err = e.CloseRequests(context.Background(), executor.CancelRequest{RolloutID: uuid.New(), Definition: f.def,
			Refs: map[domain.AgentID]string{uuid.New(): res[0].ExternalRef}})
		require.NoError(t, err)
		b, _ = json.Marshal(f.d.Requests())
		note(string(b))
		note(logs.String())

		assert.NotEmpty(t, logs.String())
		for _, s := range seen {
			assert.NotContains(t, s, secret)
			assert.NotContains(t, s, wrong)
		}
	})
}
