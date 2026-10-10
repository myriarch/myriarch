// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

const fixtures = "testdata/fixtures"

type recNotifier struct {
	mu   sync.Mutex
	list []notify.Notification
}

func (r *recNotifier) Notify(_ context.Context, n notify.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, n)
	return nil
}
func (r *recNotifier) Test(context.Context, string) error { return nil }
func (r *recNotifier) Channels() []notify.ChannelInfo     { return nil }
func (r *recNotifier) all() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Notification(nil), r.list...)
}

type env struct {
	st    *memstore.Store
	bus   eventbus.Bus
	notif *recNotifier
	reg   *prometheus.Registry
	tr    *Tracker
}

func newEnv(t *testing.T, mut ...func(c *config.ReleasesConfig)) *env {
	t.Helper()
	cfg := config.Default().Releases
	cfg.FixtureDir = fixtures
	for _, m := range mut {
		m(&cfg)
	}
	e := &env{st: memstore.New(), bus: eventbus.NewMemory("t", 0), notif: &recNotifier{}, reg: prometheus.NewRegistry()}
	t.Cleanup(func() { e.bus.Close() })
	tr, err := NewTracker(cfg, Deps{Store: e.st, Bus: e.bus, Notifier: e.notif, HTTPClient: http.DefaultClient,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: e.reg})
	require.NoError(t, err)
	e.tr = tr
	return e
}

func (e *env) releases(t *testing.T, dist string) []domain.Release {
	t.Helper()
	l, _, err := e.st.Releases().List(context.Background(), store_filter(dist))
	require.NoError(t, err)
	return l
}

func TestParseChangelogHeadings(t *testing.T) {
	for heading, want := range map[string]string{
		"v1.68.0/v0.162.0":       "0.162.0",
		"v1.1.0/v0.162.0":        "0.162.0",
		"v0.161.0":               "0.161.0",
		"v0.140.1":               "0.140.1",
		"v1.52.0/v0.146.1":       "0.146.1",
		"v0.73.0/v1.0.0-rc7":     "0.73.0",
		"v1.0.0-rcv0018/v0.89.0": "0.89.0",
		"v0.106.1":               "0.106.1",
		"v1.0.0":                 "",
		"Unreleased":             "",
		"v0.90.0-rc1":            "",
	} {
		assert.Equal(t, want, releaseVersion(heading), heading)
	}
}

func TestParseChangelogFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "raw/open-telemetry/opentelemetry-collector-contrib/v0.162.0/CHANGELOG.md"))
	require.NoError(t, err)
	secs := parseChangelog(string(raw))
	byVer := map[string]section{}
	var order []string
	for _, s := range secs {
		byVer[s.Version] = s
		order = append(order, s.Version)
	}
	assert.Equal(t, []string{"0.162.0", "0.161.0", "0.160.0", "0.159.0", "0.158.0", "0.157.0"}, order)

	s := byVer["0.162.0"]
	require.NotEmpty(t, s.Breaking)
	var wave *entry
	var es *entry
	for i := range s.Breaking {
		if strings.Contains(s.Breaking[i].Text, "wavefront receiver") {
			wave = &s.Breaking[i]
		}
		if s.Breaking[i].Component == "exporter/elasticsearch" && strings.Contains(s.Breaking[i].Text, "num_workers") {
			es = &s.Breaking[i]
		}
	}
	require.NotNil(t, wave, "sample from the 0.162.0 release notes: all: Remove the wavefront receiver after a period of deprecation (#51397)")
	assert.Equal(t, "all", wave.Component)
	assert.Equal(t, "Remove the wavefront receiver after a period of deprecation", wave.Text)
	assert.Equal(t, []int{51397}, wave.Issues)

	require.NotNil(t, es)
	assert.Equal(t, []int{42718}, es.Issues)
	assert.True(t, strings.HasPrefix(es.Text, "Remove the deprecated `flush` and `num_workers` configuration settings."))
	assert.Contains(t, es.Text, "\nMigrate `num_workers` to `sending_queue::num_consumers`", "continuation lines are appended")
	assert.Contains(t, es.Text, "the exported Go API.")
	assert.False(t, strings.HasSuffix(es.Text, " "), "trailing blank lines trimmed")

	// multiple issues
	var ottl *entry
	for i := range s.Breaking {
		if s.Breaking[i].Component == "pkg/ottl" {
			ottl = &s.Breaking[i]
		}
	}
	require.NotNil(t, ottl)
	assert.Equal(t, []int{44630, 46437}, ottl.Issues)

	// every bullet of the breaking sections matches the entry regexp (documented as 100 %)
	for v, sec := range byVer {
		for _, e := range append(append([]entry{}, sec.Breaking...), sec.Deprecations...) {
			assert.NotEmpty(t, e.Component, "version %s: %q", v, e.Text)
		}
	}
	assert.NotEmpty(t, byVer["0.158.0"].Breaking)

	// core: "v1.68.0/v0.162.0"
	raw, err = os.ReadFile(filepath.Join(fixtures, "raw/open-telemetry/opentelemetry-collector/v0.162.0/CHANGELOG.md"))
	require.NoError(t, err)
	core := parseChangelog(string(raw))
	require.NotEmpty(t, core)
	assert.Equal(t, "0.162.0", core[0].Version)
	byCore := map[string]section{}
	for _, c := range core {
		byCore[c.Version] = c
	}
	assert.Len(t, byCore["0.161.0"].Breaking, 4)
	assert.NotEmpty(t, byCore["0.160.0"].Deprecations, "deprecations are extracted")
}

