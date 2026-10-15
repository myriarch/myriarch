// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// absPath turns a slash path such as "/etc/myriarchd/token" into an absolute
// path of the running platform: the path itself on Unix, with the volume of the
// temporary directory in front on Windows ("C:/etc/myriarchd/token"; forward
// slashes are valid there and safe in YAML). The settings that name a file
// refuse a path without a volume on Windows ("must be an absolute path").
func absPath(slash string) string {
	return filepath.VolumeName(os.TempDir()) + slash
}

// missingFileReason returns the text of the operating system for a file that
// does not exist, the end of the message "cannot read <file>: <reason>" ("no
// such file or directory" on Unix, "The system cannot find the file specified."
// on Windows). file must not exist.
func missingFileReason(t *testing.T, file string) string {
	t.Helper()
	_, err := os.ReadFile(file)
	var pe *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pe) {
		t.Fatalf("%s must not exist: %v", file, err)
	}
	return pe.Err.Error()
}

const prodMinimal = `
server:
  publicUrl: https://fleet.example.com
  tls:
    terminatedUpstream: true
database:
  dsn: postgres://u:p@db/myriarch?sslmode=verify-full
gitops:
  repoUrl: https://git.example.com/otel/config.git
  cloneDir: /var/lib/myriarchd/gitops
artifacts:
  dir: /var/lib/myriarchd/artifacts
executors:
  allowedWebhookHosts: ["*.example.com"]
auth:
  oidc:
    enabled: true
    issuerUrl: https://idp.example.com/realms/test
    clientId: myriarch
    clientSecret: test-client-secret
    roleMapping:
      admin: [admins]
`

