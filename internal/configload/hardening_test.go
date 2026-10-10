// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
)

func TestProductionRefusesCatchAllTrustedProxies(t *testing.T) {
	for _, p := range []string{"0.0.0.0/0", "::/0"} {
		c := prodConfig()
		c.Server.TrustedProxies = []string{p}
		err := Validate(&c, false)
		if err == nil || !issuePaths(err)["server.trustedProxies[0]"] {
			t.Errorf("%s accepted as trusted proxy range: %v", p, err)
		}
		// development mode keeps working
		d := config.DevDefaults()
		d.Server.TrustedProxies = []string{p}
		if err := Validate(&d, true); err != nil {
			t.Errorf("dev mode with %s: %v", p, err)
		}
	}
	// narrow ranges are fine; a very broad range is refused together with a forwarded certificate header
	c := prodConfig()
	c.Server.TrustedProxies = []string{"10.0.0.0/8", "2001:db8::/32"}
	if err := Validate(&c, false); err != nil {
		t.Errorf("narrow ranges: %v", err)
	}
	c.Server.TrustedProxies = []string{"10.0.0.0/7"}
	if err := Validate(&c, false); err != nil {
		t.Errorf("a broad range without a certificate header is only a warning: %v", err)
	}
	if got := strings.Join(Warnings(&c, false), "\n"); !strings.Contains(got, "server.trustedProxies") {
		t.Errorf("no warning for a broad range:\n%s", got)
	}
	c.OpAMP.ClientCertHeader = "X-Forwarded-Client-Cert"
	if err := Validate(&c, false); err == nil || !issuePaths(err)["server.trustedProxies[0]"] {
		t.Errorf("a broad range with opamp.clientCertHeader must be refused: %v", err)
	}
}

func TestProductionRequiresADownloadTokenTTL(t *testing.T) {
	c := prodConfig()
	c.Artifacts.DownloadTokenTTL = 0
	if err := Validate(&c, false); err == nil || !issuePaths(err)["artifacts.downloadTokenTtl"] {
		t.Errorf("downloadTokenTtl 0 accepted in production: %v", err)
	}
	c.Artifacts.DownloadTokenTTL = time.Hour
	if err := Validate(&c, false); err != nil {
		t.Errorf("positive TTL: %v", err)
	}
	d := config.DevDefaults()
	d.Artifacts.DownloadTokenTTL = 0
	if err := Validate(&d, true); err != nil {
		t.Errorf("dev mode: %v", err)
	}
}

func TestWarningsHardening(t *testing.T) {
	has := func(c config.Config, substr string) bool {
		for _, w := range Warnings(&c, false) {
			if strings.Contains(w, substr) {
				return true
			}
		}
		return false
	}
	c := prodConfig()
	c.Server.TrustedProxies = []string{"10.0.0.0/8"}
	c.Server.MetricsListenAddr = ""
	if !has(c, "server.metricsListenAddr") {
		t.Error("no warning for /metrics on the main listener")
	}
	c.Telemetry.Metrics = false
	if has(c, "server.metricsListenAddr") {
		t.Error("warning although metrics are disabled")
	}

	// database transport
	for dsn, want := range map[string]bool{
		"postgres://u:pw-secret@db.example.test/myriarch?sslmode=require":     true,
		"postgres://u:pw-secret@db.example.test/myriarch":                     true,
		"postgres://u:pw-secret@db.example.test/myriarch?sslmode=disable":     true,
		"host=db.example.test user=u password=pw-secret sslmode=prefer":       true,
		"postgres://u:pw-secret@db.example.test/myriarch?sslmode=verify-full": false,
		"postgres://u:pw-secret@db.example.test/myriarch?sslmode=verify-ca":   false,
		"postgres://u:pw-secret@127.0.0.1/myriarch?sslmode=disable":           false,
		"postgres://u:pw-secret@localhost/myriarch":                           false,
		"host=/var/run/postgresql user=u sslmode=disable":                     false,
	} {
		c := prodConfig()
		c.Server.TrustedProxies = []string{"10.0.0.0/8"}
		c.Database.DSN = dsn
		got := Warnings(&c, false)
		found := false
		for _, w := range got {
			if strings.Contains(w, "database.dsn") {
				found = true
			}
			if strings.Contains(w, "pw-secret") || strings.Contains(w, dsn) {
				t.Errorf("a warning repeats the DSN: %q", w)
			}
		}
		if found != want {
			t.Errorf("DSN %q: warning = %v, want %v (%v)", dsn, found, want, got)
		}
	}

	// local directories
	c = prodConfig()
	c.Server.TrustedProxies = []string{"10.0.0.0/8"}
	c.Artifacts.Dir, c.GitOps.CloneDir = "", ""
	if !has(c, "artifacts.dir") || !has(c, "gitops.cloneDir") {
		t.Error("no warning for the default cache and clone directories")
	}
	c.Artifacts.Dir, c.GitOps.CloneDir = "/var/lib/myriarchd/artifacts", "/var/lib/myriarchd/gitops"
	if has(c, "artifacts.dir") || has(c, "gitops.cloneDir") {
		t.Error("warning although the directories are set")
	}
}

