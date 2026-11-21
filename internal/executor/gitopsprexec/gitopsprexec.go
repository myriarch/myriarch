// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package gitopsprexec implements executor type "gitops-pr": a version update
// for Kubernetes agents opens a pull request (GitHub) or merge request
// (GitLab) in the deployment repository that changes only the collector image
// (ADR-0052). The server follows the state of the request; abort and rollback
// close it. Completion is decided only by the version that the agents report.
//
// The executor makes no call to a Kubernetes API. It uses the allow list and
// the secret rules of the webhook and AWX executors (webhookexec.Access) and
// fails closed.
package gitopsprexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/executor/webhookexec"
)

const (
	// pollInterval is the shortest time between two checks of one request.
	pollInterval = 30 * time.Second
	// minBackoff and maxBackoff bound the wait after a rate-limit answer
	// that names no wait time.
	minBackoff = 60 * time.Second
	maxBackoff = 10 * time.Minute
	// maxSuffix bounds the search for a free branch suffix.
	maxSuffix = 50
)

var (
	_ executor.Executor       = (*Executor)(nil)
	_ executor.RequestTracker = (*Executor)(nil)
	_ executor.RequestCloser  = (*Executor)(nil)
)

// Options are the optional settings of New.
type Options struct {
	// PublicURL is the external URL of the server; it is used for the link
	// in the request body.
	PublicURL string
	// Now replaces the clock (tests).
	Now func() time.Time
	// Getenv replaces os.Getenv (tests).
	Getenv func(string) string
}

type cached struct {
	state executor.RequestState
	next  time.Time
	final bool
}

// Executor is the gitops-pr executor.
type Executor struct {
	cfg       config.ExecutorsConfig
	client    *http.Client
	log       *slog.Logger
	now       func() time.Time
	access    *webhookexec.Access
	publicURL string
	timeout   time.Duration

	mu         sync.Mutex
	cache      map[string]*cached
	limitUntil time.Time
	backoff    time.Duration
	apps       map[string]*appState
}

// New returns the executor.
func New(cfg config.ExecutorsConfig, client *http.Client, logger *slog.Logger, opts Options) *Executor {
	if client == nil {
		client = &http.Client{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := *client
	c.Timeout = 0 // per-call deadlines
	// An API must not redirect (SSRF, credential forwarding): a redirect is a failure.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if cfg.MaxTimeout > 0 && timeout > cfg.MaxTimeout {
		timeout = cfg.MaxTimeout
	}
	return &Executor{
		cfg: cfg, client: &c, log: logger.With("component", "gitopsprexec"), now: now,
		access: webhookexec.NewAccess(cfg, opts.Getenv), publicURL: strings.TrimRight(opts.PublicURL, "/"), timeout: timeout,
		cache: map[string]*cached{}, apps: map[string]*appState{},
	}
}

// Type implements executor.Executor.
func (e *Executor) Type() domain.ExecutorType { return domain.ExecutorGitOpsPR }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------- access ----

func (e *Executor) apiURL(s *domain.GitOpsPRExecutorSpec) (*url.URL, error) {
	u, err := url.Parse(s.EffectiveAPIURL())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("apiUrl is not a valid http(s) URL")
	}
	return u, nil
}

func spec(def *domain.Executor) (*domain.GitOpsPRExecutorSpec, error) {
	if def == nil {
		return nil, errors.New("the executor object is not resolved")
	}
	if def.Type != domain.ExecutorGitOpsPR || def.GitOpsPR == nil {
		return nil, fmt.Errorf("executor %q is not a gitops-pr executor", def.Name)
	}
	return def.GitOpsPR, nil
}