func TestParseChangelogEdgeCases(t *testing.T) {
	md := "# Changelog\n\n## v0.5.0\n\n### Breaking changes\n\n- `a`: first (#1)\n  sub line\n  \n  second paragraph\n- no component here (#2)\n- `b`: multi (#3, #4)\n\n- `ignored`: after blank line is still a bullet (#5)\n\n### Deprecations\n\n- `c`: old thing\n\n### Enhancements\n\n- `d`: ignored\n\n## 🛑 Breaking changes 🛑\n\n- `legacy`: level-2 heading belongs to v0.5.0 (#6)\n\n## Unreleased\n\n### Breaking changes\n- `x`: ignored block\n\n## v0.4.9-rc1\n\n### Breaking changes\n- `y`: prerelease token only -> ignored\n\n## v1.2.3/v0.4.0\n\n### 🚩 Deprecations 🚩\n- `z`: deprecated (#7)\n"
	secs := parseChangelog(md)
	require.Len(t, secs, 2)
	s := secs[0]
	assert.Equal(t, "0.5.0", s.Version)
	require.Len(t, s.Breaking, 5)
	assert.Equal(t, "a", s.Breaking[0].Component)
	assert.Equal(t, "first\nsub line\n\nsecond paragraph", s.Breaking[0].Text)
	assert.Equal(t, "", s.Breaking[1].Component, "bullets without a component are kept")
	assert.Equal(t, "no component here (#2)", s.Breaking[1].Text)
	assert.Equal(t, []int{3, 4}, s.Breaking[2].Issues)
	assert.Equal(t, "ignored", s.Breaking[3].Component)
	assert.Equal(t, "legacy", s.Breaking[4].Component)
	require.Len(t, s.Deprecations, 1)
	assert.Equal(t, "0.4.0", secs[1].Version)
	require.Len(t, secs[1].Deprecations, 1)
	assert.Empty(t, parseChangelog(""))
}

