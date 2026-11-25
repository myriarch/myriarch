// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/executor/gitopsprexec"
	"github.com/myriarch/myriarch/internal/executor/gitopsprexec/gitopsprtest"
)

const (
	prValuesYAML   = "# gateway values\nimage:\n  repository: registry.example.com/otel/collector\n  tag: 0.161.0 # the version\n"
	prManifestYAML = "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - name: collector\n          image: registry.example.com/otel/col:0.161.0\n"
	prTokenEnv     = "MYRIARCH_EXEC_PR_TOKEN"
)

type prDouble interface {
	Host() string
	Token() string
	URL() string
	SetFile(branch, path, content string)
	File(branch, path string) (string, bool)
	Branches() []string
	Requests() []gitopsprtest.Request
	Merge(id int)
	CloseWithoutMerge(id int)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// prEnv is an engine with the real gitops-pr executor against a double of the
// GitHub or GitLab API.
type prEnv struct {
	*env
	d    prDouble
	logs *syncBuffer
	ids  []domain.AgentID
	file string
}

func newPREnv(t *testing.T, provider string) *prEnv {
	t.Helper()
	e := newEnv(t)
	p := &prEnv{env: e, logs: &syncBuffer{}}
	gs := &domain.GitOpsPRExecutorSpec{
		Provider: provider, Repository: "example/deploy", BaseBranch: "main",
		Path: `clusters/[[ attr "site.id" ]]/app.yaml`, TokenEnv: prTokenEnv,
	}
	content := prValuesYAML
	switch provider {
	case domain.GitOpsPRProviderGitHub:
		p.d = gitopsprtest.NewGitHub(t)
		gs.Field, gs.ValueFormat = "image.tag", domain.ValueFormatTag
	default:
		p.d = gitopsprtest.NewGitLab(t)
		gs.Field, gs.ValueFormat = "spec.template.spec.containers[0].image", domain.ValueFormatImage
		content = prManifestYAML
	}
	gs.APIURL = p.d.URL()
	for _, site := range []string{"0000", "0001"} {
		p.d.SetFile("main", "clusters/"+site+"/app.yaml", content)
	}
	p.file = "clusters/0000/app.yaml"
	e.executors = []domain.Executor{{ObjectMeta: domain.ObjectMeta{Name: "pr"},
		ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorGitOpsPR, GitOpsPR: gs}}}

	log := slog.New(slog.NewTextHandler(p.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := config.ExecutorsConfig{AllowedWebhookHosts: []string{"http://" + p.d.Host()}}
	env := map[string]string{prTokenEnv: p.d.Token()}
	ex := gitopsprexec.New(cfg, nil, log, gitopsprexec.Options{
		Now: e.clk.Now, Getenv: func(k string) string { return env[k] },
	})
	e.eng = NewEngine(e.cfg, Deps{
		Store: e.st, Bus: e.bus, Control: e.ctl,
		Executors: executor.MapRegistry{domain.ExecutorGitOpsPR: ex},
		Notifier:  e.notif, Audit: auditAdapter{e.st}, Logger: log,
		Registerer: prometheus.NewRegistry(), Now: e.clk.Now,
	})
	return p
}

func eachPRProvider(t *testing.T, fn func(t *testing.T, p *prEnv)) {
	t.Helper()
	for _, name := range []string{domain.GitOpsPRProviderGitHub, domain.GitOpsPRProviderGitLab} {
		t.Run(name, func(t *testing.T) { fn(t, newPREnv(t, name)) })
	}
}

// start plans the rollout and runs the tick that opens the request.
func (p *prEnv) start(s domain.RolloutPolicySpec) domain.Rollout {
	p.t.Helper()
	p.setPolicy("p", s)
	p.addFleet("f", func(f *domain.Fleet) {
		f.Collector = domain.CollectorSpec{Version: "0.162.0"}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorGitOpsPR, ExecutorRef: "pr"}
	})
	p.ids = p.addAgents(2, "f", inSync())
	p.publishSnapshot()
	p.plan()
	p.step()
	r := p.only()
	require.Len(p.t, p.d.Requests(), 1)
	return r
}

// report makes the collectors report the version.
func (p *prEnv) report(version string) {
	for _, id := range p.ids {
		p.st.UpdateAgent(id, func(ag *domain.Agent) { ag.Version = version })
	}
}

// poll lets the executor check the requests again (it asks at most every 30 s).
func (p *prEnv) poll() {
	p.t.Helper()
	p.clk.Advance(31 * time.Second)
	p.step()
}

func (p *prEnv) changeRequests(rid uuid.UUID) []*domain.ChangeRequest {
	var out []*domain.ChangeRequest
	for _, t := range p.targets(rid) {
		var cr *domain.ChangeRequest
		if t.Progress != nil {
			cr = t.Progress.ChangeRequest
		}
		out = append(out, cr)
	}
	return out
}

func (p *prEnv) audit(action string) []domain.AuditEvent {
	var out []domain.AuditEvent
	for _, ev := range p.st.AuditEvents() {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

func TestGitOpsPRMergedAndReportedCompletes(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		for _, tg := range p.targets(r.ID) {
			assert.Equal(t, domain.TargetInProgress, tg.Status)
			assert.True(t, domain.IsChangeRequestRef(tg.ExecutorRef), tg.ExecutorRef)
		}
		for _, cr := range p.changeRequests(r.ID) {
			require.NotNil(t, cr)
			assert.Equal(t, domain.ChangeRequestOpen, cr.State)
			assert.NotEmpty(t, cr.URL)
		}
		assert.Len(t, p.audit("rollout.change_request.opened"), 1, "one request for both targets")

		p.steps(3)
		assert.Equal(t, domain.RolloutRunning, p.rollout(r.ID).Status)
		tg := p.targets(r.ID)[0]
		assert.Equal(t, domain.TargetInProgress, tg.Status)
		require.NotNil(t, tg.Progress)
		assert.Contains(t, tg.Progress.Steps[1].Detail, "waiting for merge of")

		// merged, but no version reported: nothing is done yet
		p.d.Merge(p.d.Requests()[0].ID)
		p.poll()
		mc := p.d.Requests()[0].MergeCommit
		require.NotEmpty(t, mc)
		for _, cr := range p.changeRequests(r.ID) {
			require.NotNil(t, cr)
			assert.Equal(t, domain.ChangeRequestMerged, cr.State)
			assert.Equal(t, mc, cr.MergeCommit)
		}
		assert.Contains(t, p.targets(r.ID)[0].Progress.Steps[1].Detail, "merged as")
		p.steps(10)
		assert.Equal(t, domain.RolloutRunning, p.rollout(r.ID).Status, "a merge alone does not complete the rollout")
		assert.Equal(t, 2, p.targetStatus(r.ID)[domain.TargetInProgress])

		p.report("0.162.0")
		require.True(t, p.runUntil(40, func() bool { return p.rollout(r.ID).Status == domain.RolloutCompleted }))
		assert.Equal(t, 2, p.targetStatus(r.ID)[domain.TargetSucceeded])

		merged := p.audit("rollout.change_request.merged")
		require.Len(t, merged, 1)
		assert.Equal(t, mc, merged[0].Details["mergeCommit"])
		assert.Equal(t, "merged", p.d.Requests()[0].State, "a merged request is not touched")
	})
}

func TestGitOpsPRClosedWithoutMergeFailsRollout(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		p.d.CloseWithoutMerge(p.d.Requests()[0].ID)
		p.poll()

		for _, tg := range p.targets(r.ID) {
			assert.Equal(t, domain.TargetFailed, tg.Status)
			assert.Contains(t, tg.LastError, "closed without merge")
			require.NotNil(t, tg.Progress)
			require.NotNil(t, tg.Progress.ChangeRequest)
			assert.Equal(t, domain.ChangeRequestClosed, tg.Progress.ChangeRequest.State)
		}
		// the default policy pauses a wave over its failure threshold
		assert.Equal(t, domain.RolloutPaused, p.rollout(r.ID).Status)
		closed := p.audit("rollout.change_request.closed")
		require.Len(t, closed, 1)
		assert.Equal(t, "closed without merge", closed[0].Details["reason"])
		assert.Len(t, p.d.Requests(), 1, "no second request")
	})
}

