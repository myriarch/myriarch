// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func randomSecret(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// runWithStdin runs myriarchd with the given standard input.
func runWithStdin(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	root := newRootCommand()
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errb)
	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return ee.code, out.String(), ee.msg
		}
		return 1, out.String(), err.Error()
	}
	return 0, out.String(), errb.String()
}

func TestAuthHashPassword(t *testing.T) {
	pw := "p-" + randomSecret(t, 10)
	for name, input := range map[string]string{
		"without a line break": pw,
		"with a line break":    pw + "\n",
		"with CRLF":            pw + "\r\n",
		"only the first line":  pw + "\nsecond line\n",
	} {
		code, out, errs := runWithStdin(t, input, "auth", "hash-password")
		if code != 0 || errs != "" || !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
			t.Fatalf("%s: exit %d stdout %q stderr %q", name, code, out, errs)
		}
		hash := strings.TrimSuffix(out, "\n")
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)); err != nil {
			t.Errorf("%s: the output does not verify: %v", name, err)
		}
		if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 12 {
			t.Errorf("%s: default cost %d (%v), want 12", name, cost, err)
		}
		if msg := domain.BootstrapHashProblem(hash); msg != "" {
			t.Errorf("%s: the server would refuse the hash: %s", name, msg)
		}
		if strings.Contains(out, pw) || strings.Contains(errs, pw) {
			t.Errorf("%s: the password must never be printed", name)
		}
	}
}

func TestAuthHashPasswordCost(t *testing.T) {
	pw := "p-" + randomSecret(t, 10)
	for _, cost := range []string{"10", "11", "14"} {
		code, out, _ := runWithStdin(t, pw, "auth", "hash-password", "--cost", cost)
		if code != 0 {
			t.Fatalf("cost %s: exit %d", cost, code)
		}
		got, err := bcrypt.Cost([]byte(strings.TrimSpace(out)))
		if err != nil || got != map[string]int{"10": 10, "11": 11, "14": 14}[cost] {
			t.Errorf("cost %s: %d (%v)", cost, got, err)
		}
	}
	for _, cost := range []string{"9", "4", "15", "31", "0", "-1"} {
		code, out, errs := runWithStdin(t, pw, "auth", "hash-password", "--cost", cost)
		if code != 1 || out != "" || !strings.Contains(errs, "--cost must be between 10 and 14") {
			t.Errorf("cost %s: exit %d stdout %q stderr %q", cost, code, out, errs)
		}
	}
	// a flag that is not a number is a usage error
	if code, _, _ := runWithStdin(t, pw, "auth", "hash-password", "--cost", "high"); code != 2 {
		t.Errorf("--cost high: exit %d, want 2", code)
	}
	if code, _, _ := runWithStdin(t, pw, "auth", "hash-password", "--unknown"); code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
	if code, _, _ := runWithStdin(t, pw, "auth", "hash-password", "extra"); code != 2 {
		t.Errorf("an argument: exit %d, want 2", code)
	}
}

func TestAuthHashPasswordRejectsBadInput(t *testing.T) {
	for name, input := range map[string]string{
		"empty":             "",
		"only a line break": "\n",
		"too short":         strings.Repeat("a", 11) + "\n",
		"too long":          strings.Repeat("a", 73) + "\n",
		"far too long":      strings.Repeat("a", 5000),
	} {
		code, out, errs := runWithStdin(t, input, "auth", "hash-password")
		if code != 1 || out != "" || !strings.Contains(errs, "the password must have 12 to 72 bytes") {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, code, out, errs)
		}
	}
	// the limits themselves are fine
	for _, n := range []int{12, 72} {
		if code, _, errs := runWithStdin(t, strings.Repeat("a", n), "auth", "hash-password"); code != 0 {
			t.Errorf("%d bytes: exit %d %q", n, code, errs)
		}
	}
	// a rejected password is not echoed
	secret := "S" + randomSecret(t, 4)
	_, out, errs := runWithStdin(t, secret, "auth", "hash-password")
	if strings.Contains(out, secret) || strings.Contains(errs, secret) {
		t.Error("the rejected password must not be echoed")
	}
}

