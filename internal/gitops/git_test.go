// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/store"
)

func requireGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	return p
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice Admin", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Alice Admin", "GIT_COMMITTER_EMAIL=alice@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// workRepo creates a work tree with the example repo below subdir ("" = root).
func workRepo(t *testing.T, subdir string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	runGit(t, dir, "init", "-q", "-b", "main")
	copyDir(t, "../testdata/gitops", filepath.Join(dir, subdir))
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "initial import\n\nlong body")
	return dir
}

func gitFixture(t *testing.T, repoURL, subdir string) *fixture {
	f := newFixture(t)
	f.cfg.RepoURL = repoURL
	f.cfg.Path = subdir
	f.cfg.CloneDir = filepath.Join(t.TempDir(), "clone")
	return f
}

func TestGitFileSourceCloneFetchAndForcePush(t *testing.T) {
	requireGit(t)
	work := workRepo(t, "")
	f := gitFixture(t, fileURL(work), "")
	s := f.build(t)
	ctx := context.Background()

	require.NoError(t, s.SyncNow(ctx, "startup"))
	c1 := runGit(t, work, "rev-parse", "HEAD")
	st := f.status(t)
	assert.Equal(t, "ok", st.State)
	assert.Equal(t, c1, st.AppliedCommit.SHA)
	assert.Equal(t, "Alice Admin", st.AppliedCommit.Author)
	assert.Equal(t, "initial import", st.AppliedCommit.Message, "first line only")
	assert.Equal(t, fileURL(work), st.Source)
	assert.Equal(t, "main", st.Branch)
	snap, _ := f.st.GitOps().GetSnapshot(ctx)
	assert.Equal(t, c1, snap.Commit)
	assert.Equal(t, c1, snap.Fleets[0].Source.Commit)

	// new commit -> fetch + hard reset -> applied
	p := filepath.Join(work, "fleets", "datacenter.yaml")
	b, _ := os.ReadFile(p)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), "priority: 50", "priority: 55", 1)), 0o644))
	runGit(t, work, "commit", "-q", "-am", "raise datacenter priority")
	c2 := runGit(t, work, "rev-parse", "HEAD")
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, c2, f.status(t).AppliedCommit.SHA)
	assert.Equal(t, int32(2), f.applied.Load())

	// unchanged
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, int32(2), f.applied.Load())

	// history rewrite (force push): the clone must follow
	runGit(t, work, "reset", "-q", "--hard", c1)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), "priority: 50", "priority: 56", 1)), 0o644))
	runGit(t, work, "commit", "-q", "-am", "different change")
	c3 := runGit(t, work, "rev-parse", "HEAD")
	require.NotEqual(t, c2, c3)
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, c3, f.status(t).AppliedCommit.SHA)
	snap, _ = f.st.GitOps().GetSnapshot(ctx)
	for _, fl := range snap.Fleets {
		if fl.Name == "datacenter" {
			assert.Equal(t, 56, fl.Priority)
		}
	}

	// local modifications in the clone never leak into the snapshot (hard reset)
	require.NoError(t, os.WriteFile(filepath.Join(f.cfg.CloneDir, "fleets", "datacenter.yaml"), []byte("garbage: ["), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.cfg.CloneDir, "fleets", "extra.yaml"), []byte("garbage: ["), 0o644))
	runGit(t, work, "commit", "-q", "--allow-empty", "-m", "empty")
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, "ok", f.status(t).State, "clone is reset to origin/main and cleaned")
}

func TestGitSourceSubdirectoryAndWrongBranch(t *testing.T) {
	requireGit(t)
	work := workRepo(t, "gitops")
	f := gitFixture(t, fileURL(work), "gitops")
	s := f.build(t)
	require.NoError(t, s.SyncNow(context.Background(), "startup"))
	st := f.status(t)
	assert.Equal(t, "ok", st.State)
	assert.Equal(t, "gitops", st.Path)
	snap, _ := f.st.GitOps().GetSnapshot(context.Background())
	assert.Equal(t, "gitops/fleets/datacenter.yaml", snap.Fleets[0].Source.Path, "paths are repository relative")

	f2 := gitFixture(t, fileURL(work), "gitops")
	f2.cfg.Branch = "nonexistent"
	s2 := f2.build(t)
	require.Error(t, s2.SyncNow(context.Background(), "startup"))
	assert.Equal(t, "error", f2.status(t).State)
}