func TestClassifyTagsAndAssets(t *testing.T) {
	for tag, want := range map[string][2]string{
		"v0.162.0":                     {domain.DistributionContrib, "0.162.0"},
		"cmd/opampsupervisor/v0.162.0": {domain.DistributionSupervisor, "0.162.0"},
	} {
		d, v, ok := classifyTag(tag)
		assert.True(t, ok, tag)
		assert.Equal(t, want, [2]string{d, v})
	}
	for _, tag := range []string{"cmd/builder/v0.162.0", "v0.163.0-nightly.202610060221", "cmd/opampsupervisor/v0.163.0-nightly.202610060221", "v1.0.0", "0.162.0", "v0.162", "latest"} {
		_, _, ok := classifyTag(tag)
		assert.False(t, ok, tag)
	}

	type exp struct {
		name string
		ok   bool
		os   string
		arch string
		fmt  string
		dist string
	}
	for _, want := range []exp{
		{"otelcol-contrib_0.162.0_linux_amd64.tar.gz", true, "linux", "amd64", "tar.gz", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_windows_amd64.tar.gz", true, "windows", "amd64", "tar.gz", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_windows_x64.msi", true, "windows", "amd64", "msi", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_windows_x86.msi", true, "windows", "386", "msi", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_linux_armv7.deb", true, "linux", "armv7", "deb", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_linux_ppc64le.rpm", true, "linux", "ppc64le", "rpm", "otelcol-contrib"},
		{"opampsupervisor_0.162.0_linux_amd64", true, "linux", "amd64", "binary", "opampsupervisor"},
		{"opampsupervisor_0.162.0_windows_amd64.exe", true, "windows", "amd64", "binary", "opampsupervisor"},
		{"otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256", false, "", "", "", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_linux_amd64.tar.gz.sigstore.json", false, "", "", "", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_linux_amd64.tar.gz.sbom.json", false, "", "", "", "otelcol-contrib"},
		{"otelcol-k8s_0.162.0_linux_amd64.tar.gz", false, "", "", "", "otelcol-contrib"},
		{"otelcol-contrib_0.162.0_linux_amd64.tar.gz", false, "", "", "", "opampsupervisor"},
		{"checksums.txt", false, "", "", "", "otelcol-contrib"},
	} {
		a, ok := parseAsset(want.dist, want.name)
		assert.Equal(t, want.ok, ok, want.name)
		if ok {
			assert.Equal(t, want.os, a.OS, want.name)
			assert.Equal(t, want.arch, a.Arch, want.name)
			assert.Equal(t, want.fmt, a.Format, want.name)
			assert.Equal(t, want.name, a.Name)
		}
	}
}

func TestPollFixturesFirstImport(t *testing.T) {
	e := newEnv(t)
	events, cancel := e.bus.Subscribe(eventbus.ReleaseDetected)
	defer cancel()
	require.NoError(t, e.tr.Poll(context.Background()))

	contrib := e.releases(t, domain.DistributionContrib)
	var vs []string
	for _, r := range contrib {
		vs = append(vs, r.Version)
	}
	assert.Equal(t, []string{"0.162.0", "0.161.0", "0.160.0", "0.159.0", "0.158.0"}, vs, "highest first; the cmd/builder tag is ignored")
	sup := e.releases(t, domain.DistributionSupervisor)
	vs = nil
	for _, r := range sup {
		vs = append(vs, r.Version)
	}
	assert.Equal(t, []string{"0.162.0", "0.161.0", "0.160.0"}, vs)

	r, err := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.162.0")
	require.NoError(t, err)
	assert.Equal(t, "v0.162.0", r.Tag)
	assert.False(t, r.Prerelease)
	assert.Equal(t, "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v0.162.0", r.HTMLURL)
	require.NotNil(t, r.PublishedAt)
	assert.Equal(t, time.Date(2026, 9, 29, 12, 34, 7, 0, time.UTC), r.PublishedAt.UTC())
	assert.Contains(t, r.Notes, "contrib changelog")
	assert.NotEmpty(t, r.DetectedAt)
	// assets: primary files only, with OS/arch/format
	var names []string
	for _, a := range r.Assets {
		names = append(names, a.Name)
		assert.NotEmpty(t, a.URL)
		assert.NotZero(t, a.Size)
		assert.NotEmpty(t, a.OS)
		assert.NotEmpty(t, a.Arch)
	}
	assert.ElementsMatch(t, []string{
		"otelcol-contrib_0.162.0_linux_amd64.tar.gz", "otelcol-contrib_0.162.0_linux_arm64.tar.gz",
		"otelcol-contrib_0.162.0_windows_amd64.tar.gz", "otelcol-contrib_0.162.0_windows_x64.msi",
	}, names)

	// breaking changes: contrib AND core, with source, version, component, issues
	sources := map[string]int{}
	var wave *domain.ChangelogEntry
	for i, b := range r.BreakingChanges {
		sources[b.Source]++
		assert.Equal(t, "0.162.0", b.Version)
		assert.Equal(t, "CHANGELOG.md", b.File)
		if strings.Contains(b.Text, "wavefront receiver") {
			wave = &r.BreakingChanges[i]
		}
	}
	require.NotNil(t, wave, "contrib v0.162.0 contains: all: Remove the wavefront receiver after a period of deprecation (#51397)")
	assert.Equal(t, "contrib", wave.Source)
	assert.Equal(t, "all", wave.Component)
	assert.Equal(t, []int{51397}, wave.Issues)
	assert.Greater(t, sources["contrib"], 10)
	assert.Greater(t, sources["core"], 0, "core changelog entries of the same version")
	assert.NotNil(t, r.Deprecations)

	// Older versions: the fixtures only have changelogs at tag v0.162.0 -> fallback to the newest tag.
	r158, err := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.158.0")
	require.NoError(t, err)
	assert.NotEmpty(t, r158.BreakingChanges, "sections of older versions are read from the newest tag's changelog")
	for _, b := range r158.BreakingChanges {
		assert.Equal(t, "0.158.0", b.Version)
	}
	r161, _ := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.161.0")
	coreCount := 0
	for _, b := range r161.BreakingChanges {
		if b.Source == "core" {
			coreCount++
		}
	}
	assert.Equal(t, 4, coreCount)

	// supervisor releases: only cmd/opampsupervisor entries of the contrib changelog
	rs, err := e.st.Releases().Get(context.Background(), domain.DistributionSupervisor, "0.162.0")
	require.NoError(t, err)
	assert.Equal(t, "cmd/opampsupervisor/v0.162.0", rs.Tag)
	for _, b := range append(append([]domain.ChangelogEntry{}, rs.BreakingChanges...), rs.Deprecations...) {
		assert.Equal(t, "cmd/opampsupervisor", b.Component)
		assert.Equal(t, "contrib", b.Source)
	}
	var supNames []string
	for _, a := range rs.Assets {
		supNames = append(supNames, a.Name)
		assert.Equal(t, "binary", a.Format)
	}
	assert.ElementsMatch(t, []string{"opampsupervisor_0.162.0_linux_amd64", "opampsupervisor_0.162.0_windows_amd64.exe"}, supNames)

	// bus: one event per new release
	n := 0
	for len(events) > 0 {
		<-events
		n++
	}
	assert.Equal(t, 8, n)

	// notifications: first import announces only the newest release per distribution
	ns := e.notif.all()
	require.Len(t, ns, 2)
	got := map[string]notify.Notification{}
	for _, x := range ns {
		got[x.DedupKey] = x
	}
	c, ok := got["release.new/otelcol-contrib/0.162.0"]
	require.True(t, ok, "dedup key release.new/<distribution>/<version>: %+v", got)
	assert.Equal(t, notify.EventReleaseBreaking, c.Event, "breaking changes present")
	assert.Equal(t, notify.SeverityWarning, c.Severity)
	assert.Equal(t, "/releases/otelcol-contrib/0.162.0", c.Link)
	assert.Contains(t, c.Summary, "all: Remove the wavefront receiver")
	assert.Contains(t, c.Title, "0.162.0")
	_, ok = got["release.new/opampsupervisor/0.162.0"]
	assert.True(t, ok)

	// metric
	assert.Equal(t, 1.0, metricValue(t, e.reg, "myriarch_release_latest_info", map[string]string{"distribution": "otelcol-contrib", "version": "0.162.0"}))
	assert.Equal(t, 1.0, metricValue(t, e.reg, "myriarch_release_polls_total", map[string]string{"result": "ok"}))

	// status in the KV store
	st, err := e.tr.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 9, st.ReleasesSeen)
	assert.Equal(t, 8, st.NewReleases)
	assert.Empty(t, st.LastError)
	assert.NotNil(t, st.LastSuccessAt)
}

