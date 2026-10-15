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
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/install"
)

// TestInstallPlanThroughTheRealServer runs a host through the installation from the server against a real server
// process (run) with PostgreSQL: the wiring of the authentication, the planner, the artifact cache and the routes,
// and the store queries behind them. The upstream of the artifacts is a local server.
func TestInstallPlanThroughTheRealServer(t *testing.T) {
	archive := bytes.Repeat([]byte("installer-test-bytes-"), 200)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") || strings.Contains(r.URL.Path, "/opampsupervisor_") {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	cfg, addr := devConfig(t)
	cfg.Artifacts.SourceBaseURL = upstream.URL
	cfg.Artifacts.Verify.RequireChecksum, cfg.Artifacts.Verify.RequireSignature = false, false
	startRun(t, cfg, addr)
	base := "http://" + addr

	// the releases that the tracker would have recorded
	db, _ := openForReadiness(t, cfg)
	ctx := context.Background()
	for _, r := range []struct{ dist, version string }{
		{domain.DistributionContrib, "0.162.0"}, {domain.DistributionSupervisor, "0.162.0"}, {domain.DistributionContrib, "0.161.0"},
	} {
		if _, err := db.Releases().Upsert(ctx, domain.Release{Distribution: r.dist, Version: r.version, Tag: "v" + r.version}); err != nil {
			t.Fatal(err)
		}
	}
	seed := cfg.Dev.SeedEnrollmentToken
	tok, err := db.Identity().GetEnrollmentTokenByHash(ctx, domain.SHA256([]byte(seed)))
	if err != nil {
		t.Fatalf("the dev server did not create its enrollment token: %v", err)
	}

	post := func(token string, body any) (int, http.Header, []byte) {
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

	// With the token the plan comes, after the first request has started the downloads. The GitOps source is read in
	// the background, so "no fleet" may precede it.
	var plan map[string]string
	deadline := time.Now().Add(90 * time.Second)
	for plan == nil {
		code, hdr, body := post(seed, host)
		switch code {
		case http.StatusOK:
			if got := hdr.Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Fatalf("Content-Type %q", got)
			}
			if plan, err = install.ParseText(string(body)); err != nil {
				t.Fatalf("the plan is not valid: %v\n%s", err, body)
			}
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
	if plan[install.KeyFleet] != "datacenter" || plan[install.KeyCollectorVersion] != "0.162.0" || plan[install.KeySupervisorVersion] != "0.162.0" ||
		plan[install.KeyVersionSource] != "git" || plan[install.KeyUpdaterInstall] != "true" {
		t.Errorf("plan: %v", plan)
	}
	if want := "ws://" + addr + cfg.OpAMP.Path; plan[install.KeyOpAMPEndpoint] != want {
		t.Errorf("endpoint %q, want %q", plan[install.KeyOpAMPEndpoint], want)
	}

	// The grants download the artifacts and their checksums from the same server.
	get := func(rawURL, grant string) (int, []byte) {
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
	for _, kind := range []string{"collector", "supervisor"} {
		u, grant := plan[kind+".url"], plan[kind+".grant"]
		if code, body := get(u, grant); code != http.StatusOK || !bytes.Equal(body, archive) {
			t.Fatalf("%s: %d (%d bytes)", kind, code, len(body))
		}
		if code, body := get(u+".sha256", grant); code != http.StatusOK || !strings.HasSuffix(string(body), "  "+u[strings.LastIndex(u, "/")+1:]+"\n") {
			t.Fatalf("%s checksum: %d %q", kind, code, body)
		}
	}
	if code, _ := get(plan[install.KeyCollectorURL], plan[install.KeySupervisorGrant]); code != http.StatusUnauthorized {
		t.Errorf("the grant of the supervisor downloaded the collector: %d", code)
	}

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

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