// open checks the allow list and returns the client of the provider. The
// credential is read when a call is made, never stored here.
func (e *Executor) open(def *domain.Executor) (provider, *domain.GitOpsPRExecutorSpec, error) {
	s, err := spec(def)
	if err != nil {
		return nil, nil, err
	}
	u, err := e.apiURL(s)
	if err != nil {
		return nil, nil, err
	}
	if err := e.access.HostAllowed(u); err != nil {
		return nil, nil, err
	}
	var tok tokenSource
	if s.AppID != "" {
		tok = e.appSource(s, u)
	} else {
		tok = func(context.Context) (string, error) {
			b, err := e.access.Secret(s.TokenEnv, s.TokenFile, "gitops-pr token", u)
			return string(b), err
		}
	}
	base := s.EffectiveAPIURL()
	if s.Provider == domain.GitOpsPRProviderGitLab {
		return &gitLab{c: newGitLabAPI(e.client, base, e.timeout, tok, e.now), project: s.Repository, log: e.log}, s, nil
	}
	return newGitHub(newGitHubAPI(e.client, base, e.timeout, tok, e.now), s.Repository, e.log), s, nil
}

// Validate implements executor.Executor: the environment dependent checks.
func (e *Executor) Validate(spec domain.UpdateSpec, def *domain.Executor) domain.Issues {
	issue := func(msg string) domain.Issues {
		return domain.Issues{{Severity: domain.SeverityError, Code: "executor_invalid", Message: msg, Object: "Executor/" + spec.ExecutorRef}}
	}
	s, err := specOf(def)
	if err != nil {
		return issue(err.Error())
	}
	u, err := e.apiURL(s)
	if err != nil {
		return issue(err.Error())
	}
	if err := e.access.HostAllowed(u); err != nil {
		return issue(err.Error())
	}
	if s.AppID != "" {
		_, err = e.access.Secret("", s.PrivateKeyFile, "gitops-pr app key", u)
	} else {
		_, err = e.access.Secret(s.TokenEnv, s.TokenFile, "gitops-pr token", u)
	}
	if err != nil {
		return issue(err.Error())
	}
	return nil
}

func specOf(def *domain.Executor) (*domain.GitOpsPRExecutorSpec, error) { return spec(def) }

// ------------------------------------------------------------------ refs ----

// splitRef parses a request reference of this executor's provider.
func splitRef(s *domain.GitOpsPRExecutorSpec, ref string) (id string, ok bool) {
	prefix, sep := domain.ChangeRequestRefGitHub, "#"
	if s.Provider == domain.GitOpsPRProviderGitLab {
		prefix, sep = domain.ChangeRequestRefGitLab, "!"
	}
	rest, found := strings.CutPrefix(ref, prefix)
	if !found {
		return "", false
	}
	repo, id, found := strings.Cut(rest, sep)
	if !found || repo != s.Repository {
		return "", false
	}
	if _, err := strconv.Atoi(id); err != nil {
		return "", false
	}
	return id, true
}

func branchName(rolloutID string, wave int, rollback bool, n int) string {
	b := brand.GitOpsPRBranchPrefix + rolloutID + "/wave-" + strconv.Itoa(wave+1)
	if rollback {
		b += "-rollback"
	}
	if n > 1 {
		b += "-" + strconv.Itoa(n)
	}
	return b
}

// ----------------------------------------------------------------- start ----