func TestGitOpsPRAbortClosesRequestAndDeletesBranch(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		head := p.d.Requests()[0].Head
		assert.Equal(t, brand.GitOpsPRBranchPrefix+r.ID.String()+"/wave-1", head)
		assert.Contains(t, p.d.Branches(), head)

		_, err := p.eng.Action(p.ctx, r.ID, domain.ActionAbort, 0, "stop", operator)
		require.NoError(t, err)

		rq := p.d.Requests()[0]
		assert.Equal(t, "closed", rq.State)
		assert.NotEmpty(t, rq.Comments)
		assert.NotContains(t, p.d.Branches(), head)
		assert.Len(t, p.audit("rollout.change_request.closed"), 1)
		assert.Equal(t, 2, p.targetStatus(r.ID)[domain.TargetCancelled])
	})
}

func TestGitOpsPRRollbackClosesOpenRequest(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		head := p.d.Requests()[0].Head

		_, err := p.eng.Action(p.ctx, r.ID, domain.ActionRollback, 0, "bad idea", operator)
		require.NoError(t, err)
		require.True(t, p.runUntil(20, func() bool { return p.rollout(r.ID).Status == domain.RolloutRolledBack }))

		reqs := p.d.Requests()
		require.Len(t, reqs, 1, "no new request")
		assert.Equal(t, "closed", reqs[0].State)
		assert.NotContains(t, p.d.Branches(), head)
		assert.Equal(t, 2, p.targetStatus(r.ID)[domain.TargetRolledBack])
		assert.Len(t, p.audit("rollout.change_request.closed"), 1, "closed once, not on every tick")
	})
}