func TestLoadDefaultsAndFile(t *testing.T) {
	p := writeFile(t, "myriarchd.yaml", strings.Replace(prodMinimal, "auth:\n", "auth:\n  sessionTtl: 8h\n", 1)+`
opamp:
  offlineAfter: 2m
  connectRateLimit: 50
telemetry:
  logLevel: debug
`)
	cfg, err := load(p, false, env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.OpAMP.OfflineAfter != 2*time.Minute || cfg.OpAMP.ConnectRateLimit != 50 {
		t.Errorf("file values not applied: %+v", cfg.OpAMP)
	}
	if cfg.Auth.SessionTTL != 8*time.Hour || cfg.Telemetry.LogLevel != "debug" {
		t.Errorf("auth/telemetry not applied: %+v %+v", cfg.Auth, cfg.Telemetry)
	}
	// untouched defaults survive
	if cfg.OpAMP.Path != "/v1/opamp" || cfg.OpAMP.SendTimeout != 10*time.Second || !cfg.OpAMP.RequireEnrollmentToken {
		t.Errorf("defaults lost: %+v", cfg.OpAMP)
	}
	if cfg.OpAMP.StaleApplyingAfter != 90*time.Second {
		t.Errorf("opamp.staleApplyingAfter default = %v, want 1m30s", cfg.OpAMP.StaleApplyingAfter)
	}
	if cfg.Database.MaxConns != 20 || !cfg.Database.MigrateOnStart {
		t.Errorf("database defaults lost: %+v", cfg.Database)
	}
}

func TestLoadNoFileDevDefaults(t *testing.T) {
	cfg, err := load("", true, env(nil))
	if err != nil {
		t.Fatalf("dev load: %v", err)
	}
	want := config.DevDefaults()
	if cfg.Server.PublicURL != want.Server.PublicURL || !cfg.Auth.DevLogin.Enabled || cfg.OpAMP.RequireEnrollmentToken {
		t.Errorf("dev defaults not applied: %+v", cfg.Auth)
	}
}

func TestLoadRejectsUnknownKeysAndBadDurations(t *testing.T) {
	cases := map[string]string{
		"unknown key":  prodMinimal + "\nbogus: 1\n",
		"unknown nest": prodMinimal + "\nopamp:\n  bogus: 1\n",
		"int duration": prodMinimal + "\nopamp:\n  offlineAfter: 90\n",
		"bad duration": prodMinimal + "\nopamp:\n  offlineAfter: soon\n",
		"bad yaml":     "server: [\n",
		"wrong type":   prodMinimal + "\nopamp:\n  connectRateLimit: fast\n",
		"missing file": "",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.yaml")
			if content != "" {
				path = writeFile(t, "c.yaml", content)
			}
			if _, err := load(path, false, env(nil)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestEnvExpansion(t *testing.T) {
	p := writeFile(t, "c.yaml", `
server:
  publicUrl: https://${HOST}.example.com
  tls: {terminatedUpstream: true}
executors: {allowedWebhookHosts: ["*.example.com"]}
auth: {bootstrapAdmin: {enabled: true, tokenFile: `+absPath("/etc/myriarchd/bootstrap/token")+`}}
database:
  dsn: postgres://u:${DB_PASS}@db/${DB_NAME:-myriarch}
gitops:
  repoUrl: ${REPO:-https://git.example.com/x.git}
  branch: ${BRANCH}
`)
	cfg, err := load(p, false, env(map[string]string{"HOST": "fleet", "DB_PASS": "s3cr$t", "BRANCH": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicURL != "https://fleet.example.com" {
		t.Errorf("publicUrl = %q", cfg.Server.PublicURL)
	}
	if cfg.Database.DSN != "postgres://u:s3cr$t@db/myriarch" {
		t.Errorf("dsn = %q", cfg.Database.DSN)
	}
	if cfg.GitOps.RepoURL != "https://git.example.com/x.git" {
		t.Errorf("repoUrl = %q", cfg.GitOps.RepoURL)
	}
	if cfg.GitOps.Branch != "" {
		t.Errorf("unset variable without default must expand to empty, got %q", cfg.GitOps.Branch)
	}
}

func TestExpansionCannotInjectStructure(t *testing.T) {
	p := writeFile(t, "c.yaml", prodMinimal+"\ntelemetry:\n  logLevel: ${LVL}\n")
	_, err := load(p, false, env(map[string]string{"LVL": "debug\nauth:\n  devLogin:\n    enabled: true"}))
	if err == nil {
		t.Fatal("expected validation error for the injected value")
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
}

func TestEnvOverrides(t *testing.T) {
	p := writeFile(t, "c.yaml", prodMinimal+"\nopamp:\n  sendTimeout: 5s\n")
	cfg, err := load(p, false, env(map[string]string{
		"MYRIARCH_DATABASE_DSN":            "postgres://override/db",
		"MYRIARCH_AUTH_OIDC_ENABLED":       "true",
		"MYRIARCH_AUTH_OIDC_ISSUERURL":     "https://idp.example.com/realms/otel",
		"MYRIARCH_AUTH_OIDC_CLIENTID":      "myriarchd",
		"MYRIARCH_AUTH_OIDC_CLIENTSECRET":  "$notexpanded${X}",
		"MYRIARCH_AUTH_OIDC_DEFAULTROLE":   "operator",
		"MYRIARCH_GITOPS_REPOURL":          "https://git.example.com/o.git",
		"MYRIARCH_OPAMP_SENDTIMEOUT":       "7s",
		"MYRIARCH_OPAMP_CONNECTRATELIMIT":  "12.5",
		"MYRIARCH_DATABASE_MAXCONNS":       "40",
		"MYRIARCH_SERVER_TRUSTEDPROXIES":   "10.0.0.0/8, 192.168.1.1 ,",
		"MYRIARCH_AUTH_OIDC_SCOPES":        "openid,email",
		"MYRIARCH_RETENTION_AUDIT":         "720h",
		"MYRIARCH_DATABASE_MIGRATEONSTART": "false",
		"MYRIARCH_UNKNOWN_THING":           "ignored",
		"MYRIARCH_SERVER_TLS_CLIENTCAFILE": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DSN != "postgres://override/db" || cfg.Database.MaxConns != 40 || cfg.Database.MigrateOnStart {
		t.Errorf("database overrides: %+v", cfg.Database)
	}
	if !cfg.Auth.OIDC.Enabled || cfg.Auth.OIDC.ClientSecret != "$notexpanded${X}" || cfg.Auth.OIDC.DefaultRole != domain.RoleOperator {
		t.Errorf("oidc overrides: %+v", cfg.Auth.OIDC)
	}
	if cfg.OpAMP.SendTimeout != 7*time.Second || cfg.OpAMP.ConnectRateLimit != 12.5 {
		t.Errorf("opamp overrides: %+v", cfg.OpAMP)
	}
	if got := strings.Join(cfg.Server.TrustedProxies, "|"); got != "10.0.0.0/8|192.168.1.1" {
		t.Errorf("list override = %q", got)
	}
	if got := strings.Join(cfg.Auth.OIDC.Scopes, "|"); got != "openid|email" {
		t.Errorf("scopes = %q", got)
	}
	if cfg.Retention.Audit != 720*time.Hour {
		t.Errorf("retention.audit = %v", cfg.Retention.Audit)
	}
}

func TestEnvOverrideErrors(t *testing.T) {
	for k, v := range map[string]string{
		"MYRIARCH_OPAMP_SENDTIMEOUT":      "soon",
		"MYRIARCH_AUTH_COOKIESECURE":      "maybe",
		"MYRIARCH_DATABASE_MAXCONNS":      "many",
		"MYRIARCH_OPAMP_CONNECTRATELIMIT": "x",
		"MYRIARCH_HA_LEADERLOCKKEY":       "1.5",
	} {
		if _, err := load("", true, env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s: expected error", k, v)
		}
	}
}

func TestSecretFiles(t *testing.T) {
	dsn := writeFile(t, "dsn", "postgres://file/db\n")
	sec := writeFile(t, "secret", "  oidc-secret \n")
	hook := writeFile(t, "hook", "https://hooks.example.com/abc\n")
	whs := writeFile(t, "wh", "whsecret\n")
	p := writeFile(t, "c.yaml", `
server:
  publicUrl: https://fleet.example.com
  tls: {terminatedUpstream: true}
executors: {allowedWebhookHosts: ["*.example.com"]}
database:
  dsn: postgres://inline/db
  dsnFile: `+dsn+`
gitops:
  repoUrl: https://git.example.com/x.git
  webhookSecretFile: `+whs+`
auth:
  oidc:
    enabled: true
    issuerUrl: https://idp.example.com
    clientId: x
    clientSecretFile: `+sec+`
    roleMapping: {admin: ["fleet-admins"]}
notifications:
  channels:
    - {name: teams1, type: teams, urlFile: `+hook+`}
`)
	cfg, err := load(p, false, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DSN != "postgres://file/db" {
		t.Errorf("dsn = %q (file must win over inline)", cfg.Database.DSN)
	}
	if cfg.Auth.OIDC.ClientSecret != "oidc-secret" || cfg.GitOps.WebhookSecret != "whsecret" {
		t.Errorf("secrets: %q %q", cfg.Auth.OIDC.ClientSecret, cfg.GitOps.WebhookSecret)
	}
	if cfg.Notifications.Channels[0].URL != "https://hooks.example.com/abc" {
		t.Errorf("channel url = %q", cfg.Notifications.Channels[0].URL)
	}

	p2 := writeFile(t, "c2.yaml", prodMinimal+"\ndatabase:\n  dsn: x\n  dsnFile: /does/not/exist\n")
	if _, err := load(p2, false, env(nil)); err == nil {
		t.Error("expected error for missing secret file")
	}
}

func issuePaths(err error) map[string]bool {
	out := map[string]bool{}
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		for _, i := range ve.Issues {
			out[i.Path] = true
		}
	}
	return out
}

func prodConfig() config.Config {
	c := config.Default()
	c.Server.PublicURL = "https://fleet.example.com"
	c.Database.DSN = "postgres://u:p@db/myriarch?sslmode=verify-full"
	c.GitOps.RepoURL = "https://git.example.com/x.git"
	c.GitOps.CloneDir = "/var/lib/myriarchd/gitops"
	c.Artifacts.Dir = "/var/lib/myriarchd/artifacts"
	c.Server.TLS.TerminatedUpstream = true
	c.Executors.AllowedWebhookHosts = []string{"*.example.com"}
	c.Auth.OIDC = config.OIDCConfig{
		Enabled: true, IssuerURL: "https://idp.example.com/realms/test", ClientID: "myriarch", ClientSecret: "test-client-secret",
		Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", NameClaim: "name", EmailClaim: "email",
		RoleMapping: config.RoleMapping{Admin: []string{"admins"}},
	}
	return c
}

func TestValidateProduction(t *testing.T) {
	good := prodConfig()
	if err := Validate(&good, false); err != nil {
		t.Fatalf("good production config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(c *config.Config)
		path   string
	}{
		{"missing dsn", func(c *config.Config) { c.Database.DSN = "" }, "database.dsn"},
		{"missing public url", func(c *config.Config) { c.Server.PublicURL = "" }, "server.publicUrl"},
		{"http public url", func(c *config.Config) { c.Server.PublicURL = "http://fleet.example.com" }, "server.publicUrl"},
		{"relative public url", func(c *config.Config) { c.Server.PublicURL = "fleet.example.com" }, "server.publicUrl"},
		{"dev login", func(c *config.Config) {
			c.Auth.DevLogin = config.DevLoginConfig{Enabled: true, Users: []config.DevUser{{Username: "a", Password: "b", Role: domain.RoleAdmin}}}
		}, "auth.devLogin.enabled"},
		{"insecure cookie", func(c *config.Config) { c.Auth.CookieSecure = false }, "auth.cookieSecure"},
		{"compression without acknowledgement", func(c *config.Config) { c.OpAMP.EnableCompression = true }, "opamp.enableCompression"},
		{"unprefixed secretEnvHosts name", func(c *config.Config) {
			c.Executors.SecretEnvHosts = map[string][]string{"HOME": {"awx.example.com"}}
		}, "executors.secretEnvHosts.HOME"},
		{"empty secretEnvHosts list", func(c *config.Config) {
			c.Executors.SecretEnvHosts = map[string][]string{"MYRIARCH_EXEC_AWX": nil}
		}, "executors.secretEnvHosts.MYRIARCH_EXEC_AWX"},
		{"no enrollment token", func(c *config.Config) { c.OpAMP.RequireEnrollmentToken = false }, "opamp.requireEnrollmentToken"},
		{"oidc incomplete", func(c *config.Config) { c.Auth.OIDC = config.OIDCConfig{Enabled: true} }, "auth.oidc.issuerUrl"},
		{"oidc nobody", func(c *config.Config) {
			c.Auth.OIDC = config.OIDCConfig{Enabled: true, IssuerURL: "https://i", ClientID: "c", ClientSecret: "s", Scopes: []string{"openid"}, GroupsClaim: "groups", NameClaim: "name", EmailClaim: "email"}
		}, "auth.oidc.roleMapping"},
		{"oidc no openid scope", func(c *config.Config) {
			c.Auth.OIDC = config.OIDCConfig{Enabled: true, IssuerURL: "https://i", ClientID: "c", ClientSecret: "s", Scopes: []string{"email"}, GroupsClaim: "groups", NameClaim: "name", EmailClaim: "email", DefaultRole: domain.RoleViewer}
		}, "auth.oidc.scopes"},
		{"bad role", func(c *config.Config) {
			c.Auth.OIDC = config.OIDCConfig{Enabled: true, IssuerURL: "https://i", ClientID: "c", ClientSecret: "s", Scopes: []string{"openid"}, GroupsClaim: "groups", NameClaim: "name", EmailClaim: "email", DefaultRole: "root"}
		}, "auth.oidc.defaultRole"},
		{"zero offline", func(c *config.Config) { c.OpAMP.OfflineAfter = 0 }, "opamp.offlineAfter"},
		{"flapping offline", func(c *config.Config) { c.OpAMP.OfflineAfter = 20 * time.Second }, "opamp.offlineAfter"},
		{"negative stale applying", func(c *config.Config) { c.OpAMP.StaleApplyingAfter = -time.Second }, "opamp.staleApplyingAfter"},
		{"opamp path", func(c *config.Config) { c.OpAMP.Path = "opamp" }, "opamp.path"},
		{"opamp path api", func(c *config.Config) { c.OpAMP.Path = "/api/v1/opamp" }, "opamp.path"},
		{"tiny pool", func(c *config.Config) { c.Database.MaxConns = 2 }, "database.maxConns"},
		{"rate limit", func(c *config.Config) { c.OpAMP.ConnectRateLimit = 0 }, "opamp.connectRateLimit"},
		{"tls half", func(c *config.Config) { c.Server.TLS.CertFile = "x" }, "server.tls"},
		{"proxy cidr", func(c *config.Config) { c.Server.TrustedProxies = []string{"nonsense"} }, "server.trustedProxies[0]"},
		{"client cert without ca", func(c *config.Config) { c.OpAMP.RequireClientCert = true }, "server.tls.clientCaFile"},
		{"gitops repo", func(c *config.Config) { c.GitOps.RepoURL = "" }, "gitops.repoUrl"},
		{"timezone", func(c *config.Config) { c.Rollouts.DefaultTimezone = "Mars/Olympus" }, "rollouts.defaultTimezone"},
		{"log level", func(c *config.Config) { c.Telemetry.LogLevel = "loud" }, "telemetry.logLevel"},
		{"ui default language tag", func(c *config.Config) { c.UI.DefaultLanguage = "German" }, "ui.defaultLanguage"},
		{"ui language tag", func(c *config.Config) { c.UI.Languages = []string{"en", "de_DE"} }, "ui.languages[1]"},
		{"ui duplicate language", func(c *config.Config) { c.UI.Languages = []string{"en", "de", "de"} }, "ui.languages[2]"},
		{"ui default not offered", func(c *config.Config) {
			c.UI = config.UIConfig{DefaultLanguage: "en", Languages: []string{"de"}}
		}, "ui.defaultLanguage"},
		{"channel", func(c *config.Config) {
			c.Notifications.Channels = []config.NotificationChannel{{Name: "a", Type: "sms"}}
		}, "notifications.channels[0].type"},
		{"heartbeat too slow", func(c *config.Config) { c.HA.HeartbeatInterval = time.Minute }, "ha.heartbeatInterval"},
		{"negative retention", func(c *config.Config) { c.Retention.Audit = -time.Hour }, "retention.audit"},
		// AC-14: plaintext listener
		{"plaintext listener without confirmation", func(c *config.Config) { c.Server.TLS.TerminatedUpstream = false }, "server.tls.terminatedUpstream"},
		{"executor secret without hosts", func(c *config.Config) {
			c.Executors.Secrets = map[string]config.ExecutorSecret{"deploy": {Env: "X"}}
		}, "executors.secrets.deploy.allowedHosts"},
		{"executor secret with env and file", func(c *config.Config) {
			c.Executors.Secrets = map[string]config.ExecutorSecret{"deploy": {Env: "X", File: "/f", AllowedHosts: []string{"a.example.com"}}}
		}, "executors.secrets.deploy"},
		{"relative secret dir", func(c *config.Config) { c.Executors.SecretDir = "secrets" }, "executors.secretDir"},
		{"bad outbound cidr", func(c *config.Config) { c.Outbound.AllowedCIDRs = []string{"10.0.0.0/99"} }, "outbound.allowedCidrs[0]"},
		// GIT-1 / OUT-6: sources and credential-bearing URLs
		{"git over http", func(c *config.Config) { c.GitOps.RepoURL = "http://git.example.com/x.git" }, "gitops.repoUrl"},
		{"git dir source", func(c *config.Config) { c.GitOps.RepoURL = "dir:examples/quickstart" }, "gitops.repoUrl"},
		{"git file source", func(c *config.Config) { c.GitOps.RepoURL = "file:///srv/repo" }, "gitops.repoUrl"},
		{"git local path", func(c *config.Config) { c.GitOps.RepoURL = "/srv/repo" }, "gitops.repoUrl"},
		{"git inline credentials", func(c *config.Config) { c.GitOps.RepoURL = "https://user:token@git.example.com/x.git" }, "gitops.repoUrl"},
		{"git token as user", func(c *config.Config) { c.GitOps.RepoURL = "https://ghp_secret@git.example.com/x.git" }, "gitops.repoUrl"},
		{"git ssh with password", func(c *config.Config) { c.GitOps.RepoURL = "ssh://git:pw@git.example.com/x.git" }, "gitops.repoUrl"},
		{"release api over http", func(c *config.Config) { c.Releases.GitHubAPIURL = "http://ghe.example.com/api/v3" }, "releases.githubApiUrl"},
		{"release raw over http", func(c *config.Config) { c.Releases.RawBaseURL = "http://ghe.example.com/raw" }, "releases.rawBaseUrl"},
		{"notification url over http", func(c *config.Config) {
			c.Notifications.Channels = []config.NotificationChannel{{Name: "a", Type: "teams", URL: "http://hooks.example.com/x"}}
		}, "notifications.channels[0].url"},
		// SIG-1: signed generic webhooks
		{"webhook channel without secret", func(c *config.Config) {
			c.Notifications.Channels = []config.NotificationChannel{{Name: "a", Type: "webhook", URL: "https://hooks.example.com/x"}}
		}, "notifications.channels[0].secretFile"},
		// AC-16 / AC-18: artifact verification and upstream
		{"checksum off", func(c *config.Config) { c.Artifacts.Verify.RequireChecksum = false }, "artifacts.verify"},
		{"signature off", func(c *config.Config) { c.Artifacts.Verify.RequireSignature = false }, "artifacts.verify"},
		{"artifact source over http", func(c *config.Config) { c.Artifacts.SourceBaseURL = "http://mirror.example.com/releases" }, "artifacts.sourceBaseUrl"},
		{"artifact proxy scheme", func(c *config.Config) { c.Artifacts.ProxyURL = "socks5://proxy:1080" }, "artifacts.proxyUrl"},
		// per-updater download base URLs
		{"agent base url over http", func(c *config.Config) { c.Artifacts.AgentBaseURLs = []string{"http://myriarch:8080"} }, "artifacts.agentBaseUrls[0]"},
		{"agent base url without scheme", func(c *config.Config) {
			c.Artifacts.AgentBaseURLs = []string{"https://ok.example.com", "myriarch:8080"}
		}, "artifacts.agentBaseUrls[1]"},
		{"agent base url with a path", func(c *config.Config) { c.Artifacts.AgentBaseURLs = []string{"https://fleet.example.com/base"} }, "artifacts.agentBaseUrls[0]"},
		{"agent base url with a query", func(c *config.Config) { c.Artifacts.AgentBaseURLs = []string{"https://fleet.example.com/?a=b"} }, "artifacts.agentBaseUrls[0]"},
		{"agent base url with credentials", func(c *config.Config) { c.Artifacts.AgentBaseURLs = []string{"https://u:p@fleet.example.com"} }, "artifacts.agentBaseUrls[0]"},
		// sessions
		{"no absolute session lifetime", func(c *config.Config) { c.Auth.SessionMaxAge = 0 }, "auth.sessionMaxAge"},
		{"max age shorter than idle", func(c *config.Config) { c.Auth.SessionMaxAge = time.Hour; c.Auth.SessionTTL = 2 * time.Hour }, "auth.sessionMaxAge"},
		// limits
		{"effective config limit", func(c *config.Config) { c.OpAMP.MaxEffectiveConfigBytes = 0 }, "opamp.maxEffectiveConfigBytes"},
		{"gitops limits", func(c *config.Config) { c.GitOps.MaxFiles = 0 }, "gitops.maxRepoBytes"},
		{"executor max timeout", func(c *config.Config) { c.Executors.MaxTimeout = 0 }, "executors.maxTimeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := prodConfig()
			tc.mutate(&c)
			err := Validate(&c, false)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("error is not ErrValidation: %v", err)
			}
			if !issuePaths(err)[tc.path] {
				t.Errorf("expected issue for %s, got %v", tc.path, err)
			}
		})
	}
}

// opamp.staleApplyingAfter: 0 turns the re-offer of a stale APPLYING off, every
// other non-negative value is accepted, a negative one is refused; the key
// takes a file value and an environment override like any duration.
func TestStaleApplyingAfterSetting(t *testing.T) {
	for _, v := range []time.Duration{0, time.Second, 90 * time.Second, 10 * time.Minute} {
		c := prodConfig()
		c.OpAMP.StaleApplyingAfter = v
		if err := Validate(&c, false); err != nil {
			t.Errorf("opamp.staleApplyingAfter %v rejected: %v", v, err)
		}
	}
	c := prodConfig()
	c.OpAMP.StaleApplyingAfter = -time.Nanosecond
	err := Validate(&c, false)
	if err == nil || !issuePaths(err)["opamp.staleApplyingAfter"] || !strings.Contains(err.Error(), "must not be negative") {
		t.Errorf("a negative value must be refused with the key in the message: %v", err)
	}

	p := writeFile(t, "c.yaml", prodMinimal+"\nopamp:\n  staleApplyingAfter: 0s\n")
	cfg, err := load(p, false, env(nil))
	if err != nil || cfg.OpAMP.StaleApplyingAfter != 0 {
		t.Errorf("file value 0s: %v %v", cfg.OpAMP.StaleApplyingAfter, err)
	}
	cfg, err = load(p, false, env(map[string]string{"MYRIARCH_OPAMP_STALEAPPLYINGAFTER": "2m"}))
	if err != nil || cfg.OpAMP.StaleApplyingAfter != 2*time.Minute {
		t.Errorf("environment override: %v %v", cfg.OpAMP.StaleApplyingAfter, err)
	}
	bad := writeFile(t, "bad.yaml", prodMinimal+"\nopamp:\n  staleApplyingAfter: -5s\n")
	if _, err := load(bad, false, env(nil)); err == nil || !strings.Contains(err.Error(), "opamp.staleApplyingAfter") {
		t.Errorf("a negative file value must be refused with the key in the message: %v", err)
	}
	if _, err := load(p, false, env(map[string]string{"MYRIARCH_OPAMP_STALEAPPLYINGAFTER": "90"})); err == nil {
		t.Error("a duration without a unit must be refused")
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	c := prodConfig()
	c.Database.DSN = ""
	c.Auth.CookieSecure = false
	c.OpAMP.RequireEnrollmentToken = false
	c.Server.PublicURL = ""
	err := Validate(&c, false)
	paths := issuePaths(err)
	for _, want := range []string{"database.dsn", "auth.cookieSecure", "opamp.requireEnrollmentToken", "server.publicUrl"} {
		if !paths[want] {
			t.Errorf("missing issue %s in %v", want, err)
		}
	}
}

func TestValidateDevAllowsInsecure(t *testing.T) {
	c := config.DevDefaults()
	if err := Validate(&c, true); err != nil {
		t.Fatalf("dev defaults rejected in dev mode: %v", err)
	}
	// the very same config is rejected as production config
	if err := Validate(&c, false); err == nil {
		t.Fatal("dev defaults must be rejected in production mode")
	} else {
		p := issuePaths(err)
		for _, want := range []string{"auth.devLogin.enabled", "auth.cookieSecure", "opamp.requireEnrollmentToken", "database.dsn"} {
			if !p[want] {
				t.Errorf("production check missing %s", want)
			}
		}
	}
}

func TestClientCertAllowsWithoutEnrollmentToken(t *testing.T) {
	c := prodConfig()
	c.OpAMP.RequireEnrollmentToken = false
	c.OpAMP.RequireClientCert = true
	c.Server.TLS = config.TLSServerConfig{CertFile: "c", KeyFile: "k", ClientCAFile: "ca"}
	if err := Validate(&c, false); err != nil {
		t.Fatalf("mTLS-only config rejected: %v", err)
	}
}

func TestDurationYAMLRoundTrip(t *testing.T) {
	var c config.Config
	if err := decodeYAML([]byte("opamp:\n  offlineAfter: 1m30s\nretention:\n  audit: 8760h\n"), &c); err != nil {
		t.Fatal(err)
	}
	if c.OpAMP.OfflineAfter != 90*time.Second || c.Retention.Audit != 8760*time.Hour {
		t.Fatalf("durations: %v %v", c.OpAMP.OfflineAfter, c.Retention.Audit)
	}
	if err := decodeYAML([]byte(""), &c); err != nil {
		t.Fatalf("empty document must be accepted: %v", err)
	}
}

func TestParseCIDR(t *testing.T) {
	for _, s := range []string{"10.0.0.0/8", "10.1.2.3", "::1", "fd00::/8"} {
		if _, err := ParseCIDR(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "10.0.0.0/33", "host"} {
		if _, err := ParseCIDR(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}

// The Helm chart sets the replica id through the environment.
func TestHelmStyleEnvironment(t *testing.T) {
	cfg, err := load("", true, env(map[string]string{
		"MYRIARCH_HA_REPLICAID":       "myriarchd-2",
		"MYRIARCH_SERVER_PUBLICURL":   "https://fleet.example.com",
		"MYRIARCH_DATABASE_DSN":       "postgres://x",
		"MYRIARCH_TELEMETRY_LOGLEVEL": "warn",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HA.ReplicaID != "myriarchd-2" || cfg.Server.PublicURL != "https://fleet.example.com" || cfg.Telemetry.LogLevel != "warn" {
		t.Fatalf("%+v", cfg.HA)
	}
}

func TestOptInsAreAcceptedInProduction(t *testing.T) {
	for name, mutate := range map[string]func(c *config.Config){
		"empty executor host allowlist (fail closed at call time)": func(c *config.Config) { c.Executors.AllowedWebhookHosts = nil },
		"plaintext behind a TLS terminating ingress":               func(c *config.Config) { c.Server.TLS.TerminatedUpstream = true },
		"myriarchd terminates TLS itself": func(c *config.Config) {
			c.Server.TLS = config.TLSServerConfig{CertFile: "c", KeyFile: "k"}
		},
		"git over ssh": func(c *config.Config) {
			c.GitOps.RepoURL = "ssh://git@git.example.com/x.git"
			c.GitOps.Auth = config.GitAuthConfig{SSHKeyFile: "/k", KnownHostsFile: "/h"}
		},
		"git scp-like": func(c *config.Config) {
			c.GitOps.RepoURL = "git@git.example.com:otel/config.git"
			c.GitOps.Auth = config.GitAuthConfig{SSHKeyFile: "/k", KnownHostsFile: "/h"}
		},
		"unverified artifacts": func(c *config.Config) {
			c.Artifacts.Verify.RequireSignature = false
			c.Artifacts.AllowUnverified = true
		},
		"internal mirror with a forward proxy": func(c *config.Config) {
			c.Artifacts.SourceBaseURL = "https://mirror.example.com/releases"
			c.Artifacts.ProxyURL = "http://proxy.example.com:3128"
		},
		"cluster-internal agent base url": func(c *config.Config) {
			c.Artifacts.AgentBaseURLs = []string{"http://myriarch:8080", "https://fleet-internal.example.com/"}
			c.Artifacts.AllowInsecureAgentBaseURLs = true
		},
		"https agent base url": func(c *config.Config) { c.Artifacts.AgentBaseURLs = []string{"https://fleet-internal.example.com"} },
		"signed webhook channel": func(c *config.Config) {
			c.Notifications.Channels = []config.NotificationChannel{{Name: "a", Type: "webhook", URL: "https://hooks.example.com/x", SecretFile: "/s"}}
		},
		"outbound ranges": func(c *config.Config) { c.Outbound.AllowedCIDRs = []string{"10.20.0.0/16", "192.0.2.7"} },
		"acknowledged compression": func(c *config.Config) {
			c.OpAMP.EnableCompression, c.OpAMP.AllowUnboundedCompression = true, true
		},
		"bound executor secrets": func(c *config.Config) {
			c.Executors.SecretEnvHosts = map[string][]string{"MYRIARCH_EXEC_AWX_TOKEN": {"awx.example.com"}}
			c.Executors.SecretFileHosts = map[string][]string{"awx-token": {"awx.example.com"}}
		},
		"executor secret registry": func(c *config.Config) {
			c.Executors.Secrets = map[string]config.ExecutorSecret{"awx": {Env: "AWX_PROD_TOKEN", AllowedHosts: []string{"awx.example.com"}}}
		},
	} {
		c := prodConfig()
		mutate(&c)
		if err := Validate(&c, false); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The strict rules only apply in production: dev mode keeps working with local targets.
func TestDevModeKeepsLocalTargets(t *testing.T) {
	c := config.DevDefaults()
	c.GitOps.RepoURL = "file:///tmp/repo"
	c.Notifications.Channels = []config.NotificationChannel{{Name: "a", Type: "webhook", URL: "http://127.0.0.1:9999/x"}}
	if err := Validate(&c, true); err != nil {
		t.Fatalf("dev: %v", err)
	}
}

func TestSafeDefaults(t *testing.T) {
	d := config.Default()
	switch {
	case d.Auth.OIDC.DefaultRole != "":
		t.Errorf("auth.oidc.defaultRole must be empty (deny): %q", d.Auth.OIDC.DefaultRole)
	case d.OpAMP.EnableCompression:
		t.Error("websocket compression must be off by default")
	case d.OpAMP.MaxMessageSize > 4<<20:
		t.Errorf("maxMessageSize %d", d.OpAMP.MaxMessageSize)
	case d.Auth.SessionMaxAge <= 0 || d.Auth.SessionMaxAge > 24*time.Hour || d.Auth.SessionTTL > d.Auth.SessionMaxAge:
		t.Errorf("session lifetimes: %v / %v", d.Auth.SessionTTL, d.Auth.SessionMaxAge)
	case d.Artifacts.DownloadTokenTTL <= 0:
		t.Error("download tokens must expire by default")
	case !d.Artifacts.Verify.RequireChecksum || !d.Artifacts.Verify.RequireSignature || d.Artifacts.AllowUnverified:
		t.Error("artifact verification must be on by default")
	case d.Executors.SecretEnvPrefix == "" || d.Executors.MaxTimeout <= 0:
		t.Error("executor secret prefix and timeout cap must be set")
	case d.Database.StatementTimeout <= 0:
		t.Error("statement timeout must be set")
	case d.OpAMP.ConnectRatePerIP <= 0 || d.OpAMP.MaxEffectiveConfigBytes <= 0:
		t.Error("connection and message limits must be set")
	case d.GitOps.MaxRepoBytes <= 0 || d.GitOps.MaxFiles <= 0 || d.GitOps.MaxYAMLBytes <= 0:
		t.Error("GitOps limits must be set")
	case d.Server.TLS.TerminatedUpstream:
		t.Error("plaintext must not be confirmed by default")
	}
	// production refuses OIDC without any role mapping now that nobody is "default viewer"
	c := prodConfig()
	c.Auth.OIDC = config.OIDCConfig{Enabled: true, IssuerURL: "https://i", ClientID: "c", ClientSecret: "s", Scopes: []string{"openid"}, GroupsClaim: "groups", NameClaim: "name", EmailClaim: "email", DefaultRole: d.Auth.OIDC.DefaultRole}
	if err := Validate(&c, false); err == nil || !issuePaths(err)["auth.oidc.roleMapping"] {
		t.Errorf("OIDC without a role mapping must fail at startup: %v", err)
	}
}

func TestWarnings(t *testing.T) {
	c := prodConfig()
	c.Auth.OIDC = config.OIDCConfig{Enabled: true, DefaultRole: domain.RoleViewer}
	c.OpAMP.EnableCompression, c.OpAMP.AllowUnboundedCompression = true, true
	c.Artifacts.Verify.RequireSignature = false
	c.Artifacts.AllowUnverified = true
	got := strings.Join(Warnings(&c, false), "\n")
	for _, want := range []string{"auth.oidc.defaultRole", "opamp.enableCompression", "server.trustedProxies", "artifact verification is disabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning about %q missing in:\n%s", want, got)
		}
	}
	if w := Warnings(&c, true); len(w) != 0 {
		t.Errorf("no warnings in dev mode: %v", w)
	}
	c.Artifacts.AgentBaseURLs = []string{"http://myriarch:8080"}
	c.Artifacts.AllowInsecureAgentBaseURLs = true
	if got := strings.Join(Warnings(&c, false), "\n"); !strings.Contains(got, "artifacts.agentBaseUrls contains the plain http:// URL http://myriarch:8080") {
		t.Errorf("a plain http agent base URL must be reported at startup:\n%s", got)
	}
	clean := prodConfig()
	clean.Server.TrustedProxies = []string{"10.0.0.0/8"}
	if w := Warnings(&clean, false); len(w) != 0 {
		t.Errorf("clean config: %v", w)
	}
}

func TestExecutorSecretRegistryAndOutboundFromYAML(t *testing.T) {
	secretDir := absPath("/etc/myriarchd/exec")
	p := writeFile(t, "c.yaml", strings.Replace(prodMinimal, "executors:\n  allowedWebhookHosts: [\"*.example.com\"]\n", "", 1)+`
outbound:
  allowedCidrs: ["10.20.0.0/16", "192.0.2.7"]
executors:
  allowedWebhookHosts: ["awx.example.com", "http://legacy.example.com"]
  secretDir: `+secretDir+`
  secretEnvPrefix: ${PREFIX:-MYRIARCH_EXEC_}
  secrets:
    awx-prod:
      env: AWX_PROD_TOKEN
      allowedHosts: ["awx.example.com"]
    deploy-hook:
      file: `+secretDir+`/deploy
      allowedHosts: ["*.example.com"]
opamp:
  maxConnectionsPerToken: 500
  connectRatePerIp: 20
`)
	cfg, err := load(p, false, env(map[string]string{"PREFIX": "ACME_EXEC_"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Executors.Secrets["awx-prod"]; got.Env != "AWX_PROD_TOKEN" || len(got.AllowedHosts) != 1 {
		t.Errorf("registry: %+v", cfg.Executors.Secrets)
	}
	if cfg.Executors.SecretEnvPrefix != "ACME_EXEC_" || cfg.Executors.SecretDir != secretDir || len(cfg.Outbound.AllowedCIDRs) != 2 {
		t.Errorf("executors/outbound: %+v %+v", cfg.Executors, cfg.Outbound)
	}
	if cfg.OpAMP.MaxConnectionsPerToken != 500 || cfg.OpAMP.ConnectRatePerIP != 20 {
		t.Errorf("opamp: %+v", cfg.OpAMP)
	}
	// environment overrides work for the new scalar settings
	cfg2, err := load(p, false, env(map[string]string{"MYRIARCH_AUTH_SESSIONMAXAGE": "6h", "MYRIARCH_SERVER_TLS_TERMINATEDUPSTREAM": "true", "MYRIARCH_OUTBOUND_ALLOWEDCIDRS": "172.20.0.0/16"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Auth.SessionMaxAge != 6*time.Hour || len(cfg2.Outbound.AllowedCIDRs) != 1 || cfg2.Outbound.AllowedCIDRs[0] != "172.20.0.0/16" {
		t.Errorf("env overrides: %v %v", cfg2.Auth.SessionMaxAge, cfg2.Outbound.AllowedCIDRs)
	}
	// unknown keys stay errors
	bad := writeFile(t, "bad.yaml", prodMinimal+"outbound:\n  allowedCidr: [\"10.0.0.0/8\"]\n")
	if _, err := load(bad, false, env(nil)); err == nil {
		t.Error("a misspelled key must be rejected")
	}
}

// TestAgentBaseURLsFromYAML: the key is read from the file and is accepted in dev mode.
func TestAgentBaseURLsFromYAML(t *testing.T) {
	p := writeFile(t, "c.yaml", strings.Replace(prodMinimal, "artifacts:\n", `artifacts:
  agentBaseUrls: ["http://myriarch:8080", "https://fleet.example.com"]
  allowInsecureAgentBaseUrls: true
`, 1))
	cfg, err := Load(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Artifacts.AgentBaseURLs; len(got) != 2 || got[0] != "http://myriarch:8080" || !cfg.Artifacts.AllowInsecureAgentBaseURLs {
		t.Errorf("agentBaseUrls: %+v", cfg.Artifacts)
	}
	// without the switch the http entry is refused in production ...
	bad := writeFile(t, "bad.yaml", strings.Replace(prodMinimal, "artifacts:\n", `artifacts:
  agentBaseUrls: ["http://myriarch:8080"]
`, 1))
	if _, err := Load(bad, false); err == nil || !strings.Contains(err.Error(), "artifacts.agentBaseUrls[0]") {
		t.Errorf("production must refuse a plain http agent base URL: %v", err)
	}
	// ... but not in dev mode
	c := config.DevDefaults()
	c.Artifacts.AgentBaseURLs = []string{"http://myriarch:8080"}
	if err := Validate(&c, true); err != nil {
		t.Errorf("dev: %v", err)
	}
	c.Artifacts.AgentBaseURLs = []string{"http://myriarch:8080/base"}
	if err := Validate(&c, true); err == nil {
		t.Error("a path is refused in dev mode too")
	}
}

func TestUIConfig(t *testing.T) {
	def := config.Default()
	if def.UI.DefaultLanguage != "en" || def.UI.Languages != nil {
		t.Fatalf("ui defaults = %+v", def.UI)
	}
	if def.Rollouts.DefaultTimezone != "UTC" || len(def.Artifacts.PrefetchPlatforms) != 0 {
		t.Fatalf("neutral defaults = %q %v", def.Rollouts.DefaultTimezone, def.Artifacts.PrefetchPlatforms)
	}
	ok := prodConfig()
	ok.UI = config.UIConfig{DefaultLanguage: "pt-BR", Languages: []string{"en", "pt-BR", "zh-Hant-TW"}}
	if err := Validate(&ok, false); err != nil {
		t.Fatalf("valid ui config rejected: %v", err)
	}
	p := writeFile(t, "ui.yaml", strings.Replace(prodMinimal, "artifacts:\n", "artifacts:\n  prefetchPlatforms: [linux/amd64, windows/amd64]\n", 1)+"\nui:\n  defaultLanguage: de\nrollouts:\n  defaultTimezone: Europe/Amsterdam\n")
	cfg, err := load(p, false, env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.UI.DefaultLanguage != "de" || cfg.Rollouts.DefaultTimezone != "Europe/Amsterdam" || len(cfg.Artifacts.PrefetchPlatforms) != 2 {
		t.Errorf("legacy behavior not reachable: %+v %q %v", cfg.UI, cfg.Rollouts.DefaultTimezone, cfg.Artifacts.PrefetchPlatforms)
	}
}
