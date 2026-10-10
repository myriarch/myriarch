// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
)

// prodYAML is the production configuration of the test environment: Entra ID
// with app roles, mirror credentials and the internal CA. The files it refers
// to do not exist.
const prodYAML = `
server:
  publicUrl: https://myriarch-test.example.com
  trustedProxies: [10.244.0.0/16]
  tls:
    terminatedUpstream: true
database:
  dsnFile: /nonexistent/secrets/database-dsn
auth:
  oidc:
    enabled: true
    issuerUrl: https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0
    clientId: 00000000-0000-0000-0000-000000000001
    clientSecretFile: /nonexistent/secrets/oidc-client-secret
    groupsClaim: roles
    roleMapping:
      admin: [myriarch.admin]
gitops:
  repoUrl: https://gitlab.example.com/observability/myriarch-gitops.git
  path: config
  cloneDir: /var/lib/myriarchd/gitops
artifacts:
  dir: /var/lib/myriarchd/artifacts
  sourceBaseUrl: https://artifactory.example.com/artifactory/otelcol-github-releases
  sourceAuth:
    tokenFile: /nonexistent/secrets/mirror-token
outbound:
  caFile: /nonexistent/ca/ca.crt
executors:
  allowedWebhookHosts: [awx.example.com]
`

func runMyriarchd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = execute(args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "myriarchd.yaml")
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

// FX-4: exit codes, output format and the file handling of "myriarchd config validate".
func TestConfigValidate(t *testing.T) {
	good := writeConfig(t, prodYAML)

	// valid with --skip-files: the files are not read
	code, out, errs := runMyriarchd(t, "config", "validate", "--config", good, "--skip-files")
	if code != 0 || out != "OK: "+good+" is valid\n" || errs != "" {
		t.Fatalf("skip-files: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	// ... the same with -c
	if code, _, _ = runMyriarchd(t, "config", "validate", "-c", good, "--skip-files"); code != 0 {
		t.Fatalf("short flag: exit %d", code)
	}

	// without --skip-files the missing files are reported, one ERROR line each, nothing on stdout
	code, out, errs = runMyriarchd(t, "config", "validate", "--config", good)
	if code != 1 || out != "" {
		t.Fatalf("missing files: exit %d, stdout %q", code, out)
	}
	lines := strings.Split(strings.TrimSpace(errs), "\n")
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"ERROR: database.dsnFile: cannot read /nonexistent/secrets/database-dsn",
		"ERROR: auth.oidc.clientSecretFile: cannot read /nonexistent/secrets/oidc-client-secret",
		"ERROR: artifacts.sourceAuth.tokenFile: cannot read /nonexistent/secrets/mirror-token",
		"ERROR: outbound.caFile: cannot read /nonexistent/ca/ca.crt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing in\n%s", want, got)
		}
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "ERROR: ") {
			t.Errorf("every line is an ERROR line: %q", l)
		}
	}

	// Entra: the tenant domain instead of the GUID
	contoso := writeConfig(t, strings.Replace(prodYAML, "11111111-2222-3333-4444-555555555555", "contoso.onmicrosoft.com", 1))
	code, out, errs = runMyriarchd(t, "config", "validate", "--config", contoso, "--skip-files")
	if code != 1 || out != "" || !strings.HasPrefix(errs, "ERROR: auth.oidc.issuerUrl: Entra ID needs the tenant ID (GUID)") || strings.Count(errs, "\n") != 1 {
		t.Fatalf("entra: exit %d, stdout %q, stderr %q", code, out, errs)
	}

	// YAML: unknown key, syntax error
	code, _, errs = runMyriarchd(t, "config", "validate", "--config", writeConfig(t, prodYAML+"nosuchkey: 1\n"), "--skip-files")
	if code != 1 || !strings.Contains(errs, "ERROR: ") || !strings.Contains(errs, "field nosuchkey not found") {
		t.Fatalf("unknown key: exit %d, stderr %q", code, errs)
	}
	code, _, errs = runMyriarchd(t, "config", "validate", "--config", writeConfig(t, "server: [unclosed"), "--skip-files")
	if code != 1 || !strings.HasPrefix(errs, "ERROR: ") {
		t.Fatalf("syntax error: exit %d, stderr %q", code, errs)
	}

	// usage errors: exit 2
	for name, args := range map[string][]string{
		"no --config":    {"config", "validate"},
		"missing file":   {"config", "validate", "--config", filepath.Join(t.TempDir(), "missing.yaml")},
		"unknown flag":   {"config", "validate", "--config", good, "--no-such-flag"},
		"extra argument": {"config", "validate", "--config", good, "extra"},
	} {
		code, out, errs = runMyriarchd(t, args...)
		if name == "extra argument" {
			if code == 0 {
				t.Errorf("%s: exit %d", name, code)
			}
			continue
		}
		if code != 2 || out != "" || errs == "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, out, errs)
		}
	}
	if _, _, errs = runMyriarchd(t, "config", "validate"); !strings.Contains(errs, "--config") {
		t.Errorf("the message names the missing flag: %q", errs)
	}
}

func TestConfigValidateWarnings(t *testing.T) {
	// valid, but Entra with group claims: the warning does not change the exit code
	y := strings.Replace(prodYAML, "groupsClaim: roles", "groupsClaim: groups", 1)
	code, out, errs := runMyriarchd(t, "config", "validate", "--config", writeConfig(t, y), "--skip-files")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "OK: ") || !strings.HasPrefix(lines[1], "WARNING: auth.oidc.groupsClaim") {
		t.Fatalf("stdout:\n%s", out)
	}
}