func TestGitHTTPSWithTokenAndCustomCA(t *testing.T) {
	requireGit(t)
	work := workRepo(t, "")
	bareRoot := t.TempDir()
	runGit(t, bareRoot, "clone", "-q", "--bare", work, "repo.git")
	execPath := runGit(t, bareRoot, "--exec-path")
	backend := filepath.Join(execPath, "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not available")
	}

	var gotUser, gotPass string
	cgiHandler := &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + bareRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || p != "tok-123" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		gotUser, gotPass = u, p
		cgiHandler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("tok-123\n"), 0o600))

	url := srv.URL + "/repo.git"
	f := gitFixture(t, url, "")
	f.cfg.Auth = config.GitAuthConfig{Username: "ci-bot", TokenFile: tokenFile, CAFile: caFile}
	s := f.build(t)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, "startup"))
	assert.Equal(t, "ok", f.status(t).State)
	assert.Equal(t, "ci-bot", gotUser)
	assert.Equal(t, "tok-123", gotPass)
	assert.Equal(t, url, f.status(t).Source)

	// new commit pushed to the bare repo is fetched with the same credentials
	p := filepath.Join(work, "fleets", "datacenter.yaml")
	b, _ := os.ReadFile(p)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), "priority: 50", "priority: 57", 1)), 0o644))
	runGit(t, work, "commit", "-q", "-am", "bump")
	runGit(t, work, "push", "-q", bareRoot+"/repo.git", "main")
	require.NoError(t, s.SyncNow(ctx, "poll"))
	assert.Equal(t, int32(2), f.applied.Load())

	// wrong token -> error status, old snapshot stays
	require.NoError(t, os.WriteFile(tokenFile, []byte("wrong\n"), 0o600))
	runGit(t, work, "commit", "-q", "--allow-empty", "-m", "noop")
	runGit(t, work, "push", "-q", bareRoot+"/repo.git", "main")
	err := s.SyncNow(ctx, "poll")
	require.Error(t, err)
	assert.Equal(t, "error", f.status(t).State)
	assert.NotContains(t, err.Error(), "wrong", "token must not leak into errors")
	snap, _ := f.st.GitOps().GetSnapshot(ctx)
	assert.NotEmpty(t, snap.Commit)

	// a client without the custom CA must fail the TLS handshake
	f3 := gitFixture(t, url, "")
	f3.cfg.Auth = config.GitAuthConfig{TokenFile: tokenFile}
	require.NoError(t, os.WriteFile(tokenFile, []byte("tok-123\n"), 0o600))
	s3 := f3.build(t)
	require.Error(t, s3.SyncNow(ctx, "startup"))
}