// Start implements executor.Executor: it opens (or reuses) one request per
// wave that changes the image in the files of the targets.
func (e *Executor) Start(ctx context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	results := make([]executor.TargetResult, len(req.Targets))
	for i, t := range req.Targets {
		results[i].AgentID = t.AgentID
	}
	prov, s, err := e.open(req.Definition)
	if err != nil {
		return nil, err
	}
	format := s.ValueFormat
	if format == "" {
		format = domain.ValueFormatImage
	}

	groups := map[string][]int{}
	for i, t := range req.Targets {
		p, err := renderPath(s.Path, t.Attributes)
		if err != nil {
			results[i].Error = "gitops-pr: " + err.Error()
			continue
		}
		groups[p] = append(groups[p], i)
	}
	fail := func(idx []int, msg string) {
		for _, i := range idx {
			results[i].Error = "gitops-pr: " + msg
		}
	}

	files := map[string][]byte{}
	for _, p := range sortedKeys(groups) {
		content, found, err := prov.readFile(ctx, s.BaseBranch, p)
		if err != nil {
			return nil, err
		}
		if !found {
			fail(groups[p], fmt.Sprintf("file %s does not exist on branch %s", p, s.BaseBranch))
			continue
		}
		out, _, changed, err := setImageScalar(content, s.Field, format, req.ToVersion)
		if err != nil {
			fail(groups[p], fmt.Sprintf("%s: %v", p, err))
			continue
		}
		if !changed {
			for _, i := range groups[p] {
				results[i].Started = true // the base branch already holds the version
			}
			continue
		}
		files[p] = out
	}
	if len(files) == 0 {
		return results, nil
	}

	// the branch: the first suffix whose request is not finished
	rid := req.RolloutID.String()
	var branch string
	var rq *remoteRequest
	for n := 1; n <= maxSuffix; n++ {
		branch = branchName(rid, req.Wave, req.Rollback, n)
		if rq, err = prov.findRequest(ctx, branch); err != nil {
			return nil, err
		}
		if rq == nil || rq.State == domain.ChangeRequestOpen {
			break
		}
		if n == maxSuffix {
			return nil, fmt.Errorf("no free branch name for rollout %s wave %d", rid, req.Wave+1)
		}
	}

	msg := fmt.Sprintf("Update collector to %s (fleet %s, wave %d)", req.ToVersion, req.Fleet, req.Wave+1)
	if req.Rollback {
		msg = fmt.Sprintf("Roll back collector to %s (fleet %s, wave %d)", req.ToVersion, req.Fleet, req.Wave+1)
	}
	_, exists, err := prov.headSHA(ctx, branch)
	if err != nil {
		return nil, err
	}
	needCommit := !exists
	if exists {
		for p, want := range files {
			have, found, err := prov.readFile(ctx, branch, p)
			if err != nil {
				return nil, err
			}
			if !found || !bytes.Equal(have, want) {
				needCommit = true
			}
		}
	}
	if needCommit {
		if _, err := prov.commit(ctx, branch, s.BaseBranch, s.BaseBranch, msg, files); err != nil {
			return nil, err
		}
	}
	if rq == nil {
		body := e.body(req, rid)
		if rq, err = prov.openRequest(ctx, branch, s.BaseBranch, msg, body, s.Labels, s.Reviewers); err != nil {
			return nil, err
		}
	}
	e.log.Info("change request ready", "repository", s.Repository, "ref", rq.Ref, "state", string(rq.State))
	for _, p := range sortedKeys(groups) {
		if _, changed := files[p]; !changed {
			continue
		}
		for _, i := range groups[p] {
			results[i].Started, results[i].ExternalRef, results[i].ExternalURL = true, rq.Ref, rq.URL
		}
	}
	return results, nil
}

func (e *Executor) body(req executor.UpdateRequest, rid string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opened by %s.\n\n- Rollout: %s\n- Fleet: %s\n- Version: %s\n- Wave: %d\n", brand.ServerBinary, rid, req.Fleet, req.ToVersion, req.Wave+1)
	if e.publicURL != "" {
		fmt.Fprintf(&b, "- Details: %s/rollouts/%s\n", e.publicURL, rid)
	}
	b.WriteString("\nThe change touches only the collector image. Merge it to roll out the version; close it to stop.\n")
	return b.String()
}

// ---------------------------------------------------------------- states ----