func TestSecondPollIsIdempotent(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.tr.Poll(context.Background()))
	r1, _ := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.162.0")
	ack := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	require.NoError(t, e.st.Releases().Acknowledge(context.Background(), domain.DistributionContrib, "0.162.0", "user:1", ack))
	events, cancel := e.bus.Subscribe(eventbus.ReleaseDetected)
	defer cancel()
	before := len(e.notif.all())

	require.NoError(t, e.tr.Poll(context.Background()))
	assert.Len(t, events, 0, "known releases are not detected again")
	assert.Len(t, e.notif.all(), before)
	r2, _ := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.162.0")
	assert.Equal(t, r1.DetectedAt, r2.DetectedAt)
	assert.Equal(t, "user:1", r2.AcknowledgedBy, "acknowledgement survives")
	assert.Equal(t, len(r1.BreakingChanges), len(r2.BreakingChanges))
}

func TestNewReleaseAfterInitialImportIsAnnounced(t *testing.T) {
	e := newEnv(t)
	// Pre-populate the store with everything except 0.162.0 (as if the previous poll happened two weeks ago).
	full := newEnv(t)
	require.NoError(t, full.tr.Poll(context.Background()))
	for _, d := range []string{domain.DistributionContrib, domain.DistributionSupervisor} {
		for _, r := range full.releases(t, d) {
			if r.Version != "0.162.0" {
				_, err := e.st.Releases().Upsert(context.Background(), r)
				require.NoError(t, err)
			}
		}
	}
	require.NoError(t, e.tr.Poll(context.Background()))
	ns := e.notif.all()
	require.Len(t, ns, 2, "0.162.0 of both distributions")
	keys := []string{ns[0].DedupKey, ns[1].DedupKey}
	assert.ElementsMatch(t, []string{"release.new/otelcol-contrib/0.162.0", "release.new/opampsupervisor/0.162.0"}, keys)
}