// The hash of the command works as the password file of a bootstrap admin.
func TestAuthHashPasswordOutputIsAUsablePasswordFile(t *testing.T) {
	pw := "p-" + randomSecret(t, 10)
	_, out, _ := runWithStdin(t, pw, "auth", "hash-password", "--cost", "10")
	file := filepath.Join(t.TempDir(), "password-hash")
	if err := os.WriteFile(file, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := noLoginYAML + "auth:\n  bootstrapAdmin:\n    enabled: true\n    passwordHashFile: " + file + "\n"
	if code, outv, errs := runMyriarchd(t, "config", "validate", "--config", writeConfig(t, cfg)); code != 0 || errs != "" {
		t.Fatalf("validate: exit %d, stdout %q, stderr %q", code, outv, errs)
	}
}

// ------------------------------------------------------ config validate ---

const noLoginYAML = `
server:
  publicUrl: https://myriarch-test.example.com
  tls:
    terminatedUpstream: true
database:
  dsn: postgres://u:p@db/myriarch
gitops:
  repoUrl: https://git.example.com/otel/config.git
`

func TestConfigValidateReportsAMissingLoginPath(t *testing.T) {
	wantLine := `ERROR: auth: no login path is configured for serve mode: enable auth.oidc (single sign-on) or auth.bootstrapAdmin with passwordHashFile or tokenFile ` +
		`(break-glass administrator); the dev login exists only in "` + brand.ServerBinary + ` dev"` + "\n"
	p := writeConfig(t, noLoginYAML)

	code, out, errs := runMyriarchd(t, "config", "validate", "--config", p, "--skip-files")
	if code != 1 || out != "" || errs != wantLine {
		t.Fatalf("validate: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	// serve stops with the same reason (migrate reads the database settings only and does not check the login path:
	// TestMigrateNeedsTheDatabaseSettingsOnly)
	code, _, errs = runMyriarchd(t, "serve", "--config", p)
	if code != 1 || !strings.Contains(errs, strings.TrimSuffix(strings.TrimPrefix(wantLine, "ERROR: "), "\n")) {
		t.Errorf("serve: exit %d stderr %q", code, errs)
	}
	// a bootstrap admin or OIDC is a login path
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(randomSecret(t, 24)), 0o600); err != nil {
		t.Fatal(err)
	}
	withBootstrap := writeConfig(t, noLoginYAML+"auth:\n  bootstrapAdmin:\n    enabled: true\n    tokenFile: "+tokenFile+"\n")
	code, out, errs = runMyriarchd(t, "config", "validate", "--config", withBootstrap)
	if code != 0 || errs != "" || !strings.Contains(out, "OK: ") ||
		!strings.Contains(out, "WARNING: auth.bootstrapAdmin is the only login path: configure single sign-on (auth.oidc) and disable the bootstrap admin after the initial setup") {
		t.Fatalf("bootstrap admin: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	// an unreadable credential file is reported with the setting
	missingToken := absPath("/nonexistent/bootstrap/token")
	missing := writeConfig(t, noLoginYAML+"auth:\n  bootstrapAdmin:\n    enabled: true\n    tokenFile: "+missingToken+"\n")
	code, out, errs = runMyriarchd(t, "config", "validate", "--config", missing)
	if code != 1 || out != "" || !strings.HasPrefix(errs, "ERROR: auth.bootstrapAdmin.tokenFile: cannot read "+missingToken) {
		t.Fatalf("missing token file: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if code, _, _ := runMyriarchd(t, "config", "validate", "--config", missing, "--skip-files"); code != 0 {
		t.Fatalf("--skip-files: exit %d", code)
	}
}

// ------------------------------------------------- run (serve mode) ---

// TestRunBootstrapAdminAndDeclarativeTokens starts a complete server twice on
// one database: the bootstrap admin signs in with its one-time token, the
// declared enrollment token exists once and nothing is created twice.
func TestRunBootstrapAdminAndDeclarativeTokens(t *testing.T) {
	dsn := pgtest.NewPool(t).Config().ConnString()
	dir := t.TempDir()
	bootstrapToken := randomSecret(t, 24)
	bootstrapFile := filepath.Join(dir, "bootstrap-token")
	enrollToken, err := authn.NewToken(brand.EnrollmentTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	enrollFile := filepath.Join(dir, "enrollment-edge")
	for f, v := range map[string]string{bootstrapFile: bootstrapToken + "\n", enrollFile: enrollToken + "\n"} {
		if err := os.WriteFile(f, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkConfig := func() (config.Config, string) {
		cfg, addr := devConfigDSN(t, dsn)
		cfg.Auth.DevLogin = config.DevLoginConfig{}
		cfg.OpAMP.RequireEnrollmentToken = true
		cfg.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin", TokenFile: bootstrapFile}
		cfg.OpAMP.EnrollmentTokens = []config.EnrollmentTokenConfig{{Name: "edge", TokenFile: enrollFile, Labels: map[string]string{"channel": "edge"}}}
		return cfg, addr
	}
	get := func(hc *http.Client, addr, path string, v any) int {
		t.Helper()
		resp, err := hc.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if v != nil {
			_ = json.NewDecoder(resp.Body).Decode(v)
		}
		return resp.StatusCode
	}
	signIn := func(addr, token string) (*http.Client, int) {
		jar, _ := cookiejar.New(nil)
		hc := &http.Client{Jar: jar, Timeout: 20 * time.Second}
		body, _ := json.Marshal(map[string]string{"token": token})
		resp, err := hc.Post("http://"+addr+"/api/v1/auth/bootstrap-login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return hc, resp.StatusCode
	}

	cfg, addr := mkConfig()
	rs := startRunWith(t, cfg, addr, runOptions{})
	hc := &http.Client{Timeout: 20 * time.Second}
	var info dto.AuthConfigInfo
	if code := get(hc, addr, "/api/v1/auth/config", &info); code != 200 || info.DevLoginEnabled || info.OIDCEnabled || info.BootstrapPasswordEnabled || !info.BootstrapTokenEnabled {
		t.Fatalf("auth config: %d %+v", code, info)
	}
	admin, status := signIn(addr, bootstrapToken)
	if status != 200 {
		t.Fatalf("bootstrap sign-in: %d", status)
	}
	var me dto.Me
	if get(admin, addr, "/api/v1/auth/me", &me) != 200 || me.AuthMethod != "bootstrap" || me.Role != domain.RoleAdmin {
		t.Fatalf("me: %+v", me)
	}
	var sys dto.SystemInfo
	if get(admin, addr, "/api/v1/system/info", &sys) != 200 || !sys.Features.BootstrapAdmin || sys.Features.DevLogin || sys.Features.DevMode {
		t.Fatalf("features: %+v", sys.Features)
	}
	var tokens api.Page[domain.EnrollmentToken]
	if get(admin, addr, "/api/v1/enrollment-tokens", &tokens) != 200 || len(tokens.Items) != 1 {
		t.Fatalf("enrollment tokens: %+v", tokens)
	}
	if tk := tokens.Items[0]; tk.Name != "edge" || tk.CreatedBy != "config" || tk.Labels["channel"] != "edge" || tk.DisplayPrefix != enrollToken[:12] {
		t.Fatalf("declared token: %+v", tk)
	}
	firstID := tokens.Items[0].ID
	if err := rs.stop(t); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// second start on the same database
	cfg2, addr2 := mkConfig()
	startRunWith(t, cfg2, addr2, runOptions{})
	// the one-time token was consumed by the first start
	if _, status := signIn(addr2, bootstrapToken); status != 401 {
		t.Fatalf("a consumed token must not work after a restart: %d", status)
	}
	// ... a new value in the file issues a new one
	next := randomSecret(t, 24)
	if err := os.WriteFile(bootstrapFile, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	admin2, status := signIn(addr2, next)
	if status != 200 {
		t.Fatalf("rotated token: %d", status)
	}
	if get(admin2, addr2, "/api/v1/enrollment-tokens", &tokens) != 200 || len(tokens.Items) != 1 || tokens.Items[0].ID != firstID {
		t.Fatalf("two starts must leave exactly one declared token: %+v", tokens)
	}
	var users api.Page[domain.User]
	if get(admin2, addr2, "/api/v1/users", &users) != 200 || len(users.Items) != 1 || users.Items[0].Provider != "bootstrap" {
		t.Fatalf("users: %+v", users)
	}
}

// A server whose configuration disables the bootstrap admin deactivates the
// account that an earlier start created.
func TestRunDeactivatesTheBootstrapAdminWhenItIsDisabled(t *testing.T) {
	dsn := pgtest.NewPool(t).Config().ConnString()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(randomSecret(t, 24)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, addr := devConfigDSN(t, dsn)
	cfg.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin", TokenFile: tokenFile}
	rs := startRunWith(t, cfg, addr, runOptions{})
	if err := rs.stop(t); err != nil {
		t.Fatal(err)
	}

	cfg2, addr2 := devConfigDSN(t, dsn)
	cfg2.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Username: "admin", TokenFile: tokenFile}
	startRunWith(t, cfg2, addr2, runOptions{dev: true})
	hc := &http.Client{Timeout: 20 * time.Second}
	jar, _ := cookiejar.New(nil)
	hc.Jar = jar
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin"})
	resp, err := hc.Post("http://"+addr2+"/api/v1/auth/dev-login", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("dev login: %v %v", resp, err)
	}
	resp.Body.Close()
	resp, err = hc.Get("http://" + addr2 + "/api/v1/users")
	if err != nil {
		t.Fatal(err)
	}
	var users api.Page[domain.User]
	_ = json.NewDecoder(resp.Body).Decode(&users)
	resp.Body.Close()
	found := false
	for _, u := range users.Items {
		if u.Provider == "bootstrap" {
			found = true
			if !u.Disabled {
				t.Errorf("the bootstrap user must be disabled: %+v", u)
			}
		}
	}
	if !found {
		t.Fatalf("users: %+v", users.Items)
	}
}