// Environment overrides apply like for serve: a MYRIARCH_* variable can make an
// invalid file valid and the other way round.
func TestConfigValidateAppliesEnvironmentOverrides(t *testing.T) {
	p := writeConfig(t, strings.Replace(prodYAML, "11111111-2222-3333-4444-555555555555", "contoso.onmicrosoft.com", 1))
	t.Setenv("MYRIARCH_AUTH_OIDC_ISSUERURL", "https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0")
	if code, _, errs := runMyriarchd(t, "config", "validate", "--config", p, "--skip-files"); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	t.Setenv("MYRIARCH_OUTBOUND_CAFILE", "")
	t.Setenv("MYRIARCH_ARTIFACTS_SOURCEAUTH_USERNAME", "svc") // token and basic at once
	code, _, errs := runMyriarchd(t, "config", "validate", "--config", p, "--skip-files")
	if code != 1 || !strings.Contains(errs, "ERROR: artifacts.sourceAuth: ") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}

func TestConfigPrintRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "dsn")
	secret := filepath.Join(dir, "secret")
	_ = os.WriteFile(dsn, []byte("postgres://myriarch:hunter2@pg.example.com/myriarch\n"), 0o600)
	_ = os.WriteFile(secret, []byte("client-secret-value\n"), 0o600)
	y := strings.NewReplacer("/nonexistent/secrets/database-dsn", dsn, "/nonexistent/secrets/oidc-client-secret", secret).Replace(prodYAML)
	code, out, errs := runMyriarchd(t, "config", "print", "--config", writeConfig(t, y), "--skip-files")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, leak := range []string{"hunter2", "client-secret-value"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q must not be printed", leak)
		}
	}
	if !strings.Contains(out, "publicUrl: https://myriarch-test.example.com") || !strings.Contains(out, "groupsClaim: roles") {
		t.Errorf("effective configuration expected:\n%s", out)
	}
	// secrets are read without --skip-files (and still not printed)
	_ = os.WriteFile(filepath.Join(dir, "ca.crt"), nil, 0o600)
	y2 := strings.Replace(y, "/nonexistent/ca/ca.crt", "", 1)
	y2 = strings.Replace(y2, "  caFile: \n", "", 1)
	y2 = strings.Replace(y2, "    tokenFile: /nonexistent/secrets/mirror-token\n", "    tokenFile: "+secret+"\n", 1)
	code, out, errs = runMyriarchd(t, "config", "print", "--config", writeConfig(t, y2))
	if code != 0 || strings.Contains(out, "hunter2") || strings.Contains(out, "client-secret-value") {
		t.Fatalf("exit %d: %s\n%s", code, errs, out)
	}
	// invalid configuration and usage errors: like validate
	if code, _, _ = runMyriarchd(t, "config", "print", "--config", writeConfig(t, "bogus: 1"), "--skip-files"); code != 1 {
		t.Errorf("invalid: exit %d", code)
	}
	if code, _, _ = runMyriarchd(t, "config", "print"); code != 2 {
		t.Errorf("no --config: exit %d", code)
	}
}

// FX-5: outbound.caFile reaches the HTTP clients that run() builds from the policy.
func TestOutboundPolicyTrustsCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)

	get := func(p interface {
		Client(time.Duration) *http.Client
	}) error {
		resp, err := p.Client(5 * time.Second).Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	// production: the CIDR of the (loopback) test server is opened explicitly
	without, err := newOutboundPolicy(config.OutboundConfig{AllowedCIDRs: []string{"127.0.0.1"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := get(without); err == nil {
		t.Fatal("without outbound.caFile the certificate of an unknown CA must be refused")
	}
	with, err := newOutboundPolicy(config.OutboundConfig{AllowedCIDRs: []string{"127.0.0.1"}, CAFile: caFile}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := get(with); err != nil {
		t.Fatalf("with outbound.caFile: %v", err)
	}
	// dev mode takes it as well
	dev, err := newOutboundPolicy(config.OutboundConfig{CAFile: caFile}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := get(dev); err != nil {
		t.Fatalf("dev: %v", err)
	}

	// a file that is unreadable or holds no certificate stops the start
	bad := filepath.Join(t.TempDir(), "bad.crt")
	_ = os.WriteFile(bad, []byte("nothing"), 0o600)
	for _, f := range []string{bad, filepath.Join(t.TempDir(), "missing.crt")} {
		if _, err := newOutboundPolicy(config.OutboundConfig{CAFile: f}, false); err == nil || !strings.Contains(err.Error(), "outbound.caFile") {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// myriarchd config validate and print know artifacts.agentBaseUrls.
func TestConfigAgentBaseURLs(t *testing.T) {
	y := strings.Replace(prodYAML, "artifacts:\n", "artifacts:\n  agentBaseUrls: [\"http://myriarch:8080\"]\n  allowInsecureAgentBaseUrls: true\n", 1)
	code, out, errs := runMyriarchd(t, "config", "validate", "--config", writeConfig(t, y), "--skip-files")
	if code != 0 || errs != "" {
		t.Fatalf("validate: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runMyriarchd(t, "config", "print", "--config", writeConfig(t, y), "--skip-files")
	if code != 0 || !strings.Contains(out, "agentBaseUrls") || !strings.Contains(out, "http://myriarch:8080") {
		t.Fatalf("print must show the key: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	// without the switch the plain http entry is refused in production
	y2 := strings.Replace(prodYAML, "artifacts:\n", "artifacts:\n  agentBaseUrls: [\"http://myriarch:8080\"]\n", 1)
	code, _, errs = runMyriarchd(t, "config", "validate", "--config", writeConfig(t, y2), "--skip-files")
	if code != 1 || !strings.Contains(errs, "ERROR: artifacts.agentBaseUrls[0]") {
		t.Fatalf("validate without the switch: exit %d, stderr %q", code, errs)
	}
}