func TestReleaseWithoutBreakingChangesIsReleaseNew(t *testing.T) {
	dir := t.TempDir()
	copyFixtureWith(t, dir, func(rels []map[string]any) []map[string]any { return rels })
	// changelogs without any breaking section
	for _, repo := range []string{"opentelemetry-collector", "opentelemetry-collector-contrib"} {
		p := filepath.Join(dir, "raw/open-telemetry", repo, "v0.162.0")
		require.NoError(t, os.MkdirAll(p, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(p, "CHANGELOG.md"), []byte("## v0.162.0\n\n### Enhancements\n\n- `x`: nice (#1)\n"), 0o644))
	}
	e := newEnv(t, func(c *config.ReleasesConfig) { c.FixtureDir = dir })
	require.NoError(t, e.tr.Poll(context.Background()))
	var contrib *notify.Notification
	for _, n := range e.notif.all() {
		if n.DedupKey == "release.new/otelcol-contrib/0.162.0" {
			n := n
			contrib = &n
		}
	}
	require.NotNil(t, contrib)
	assert.Equal(t, notify.EventReleaseNew, contrib.Event)
	assert.Equal(t, notify.SeverityInfo, contrib.Severity)
}

func TestMissingChangelogStoresReleaseWithoutChanges(t *testing.T) {
	dir := t.TempDir()
	copyFixtureWith(t, dir, func(rels []map[string]any) []map[string]any { return rels })
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "raw")))
	e := newEnv(t, func(c *config.ReleasesConfig) { c.FixtureDir = dir })
	require.NoError(t, e.tr.Poll(context.Background()), "a missing changelog must not stop release tracking")
	r, err := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.162.0")
	require.NoError(t, err)
	assert.Empty(t, r.BreakingChanges)
	assert.NotNil(t, r.BreakingChanges, "never null")
}

