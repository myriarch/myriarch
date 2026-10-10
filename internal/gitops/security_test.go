// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

const minimalBlueprint = hdr + "kind: Blueprint\nmetadata:\n  name: base\nspec:\n  config:\n    receivers: {}\n"

// GIT-5: symbolic links and special files are never followed.
func TestParseReportsSymlinksAndSpecialFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"blueprints/base.yaml":     &fstest.MapFile{Data: []byte(minimalBlueprint)},
		"blueprints/escape.yaml":   &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("../../../etc/passwd")},
		"blueprints/pipe.yaml":     &fstest.MapFile{Mode: fs.ModeNamedPipe},
		"samples/agents/link.yaml": &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("/etc/passwd")},
	}
	pr := ParseDetailed(fsys, ".", "c")
	var bad []string
	for _, is := range pr.Issues {
		if is.Code == "not_regular_file" {
			bad = append(bad, is.File)
		}
	}
	assert.ElementsMatch(t, []string{"blueprints/escape.yaml", "blueprints/pipe.yaml"}, bad)
	assert.Len(t, pr.Snapshot.Blueprints, 1, "regular files are still parsed")

	_, issues := ParseSamples(fsys, ".")
	require.Len(t, issues, 1)
	assert.Equal(t, "not_regular_file", issues[0].Code)
	assert.Equal(t, "samples/agents/link.yaml", issues[0].File)
}

// The checked-out directory is opened as a root: a symbolic link to a file outside the repository is neither read
// nor does it make the revision valid.
func TestSyncRefusesSymlinkToFileOutsideTheRepository(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte(hdr+"kind: Blueprint\nmetadata:\n  name: smuggled\nspec:\n  config:\n    receivers: {}\n"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.dir, "blueprints", "smuggled.yaml")))
	s := f.build(t)
	require.NoError(t, s.SyncNow(context.Background(), "poll"))

	st := f.status(t)
	assert.Equal(t, "error", st.State, "a repository with a symbolic link is invalid")
	found := false
	for _, is := range st.Issues {
		if is.Code == "not_regular_file" && strings.HasSuffix(is.File, "smuggled.yaml") {
			found = true
		}
	}
	assert.True(t, found, "%+v", st.Issues)
	snap, _ := f.st.GitOps().GetSnapshot(context.Background())
	for _, b := range snap.Blueprints {
		assert.NotEqual(t, "smuggled", b.Name, "the outside file must not be parsed")
	}
	// the root based file system also refuses direct reads through the link
	rootFS, err := os.OpenRoot(f.dir)
	require.NoError(t, err)
	defer rootFS.Close()
	_, err = fs.ReadFile(rootFS.FS(), "blueprints/smuggled.yaml")
	assert.Error(t, err, "os.Root must not follow a link out of the repository")
}

// GIT-2: sizes are checked before reading, counts and totals are bounded.
func TestParseLimits(t *testing.T) {
	big := hdr + "kind: Blueprint\nmetadata:\n  name: big\nspec:\n  config:\n    receivers:\n      x: " + strings.Repeat("a", 2048) + "\n"
	fsys := fstest.MapFS{
		"blueprints/base.yaml": &fstest.MapFile{Data: []byte(minimalBlueprint)},
		"blueprints/big.yaml":  &fstest.MapFile{Data: []byte(big)},
	}
	pr := ParseDetailedLimited(fsys, ".", "c", Limits{MaxFileBytes: 1024})
	assert.Contains(t, codes(pr.Issues), "file_too_large")
	assert.Len(t, pr.Snapshot.Blueprints, 1, "the large file is skipped")

	many := fstest.MapFS{}
	for i := 0; i < 10; i++ {
		many[fmt.Sprintf("blueprints/b%d.yaml", i)] = &fstest.MapFile{Data: []byte(strings.Replace(minimalBlueprint, "name: base", fmt.Sprintf("name: b%d", i), 1))}
	}
	pr = ParseDetailedLimited(many, ".", "c", Limits{MaxFiles: 4})
	assert.Equal(t, 1, countCode(pr.Issues, "too_many_files"), "reported once")
	assert.Len(t, pr.Snapshot.Blueprints, 4)

	pr = ParseDetailedLimited(many, ".", "c", Limits{MaxTotalBytes: 3 * int64(len(minimalBlueprint))})
	assert.Contains(t, codes(pr.Issues), "repository_too_large")

	// a file that grows after the size check is still read with a hard cap
	b, err := readLimited(fstest.MapFS{"x.yaml": &fstest.MapFile{Data: bytes.Repeat([]byte("a"), 100)}}, "x.yaml", 10)
	assert.Error(t, err)
	assert.Nil(t, b)

	// time budget
	pr = ParseDetailedLimited(many, ".", "c", Limits{Deadline: time.Now().Add(-time.Second)})
	assert.Contains(t, codes(pr.Issues), "time_budget_exceeded")

	// samples
	samples := fstest.MapFS{"samples/agents/big.yaml": &fstest.MapFile{Data: []byte(strings.Repeat("a", 4096))}}
	_, issues := ParseSamplesLimited(samples, ".", Limits{MaxFileBytes: 1024})
	assert.Contains(t, codes(issues), "file_too_large")
}