// A request that a person closed between two polls is not recorded as merged
// when the rollback finds it closed already.
func TestGitOpsPRRollbackOfRequestClosedByPersonIsNotMerged(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		p.d.CloseWithoutMerge(p.d.Requests()[0].ID) // the engine has not polled yet

		_, err := p.eng.Action(p.ctx, r.ID, domain.ActionRollback, 0, "bad idea", operator)
		require.NoError(t, err)
		require.True(t, p.runUntil(20, func() bool { return p.rollout(r.ID).Status == domain.RolloutRolledBack }))

		assert.Len(t, p.d.Requests(), 1, "no rollback request for a change that was never merged")
		for _, cr := range p.changeRequests(r.ID) {
			require.NotNil(t, cr)
			assert.Equal(t, domain.ChangeRequestClosed, cr.State)
		}
		assert.Empty(t, p.audit("rollout.change_request.merged"))
		assert.Equal(t, 2, p.targetStatus(r.ID)[domain.TargetRolledBack])
	})
}

func TestGitOpsPRRollbackOfMergedChangeOpensRollbackRequest(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		p.d.Merge(p.d.Requests()[0].ID)
		p.poll()
		p.report("0.162.0")
		p.step()

		_, err := p.eng.Action(p.ctx, r.ID, domain.ActionRollback, 0, "bad idea", operator)
		require.NoError(t, err)
		p.step()

		reqs := p.d.Requests()
		require.Len(t, reqs, 2)
		assert.Equal(t, brand.GitOpsPRBranchPrefix+r.ID.String()+"/wave-1-rollback", reqs[1].Head)
		got, ok := p.d.File(reqs[1].Head, p.file)
		require.True(t, ok)
		assert.Contains(t, got, "0.161.0")
		assert.NotContains(t, got, "0.162.0")
		base, _ := p.d.File("main", p.file)
		assert.Contains(t, base, "0.162.0", "the merged change is not reverted by the server")
		for _, cr := range p.changeRequests(r.ID) {
			require.NotNil(t, cr)
			assert.True(t, cr.Rollback)
			assert.Equal(t, domain.ChangeRequestOpen, cr.State)
		}

		p.report("0.161.0")
		require.True(t, p.runUntil(20, func() bool { return p.rollout(r.ID).Status == domain.RolloutRolledBack }))
		assert.Equal(t, "open", p.d.Requests()[1].State, "the rollback request stays open for the operators")
		assert.Equal(t, "merged", p.d.Requests()[0].State)
	})
}

func TestGitOpsPRVersionWithoutMergeSucceeds(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		p.report("0.162.0")
		require.True(t, p.runUntil(40, func() bool { return p.rollout(r.ID).Status == domain.RolloutCompleted }))
		assert.Equal(t, 2, p.targetStatus(r.ID)[domain.TargetSucceeded])
		assert.Equal(t, "closed", p.d.Requests()[0].State, "the unmerged request is closed when the rollout ends")
	})
}

func TestGitOpsPRCredentialsStayOutOfAuditAndAPI(t *testing.T) {
	eachPRProvider(t, func(t *testing.T, p *prEnv) {
		r := p.start(spec(allWave()))
		p.d.Merge(p.d.Requests()[0].ID)
		p.poll()
		p.report("0.162.0")
		require.True(t, p.runUntil(40, func() bool { return p.rollout(r.ID).Status == domain.RolloutCompleted }))

		token := p.d.Token()
		require.GreaterOrEqual(t, len(token), 8)
		// what the API returns: the rollout, its targets, and the audit events
		api, err := json.Marshal(map[string]any{"rollout": p.rollout(r.ID), "targets": p.targets(r.ID), "audit": p.st.AuditEvents()})
		require.NoError(t, err)
		assert.False(t, strings.Contains(string(api), token), "the token appears in an API response or the audit log")
		assert.NotContains(t, string(api), prTokenEnv, "not even the name of the reference")
		assert.False(t, strings.Contains(p.logs.String(), token), "the token appears in a log")
	})
}