func TestFiltersDraftsPrereleasesNightliesAndMinVersion(t *testing.T) {
	dir := t.TempDir()
	copyFixtureWith(t, dir, func(rels []map[string]any) []map[string]any {
		add := func(tag string, draft, pre bool) {
			rels = append(rels, map[string]any{"tag_name": tag, "draft": draft, "prerelease": pre, "published_at": "2026-10-01T00:00:00Z", "html_url": "https://x/" + tag, "body": "", "assets": []any{}})
		}
		add("v0.163.0", true, false)                       // draft
		add("v0.164.0", false, true)                       // prerelease
		add("v0.165.0-nightly.202610060221", false, false) // nightly
		add("cmd/opampsupervisor/v0.163.0-nightly.202610060221", false, false)
		add("v0.119.0", false, false) // below minVersion 0.120.0
		add("v0.120.0", false, false) // exactly minVersion: kept
		return rels
	})
	e := newEnv(t, func(c *config.ReleasesConfig) { c.FixtureDir = dir })
	require.NoError(t, e.tr.Poll(context.Background()))
	versions := func(dist string) []string {
		var out []string
		for _, r := range e.releases(t, dist) {
			out = append(out, r.Version)
		}
		return out
	}
	assert.Equal(t, []string{"0.162.0", "0.161.0", "0.160.0", "0.159.0", "0.158.0", "0.120.0"}, versions(domain.DistributionContrib))

	e2 := newEnv(t, func(c *config.ReleasesConfig) { c.FixtureDir = dir; c.IncludePrereleases = true })
	require.NoError(t, e2.tr.Poll(context.Background()))
	l, _, _ := e2.st.Releases().List(context.Background(), store_filterPre(domain.DistributionContrib))
	has164 := false
	for _, r := range l {
		if r.Version == "0.164.0" {
			has164 = true
			assert.True(t, r.Prerelease)
		}
		assert.NotEqual(t, "0.163.0", r.Version, "drafts are always ignored")
	}
	assert.True(t, has164)
	latest, err := e2.st.Releases().Latest(context.Background(), domain.DistributionContrib, false)
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", latest.Version, "stable latest ignores prereleases")

	// only tracked distributions
	e3 := newEnv(t, func(c *config.ReleasesConfig) { c.FixtureDir = dir; c.Distributions = []string{"otelcol-contrib"} })
	require.NoError(t, e3.tr.Poll(context.Background()))
	assert.Empty(t, e3.releases(t, domain.DistributionSupervisor))
}

func TestChangedAssetsOfAKnownReleaseAreRefreshed(t *testing.T) {
	dir := t.TempDir()
	copyFixtureWith(t, dir, func(rels []map[string]any) []map[string]any { return rels })
	e := newEnv(t, func(c *config.ReleasesConfig) { c.FixtureDir = dir })
	require.NoError(t, e.tr.Poll(context.Background()))
	before, _ := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.162.0")

	// upstream replaced an asset (mutable releases): new size
	copyFixtureWith(t, dir, func(rels []map[string]any) []map[string]any {
		for _, r := range rels {
			if r["tag_name"] == "v0.162.0" {
				for _, a := range r["assets"].([]any) {
					am := a.(map[string]any)
					if am["name"] == "otelcol-contrib_0.162.0_linux_amd64.tar.gz" {
						am["size"] = 1234.0
					}
				}
			}
		}
		return rels
	})
	require.NoError(t, e.tr.Poll(context.Background()))
	after, _ := e.st.Releases().Get(context.Background(), domain.DistributionContrib, "0.162.0")
	var size int64
	for _, a := range after.Assets {
		if a.Name == "otelcol-contrib_0.162.0_linux_amd64.tar.gz" {
			size = a.Size
		}
	}
	assert.Equal(t, int64(1234), size)
	assert.Equal(t, before.BreakingChanges, after.BreakingChanges, "extracted changes are kept")
	assert.Equal(t, before.DetectedAt, after.DetectedAt)
}