// FX-5: Git over HTTPS in the production wiring (HTTP client of the outbound
// policy): the internal CA comes from outbound.caFile (trust store of the
// client), from gitops.auth.caFile (extends it) or from both. A client without
// any of them refuses the server.
func TestGitHTTPSThroughThePolicyClientTrustsTheConfiguredCAs(t *testing.T) {
	requireGit(t)
	t.Cleanup(func() { // the protocols are process wide: restore go-git's defaults
		installMu.Lock()
		defer installMu.Unlock()
		client.InstallProtocol("https", githttp.DefaultClient)
		client.InstallProtocol("http", githttp.DefaultClient)
	})
	work := workRepo(t, "")
	bareRoot := t.TempDir()
	runGit(t, bareRoot, "clone", "-q", "--bare", work, "repo.git")
	backend := filepath.Join(runGit(t, bareRoot, "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not available")
	}
	cgiHandler := &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + bareRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"},
	}
	srv := httptest.NewTLSServer(cgiHandler)
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	url := srv.URL + "/repo.git"

	sync := func(t *testing.T, outboundCA, gitCA string) error {
		policy, err := netguard.New([]string{"127.0.0.1"})
		require.NoError(t, err)
		pool, err := netguard.RootPool(outboundCA)
		require.NoError(t, err)
		policy.SetRootCAs(pool)
		f := gitFixture(t, url, "")
		f.policy, f.httpClient = policy, policy.Client(time.Minute)
		f.cfg.Auth = config.GitAuthConfig{CAFile: gitCA}
		s, err := NewSyncer(f.cfg, Deps{
			Store: f.st, Bus: f.bus, Renderer: blueprint.NewRenderer(blueprint.Options{}), Audit: auditAdapter{f.st},
			Notifier: f.notifier, Registerer: prometheus.NewRegistry(), HTTPClient: f.httpClient, Policy: policy,
			OnApplied: func(context.Context, *domain.GitOpsSnapshot, store.ObjectChanges) error { return nil },
		})
		if err != nil {
			return err
		}
		return s.SyncNow(context.Background(), "startup")
	}

	t.Run("outbound.caFile", func(t *testing.T) { require.NoError(t, sync(t, caFile, "")) })
	t.Run("gitops.auth.caFile", func(t *testing.T) { require.NoError(t, sync(t, "", caFile)) })
	t.Run("both", func(t *testing.T) { require.NoError(t, sync(t, caFile, caFile)) })
	t.Run("neither", func(t *testing.T) {
		err := sync(t, "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "certificate")
	})
	t.Run("an unusable gitops.auth.caFile stops the start", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.pem")
		require.NoError(t, os.WriteFile(bad, []byte("not pem"), 0o600))
		err := sync(t, caFile, bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gitops.auth.caFile")
		err = sync(t, caFile, filepath.Join(t.TempDir(), "missing.pem"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gitops.auth.caFile")
	})
}

func TestCheckConfig(t *testing.T) {
	ok := func(c config.GitOpsConfig, skip bool) { assert.NoError(t, CheckConfig(c, skip)) }
	bad := func(c config.GitOpsConfig, skip bool, want string) {
		err := CheckConfig(c, skip)
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), want)
		}
	}
	ok(config.GitOpsConfig{}, false) // disabled
	ok(config.GitOpsConfig{Enabled: true, RepoURL: "dir:examples/quickstart"}, false)
	ok(config.GitOpsConfig{Enabled: true, RepoURL: "https://gitlab.example.com/x.git"}, false)
	bad(config.GitOpsConfig{Enabled: true}, false, "gitops.repoUrl: is required")
	bad(config.GitOpsConfig{Enabled: true, RepoURL: "dir:"}, false, "needs a path")
	bad(config.GitOpsConfig{Enabled: true, RepoURL: "ftp://x/y"}, false, "unsupported repository URL")
	bad(config.GitOpsConfig{Enabled: true, RepoURL: "ssh://git@x/y.git"}, false, "sshKeyFile")
	bad(config.GitOpsConfig{Enabled: true, RepoURL: "git@x:y.git", Auth: config.GitAuthConfig{SSHKeyFile: "/k"}}, true, "knownHostsFile")
	ok(config.GitOpsConfig{Enabled: true, RepoURL: "git@x:y.git", Auth: config.GitAuthConfig{SSHKeyFile: "/k", KnownHostsFile: "/h"}}, true)
	// files are only read without skipFiles
	missing := filepath.Join(t.TempDir(), "missing")
	https := config.GitOpsConfig{Enabled: true, RepoURL: "https://gitlab.example.com/x.git"}
	https.Auth.TokenFile = missing
	ok(https, true)
	bad(https, false, "gitops.auth.tokenFile")
	https.Auth.TokenFile = ""
	https.Auth.CAFile = missing
	ok(https, true)
	bad(https, false, "gitops.auth.caFile")
	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, []byte("\n"), 0o600))
	https.Auth.CAFile, https.Auth.TokenFile = "", empty
	bad(https, false, "empty")
}
