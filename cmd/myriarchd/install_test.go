// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/store/postgres"
)

// testGrantTTL is install.grantTtl of the real-server test: long enough for the few requests that use a fresh grant,
// short enough to wait for.
const testGrantTTL = 5 * time.Second

// postInstallPlan asks the server at base for a plan, with an enrollment token if token is not empty.
func postInstallPlan(t *testing.T, base, token string, body any) (int, http.Header, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/install/plan", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// installTestArchive is what the upstream of the install tests serves for every artifact.
var installTestArchive = bytes.Repeat([]byte("installer-test-bytes-"), 200)

// newInstallUpstream starts a local server that serves installTestArchive for the collector and the supervisor.
func newInstallUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") || strings.Contains(r.URL.Path, "/opampsupervisor_") {
			_, _ = w.Write(installTestArchive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

// seedInstallReleases records the releases that the tracker would have recorded.
func seedInstallReleases(t *testing.T, cfg config.Config) *postgres.DB {
	t.Helper()
	db, _ := openForReadiness(t, cfg)
	for _, r := range []struct{ dist, version string }{
		{domain.DistributionContrib, "0.162.0"}, {domain.DistributionSupervisor, "0.162.0"}, {domain.DistributionContrib, "0.161.0"},
	} {
		if _, err := db.Releases().Upsert(context.Background(), domain.Release{Distribution: r.dist, Version: r.version, Tag: "v" + r.version}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// requireInstallPlan asks the server at base for the plan of host until it comes, after the first request has started
// the downloads. The GitOps source is read in the background, so "no fleet" may precede it.
func requireInstallPlan(t *testing.T, base, token string, host map[string]any) map[string]string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		code, hdr, body := postInstallPlan(t, base, token, host)
		switch code {
		case http.StatusOK:
			if got := hdr.Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Fatalf("Content-Type %q", got)
			}
			plan, err := install.ParseText(string(body))
			if err != nil {
				t.Fatalf("the plan is not valid: %v\n%s", err, body)
			}
			return plan
		case http.StatusServiceUnavailable:
			if secs, _ := strconv.Atoi(hdr.Get("Retry-After")); secs < 1 || !strings.Contains(string(body), `"preparing"`) {
				t.Fatalf("preparing without Retry-After: %v %s", hdr, body)
			}
			time.Sleep(200 * time.Millisecond)
		case http.StatusConflict:
			if !strings.Contains(string(body), `"no_fleet"`) {
				t.Fatalf("409: %s", body)
			}
			time.Sleep(200 * time.Millisecond)
		default:
			t.Fatalf("plan: %d %s", code, body)
		}
		if time.Now().After(deadline) {
			t.Fatal("no plan within 90 seconds")
		}
	}
}

// getWithGrant downloads rawURL with the grant as the bearer token.
func getWithGrant(t *testing.T, rawURL, grant string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestInstallPlanThroughTheRealServer runs a host through the installation from the server against a real server
// process (run) with PostgreSQL: the wiring of the authentication, the planner, the artifact cache and the routes,
// and the store queries behind them. The upstream of the artifacts is a local server.
func TestInstallPlanThroughTheRealServer(t *testing.T) {
	archive := installTestArchive
	upstream := newInstallUpstream(t)

	cfg, addr := devConfig(t)
	cfg.Artifacts.SourceBaseURL = upstream.URL
	cfg.Artifacts.Verify.RequireChecksum, cfg.Artifacts.Verify.RequireSignature = false, false
	cfg.Install.GrantTTL = testGrantTTL
	startRun(t, cfg, addr)
	base := "http://" + addr

	db := seedInstallReleases(t, cfg)
	ctx := context.Background()
	seed := cfg.Dev.SeedEnrollmentToken
	tok, err := db.Identity().GetEnrollmentTokenByHash(ctx, domain.SHA256([]byte(seed)))
	if err != nil {
		t.Fatalf("the dev server did not create its enrollment token: %v", err)
	}

	post := func(token string, body any) (int, http.Header, []byte) {
		t.Helper()
		return postInstallPlan(t, base, token, body)
	}
	host := map[string]any{"os": "linux", "arch": "amd64", "attributes": map[string]string{"site.type": "datacenter", "host.name": "dc-01.example.test"}}

	// Dev mode lets agents connect without a token, the installation still needs one, and every failure looks alike.
	var details []string
	for name, token := range map[string]string{"none": "", "unknown": "myre_" + strings.Repeat("z", 32), "malformed": "nonsense"} {
		code, hdr, body := post(token, host)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if hdr.Get("WWW-Authenticate") == "" || hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", name, hdr)
		}
		var p api.Problem
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		details = append(details, p.Detail)
	}
	if details[0] != details[1] || details[1] != details[2] {
		t.Errorf("the details of the 401 differ: %q", details)
	}

	requirePlan := func() map[string]string { return requireInstallPlan(t, base, seed, host) }
	plan := requirePlan()
	planned := time.Now() // after the answer: the grants of this plan are at least this old when the time below has passed
	if plan[install.KeyFleet] != "datacenter" || plan[install.KeyCollectorVersion] != "0.162.0" || plan[install.KeySupervisorVersion] != "0.162.0" ||
		plan[install.KeyVersionSource] != "git" || plan[install.KeyUpdaterInstall] != "true" {
		t.Errorf("plan: %v", plan)
	}
	if want := "ws://" + addr + cfg.OpAMP.Path; plan[install.KeyOpAMPEndpoint] != want {
		t.Errorf("endpoint %q, want %q", plan[install.KeyOpAMPEndpoint], want)
	}

	// The grants download the artifacts and their checksums from the same server.
	get := func(rawURL, grant string) (int, []byte) { t.Helper(); return getWithGrant(t, rawURL, grant) }
	downloadAll := func(plan map[string]string) {
		t.Helper()
		for _, kind := range []string{"collector", "supervisor"} {
			u, grant := plan[kind+".url"], plan[kind+".grant"]
			if code, body := get(u, grant); code != http.StatusOK || !bytes.Equal(body, archive) {
				t.Fatalf("%s: %d (%d bytes)", kind, code, len(body))
			}
			if code, body := get(u+".sha256", grant); code != http.StatusOK || !strings.HasSuffix(string(body), "  "+u[strings.LastIndex(u, "/")+1:]+"\n") {
				t.Fatalf("%s checksum: %d %q", kind, code, body)
			}
		}
	}
	downloadAll(plan)
	if code, _ := get(plan[install.KeyCollectorURL], plan[install.KeySupervisorGrant]); code != http.StatusUnauthorized {
		t.Errorf("the grant of the supervisor downloaded the collector: %d", code)
	}

	// A grant lives for install.grantTtl, no longer: the setting reaches the artifact cache. The wait is measured
	// from after the answer, so the grants are older than the lifetime, whatever the rounding of their time.
	time.Sleep(time.Until(planned.Add(testGrantTTL + 1500*time.Millisecond)))
	for _, kind := range []string{"collector", "supervisor"} {
		code, body := get(plan[kind+".url"], plan[kind+".grant"])
		if code != http.StatusUnauthorized || !strings.Contains(string(body), "expired") {
			t.Errorf("%s: a grant older than install.grantTtl: %d %.120s", kind, code, body) // not the whole archive
		}
	}
	// the host asks again and gets grants that work
	plan = requirePlan()
	downloadAll(plan)

	// Revoking the enrollment token stops its grants at once, on the next request.
	if err := db.Identity().RevokeEnrollmentToken(ctx, tok.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(plan[install.KeyCollectorURL], plan[install.KeyCollectorGrant]); code != http.StatusUnauthorized {
		t.Errorf("a grant of a revoked token downloaded the collector: %d", code)
	}

	// The counter is exported, and nothing secret is in the metrics.
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	metrics, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(metrics), `myriarch_install_plans_total{result="ok"}`) || !strings.Contains(string(metrics), `myriarch_install_plans_total{result="unauthorized"} 3`) {
		t.Errorf("metrics lack the plan counter:\n%s", grepLines(string(metrics), "myriarch_install"))
	}
	for _, secret := range []string{seed, plan[install.KeyCollectorGrant], plan[install.KeySupervisorGrant]} {
		if strings.Contains(string(metrics), secret) {
			t.Error("a credential is in the metrics")
		}
	}
}

// With install.enabled off, the routes of the installation answer 404 before they look at a token, even for the
// token that the development server knows, and the server does what it did before.
func TestInstallRoutesAreOffWhenNotEnabled(t *testing.T) {
	cfg, addr := devConfig(t)
	cfg.Install.Enabled = false
	startRun(t, cfg, addr)
	base := "http://" + addr

	host := map[string]any{"os": "linux", "arch": "amd64"}
	for name, token := range map[string]string{"the dev token": cfg.Dev.SeedEnrollmentToken, "no token": "", "an unknown token": "myre_" + strings.Repeat("z", 32)} {
		code, hdr, body := postInstallPlan(t, base, token, host)
		if code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if hdr.Get("WWW-Authenticate") != "" {
			t.Errorf("%s: the answer asks for a credential", name)
		}
		var p api.Problem
		if err := json.Unmarshal(body, &p); err != nil || p.Code != "not_found" {
			t.Errorf("%s: %s", name, body)
		}
	}

	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	metrics, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(metrics), `myriarch_install_plans_total{result="disabled"} 3`) {
		t.Errorf("metrics lack the plan counter:\n%s", grepLines(string(metrics), "myriarch_install"))
	}
}

// A grant is the credential of the feature that issues it. A server that restarts on the same database and the same
// artifact directory with install.enabled=false refuses the grants of the plan that the earlier server gave, although
// they have not expired and their enrollment token is still valid: the setting reaches the artifact cache through the
// start (run).
func TestInstallGrantIsRefusedAfterTheInstallationWasTurnedOff(t *testing.T) {
	upstream := newInstallUpstream(t)
	cfg, addr := devConfig(t)
	cfg.Artifacts.SourceBaseURL = upstream.URL
	cfg.Artifacts.Verify.RequireChecksum, cfg.Artifacts.Verify.RequireSignature = false, false
	on := startRun(t, cfg, addr)
	seedInstallReleases(t, cfg)

	host := map[string]any{"os": "linux", "arch": "amd64", "attributes": map[string]string{"site.type": "datacenter", "host.name": "dc-01.example.test"}}
	plan := requireInstallPlan(t, "http://"+addr, cfg.Dev.SeedEnrollmentToken, host)
	grant, rawURL := plan[install.KeyCollectorGrant], plan[install.KeyCollectorURL]
	if code, body := getWithGrant(t, rawURL, grant); code != http.StatusOK || !bytes.Equal(body, installTestArchive) {
		t.Fatalf("the grant does not download while the installation is on: %d (%d bytes)", code, len(body))
	}

	if err := on.stop(t); err != nil {
		t.Fatalf("stop: %v", err)
	}
	off, offAddr := devConfigDSN(t, cfg.Database.DSN)
	off.Artifacts = cfg.Artifacts
	off.Install.Enabled = false
	startRun(t, off, offAddr)

	offURL := strings.Replace(rawURL, "http://"+addr, "http://"+offAddr, 1)
	if offURL == rawURL {
		t.Fatalf("the URL %q is not on %s", rawURL, addr)
	}
	for _, suffix := range []string{"", ".sha256", ".sigstore.json"} {
		code, body := getWithGrant(t, offURL+suffix, grant)
		if code != http.StatusUnauthorized || !strings.Contains(string(body), "invalid install grant") {
			t.Errorf("%q: a grant on a server with the installation off: %d %.120s", suffix, code, body) // not the whole archive
		}
		if strings.Contains(string(body), grant) {
			t.Errorf("%q: the answer repeats the grant", suffix)
		}
	}
	if code, _, _ := postInstallPlan(t, "http://"+offAddr, cfg.Dev.SeedEnrollmentToken, host); code != http.StatusNotFound {
		t.Errorf("the plan route of a server with the installation off: %d", code)
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