func TestCompare(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.tr.Poll(context.Background()))
	ctx := context.Background()

	cmp, err := e.tr.Compare(ctx, domain.DistributionContrib, "0.159.0", "0.162.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"0.160.0", "0.161.0", "0.162.0"}, cmp.Versions, "(from, to] in ascending order")
	assert.Equal(t, "0.159.0", cmp.From)
	assert.Equal(t, "0.162.0", cmp.To)
	last := ""
	for _, b := range cmp.BreakingChanges {
		assert.GreaterOrEqual(t, b.Version, last, "grouped by ascending version")
		last = b.Version
		assert.NotEqual(t, "0.159.0", b.Version)
	}
	var wave bool
	for _, b := range cmp.BreakingChanges {
		if strings.Contains(b.Text, "wavefront receiver") && b.Version == "0.162.0" {
			wave = true
		}
	}
	assert.True(t, wave)
	assert.NotNil(t, cmp.Deprecations)

	// "v" prefixes are accepted; equal/descending ranges are empty, not errors
	cmp, err = e.tr.Compare(ctx, domain.DistributionContrib, "v0.161.0", "v0.162.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"0.162.0"}, cmp.Versions)
	cmp, err = e.tr.Compare(ctx, domain.DistributionContrib, "0.162.0", "0.162.0")
	require.NoError(t, err)
	assert.Empty(t, cmp.Versions)
	assert.NotNil(t, cmp.BreakingChanges)
	cmp, err = e.tr.Compare(ctx, domain.DistributionContrib, "0.162.0", "0.150.0")
	require.NoError(t, err)
	assert.Empty(t, cmp.Versions)

	// from older than the first import: the changelog at tag v<to> fills the gap (sections down to 0.157.0 exist there)
	cmp, err = e.tr.Compare(ctx, domain.DistributionContrib, "0.150.0", "0.162.0")
	require.NoError(t, err)
	assert.Contains(t, cmp.Versions, "0.157.0", "known only from the changelog")
	assert.Contains(t, cmp.Versions, "0.158.0")
	assert.Equal(t, "0.157.0", cmp.Versions[0])
	has157 := false
	for _, b := range cmp.BreakingChanges {
		if b.Version == "0.157.0" {
			has157 = true
		}
	}
	assert.True(t, has157, "breaking changes of 0.157.0 come from the changelog")

	// supervisor
	cmp, err = e.tr.Compare(ctx, domain.DistributionSupervisor, "0.159.0", "0.162.0")
	require.NoError(t, err)
	for _, b := range cmp.BreakingChanges {
		assert.Equal(t, "cmd/opampsupervisor", b.Component)
	}

	// validation
	for _, c := range [][3]string{{domain.DistributionContrib, "x", "0.162.0"}, {domain.DistributionContrib, "0.1.0", "y"}, {"otelcol", "0.1.0", "0.2.0"}, {domain.DistributionContrib, "0.162", "0.163.0"}} {
		_, err := e.tr.Compare(ctx, c[0], c[1], c[2])
		assert.ErrorIs(t, err, domain.ErrInvalidArgument, "%v", c)
	}
}

func TestCompareWithEmptyStoreUsesTheChangelogOnly(t *testing.T) {
	e := newEnv(t)
	cmp, err := e.tr.Compare(context.Background(), domain.DistributionContrib, "0.157.0", "0.162.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"0.158.0", "0.159.0", "0.160.0", "0.161.0", "0.162.0"}, cmp.Versions)
	assert.NotEmpty(t, cmp.BreakingChanges)
}

func TestRefreshPublishesAndRunPolls(t *testing.T) {
	e := newEnv(t)
	events, cancel := e.bus.Subscribe(eventbus.ReleaseRefreshRequested)
	require.NoError(t, e.tr.Refresh(context.Background()))
	require.NoError(t, e.tr.Refresh(context.Background()), "always publishes")
	assert.Len(t, events, 2)
	cancel()

	e.tr.cfg.PollInterval = time.Hour
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.tr.Run(ctx) }()
	require.Eventually(t, func() bool { return len(e.releases(t, domain.DistributionContrib)) == 5 }, 5*time.Second, 10*time.Millisecond, "initial poll")

	// remove a release from the store; a refresh request makes the leader poll again
	require.NoError(t, e.tr.Refresh(ctx))
	require.Eventually(t, func() bool {
		return metricValue(t, e.reg, "myriarch_release_polls_total", map[string]string{"result": "ok"}) >= 2
	}, 5*time.Second, 10*time.Millisecond, "refresh triggers an immediate poll")
	stop()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}

	// disabled tracker blocks until cancel
	e2 := newEnv(t, func(c *config.ReleasesConfig) { c.Enabled = false })
	ctx2, stop2 := context.WithCancel(context.Background())
	d2 := make(chan error, 1)
	go func() { d2 <- e2.tr.Run(ctx2) }()
	select {
	case <-d2:
		t.Fatal("disabled tracker returned early")
	case <-time.After(50 * time.Millisecond):
	}
	stop2()
	assert.NoError(t, <-d2)
}