func TestStrictCheckBudgetIsReported(t *testing.T) {
	old := maxNodeVisit
	maxNodeVisit = 5
	defer func() { maxNodeVisit = old }()
	fsys := osDirFS(t, "../testdata/gitops")
	pr := ParseDetailed(fsys, ".", "c")
	assert.Contains(t, codes(pr.Issues), "repository_too_complex", "an exhausted budget must not silently disable the strict checks")
}

func countCode(is domain.Issues, code string) int {
	n := 0
	for _, i := range is {
		if i.Code == code {
			n++
		}
	}
	return n
}

// The sync cycle applies the configured limits.
func TestSyncAppliesConfiguredLimits(t *testing.T) {
	f := newFixture(t)
	f.cfg.MaxFiles = 3
	s := f.build(t)
	require.NoError(t, s.SyncNow(context.Background(), "poll"))
	st := f.status(t)
	assert.Equal(t, "error", st.State)
	found := false
	for _, is := range st.Issues {
		found = found || is.Code == "too_many_files"
	}
	assert.True(t, found)
}

// IN-1: delivery ids are processed once, the endpoint is rate limited and every call is audited.
func TestWebhookReplayRateLimitAndAudit(t *testing.T) {
	f := newFixture(t)
	s := f.build(t)
	events, cancel := f.bus.Subscribe(eventbus.GitOpsSyncRequested)
	defer cancel()
	count := func() int {
		n := 0
		for {
			select {
			case <-events:
				n++
			case <-time.After(150 * time.Millisecond):
				return n
			}
		}
	}
	send := func(delivery string) *httptest.ResponseRecorder {
		return webhookRequest(t, s, func(r *http.Request, b []byte) {
			r.Header.Set("X-Gitlab-Token", "s3cret")
			if delivery != "" {
				r.Header.Set("X-Gitlab-Event-UUID", delivery)
			}
		})
	}
	assert.Equal(t, http.StatusAccepted, send("d-1").Code)
	assert.Equal(t, 1, count())
	// the same delivery (provider retry or captured request) is acknowledged without a second sync
	w := send("d-1")
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "duplicate")
	assert.Equal(t, 0, count())
	assert.Equal(t, http.StatusAccepted, send("d-2").Code)
	assert.Equal(t, 1, count())

	// rejected calls are audited
	w = webhookRequest(t, s, func(r *http.Request, b []byte) { r.Header.Set("X-Gitlab-Token", "wrong") })
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	actions := map[string]int{}
	results := map[string]int{}
	for _, e := range f.st.AuditEvents() {
		if e.Action == "gitops.webhook" {
			actions[fmt.Sprint(e.Details["outcome"])]++
			results[e.Result]++
		}
	}
	assert.Equal(t, 2, actions["accepted"])
	assert.Equal(t, 1, actions["duplicate"])
	assert.Equal(t, 1, actions["rejected"])
	assert.Equal(t, 1, results["failure"])

	// burst limit per replica
	limited := 0
	for i := 0; i < 30; i++ {
		if send(fmt.Sprintf("flood-%d", i)).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	assert.Greater(t, limited, 10, "the webhook must be rate limited")
	last := send("flood-last")
	if last.Code == http.StatusTooManyRequests {
		assert.NotEmpty(t, last.Header().Get("Retry-After"))
	}
}