func TestIdentityPatternsMustBeAnchored(t *testing.T) {
	c := prodConfig()
	for pat, ok := range map[string]bool{
		"opentelemetry-collector-releases":                  false,
		"^https://example\\.test/wf@refs/tags/v1$":          true,
		"^https://example\\.test/wf@refs/tags/v1":           false,
		"https://example\\.test/wf@refs/tags/v1$":           false,
		"^https://example\\.test/wf@refs/tags/v1\\$":        false,
		"^(https://a\\.test/x|https://b\\.test/y)@refs/.*$": true,
	} {
		c.Artifacts.Verify.CertificateIdentityRegexp = pat
		err := Validate(&c, false)
		if ok && err != nil {
			t.Errorf("%q refused: %v", pat, err)
		}
		if !ok && (err == nil || !issuePaths(err)["artifacts.verify.certificateIdentityRegexp"]) {
			t.Errorf("%q accepted: %v", pat, err)
		}
	}
}

func TestRedactedYAMLHidesProxyCredentials(t *testing.T) {
	c := prodConfig()
	c.Artifacts.ProxyURL = "http://proxy-user:proxy-pass-123@proxy.example.test:3128"
	c.GitOps.RepoURL = "https://git-user:git-pass-456@git.example.test/x.git"
	out, err := RedactedYAML(&c)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"proxy-pass-123", "git-pass-456"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("RedactedYAML leaks %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(string(out), "proxy.example.test:3128") {
		t.Errorf("the proxy host should stay visible:\n%s", out)
	}
}

func TestInlineSecretsInTheFileAreReported(t *testing.T) {
	p := writeFile(t, "c.yaml", strings.Replace(prodMinimal, "postgres://u:p@db/myriarch?sslmode=verify-full", "postgres://u:inline-secret-pw@db.example.test/myriarch?sslmode=verify-full", 1))
	_, warnings, err := loadWith(p, Options{SkipFiles: true}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(warnings, "\n")
	if !strings.Contains(got, "database.dsn") || !strings.Contains(got, "inline") {
		t.Errorf("no warning about the inline DSN:\n%s", got)
	}
	if strings.Contains(got, "inline-secret-pw") {
		t.Errorf("the warning repeats the secret:\n%s", got)
	}
	// a reference to the environment is not an inline secret
	p = writeFile(t, "c2.yaml", strings.Replace(strings.Replace(prodMinimal, "postgres://u:p@db/myriarch?sslmode=verify-full", "${TEST_DSN}", 1), "clientSecret: test-client-secret", "clientSecret: ${OIDC_CLIENT_VAL}", 1))
	_, warnings, err = loadWith(p, Options{SkipFiles: true}, env(map[string]string{"TEST_DSN": "postgres://u:p@db.example.test/myriarch?sslmode=verify-full", "OIDC_CLIENT_VAL": "value-from-env"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(warnings, "\n"), "inline") {
		t.Errorf("an environment reference is not inline: %v", warnings)
	}
}

func TestAPITokenMaxTTLValidationAndWarning(t *testing.T) {
	c := prodConfig()
	if c.Auth.APITokenMaxTTL != 90*24*time.Hour {
		t.Fatalf("default auth.apiTokenMaxTtl = %s, want 90 days", c.Auth.APITokenMaxTTL)
	}
	c.Server.TrustedProxies = []string{"10.0.0.0/8"}
	if w := Warnings(&c, false); len(w) != 0 {
		t.Errorf("default maximum: %v", w)
	}
	c.Auth.APITokenMaxTTL = -time.Hour
	if err := Validate(&c, false); err == nil || !issuePaths(err)["auth.apiTokenMaxTtl"] {
		t.Errorf("negative maximum accepted: %v", err)
	}
	c.Auth.APITokenMaxTTL = 0
	if err := Validate(&c, false); err != nil {
		t.Errorf("0 (unlimited) is allowed: %v", err)
	}
	if got := strings.Join(Warnings(&c, false), "\n"); !strings.Contains(got, "auth.apiTokenMaxTtl") {
		t.Errorf("no warning for an unlimited token lifetime:\n%s", got)
	}
}