func TestNewTrackerValidation(t *testing.T) {
	d := Deps{Store: memstore.New()}
	_, err := NewTracker(config.ReleasesConfig{MinVersion: "nope"}, d)
	assert.Error(t, err)
	_, err = NewTracker(config.ReleasesConfig{Distributions: []string{"otelcol"}}, d)
	assert.Error(t, err)
	_, err = NewTracker(config.ReleasesConfig{ReleasesRepo: "no-slash"}, d)
	assert.Error(t, err)
	tr, err := NewTracker(config.ReleasesConfig{}, d)
	require.NoError(t, err, "defaults fill in")
	assert.Equal(t, "https://api.github.com", tr.cfg.GitHubAPIURL)
	assert.Equal(t, "open-telemetry/opentelemetry-collector-releases", tr.cfg.ReleasesRepo)
}

// ----------------------------------------------------------------- helpers ----

func copyFixtureWith(t *testing.T, dst string, mut func([]map[string]any) []map[string]any) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dst, "raw")); err != nil {
		require.NoError(t, os.CopyFS(filepath.Join(dst, "raw"), os.DirFS(filepath.Join(fixtures, "raw"))))
	}
	raw, err := os.ReadFile(filepath.Join(fixtures, "github/repos/open-telemetry/opentelemetry-collector-releases/releases.json"))
	require.NoError(t, err)
	var rels []map[string]any
	require.NoError(t, json.Unmarshal(raw, &rels))
	rels = mut(rels)
	out, err := json.Marshal(rels)
	require.NoError(t, err)
	p := filepath.Join(dst, "github/repos/open-telemetry/opentelemetry-collector-releases")
	require.NoError(t, os.MkdirAll(p, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p, "releases.json"), out, 0o644))
}

func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			ok := true
			for _, l := range m.GetLabel() {
				if want, has := labels[l.GetName()]; has && want != l.GetValue() {
					ok = false
				}
			}
			if ok {
				if m.GetCounter() != nil {
					return m.GetCounter().GetValue()
				}
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}

// OUT-2: redirects of the release tracker are limited (https, GitHub or the configured Enterprise host).
func TestRedirectPolicy(t *testing.T) {
	tr, err := NewTracker(config.ReleasesConfig{Enabled: true, GitHubAPIURL: "https://ghe.example.com/api/v3", RawBaseURL: "https://ghe.example.com/raw", FixtureDir: t.TempDir()}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	check := tr.client.CheckRedirect
	if check == nil {
		t.Fatal("redirects are not limited")
	}
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	for _, ok := range []string{"https://api.github.com/repos/x", "https://raw.githubusercontent.com/x", "https://ghe.example.com/other", "https://objects.githubusercontent.com/x"} {
		if err := check(req(ok), nil); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://api.github.com/x", "https://evil.example.net/x", "https://169.254.169.254/x", "https://api.github.com.evil.net/x"} {
		if err := check(req(bad), nil); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	if err := check(req("https://api.github.com/x"), make([]*http.Request, 5)); err == nil {
		t.Error("hop limit")
	}
}
