// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package release tracks upstream collector releases (GitHub releases API
// with ETag caching, GitHub Enterprise compatible) and extracts breaking
// changes and deprecations from the core/contrib/releases CHANGELOG.md files.
//
// Algorithm: docs/design/opamp-server.md, Release tracker; GitHub API facts:
// docs/design/upstream-notes.md, Release tracking and rate limits.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/version"
)

const (
	listPerPage     = 30
	maxBody         = 64 << 20 // contrib CHANGELOG.md is ~1.2 MB; leave room to grow
	maxListBody     = 16 << 20
	requestTimeout  = 2 * time.Minute
	statusKVKey     = "release/status"
	etagKVPrefix    = "release/etag/"
	defaultBackoff  = 15 * time.Minute
	maxBackoff      = 2 * time.Hour
	compareMaxItems = 500
)

// Deps are the dependencies of the tracker.
type Deps struct {
	Store      store.Store
	Bus        eventbus.Bus
	Notifier   notify.Notifier
	HTTPClient *http.Client
	Logger     *slog.Logger
	Registerer prometheus.Registerer
}

// Status is the poll state (stored in the KV store key "release/status" so
// that every replica can show it).
type Status struct {
	LastPollAt     *time.Time `json:"lastPollAt,omitempty"`
	LastSuccessAt  *time.Time `json:"lastSuccessAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	RateLimited    bool       `json:"rateLimited,omitempty"`
	BackoffUntil   *time.Time `json:"backoffUntil,omitempty"`
	RateRemaining  *int       `json:"rateLimitRemaining,omitempty"`
	Authenticated  bool       `json:"authenticated"`
	NotModified    bool       `json:"notModified,omitempty"`
	ReleasesSeen   int        `json:"releasesSeen"`
	NewReleases    int        `json:"newReleases"`
	Source         string     `json:"source,omitempty"`
	TokenRecommend string     `json:"hint,omitempty"`
}

type clEntry struct {
	at   time.Time
	secs []section
}

const changelogCacheTTL = 10 * time.Minute

// Tracker polls releases.
type Tracker struct {
	cfg  config.ReleasesConfig
	deps Deps
	log  *slog.Logger
	// now is the clock (tests).
	now func() time.Time

	client     *http.Client
	minVersion *semver.Version
	dists      map[string]bool

	pollMu       sync.Mutex // one poll at a time
	backoffUntil time.Time

	// clCache caches parsed changelogs ("repo@ref") for a few minutes (a poll
	// reads each file once; version comparisons in the UI reuse them).
	clMu    sync.Mutex
	clCache map[string]clEntry

	cycle      func(job string) func()
	polls      *prometheus.CounterVec
	latestInfo *prometheus.GaugeVec
	latestMu   sync.Mutex
	latestSeen map[string]string // distribution -> version currently exported
}

// NewTracker creates the tracker.
func NewTracker(cfg config.ReleasesConfig, deps Deps) (*Tracker, error) {
	t := &Tracker{cfg: cfg, deps: deps, now: func() time.Time { return time.Now().UTC() }, latestSeen: map[string]string{}, clCache: map[string]clEntry{}}
	t.log = deps.Logger
	if t.log == nil {
		t.log = slog.Default()
	}
	t.log = t.log.With("component", "release")
	def := config.Default().Releases
	if t.cfg.GitHubAPIURL == "" {
		t.cfg.GitHubAPIURL = def.GitHubAPIURL
	}
	if t.cfg.RawBaseURL == "" {
		t.cfg.RawBaseURL = def.RawBaseURL
	}
	if t.cfg.ReleasesRepo == "" {
		t.cfg.ReleasesRepo = def.ReleasesRepo
	}
	if t.cfg.CoreRepo == "" {
		t.cfg.CoreRepo = def.CoreRepo
	}
	if t.cfg.ContribRepo == "" {
		t.cfg.ContribRepo = def.ContribRepo
	}
	if t.cfg.PollInterval <= 0 {
		t.cfg.PollInterval = def.PollInterval
	}
	if t.cfg.MinVersion == "" {
		t.cfg.MinVersion = def.MinVersion
	}
	if len(t.cfg.Distributions) == 0 {
		t.cfg.Distributions = def.Distributions
	}
	mv, err := checkConfig(t.cfg)
	if err != nil {
		return nil, err
	}
	t.minVersion = mv
	t.dists = map[string]bool{}
	for _, d := range t.cfg.Distributions {
		t.dists[d] = true
	}
	base := deps.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	c.Timeout = requestTimeout
	c.CheckRedirect = t.redirectPolicy()
	t.client = &c

	t.cycle = metrics.CycleTimer(deps.Registerer)
	t.polls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_release_polls_total", Help: "Release tracker polls by result (ok, not_modified, error, rate_limited).",
	}, []string{"result"})
	t.latestInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "myriarch_release_latest_info", Help: "Latest stable upstream release per distribution (value 1).",
	}, []string{"distribution", "version"})
	if deps.Registerer != nil {
		t.polls = registerOrGet(deps.Registerer, t.polls)
		t.latestInfo = registerOrGet(deps.Registerer, t.latestInfo)
	}
	return t, nil
}

// CheckConfig validates the settings of the release tracker that NewTracker
// would reject (minVersion, distributions, repositories), without creating
// it. Empty values stand for the defaults.
func CheckConfig(cfg config.ReleasesConfig) error {
	def := config.Default().Releases
	if cfg.MinVersion == "" {
		cfg.MinVersion = def.MinVersion
	}
	if len(cfg.Distributions) == 0 {
		cfg.Distributions = def.Distributions
	}
	if cfg.ReleasesRepo == "" {
		cfg.ReleasesRepo = def.ReleasesRepo
	}
	if cfg.CoreRepo == "" {
		cfg.CoreRepo = def.CoreRepo
	}
	if cfg.ContribRepo == "" {
		cfg.ContribRepo = def.ContribRepo
	}
	_, err := checkConfig(cfg)
	return err
}

// checkConfig validates minVersion, distributions and repositories of a
// configuration whose defaults are filled in.
func checkConfig(cfg config.ReleasesConfig) (*semver.Version, error) {
	mv, err := semver.NewVersion(domain.NormalizeVersion(cfg.MinVersion))
	if err != nil {
		return nil, domain.SettingError("releases.minVersion", fmt.Sprintf("%q is not a valid version (%v)", cfg.MinVersion, err))
	}
	for _, d := range cfg.Distributions {
		if d != domain.DistributionContrib && d != domain.DistributionSupervisor {
			return nil, domain.SettingError("releases.distributions", fmt.Sprintf("%q is not supported (%s, %s)", d, domain.DistributionContrib, domain.DistributionSupervisor))
		}
	}
	for _, r := range []struct{ key, val string }{{"releasesRepo", cfg.ReleasesRepo}, {"coreRepo", cfg.CoreRepo}, {"contribRepo", cfg.ContribRepo}} {
		if o, n, ok := strings.Cut(r.val, "/"); !ok || o == "" || n == "" || strings.Contains(n, "/") {
			return nil, domain.SettingError("releases."+r.key, fmt.Sprintf("repository %q must be \"owner/name\"", r.val))
		}
	}
	return mv, nil
}

// redirectHosts are the hosts the tracker follows redirects to besides the
// configured API and raw hosts.
var redirectHosts = []string{"api.github.com", "github.com", "raw.githubusercontent.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

// redirectPolicy limits redirects: at most 5 hops, https only, and only to the
// GitHub hosts, the configured GitHub Enterprise hosts or the same host again
// (a plain-http mirror may redirect within itself).
func (t *Tracker) redirectPolicy() func(*http.Request, []*http.Request) error {
	hosts := append([]string(nil), redirectHosts...)
	var bases []*url.URL
	for _, raw := range []string{t.cfg.GitHubAPIURL, t.cfg.RawBaseURL} {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			hosts = append(hosts, u.Hostname())
			bases = append(bases, u)
		}
	}
	httpsOnly := netguard.RedirectPolicy(netguard.MaxRedirects, hosts)
	return func(req *http.Request, via []*http.Request) error {
		for _, b := range bases {
			if req.URL.Scheme == b.Scheme && req.URL.Host == b.Host && len(via) < netguard.MaxRedirects {
				return nil
			}
		}
		return httpsOnly(req, via)
	}
}

func registerOrGet[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(T); ok {
				return existing
			}
		}
	}
	return c
}

// Run (leader only) polls every cfg.PollInterval until ctx is done.
func (t *Tracker) Run(ctx context.Context) error {
	if !t.cfg.Enabled {
		t.log.Info("release tracker disabled")
		<-ctx.Done()
		return nil
	}
	events, cancel := t.deps.Bus.Subscribe(eventbus.ReleaseRefreshRequested)
	defer cancel()
	t.pollSafe(ctx)
	ticker := time.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case _, ok := <-events:
			if !ok {
				return nil
			}
			for drained := false; !drained; {
				select {
				case <-events:
				default:
					drained = true
				}
			}
			// An explicit refresh overrides a rate-limit backoff that already expired.
		}
		t.pollSafe(ctx)
	}
}

// pollBudget bounds one complete poll (all requests together).
const pollBudget = 10 * time.Minute

func (t *Tracker) pollSafe(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, pollBudget)
	defer cancel()
	defer t.cycle("release_poll")()
	defer func() {
		if r := recover(); r != nil {
			t.log.Error("release poll panicked", "panic", r)
		}
	}()
	if err := t.Poll(ctx); err != nil && ctx.Err() == nil {
		t.log.Warn("release poll failed", "error", err)
	}
}

// Refresh requests an immediate poll from any replica: it always publishes
// eventbus.ReleaseRefreshRequested; the leader's Run loop performs the poll.
func (t *Tracker) Refresh(ctx context.Context) error {
	return t.deps.Bus.Publish(ctx, eventbus.ReleaseRefreshRequested, struct{}{})
}

// GetStatus returns the last poll status stored by the leader.
func (t *Tracker) GetStatus(ctx context.Context) (*Status, error) {
	b, err := t.deps.Store.KV().Get(ctx, statusKVKey)
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ---------------------------------------------------------------- GitHub ----

// ghRelease is the subset of the GitHub release JSON the tracker uses.
type ghRelease struct {
	TagName     string     `json:"tag_name"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
	HTMLURL     string     `json:"html_url"`
	Body        string     `json:"body"`
	Assets      []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		ContentType        string `json:"content_type"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type apiResult struct {
	status        int
	body          []byte
	etag          string
	remaining     *int
	reset         *time.Time
	retryAfter    time.Duration
	authenticated bool
}

var errNotFound = errors.New("not found")

func (t *Tracker) token() string {
	if t.cfg.TokenFile == "" {
		return ""
	}
	b, err := os.ReadFile(t.cfg.TokenFile)
	if err != nil {
		t.log.Warn("cannot read the GitHub token file (continuing unauthenticated)", "error", stripPath(err))
		return ""
	}
	return strings.TrimSpace(string(b))
}

func stripPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func (t *Tracker) get(ctx context.Context, rawURL, etag string, limit int64, github bool) (*apiResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent(brand.ServerBinary))
	res := &apiResult{}
	if github {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if tok := t.token(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
			res.authenticated = true
		}
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s: %w", redact(rawURL), err)
	}
	defer resp.Body.Close()
	res.status = resp.StatusCode
	res.etag = resp.Header.Get("ETag")
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			res.remaining = &n
		}
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			tt := time.Unix(n, 0).UTC()
			res.reset = &tt
		}
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			res.retryAfter = time.Duration(n) * time.Second
		}
	}
	if resp.StatusCode == http.StatusNotModified || resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return res, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", redact(rawURL), err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", redact(rawURL), limit)
	}
	res.body = body
	return res, nil
}

func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// ----------------------------------------------------------------- polling ----

func repoPath(repo string) (owner, name string) {
	owner, name, _ = strings.Cut(repo, "/")
	return owner, name
}

// fetchReleases returns the releases page (nil, nil when unchanged).
func (t *Tracker) fetchReleases(ctx context.Context, st *Status) ([]ghRelease, string, error) {
	if t.cfg.FixtureDir != "" {
		owner, name := repoPath(t.cfg.ReleasesRepo)
		b, err := os.ReadFile(filepath.Join(t.cfg.FixtureDir, "github", "repos", owner, name, "releases.json"))
		if err != nil {
			return nil, "", fmt.Errorf("read releases fixture: %w", stripPath(err))
		}
		var list []ghRelease
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, "", fmt.Errorf("decode releases fixture: %w", err)
		}
		st.Source = "fixtures:" + t.cfg.FixtureDir
		return list, "", nil
	}

	api := strings.TrimRight(t.cfg.GitHubAPIURL, "/") + "/repos/" + t.cfg.ReleasesRepo + "/releases?per_page=" + strconv.Itoa(listPerPage)
	st.Source = redact(api)
	etagKey := etagKVPrefix + t.cfg.ReleasesRepo
	var etag string
	if b, err := t.deps.Store.KV().Get(ctx, etagKey); err == nil {
		etag = string(b)
	}
	res, err := t.get(ctx, api, etag, maxListBody, true)
	if err != nil {
		return nil, "", err
	}
	st.Authenticated = res.authenticated
	st.RateRemaining = res.remaining
	if res.remaining != nil && *res.remaining == 0 {
		t.setBackoff(st, res, "GitHub rate limit exhausted")
	}
	switch {
	case res.status == http.StatusNotModified:
		st.NotModified = true
		return nil, "", nil
	case res.status == http.StatusOK:
	case res.status == http.StatusForbidden || res.status == http.StatusTooManyRequests:
		t.setBackoff(st, res, "GitHub rate limit")
		return nil, "", fmt.Errorf("GitHub API returned HTTP %d (rate limit?): %s%s", res.status, shorten(string(res.body)), t.tokenHint(res))
	case res.status == http.StatusNotFound:
		return nil, "", fmt.Errorf("GitHub API: repository %s not found (HTTP 404; private repository without token?)", t.cfg.ReleasesRepo)
	default:
		return nil, "", fmt.Errorf("GitHub API returned HTTP %d: %s", res.status, shorten(string(res.body)))
	}
	var list []ghRelease
	if err := json.Unmarshal(res.body, &list); err != nil {
		return nil, "", fmt.Errorf("decode GitHub releases: %w", err)
	}
	return list, res.etag, nil
}

func (t *Tracker) tokenHint(res *apiResult) string {
	if res.authenticated {
		return ""
	}
	h := " (unauthenticated requests are limited to 60/h and 304 responses count too: set releases.tokenFile)"
	return h
}

func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// setBackoff pauses polling after a rate limit (Retry-After / reset time).
func (t *Tracker) setBackoff(st *Status, res *apiResult, why string) {
	until := t.now().Add(defaultBackoff)
	switch {
	case res.retryAfter > 0:
		until = t.now().Add(res.retryAfter)
	case res.reset != nil && res.reset.After(t.now()):
		until = *res.reset
	}
	if until.After(t.now().Add(maxBackoff)) {
		until = t.now().Add(maxBackoff)
	}
	t.backoffUntil = until
	st.RateLimited = true
	st.BackoffUntil = &until
	t.log.Warn("pausing release polling", "reason", why, "until", until.Format(time.RFC3339))
}

// Poll fetches the release list once and processes new releases.
func (t *Tracker) Poll(ctx context.Context) error {
	t.pollMu.Lock()
	defer t.pollMu.Unlock()
	now := t.now()
	st := &Status{LastPollAt: &now, TokenRecommend: ""}
	if prev, err := t.GetStatus(ctx); err == nil {
		st.LastSuccessAt = prev.LastSuccessAt
		st.RateRemaining, st.Authenticated, st.Source = prev.RateRemaining, prev.Authenticated, prev.Source
	}
	save := func() {
		b, _ := json.Marshal(st)
		if err := t.deps.Store.KV().Put(context.WithoutCancel(ctx), statusKVKey, b); err != nil {
			t.log.Warn("save release status failed", "error", err)
		}
	}
	if now.Before(t.backoffUntil) {
		until := t.backoffUntil
		st.RateLimited, st.BackoffUntil, st.LastError = true, &until, "waiting for the GitHub rate limit to reset"
		save()
		return fmt.Errorf("release polling is paused until %s (rate limit)", until.Format(time.RFC3339))
	}
	list, etag, err := t.fetchReleases(ctx, st)
	if err != nil {
		st.LastError = err.Error()
		if st.RateLimited {
			t.polls.WithLabelValues("rate_limited").Inc()
		} else {
			t.polls.WithLabelValues("error").Inc()
		}
		save()
		return err
	}
	if list == nil && st.NotModified {
		ok := t.now()
		st.LastSuccessAt = &ok
		t.polls.WithLabelValues("not_modified").Inc()
		save()
		t.refreshLatestMetric(ctx)
		return nil
	}
	st.ReleasesSeen = len(list)
	created, err := t.process(ctx, list)
	st.NewReleases = created
	if err != nil {
		st.LastError = err.Error()
		t.polls.WithLabelValues("error").Inc()
		save()
		return err
	}
	// Remember the ETag only after the page was processed completely.
	if etag != "" {
		_ = t.deps.Store.KV().Put(ctx, etagKVPrefix+t.cfg.ReleasesRepo, []byte(etag))
	}
	ok := t.now()
	st.LastSuccessAt, st.LastError = &ok, ""
	t.polls.WithLabelValues("ok").Inc()
	save()
	t.refreshLatestMetric(ctx)
	return nil
}

// tracked is a release of a tracked distribution.
type tracked struct {
	dist, version string
	gh            ghRelease
}

func (t *Tracker) process(ctx context.Context, list []ghRelease) (int, error) {
	var items []tracked
	for _, r := range list {
		if r.Draft {
			continue
		}
		dist, ver, ok := classifyTag(r.TagName)
		if !ok || !t.dists[dist] {
			continue
		}
		if r.Prerelease && !t.cfg.IncludePrereleases {
			continue
		}
		v, err := semver.NewVersion(ver)
		if err != nil || v.LessThan(t.minVersion) {
			continue
		}
		items = append(items, tracked{dist: dist, version: ver, gh: r})
	}
	// Newest first: the changelog of the newest tag contains all older sections.
	sort.SliceStable(items, func(i, j int) bool { return versionLess(items[j].version, items[i].version) })

	// The newest contrib tag is the fallback ref for changelogs.
	newest := ""
	for _, it := range items {
		if it.dist == domain.DistributionContrib {
			newest = it.version
			break
		}
	}

	existed := map[string]bool{} // distribution -> store already has releases
	for d := range t.dists {
		if l, _, err := t.deps.Store.Releases().List(ctx, store.ReleaseFilter{Distribution: d, IncludePrerelease: true, Limit: 1}); err == nil && len(l) > 0 {
			existed[d] = true
		}
	}

	created := 0
	notifiedInitial := map[string]bool{}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		rel, isNew, err := t.buildRelease(ctx, it, newest)
		if err != nil {
			return created, err
		}
		if !isNew {
			continue
		}
		if _, err := t.deps.Store.Releases().Upsert(ctx, *rel); err != nil {
			return created, fmt.Errorf("store release %s %s: %w", it.dist, it.version, err)
		}
		created++
		t.log.Info("new upstream release detected", "distribution", it.dist, "version", it.version,
			"breaking", len(rel.BreakingChanges), "deprecations", len(rel.Deprecations))
		if t.deps.Bus != nil {
			if err := t.deps.Bus.Publish(ctx, eventbus.ReleaseDetected, eventbus.ReleaseDetectedPayload{Distribution: it.dist, Version: it.version}); err != nil {
				t.log.Warn("publish release.detected failed", "error", err)
			}
		}
		// First import: only the newest release of a distribution is announced.
		if !existed[it.dist] {
			if notifiedInitial[it.dist] {
				continue
			}
			notifiedInitial[it.dist] = true
		}
		t.notifyRelease(ctx, rel)
	}
	return created, nil
}

// buildRelease converts a GitHub release; isNew is false if the release is
// already stored (then assets/notes are refreshed if they changed upstream).
func (t *Tracker) buildRelease(ctx context.Context, it tracked, newest string) (*domain.Release, bool, error) {
	rel := &domain.Release{
		Distribution: it.dist, Version: it.version, Tag: it.gh.TagName, PublishedAt: it.gh.PublishedAt,
		Prerelease: it.gh.Prerelease, HTMLURL: it.gh.HTMLURL, Notes: it.gh.Body,
		BreakingChanges: []domain.ChangelogEntry{}, Deprecations: []domain.ChangelogEntry{}, Assets: []domain.ReleaseAsset{},
		DetectedAt: t.now(),
	}
	for _, a := range it.gh.Assets {
		if pa, ok := parseAsset(it.dist, a.Name); ok {
			pa.URL, pa.Size, pa.ContentType = a.BrowserDownloadURL, a.Size, a.ContentType
			rel.Assets = append(rel.Assets, pa)
		}
	}
	existing, err := t.deps.Store.Releases().Get(ctx, it.dist, it.version)
	switch {
	case err == nil:
		// Known release: keep the extracted changes, refresh mutable upstream data.
		rel.BreakingChanges, rel.Deprecations = existing.BreakingChanges, existing.Deprecations
		rel.DetectedAt = existing.DetectedAt
		if sameRelease(existing, rel) {
			return rel, false, nil
		}
		if _, err := t.deps.Store.Releases().Upsert(ctx, *rel); err != nil {
			return nil, false, fmt.Errorf("update release %s %s: %w", it.dist, it.version, err)
		}
		return rel, false, nil
	case !errors.Is(err, domain.ErrNotFound):
		return nil, false, fmt.Errorf("load release %s %s: %w", it.dist, it.version, err)
	}

	breaking, deprecations, cerr := t.changes(ctx, it.dist, it.version, newest)
	if cerr != nil {
		// A temporary changelog problem must not hide the release: store it
		// without changes and log; the next poll does not retry (release is
		// known), so make this loud.
		t.log.Error("changelog could not be read, release stored without breaking changes", "distribution", it.dist, "version", it.version, "error", cerr)
	}
	rel.BreakingChanges, rel.Deprecations = breaking, deprecations
	return rel, true, nil
}

func sameRelease(a, b *domain.Release) bool {
	if a.Notes != b.Notes || a.HTMLURL != b.HTMLURL || a.Prerelease != b.Prerelease || len(a.Assets) != len(b.Assets) {
		return false
	}
	for i := range a.Assets {
		if a.Assets[i] != b.Assets[i] {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------- changelogs ----

// changes extracts the breaking changes and deprecations of one version from
// the core and contrib changelogs. For the supervisor distribution only the
// entries of component "cmd/opampsupervisor" of the contrib changelog count.
func (t *Tracker) changes(ctx context.Context, dist, version, newest string) (breaking, deprecations []domain.ChangelogEntry, err error) {
	breaking, deprecations = []domain.ChangelogEntry{}, []domain.ChangelogEntry{}
	var errs []error
	for _, src := range t.changelogSources(dist) {
		secs, serr := t.sectionsFor(ctx, src.repo, version, newest)
		if serr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.name, serr))
			continue
		}
		for _, s := range secs {
			if s.Version != version {
				continue
			}
			breaking = append(breaking, filterEntries(src.name, version, s.Breaking, src.component)...)
			deprecations = append(deprecations, filterEntries(src.name, version, s.Deprecations, src.component)...)
		}
	}
	return breaking, deprecations, errors.Join(errs...)
}

type changelogSource struct {
	name      string // "core" | "contrib"
	repo      string
	component string // restrict to this component ("" = all)
}

func (t *Tracker) changelogSources(dist string) []changelogSource {
	if dist == domain.DistributionSupervisor {
		return []changelogSource{{name: "contrib", repo: t.cfg.ContribRepo, component: "cmd/opampsupervisor"}}
	}
	return []changelogSource{{name: "core", repo: t.cfg.CoreRepo}, {name: "contrib", repo: t.cfg.ContribRepo}}
}

func filterEntries(source, version string, es []entry, component string) []domain.ChangelogEntry {
	if component != "" {
		var keep []entry
		for _, e := range es {
			if e.Component == component {
				keep = append(keep, e)
			}
		}
		es = keep
	}
	return toDomain(source, version, es)
}

// sectionsFor returns parsed changelog sections that include the version:
// from the changelog at tag v<version>, falling back to the newest tag (it
// contains all older sections). Parsed files are cached for a few minutes, so
// one file serves all versions of a poll.
func (t *Tracker) sectionsFor(ctx context.Context, repo, version, newest string) ([]section, error) {
	t.clMu.Lock()
	now := t.now()
	for k, e := range t.clCache {
		if now.Sub(e.at) > changelogCacheTTL {
			delete(t.clCache, k)
		}
	}
	for key, e := range t.clCache {
		if strings.HasPrefix(key, repo+"@") {
			for _, s := range e.secs {
				if s.Version == version {
					t.clMu.Unlock()
					return e.secs, nil
				}
			}
		}
	}
	t.clMu.Unlock()

	refs := []string{"v" + version}
	if newest != "" && newest != version {
		refs = append(refs, "v"+newest)
	}
	var lastErr error
	for _, ref := range refs {
		md, err := t.fetchChangelog(ctx, repo, ref)
		if errors.Is(err, errNotFound) {
			lastErr = fmt.Errorf("CHANGELOG.md of %s at %s does not exist", repo, ref)
			continue
		}
		if err != nil {
			return nil, err
		}
		secs := parseChangelog(md)
		t.clMu.Lock()
		t.clCache[repo+"@"+ref] = clEntry{at: t.now(), secs: secs}
		t.clMu.Unlock()
		return secs, nil
	}
	return nil, lastErr
}

// fetchChangelog loads CHANGELOG.md of repo at ref (fixture directory or HTTP).
func (t *Tracker) fetchChangelog(ctx context.Context, repo, ref string) (string, error) {
	if t.cfg.FixtureDir != "" {
		owner, name := repoPath(repo)
		b, err := os.ReadFile(filepath.Join(t.cfg.FixtureDir, "raw", owner, name, ref, "CHANGELOG.md"))
		if errors.Is(err, os.ErrNotExist) {
			return "", errNotFound
		}
		if err != nil {
			return "", fmt.Errorf("read changelog fixture: %w", stripPath(err))
		}
		return string(b), nil
	}
	u := strings.TrimRight(t.cfg.RawBaseURL, "/") + "/" + repo + "/" + ref + "/CHANGELOG.md"
	res, err := t.get(ctx, u, "", maxBody, true)
	if err != nil {
		return "", err
	}
	switch res.status {
	case http.StatusOK:
		return string(res.body), nil
	case http.StatusNotFound:
		return "", errNotFound
	default:
		if res.status == http.StatusForbidden || res.status == http.StatusTooManyRequests {
			st := &Status{}
			t.setBackoff(st, res, "rate limit while reading changelogs")
		}
		return "", fmt.Errorf("GET %s: HTTP %d", redact(u), res.status)
	}
}

// ---------------------------------------------------------------- Compare ----

// Compare returns breaking changes/deprecations for versions in (from, to].
// Versions the store knows are answered from the store; if older versions are
// missing (before the first import / minVersion), the changelogs at tag
// v<to> are parsed once - they contain all older sections.
func (t *Tracker) Compare(ctx context.Context, distribution, from, to string) (*domain.VersionComparison, error) {
	from, to = domain.NormalizeVersion(from), domain.NormalizeVersion(to)
	vf, err := semver.StrictNewVersion(from)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid from version %q", domain.ErrInvalidArgument, from)
	}
	vt, err := semver.StrictNewVersion(to)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid to version %q", domain.ErrInvalidArgument, to)
	}
	if distribution != domain.DistributionContrib && distribution != domain.DistributionSupervisor {
		return nil, fmt.Errorf("%w: unsupported distribution %q", domain.ErrInvalidArgument, distribution)
	}
	cmp := &domain.VersionComparison{
		Distribution: distribution, From: from, To: to, Versions: []string{},
		BreakingChanges: []domain.ChangelogEntry{}, Deprecations: []domain.ChangelogEntry{},
	}
	if !vf.LessThan(vt) {
		return cmp, nil
	}

	rels, _, err := t.deps.Store.Releases().List(ctx, store.ReleaseFilter{
		Distribution: distribution, IncludePrerelease: true, SinceVersion: from, Limit: compareMaxItems,
	})
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	known := map[string]domain.Release{}
	for _, r := range rels {
		if v, err := semver.NewVersion(r.Version); err == nil && v.GreaterThan(vf) && !v.GreaterThan(vt) {
			known[r.Version] = r
		}
	}

	var fromLog map[string]logChanges
	if hasGap(known, vf, vt) {
		fromLog, err = t.changesInRange(ctx, distribution, vf, vt)
		if err != nil {
			t.log.Warn("changelog lookup for a version comparison failed, using stored releases only", "from", from, "to", to, "error", err)
		}
	}

	versions := map[string]bool{}
	for v := range known {
		versions[v] = true
	}
	for v := range fromLog {
		versions[v] = true
	}
	for v := range versions {
		cmp.Versions = append(cmp.Versions, v)
	}
	sortVersions(cmp.Versions)
	for _, v := range cmp.Versions {
		if r, ok := known[v]; ok {
			cmp.BreakingChanges = append(cmp.BreakingChanges, r.BreakingChanges...)
			cmp.Deprecations = append(cmp.Deprecations, r.Deprecations...)
			continue
		}
		c := fromLog[v]
		cmp.BreakingChanges = append(cmp.BreakingChanges, c.breaking...)
		cmp.Deprecations = append(cmp.Deprecations, c.deprecations...)
	}
	return cmp, nil
}

// hasGap reports whether the stored releases cannot cover (from, to]: the
// target itself is unknown, or the oldest stored release is more than one
// minor version above "from" (releases are consecutive minor versions).
func hasGap(known map[string]domain.Release, from, to *semver.Version) bool {
	if _, ok := known[to.String()]; !ok {
		return true
	}
	oldest := to
	for v := range known {
		if sv, err := semver.NewVersion(v); err == nil && sv.LessThan(oldest) {
			oldest = sv
		}
	}
	return oldest.Major() != from.Major() || oldest.Minor() > from.Minor()+1
}

type logChanges struct {
	breaking, deprecations []domain.ChangelogEntry
}

// changesInRange reads the changelogs at tag v<to> (fallback: the newest
// stored release) and extracts the sections with from < version <= to.
func (t *Tracker) changesInRange(ctx context.Context, dist string, from, to *semver.Version) (map[string]logChanges, error) {
	newest := ""
	if l, err := t.deps.Store.Releases().Latest(ctx, domain.DistributionContrib, true); err == nil {
		newest = l.Version
	}
	out := map[string]logChanges{}
	var errs []error
	for _, src := range t.changelogSources(dist) {
		secs, err := t.sectionsFor(ctx, src.repo, to.String(), newest)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.name, err))
			continue
		}
		for _, s := range secs {
			v, err := semver.NewVersion(s.Version)
			if err != nil || !v.GreaterThan(from) || v.GreaterThan(to) {
				continue
			}
			c := out[s.Version]
			c.breaking = append(c.breaking, filterEntries(src.name, s.Version, s.Breaking, src.component)...)
			c.deprecations = append(c.deprecations, filterEntries(src.name, s.Version, s.Deprecations, src.component)...)
			out[s.Version] = c
		}
	}
	return out, errors.Join(errs...)
}

// ------------------------------------------------------- notify and metrics ----

func (t *Tracker) notifyRelease(ctx context.Context, r *domain.Release) {
	if t.deps.Notifier == nil {
		return
	}
	ev, sev := notify.EventReleaseNew, notify.SeverityInfo
	title := fmt.Sprintf("New release: %s %s", r.Distribution, r.Version)
	summary := fmt.Sprintf("Version %s of %s was released upstream.", r.Version, r.Distribution)
	if len(r.BreakingChanges) > 0 {
		ev, sev = notify.EventReleaseBreaking, notify.SeverityWarning
		title = fmt.Sprintf("New release with breaking changes: %s %s", r.Distribution, r.Version)
		var lines []string
		for i, e := range r.BreakingChanges {
			if i == 5 {
				lines = append(lines, fmt.Sprintf("... and %d more", len(r.BreakingChanges)-5))
				break
			}
			first := e.Text
			if j := strings.IndexByte(first, '\n'); j >= 0 {
				first = first[:j]
			}
			if e.Component != "" {
				first = e.Component + ": " + first
			}
			lines = append(lines, "- "+first)
		}
		summary = fmt.Sprintf("Version %s of %s contains %d breaking change(s):\n%s", r.Version, r.Distribution, len(r.BreakingChanges), strings.Join(lines, "\n"))
	}
	fields := []notify.Field{
		{Name: "Distribution", Value: r.Distribution}, {Name: "Version", Value: r.Version},
		{Name: "Breaking changes", Value: strconv.Itoa(len(r.BreakingChanges))},
		{Name: "Deprecations", Value: strconv.Itoa(len(r.Deprecations))},
	}
	if r.PublishedAt != nil {
		fields = append(fields, notify.Field{Name: "Published", Value: r.PublishedAt.UTC().Format("2006-01-02 15:04 UTC")})
	}
	if r.HTMLURL != "" {
		fields = append(fields, notify.Field{Name: "Release notes", Value: r.HTMLURL})
	}
	n := notify.Notification{
		Event: ev, Severity: sev, Title: title, Summary: summary,
		Link: "/releases/" + r.Distribution + "/" + r.Version, Fields: fields, Time: t.now(),
		DedupKey: "release.new/" + r.Distribution + "/" + r.Version,
	}
	if err := t.deps.Notifier.Notify(ctx, n); err != nil {
		t.log.Warn("release notification failed", "distribution", r.Distribution, "version", r.Version, "error", err)
	}
}

// refreshLatestMetric exports the latest stable release per distribution.
func (t *Tracker) refreshLatestMetric(ctx context.Context) {
	t.latestMu.Lock()
	defer t.latestMu.Unlock()
	for d := range t.dists {
		l, err := t.deps.Store.Releases().Latest(ctx, d, false)
		if err != nil {
			continue
		}
		if prev, ok := t.latestSeen[d]; ok && prev != l.Version {
			t.latestInfo.DeleteLabelValues(d, prev)
		}
		t.latestInfo.WithLabelValues(d, l.Version).Set(1)
		t.latestSeen[d] = l.Version
	}
}
