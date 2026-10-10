// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"": false, "localhost": true, "LOCALHOST": true, "127.0.0.1": true, "127.5.5.5": true, "::1": true, "[::1]": true,
		"0.0.0.0": false, "10.0.0.5": false, "192.168.1.1": false, "db.example.com": false, "/var/run/postgresql": true,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// AUTH-DEV-1: dev mode refuses everything that looks like a production setup unless confirmed.
func TestDevModeRefusesNonLocalSetups(t *testing.T) {
	local := devOptions{listen: "127.0.0.1:8080"}
	if r := devNonLocalReasons(config.DevDefaults(), local); len(r) != 0 {
		t.Fatalf("a local dev setup must be accepted: %v", r)
	}
	loopbackMetrics := config.DevDefaults()
	loopbackMetrics.Server.MetricsListenAddr = "127.0.0.1:9090"
	if r := devNonLocalReasons(loopbackMetrics, local); len(r) != 0 {
		t.Fatalf("a loopback metrics listener must be accepted: %v", r)
	}
	for _, o := range []devOptions{{listen: "localhost:9090"}, {listen: "[::1]:8080"}, {listen: "127.0.0.1:8080", dsn: "postgres://u:p@127.0.0.1:54329/myriarch?sslmode=disable"},
		{listen: "127.0.0.1:8080", dsn: "postgres://u:p@localhost/myriarch"}} {
		if r := devNonLocalReasons(config.DevDefaults(), o); len(r) != 0 {
			t.Errorf("%+v must be accepted: %v", o, r)
		}
	}
	cases := map[string]struct {
		mutate func(c *config.Config)
		opts   devOptions
		want   string
	}{
		"all interfaces":              {opts: devOptions{listen: ":8080"}, want: "not a loopback address"},
		"wildcard address":            {opts: devOptions{listen: "0.0.0.0:8080"}, want: "not a loopback address"},
		"public address":              {opts: devOptions{listen: "10.1.2.3:8080"}, want: "not a loopback address"},
		"invalid listen":              {opts: devOptions{listen: "8080"}, want: "not a loopback address"},
		"oidc":                        {mutate: func(c *config.Config) { c.Auth.OIDC.Enabled = true }, opts: local, want: "auth.oidc.enabled"},
		"tls":                         {mutate: func(c *config.Config) { c.Server.TLS.CertFile = "c.pem" }, opts: local, want: "server.tls.certFile"},
		"https public url":            {mutate: func(c *config.Config) { c.Server.PublicURL = "https://fleet.example.com" }, opts: local, want: "https"},
		"metrics on all interfaces":   {mutate: func(c *config.Config) { c.Server.MetricsListenAddr = ":9090" }, opts: local, want: "server.metricsListenAddr"},
		"metrics on a public address": {mutate: func(c *config.Config) { c.Server.MetricsListenAddr = "10.1.2.3:9090" }, opts: local, want: "server.metricsListenAddr"},
		"remote public url":           {mutate: func(c *config.Config) { c.Server.PublicURL = "http://fleet.example.com" }, opts: local, want: "not a local address"},
		"remote database":             {opts: devOptions{listen: "127.0.0.1:8080", dsn: "postgres://u:p@db.prod.example.com:5432/myriarch"}, want: "non-local host"},
		"remote fallback":             {opts: devOptions{listen: "127.0.0.1:8080", dsn: "postgres://u:p@localhost,db.example.com/myriarch"}, want: "non-local host"},
		"private database":            {opts: devOptions{listen: "127.0.0.1:8080", dsn: "postgres://u:p@10.0.0.7/myriarch"}, want: "non-local host"},
	}
	for name, tc := range cases {
		cfg := config.DevDefaults()
		if tc.mutate != nil {
			tc.mutate(&cfg)
		}
		reasons := devNonLocalReasons(cfg, tc.opts)
		if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, ";"), tc.want) {
			t.Errorf("%s: reasons %v, want one containing %q", name, reasons, tc.want)
		}
	}
	// runDev refuses to start (before it touches any database) and names the override
	err := runDev(context.Background(), devOptions{listen: ":8080", dsn: "postgres://u:p@db.example.com/myriarch"})
	if err == nil || !strings.Contains(err.Error(), "refusing to start dev mode") || !strings.Contains(err.Error(), "--allow-nonlocal") {
		t.Fatalf("runDev: %v", err)
	}
}

func TestDevBannerAndDSNMasking(t *testing.T) {
	if got := maskDSN("postgres://myriarch:dev-password@127.0.0.1:54329/myriarch?sslmode=disable"); strings.Contains(got, "dev-password") || !strings.Contains(got, "xxxxx") {
		t.Fatalf("masked DSN: %s", got)
	}
	if got := maskDSN("postgres://127.0.0.1/db"); got != "postgres://127.0.0.1/db" {
		t.Fatalf("DSN without password: %s", got)
	}
	// the banner is logged at warn level and repeated until the context ends
	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	h := &captureHandler{lines: &lines}
	logDevBanner(ctx, newCaptureLogger(h), "logins", "admin/admin")
	cancel()
	time.Sleep(20 * time.Millisecond)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "WARN") || !strings.Contains(lines[0], "DEV MODE") {
		t.Fatalf("banner: %v", lines)
	}
}

// AUTH-DEV-1: `myriarchd serve` revokes the well-known dev enrollment token it finds in the database.
func TestServeRevokesTheWellKnownDevEnrollmentToken(t *testing.T) {
	pool := pgtest.NewPool(t)
	db := postgres.New(pool, nil)
	seed := config.Default().Dev.SeedEnrollmentToken
	svc, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, config.OpAMPConfig{}, authn.Deps{Store: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureDevSeed(context.Background(), config.Default().Dev); err != nil {
		t.Fatal(err)
	}
	hash := domain.SHA256([]byte(seed))
	if tok, err := db.Identity().GetEnrollmentTokenByHash(context.Background(), hash); err != nil || tok.RevokedAt != nil {
		t.Fatalf("seeded token: %+v %v", tok, err)
	}

	cfg, addr := devConfigDSN(t, pool.Config().ConnString())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, runOptions{}) }() // production mode on that database
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	tok, err := db.Identity().GetEnrollmentTokenByHash(context.Background(), hash)
	if err != nil || tok.RevokedAt == nil {
		t.Fatalf("the dev token must be revoked after a production start: %+v %v", tok, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestDevPublicURL(t *testing.T) {
	def := config.DevDefaults().Server.PublicURL
	cases := map[string]struct{ configured, listen, want string }{
		"default follows the listen port":      {def, "127.0.0.1:8081", "http://localhost:8081"},
		"default follows an any-address port":  {def, "0.0.0.0:9090", "http://localhost:9090"},
		"configured public url is kept":        {"https://fleet.example.com", "0.0.0.0:8080", "https://fleet.example.com"},
		"configured http url is kept":          {"http://dev.example.com:8080", "127.0.0.1:8081", "http://dev.example.com:8080"},
		"default without a port stays default": {def, "invalid", def},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := devPublicURL(c.configured, c.listen); got != c.want {
				t.Fatalf("devPublicURL(%q, %q) = %q, want %q", c.configured, c.listen, got, c.want)
			}
		})
	}
}
