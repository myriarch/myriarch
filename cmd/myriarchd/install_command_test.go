// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/testutil/linuxhost"
)

// session is a user of the web UI who signed in with the development login.
type session struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
}

func signIn(t *testing.T, base, user string) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	s := &session{t: t, base: base, hc: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	login, _ := json.Marshal(map[string]string{"username": user, "password": user})
	resp, err := s.hc.Post(base+"/api/v1/auth/dev-login", "application/json", bytes.NewReader(login))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("dev login of %s: %v %v", user, resp, err)
	}
	defer resp.Body.Close()
	var me dto.Me
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil || me.CSRFToken == "" {
		t.Fatalf("me: %+v %v", me, err)
	}
	s.csrf = me.CSRFToken
	return s
}

// do sends a request with the session cookie, and the CSRF header when csrf is true, and returns status and body.
func (s *session) do(method, path string, body any, csrf bool) (int, []byte) {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, s.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestInstallCommandThroughTheRealServer runs the whole path of a host that is installed from the server, against a real
// server process with PostgreSQL: a person renders the command in the web UI (a session, the CSRF header), the command
// runs in a real shell with the real curl, the server authenticates the enrollment token and delivers the bundle, the
// command checks the hash that it pins and starts the installer from the bundle. The server is a development server with
// a directory of bundles, because a build from a source tree has no release to take them from.
func TestInstallCommandThroughTheRealServer(t *testing.T) {
	linuxhost.SkipWithoutShell(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	bundle := linuxhost.NewBundle(t, "")
	dir := t.TempDir()
	const version = "0.3.0-dev"
	for _, p := range []install.Platform{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}} {
		if err := os.WriteFile(filepath.Join(dir, install.BundleFilename(version, p)), bundle.Archive, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, install.BundleFilename(version, install.Platform{OS: "windows", Arch: "amd64"})), []byte("PK zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := install.BundleFilename(version, install.Platform{OS: "linux", Arch: "amd64"})

	cfg, addr := devConfig(t)
	startRunWith(t, cfg, addr, runOptions{dev: true, installBundleDir: dir})
	base := "http://" + addr
	token := cfg.Dev.SeedEnrollmentToken

	// the list needs a session; the directory was read at the start
	if code, _ := (&session{t: t, base: base, hc: http.DefaultClient}).do(http.MethodGet, "/api/v1/install/bundles", nil, false); code != http.StatusUnauthorized {
		t.Fatalf("the list without a session: %d", code)
	}
	admin := signIn(t, base, "admin")
	code, body := admin.do(http.MethodGet, "/api/v1/install/bundles", nil, false)
	var list install.BundleList
	if code != http.StatusOK || json.Unmarshal(body, &list) != nil || list.Source != install.BundleSourceDirectory || list.Version != version || len(list.Items) != 3 {
		t.Fatalf("bundles: %d %s", code, body)
	}
	for _, it := range list.Items {
		if it.Status != install.BundleReady || len(it.SHA256) != 64 {
			t.Fatalf("a bundle of the directory is not ready: %+v", it)
		}
	}

	// rendering is a POST with a session: it needs the CSRF header, and a viewer may do it
	request := map[string]any{"variant": "lab", "platforms": []string{"linux/amd64"}, "fleet": "datacenter",
		"attributes": map[string]string{"site.type": "datacenter", "site.id": "dc-01"}}
	if code, _ := admin.do(http.MethodPost, "/api/v1/install/commands", request, false); code != http.StatusForbidden {
		t.Fatalf("rendering without the CSRF header: %d", code)
	}
	render := func(s *session) install.Commands {
		t.Helper()
		code, body := s.do(http.MethodPost, "/api/v1/install/commands", request, true)
		var c install.Commands
		if code != http.StatusOK || json.Unmarshal(body, &c) != nil || len(c.Commands) != 1 {
			t.Fatalf("commands: %d %s", code, body)
		}
		return c
	}
	cmds := render(signIn(t, base, "viewer"))
	if !cmds.Insecure || cmds.Server != base || cmds.Version != version || cmds.TokenPlaceholder != brand.EnrollmentTokenPlaceholder {
		t.Fatalf("commands: %+v", cmds)
	}
	command := cmds.Commands[0].Command
	if cmds.Commands[0].Bundle.SHA256 != bundle.SHA256 || strings.Count(command, brand.EnrollmentTokenPlaceholder) != 1 {
		t.Fatalf("the command does not pin the bundle or has no single place for the token: %s", command)
	}
	// nothing that was rendered holds a token
	if strings.Contains(string(body), "myre_") || strings.Contains(command, "myre_") {
		t.Fatal("a token in the commands")
	}

	// 1. the command, with the token in place of the placeholder, installs the host
	host := linuxhost.New(t, linuxhost.Options{Bundle: bundle})
	host.UseRealCurl()
	r := host.Run(strings.ReplaceAll(command, brand.EnrollmentTokenPlaceholder, token))
	if r.Code != 0 {
		t.Fatalf("the command failed: %d\n%s", r.Code, r.Out)
	}
	args := host.Argv("installer")
	want := []string{"--server", base, "--token-file"}
	if len(args) < 6 || strings.Join(args[:3], " ") != strings.Join(want, " ") || !strings.HasSuffix(args[3], "/enrollment-token") ||
		strings.Join(args[4:], " ") != "--fleet datacenter --site-type datacenter --site-id dc-01 --insecure" {
		t.Fatalf("the installer was started with %q", args)
	}
	if got := host.Log("installer.token"); got != token+"\n" {
		t.Errorf("the token file of the installer: %q", got)
	}
	if len(host.PrivateDirs()) != 0 {
		t.Errorf("left behind: %v", host.PrivateDirs())
	}
	host.AssertTokenNowhere(token, r.Out)

	// 2. a download needs the enrollment token, a range works, and the token is not taken from the URL
	get := func(path string, hdr ...string) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}
	url := "/api/v1/install/bundles/" + name
	if code, hdr, _ := get(url); code != http.StatusUnauthorized || hdr.Get("WWW-Authenticate") == "" {
		t.Errorf("without a token: %d %v", code, hdr)
	}
	if code, _, _ := get(url + "?token=" + token); code != http.StatusUnauthorized {
		t.Errorf("with the token in the URL: %d", code)
	}
	if code, _, _ := get(url, "Authorization", "Bearer myre_"+strings.Repeat("z", 32)); code != http.StatusUnauthorized {
		t.Errorf("with an unknown token: %d", code)
	}
	code, hdr, part := get(url, "Authorization", "Bearer "+token, "Range", "bytes=0-9")
	if code != http.StatusPartialContent || !bytes.Equal(part, bundle.Archive[:10]) || hdr.Get("ETag") != `"sha256-`+bundle.SHA256+`"` {
		t.Errorf("range: %d %v", code, hdr)
	}
	if code, _, _ := get("/api/v1/install/bundles/myriarch-updater_0.1.0_linux_amd64.tar.gz", "Authorization", "Bearer "+token); code != http.StatusNotFound {
		t.Errorf("a bundle of another version: %d", code)
	}

	// 3. a bundle that is not the one in the command stops the command: the new build in the directory has another hash
	next := linuxhost.NewBundle(t, "#!/usr/bin/env bash\necho second build > \"$LOGDIR/installer.argv\"\n")
	if err := os.WriteFile(filepath.Join(dir, name), next.Archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if now := render(admin); now.Commands[0].Bundle.SHA256 != next.SHA256 {
		t.Fatalf("the directory was not read again: %s", now.Commands[0].Bundle.SHA256)
	}
	stale := linuxhost.New(t, linuxhost.Options{Bundle: next})
	stale.UseRealCurl()
	r = stale.Run(strings.ReplaceAll(command, brand.EnrollmentTokenPlaceholder, token)) // the command of before pins the old hash
	if r.Code == 0 || !strings.Contains(r.Out, "[myriarch-install] ERROR [checksum_mismatch]") || stale.Log("installer.argv") != "" {
		t.Errorf("the command accepted another bundle than the one it pins: %d %s", r.Code, r.Out)
	}

	// 4. revoking the enrollment token stops the downloads at once
	code, body = admin.do(http.MethodGet, "/api/v1/enrollment-tokens", nil, false)
	var tokens struct {
		Items []struct {
			ID     string `json:"id"`
			Prefix string `json:"displayPrefix"`
		} `json:"items"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &tokens) != nil {
		t.Fatalf("tokens: %d %s", code, body)
	}
	var id string
	for _, it := range tokens.Items {
		if strings.HasPrefix(token, it.Prefix) && it.Prefix != "" {
			id = it.ID
		}
	}
	if id == "" {
		t.Fatalf("the development token is not in the list: %s", body)
	}
	if code, body := admin.do(http.MethodDelete, "/api/v1/enrollment-tokens/"+id, nil, true); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", code, body)
	}
	if code, _, _ := get(url, "Authorization", "Bearer "+token); code != http.StatusUnauthorized {
		t.Errorf("a revoked token downloaded the bundle: %d", code)
	}
	revoked := linuxhost.New(t, linuxhost.Options{Bundle: next})
	revoked.UseRealCurl()
	fresh := render(admin).Commands[0].Command
	r = revoked.Run(strings.ReplaceAll(fresh, brand.EnrollmentTokenPlaceholder, token))
	if r.Code == 0 || !strings.Contains(r.Out, "[myriarch-install] ERROR [download_failed]") || revoked.Log("installer.argv") != "" {
		t.Errorf("the command went on with a revoked token: %d %s", r.Code, r.Out)
	}

	// the counters, and no credential in them
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	metrics, _ := io.ReadAll(resp.Body)
	for _, line := range []string{
		`myriarch_install_bundle_downloads_total{result="ok"}`, `myriarch_install_bundle_downloads_total{result="unauthorized"}`,
	} {
		if !strings.Contains(string(metrics), line) {
			t.Errorf("the metrics lack %s:\n%s", line, grepLines(string(metrics), "myriarch_install"))
		}
	}
	if strings.Contains(string(metrics), token) {
		t.Error("the token is in the metrics")
	}
}

// serve refuses the flag of the directory, and so does run outside development mode.
func TestInstallBundleDirIsForDevelopmentOnly(t *testing.T) {
	var out, errOut bytes.Buffer
	code := execute([]string{"serve", "--install-bundle-dir", t.TempDir()}, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "--install-bundle-dir exists only in development mode") {
		t.Fatalf("serve with the flag: %d %q", code, errOut.String())
	}
	// the flag is not part of the help of serve, but of the help of dev
	out.Reset()
	if code := execute([]string{"serve", "--help"}, &out, &errOut); code != 0 || strings.Contains(out.String(), "install-bundle-dir") {
		t.Errorf("serve --help: %d %s", code, out.String())
	}
	out.Reset()
	if code := execute([]string{"dev", "--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "--install-bundle-dir") {
		t.Errorf("dev --help: %d %s", code, out.String())
	}

	// run itself refuses it outside development mode, before it touches anything
	cfg, _ := devConfig(t)
	err := run(t.Context(), cfg, runOptions{installBundleDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "only in development mode") {
		t.Fatalf("run: %v", err)
	}
}
