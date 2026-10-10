// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
)

const tenant = "11111111-2222-3333-4444-555555555555"

// pemFiles writes a self-signed certificate (RSA) and its key (PKCS#8) and
// returns the paths. The certificate also serves as a CA file.
func pemFiles(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "myriarch test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certFile, keyFile
}

// phase0Files are the secret and CA files of the test environment.
type phase0Files struct {
	dir                                              string
	dsn, secret, git, webhook, github, mirror, teams string
	download, ca                                     string
	cert, key                                        string
}

func newPhase0Files(t *testing.T) phase0Files {
	t.Helper()
	f := phase0Files{dir: t.TempDir()}
	w := func(name, content string) string {
		p := filepath.Join(f.dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.dsn = w("database-dsn", "postgres://myriarch:pw@pg.example.com:5432/myriarch?sslmode=verify-full\n")
	f.secret = w("oidc-client-secret", "oidc-secret\n")
	f.git = w("git-token", "git-token\n")
	f.webhook = w("gitops-webhook-secret", "s3cr3t-webhook-value\n")
	f.github = w("github-token", "github-token\n")
	f.mirror = w("mirror-token", "mirror-token\n")
	f.teams = w("teams-webhook-url", "https://prod-00.westeurope.logic.azure.com/workflows/abc\n")
	f.download = w("download-token-secret", strings.Repeat("k", 40)+"\n")
	f.cert, f.key = pemFiles(t)
	f.ca = f.cert
	return f
}

// yaml is the production configuration of the test environment (Entra ID with
// app roles, mirror credentials, internal CA, Teams) referring to the files.
func (f phase0Files) yaml() string {
	return `
server:
  publicUrl: https://myriarch-test.example.com
  trustedProxies: [10.244.0.0/16]
  tls:
    terminatedUpstream: true
database:
  dsnFile: ` + f.dsn + `
auth:
  oidc:
    enabled: true
    issuerUrl: https://login.microsoftonline.com/` + tenant + `/v2.0
    clientId: 00000000-0000-0000-0000-000000000001
    clientSecretFile: ` + f.secret + `
    groupsClaim: roles
    roleMapping:
      admin: [myriarch.admin]
      operator: [myriarch.operator]
      viewer: [myriarch.viewer]
  downloadTokenSecretFile: ` + f.download + `
gitops:
  repoUrl: https://gitlab.example.com/observability/myriarch-gitops.git
  path: config
  cloneDir: /var/lib/myriarchd/gitops
  auth:
    username: deploy
    tokenFile: ` + f.git + `
  webhookSecretFile: ` + f.webhook + `
releases:
  tokenFile: ` + f.github + `
artifacts:
  dir: /var/lib/myriarchd/artifacts
  sourceBaseUrl: https://artifactory.example.com/artifactory/otelcol-github-releases
  sourceAuth:
    tokenFile: ` + f.mirror + `
  verify:
    tufRepositoryUrl: https://artifactory.example.com/artifactory/sigstore-tuf
outbound:
  caFile: ` + f.ca + `
  allowedCidrs: [10.20.1.10/32]
executors:
  allowedWebhookHosts: [awx.example.com]
notifications:
  channels:
    - name: teams
      type: teams
      urlFile: ` + f.teams + `
      events: ["release.*", "rollout.*"]
`
}

func loadOpts(t *testing.T, content string, o Options) (config.Config, []string, error) {
	t.Helper()
	return loadWith(writeFile(t, "myriarchd.yaml", content), o, env(nil))
}

func TestPhase0ProductionConfigIsValid(t *testing.T) {
	f := newPhase0Files(t)
	for name, o := range map[string]Options{
		"files checked": {CheckFiles: true},
		"serve":         {},
		"skip files":    {SkipFiles: true},
	} {
		cfg, warnings, err := loadOpts(t, f.yaml(), o)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(warnings) != 0 {
			t.Errorf("%s: unexpected warnings %v", name, warnings)
		}
		if cfg.Outbound.CAFile != f.ca || cfg.Artifacts.SourceAuth.TokenFile != f.mirror || cfg.Artifacts.Verify.TUFRepositoryURL == "" {
			t.Errorf("%s: new keys not decoded: %+v %+v", name, cfg.Outbound, cfg.Artifacts)
		}
		if o.SkipFiles && cfg.Database.DSN != "" {
			t.Errorf("%s: the secret files must not be read", name)
		}
	}
}

func TestPhase0SkipFilesDoesNotNeedTheFiles(t *testing.T) {
	var y string
	y = `
server:
  publicUrl: https://myriarch-test.example.com
  tls:
    terminatedUpstream: true
database:
  dsnFile: /etc/myriarchd/secrets/database-dsn
auth:
  oidc:
    enabled: true
    issuerUrl: https://login.microsoftonline.com/` + tenant + `/v2.0
    clientId: c
    clientCertificateFile: /etc/myriarchd/secrets/oidc-client-cert
    clientKeyFile: /etc/myriarchd/secrets/oidc-client-key
    groupsClaim: roles
    roleMapping: {admin: [myriarch.admin]}
  downloadTokenSecretFile: /etc/myriarchd/secrets/download-token-secret
gitops:
  repoUrl: https://gitlab.example.com/x.git
  auth: {username: u, tokenFile: /etc/myriarchd/secrets/git-token}
  webhookSecretFile: /etc/myriarchd/secrets/gitops-webhook-secret
releases: {tokenFile: /etc/myriarchd/secrets/github-token}
artifacts:
  sourceAuth: {username: svc, passwordFile: /etc/myriarchd/secrets/mirror-password}
  verify: {trustedRootFile: /etc/myriarchd/ca/trusted_root.json}
outbound: {caFile: /etc/myriarchd/ca/ca.crt}
executors: {allowedWebhookHosts: [awx.example.com]}
notifications:
  channels:
    - {name: teams, type: teams, urlFile: /etc/myriarchd/secrets/teams-webhook-url}
    - {name: hook, type: webhook, url: "https://hooks.example.com/x", secretFile: /etc/myriarchd/secrets/hook-secret}
`
	// the files are absolute paths of the running platform (on Windows with a volume)
	y = strings.ReplaceAll(y, "/etc/myriarchd/", absPath("/etc/myriarchd/"))
	if _, _, err := loadOpts(t, y, Options{SkipFiles: true}); err != nil {
		t.Fatalf("--skip-files: %v", err)
	}
	// without --skip-files every missing file is named, at once
	_, _, err := loadOpts(t, y, Options{CheckFiles: true})
	if err == nil {
		t.Fatal("missing files must be reported")
	}
	// some messages end with the text of the operating system and omit the path
	reason := missingFileReason(t, absPath("/etc/myriarchd/secrets/database-dsn"))
	got := map[string]bool{}
	for _, p := range Problems(err) {
		got[p.Path] = true
		if !strings.Contains(p.Message, reason) && !strings.Contains(p.Message, "cannot read") {
			t.Errorf("%s: %s", p.Path, p.Message)
		}
	}
	for _, want := range []string{
		"database.dsnFile", "gitops.webhookSecretFile", "notifications.channels[0].urlFile",
		"auth.oidc.clientCertificateFile", "auth.downloadTokenSecretFile", "gitops.auth.tokenFile", "releases.tokenFile",
		"artifacts.sourceAuth.passwordFile", "artifacts.verify.trustedRootFile", "outbound.caFile", "notifications.channels[1]",
	} {
		if !got[want] {
			t.Errorf("no problem reported for %s (got %v)", want, got)
		}
	}
}

func TestPhase0Invalid(t *testing.T) {
	f := newPhase0Files(t)
	for _, tc := range []struct {
		name, from, to, path string
	}{
		{"Entra: domain name instead of the GUID", "login.microsoftonline.com/" + tenant + "/v2.0", "login.microsoftonline.com/contoso.onmicrosoft.com/v2.0", "auth.oidc.issuerUrl"},
		{"Entra: common", "login.microsoftonline.com/" + tenant + "/v2.0", "login.microsoftonline.com/common/v2.0", "auth.oidc.issuerUrl"},
		{"Entra: v1 endpoint", "/" + tenant + "/v2.0", "/" + tenant, "auth.oidc.issuerUrl"},
		{"Entra: trailing slash", "/" + tenant + "/v2.0", "/" + tenant + "/v2.0/", "auth.oidc.issuerUrl"},
		{"secret and certificate", "    clientSecretFile: " + f.secret, "    clientSecretFile: " + f.secret + "\n    clientCertificateFile: " + f.cert + "\n    clientKeyFile: " + f.key, "auth.oidc.clientCertificateFile"},
		{"certificate without key", "    clientSecretFile: " + f.secret, "    clientCertificateFile: " + f.cert, "auth.oidc.clientCertificateFile"},
		{"sourceAuth: token and basic", "    tokenFile: " + f.mirror, "    tokenFile: " + f.mirror + "\n    username: svc\n    passwordFile: " + f.mirror, "artifacts.sourceAuth"},
		{"sourceAuth: user without password", "    tokenFile: " + f.mirror, "    username: svc", "artifacts.sourceAuth"},
		{"TUF repository over http", "tufRepositoryUrl: https://", "tufRepositoryUrl: http://", "artifacts.verify.tufRepositoryUrl"},
		{"TUF repository not a URL", "tufRepositoryUrl: https://artifactory.example.com/artifactory/sigstore-tuf", "tufRepositoryUrl: sigstore-tuf", "artifacts.verify.tufRepositoryUrl"},
		{"CA file without certificate", "  caFile: " + f.ca, "  caFile: " + f.git, "outbound.caFile"},
		{"unknown platform", "artifacts:\n", "artifacts:\n  prefetchPlatforms: [linux]\n", "artifacts.prefetchPlatforms[0]"},
		{"unknown operating system", "artifacts:\n", "artifacts:\n  prefetchPlatforms: [plan9/amd64]\n", "artifacts.prefetchPlatforms[0]"},
		{"bad min version", "releases:\n", "releases:\n  minVersion: latest\n", "releases.minVersion"},
		{"unknown distribution", "releases:\n", "releases:\n  distributions: [otelcol]\n", "releases.distributions"},
		{"repository not owner/name", "releases:\n", "releases:\n  coreRepo: opentelemetry-collector\n", "releases.coreRepo"},
		{"identity regexp does not compile", "  verify:\n", "  verify:\n    certificateIdentityRegexp: '(unclosed'\n", "artifacts.verify.certificateIdentityRegexp"},
		{"proxy url", "artifacts:\n", "artifacts:\n  proxyUrl: proxy.example.com:3128\n", "artifacts.proxyUrl"},
		{"unknown channel event", "events: [\"release.*\", \"rollout.*\"]", "events: [\"no.such.event\"]", "notifications.channels[0]"},
		{"download secret too short", strings.Repeat("k", 40), "short", "auth.downloadTokenSecretFile"},
		{"invalid dsn", "postgres://myriarch:pw@pg.example.com:5432/myriarch?sslmode=verify-full", "postgres://myriarch:pw@pg.example.com:5432/myriarch?sslmode=nonsense", "database.dsn"},
	} {
		y := f.yaml()
		if tc.name == "download secret too short" || tc.name == "invalid dsn" {
			// these live in files: rewrite the file
			target := f.download
			if tc.name == "invalid dsn" {
				target = f.dsn
			}
			orig, _ := os.ReadFile(target)
			_ = os.WriteFile(target, []byte(strings.Replace(string(orig), tc.from, tc.to, 1)), 0o600)
			defer os.WriteFile(target, orig, 0o600)
		} else {
			if !strings.Contains(y, tc.from) {
				t.Fatalf("%s: %q not in the test configuration", tc.name, tc.from)
			}
			y = strings.Replace(y, tc.from, tc.to, 1)
		}
		_, _, err := loadOpts(t, y, Options{CheckFiles: true})
		if err == nil {
			t.Errorf("%s: must be invalid", tc.name)
			continue
		}
		found := false
		for _, p := range Problems(err) {
			found = found || p.Path == tc.path
		}
		if !found {
			t.Errorf("%s: no problem for %s: %v", tc.name, tc.path, err)
		}
	}
}

func TestEntraIssuerMessageNamesTheFormat(t *testing.T) {
	f := newPhase0Files(t)
	y := strings.Replace(f.yaml(), tenant+"/v2.0", "contoso.onmicrosoft.com/v2.0", 1)
	_, _, err := loadOpts(t, y, Options{SkipFiles: true})
	if err == nil {
		t.Fatal("must be invalid")
	}
	msg := err.Error()
	for _, want := range []string{"Entra ID needs the tenant ID (GUID)", "https://login.microsoftonline.com/<TENANT-GUID>/v2.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q missing in %s", want, msg)
		}
	}
	// the check also applies in dev mode, and not to other identity providers
	c := config.DevDefaults()
	c.Auth.OIDC = config.OIDCConfig{Enabled: true, IssuerURL: "https://login.microsoftonline.com/common/v2.0", ClientID: "c", ClientSecret: "s",
		Scopes: []string{"openid"}, GroupsClaim: "roles", NameClaim: "name", EmailClaim: "email", DefaultRole: "viewer"}
	if err := Validate(&c, true); err == nil || !issuePaths(err)["auth.oidc.issuerUrl"] {
		t.Errorf("dev mode: %v", err)
	}
	c.Auth.OIDC.IssuerURL = "https://keycloak.example.com/realms/otel"
	if err := Validate(&c, true); err != nil {
		t.Errorf("other providers are not constrained: %v", err)
	}
	for _, ok := range []string{
		"https://login.microsoftonline.com/" + tenant + "/v2.0",
		"https://login.microsoftonline.com/" + strings.ToUpper(tenant) + "/v2.0",
	} {
		u, _ := url.Parse(ok)
		if m := checkEntraIssuer(u); m != "" {
			t.Errorf("%s: %s", ok, m)
		}
	}
}