// RequestStates implements executor.RequestTracker.
func (e *Executor) RequestStates(ctx context.Context, req executor.StatusRequest) (map[string]executor.RequestState, error) {
	out := map[string]executor.RequestState{}
	var prov provider
	var s *domain.GitOpsPRExecutorSpec
	var firstErr error
	seen := map[string]bool{}
	for _, ref := range req.Refs {
		if seen[ref] || !domain.IsChangeRequestRef(ref) {
			continue
		}
		seen[ref] = true
		now := e.now()
		e.mu.Lock()
		c := e.cache[ref]
		limited := now.Before(e.limitUntil)
		e.mu.Unlock()
		if c != nil && (c.final || limited || now.Before(c.next)) {
			out[ref] = c.state
			continue
		}
		if limited {
			continue
		}
		if prov == nil {
			var err error
			if prov, s, err = e.open(req.Definition); err != nil {
				return out, err
			}
		}
		id, ok := splitRef(s, ref)
		if !ok {
			continue
		}
		rq, err := prov.getRequest(ctx, id)
		if err != nil {
			var rl *rateLimitError
			if errors.As(err, &rl) {
				e.noteRateLimit(rl, now)
				e.log.Warn("rate limit reached, backing off", "repository", s.Repository, "until", e.limitUntil.Format(time.RFC3339))
				continue
			}
			e.log.Warn("checking a change request failed", "repository", s.Repository, "ref", ref, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		e.mu.Lock()
		e.backoff = 0
		st := executor.RequestState{Ref: ref, URL: rq.URL, State: rq.State, MergeCommit: rq.MergeCommit}
		e.cache[ref] = &cached{state: st, next: now.Add(pollInterval), final: rq.State != domain.ChangeRequestOpen}
		e.mu.Unlock()
		out[ref] = st
	}
	return out, firstErr
}

func (e *Executor) noteRateLimit(rl *rateLimitError, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	wait := rl.wait
	if wait <= 0 {
		if e.backoff <= 0 {
			e.backoff = minBackoff
		} else if e.backoff *= 2; e.backoff > maxBackoff {
			e.backoff = maxBackoff
		}
		wait = e.backoff
	}
	e.limitUntil = now.Add(wait)
}

// ----------------------------------------------------------------- close ----

// Cancel implements executor.Executor.
func (e *Executor) Cancel(ctx context.Context, req executor.CancelRequest) error {
	_, err := e.CloseRequests(ctx, req)
	return err
}

// CloseRequests implements executor.RequestCloser.
func (e *Executor) CloseRequests(ctx context.Context, req executor.CancelRequest) ([]executor.RequestState, error) {
	refs := map[string]bool{}
	for _, r := range req.Refs {
		if domain.IsChangeRequestRef(r) {
			refs[r] = true
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	prov, s, err := e.open(req.Definition)
	if err != nil {
		return nil, err
	}
	ownPrefix := brand.GitOpsPRBranchPrefix + req.RolloutID.String() + "/"
	comment := fmt.Sprintf("Closed by %s: rollout %s was aborted, superseded or finished.", brand.ServerBinary, req.RolloutID)
	var closed []executor.RequestState
	var errs []error
	for _, ref := range sortedKeys(refs) {
		id, ok := splitRef(s, ref)
		if !ok {
			continue
		}
		rq, err := prov.getRequest(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if rq.State == domain.ChangeRequestOpen {
			if err := prov.closeRequest(ctx, id, comment); err != nil {
				errs = append(errs, err)
				continue
			}
			closed = append(closed, executor.RequestState{Ref: ref, URL: rq.URL, State: domain.ChangeRequestClosed})
			e.mu.Lock()
			e.cache[ref] = &cached{state: executor.RequestState{Ref: ref, URL: rq.URL, State: domain.ChangeRequestClosed}, final: true}
			e.mu.Unlock()
			e.log.Info("change request closed", "repository", s.Repository, "ref", ref)
		}
		// only branches that this executor created for this rollout are deleted
		if strings.HasPrefix(rq.Head, ownPrefix) {
			if err := prov.deleteBranch(ctx, rq.Head); err != nil {
				e.log.Warn("deleting a branch failed", "repository", s.Repository, "ref", ref, "error", err)
			}
		}
	}
	return closed, errors.Join(errs...)
}