func TestPhase0Warnings(t *testing.T) {
	f := newPhase0Files(t)
	// Entra with a groups claim: group overage
	y := strings.Replace(f.yaml(), "groupsClaim: roles", "groupsClaim: groups", 1)
	_, warnings, err := loadOpts(t, y, Options{SkipFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "group overage") || !strings.Contains(warnings[0], "app roles") {
		t.Errorf("warnings: %v", warnings)
	}
	// the default claim "groups" (nothing set) is the same case
	y = strings.Replace(f.yaml(), "    groupsClaim: roles\n", "", 1)
	_, warnings, _ = loadOpts(t, y, Options{SkipFiles: true})
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"groups"`) {
		t.Errorf("default claim: %v", warnings)
	}
	// other identity providers: no warning
	y = strings.Replace(strings.Replace(f.yaml(), "groupsClaim: roles", "groupsClaim: groups", 1), "login.microsoftonline.com/"+tenant+"/v2.0", "keycloak.example.com/realms/otel", 1)
	if _, warnings, _ = loadOpts(t, y, Options{SkipFiles: true}); len(warnings) != 0 {
		t.Errorf("keycloak: %v", warnings)
	}
	// mirror credentials with the public GitHub URL
	y = strings.Replace(f.yaml(), "  sourceBaseUrl: https://artifactory.example.com/artifactory/otelcol-github-releases\n", "", 1)
	_, warnings, _ = loadOpts(t, y, Options{SkipFiles: true})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "github.com") {
		t.Errorf("sourceAuth with the GitHub URL: %v", warnings)
	}
}

func TestPhase0EnvOverrides(t *testing.T) {
	cfg, err := load(writeFile(t, "c.yaml", prodMinimal), false, env(map[string]string{
		"MYRIARCH_OUTBOUND_CAFILE":                   "/etc/myriarchd/ca/ca.crt",
		"MYRIARCH_ARTIFACTS_SOURCEAUTH_TOKENFILE":    "/etc/myriarchd/secrets/mirror-token",
		"MYRIARCH_ARTIFACTS_SOURCEAUTH_USERNAME":     "",
		"MYRIARCH_ARTIFACTS_VERIFY_TUFREPOSITORYURL": "https://mirror.example.com/sigstore-tuf",
		"MYRIARCH_AUTH_OIDC_CLIENTCERTIFICATEFILE":   "",
		"MYRIARCH_AUTH_OIDC_CLIENTKEYFILE":           "",
		"MYRIARCH_ARTIFACTS_SOURCEAUTH_PASSWORDFILE": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outbound.CAFile != "/etc/myriarchd/ca/ca.crt" || cfg.Artifacts.SourceAuth.TokenFile != "/etc/myriarchd/secrets/mirror-token" ||
		cfg.Artifacts.Verify.TUFRepositoryURL != "https://mirror.example.com/sigstore-tuf" {
		t.Errorf("overrides not applied: %+v / %+v", cfg.Outbound, cfg.Artifacts)
	}
	cfg, err = load("", true, env(map[string]string{
		"MYRIARCH_AUTH_OIDC_CLIENTCERTIFICATEFILE": "/c", "MYRIARCH_AUTH_OIDC_CLIENTKEYFILE": "/k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.OIDC.ClientCertificateFile != "/c" || cfg.Auth.OIDC.ClientKeyFile != "/k" {
		t.Errorf("oidc override: %+v", cfg.Auth.OIDC)
	}
}

func TestProblems(t *testing.T) {
	// unknown key and syntax error: one problem per finding, named by file
	p := writeFile(t, "c.yaml", prodMinimal+"bogus: 1\nopamp:\n  nosuchkey: 2\n")
	_, _, err := loadWith(p, Options{}, env(nil))
	if err == nil {
		t.Fatal("unknown keys must be an error")
	}
	probs := Problems(err)
	if len(probs) != 2 {
		t.Fatalf("one problem per unknown key: %v", probs)
	}
	for _, pr := range probs {
		if pr.Path != p || !strings.Contains(pr.Message, "not found in type") {
			t.Errorf("problem: %+v", pr)
		}
	}
	_, _, err = loadWith(writeFile(t, "c.yaml", "server: [unclosed"), Options{}, env(nil))
	if probs := Problems(err); len(probs) != 1 || !strings.Contains(probs[0].Message, "did not find") && !strings.Contains(probs[0].Message, "line") {
		t.Errorf("yaml error: %v", probs)
	}
	// a file that cannot be read is a ReadError, not a validation problem
	_, _, err = loadWith(filepath.Join(t.TempDir(), "missing.yaml"), Options{}, env(nil))
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("want ReadError, got %v", err)
	}
	// validation problems: path and message separately
	c := prodConfig()
	c.Database.DSN = ""
	probs = Problems(Validate(&c, false))
	if len(probs) != 1 || probs[0].Path != "database.dsn" || strings.HasPrefix(probs[0].Message, "database.dsn") || probs[0].String() != "database.dsn: "+probs[0].Message {
		t.Errorf("validation problem: %+v", probs)
	}
	if Problems(nil) != nil {
		t.Error("no error, no problems")
	}
}

func TestEmptySecretFileIsAnError(t *testing.T) {
	f := newPhase0Files(t)
	_ = os.WriteFile(f.dsn, []byte(" \n"), 0o600)
	_, _, err := loadOpts(t, f.yaml(), Options{})
	if err == nil {
		t.Fatal("an empty DSN file must stop the start")
	}
	probs := Problems(err)
	if probs[0].Path != "database.dsnFile" || !strings.Contains(probs[0].Message, "is empty") {
		t.Errorf("%+v", probs)
	}
}

func TestClientCertificateAuthentication(t *testing.T) {
	f := newPhase0Files(t)
	y := strings.Replace(f.yaml(), "    clientSecretFile: "+f.secret+"\n", "    clientCertificateFile: "+f.cert+"\n    clientKeyFile: "+f.key+"\n", 1)
	if _, _, err := loadOpts(t, y, Options{CheckFiles: true}); err != nil {
		t.Fatalf("certificate instead of a secret: %v", err)
	}
	// a key that does not belong to the certificate
	_, otherKey := pemFiles(t)
	y2 := strings.Replace(y, f.key, otherKey, 1)
	_, _, err := loadOpts(t, y2, Options{CheckFiles: true})
	if err == nil || !strings.Contains(err.Error(), "no certificate matches the private key") {
		t.Errorf("mismatching key: %v", err)
	}
	// ... which --skip-files does not look at
	if _, _, err := loadOpts(t, y2, Options{SkipFiles: true}); err != nil {
		t.Errorf("skip files: %v", err)
	}
}

func TestRedactedYAML(t *testing.T) {
	f := newPhase0Files(t)
	cfg, _, err := loadOpts(t, f.yaml(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Notifications.Channels = append(cfg.Notifications.Channels, config.NotificationChannel{Name: "w", Type: "webhook", URL: "https://hooks.example.com/secret-path", SecretFile: "/s"})
	out, err := RedactedYAML(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, secret := range []string{"pw@pg", "oidc-secret", "s3cr3t-webhook-value", "logic.azure.com", "secret-path"} {
		if strings.Contains(s, secret) {
			t.Errorf("secret %q is printed:\n%s", secret, s)
		}
	}
	for _, want := range []string{"<redacted>", "publicUrl: https://myriarch-test.example.com", "caFile: " + f.ca, "groupsClaim: roles", "shutdownTimeout: 30s"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q missing:\n%s", want, s)
		}
	}
	// the original is untouched
	if cfg.Database.DSN == "<redacted>" || cfg.Notifications.Channels[1].URL != "https://hooks.example.com/secret-path" {
		t.Error("RedactedYAML must not modify its argument")
	}
}

// `cosign trusted-root create` without flags writes a document without any trust
// material; sigstore-go loads it, but every verification would fail later.
func TestEmptyTrustedRootIsRejected(t *testing.T) {
	f := newPhase0Files(t)
	empty := filepath.Join(f.dir, "trusted_root.json")
	if err := os.WriteFile(empty, []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(f.yaml(), "  verify:\n", "  verify:\n    trustedRootFile: "+empty+"\n", 1)
	_, _, err := loadOpts(t, y, Options{CheckFiles: true})
	if err == nil {
		t.Fatal("an empty trusted root must be rejected")
	}
	found := false
	for _, p := range Problems(err) {
		if p.Path == "artifacts.verify.trustedRootFile" && strings.Contains(p.Message, "no trust material") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no trusted root problem: %v", err)
	}
}
