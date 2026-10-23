// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run deploy/scripts/install-windows.ps1 in its server mode (-Server) in PowerShell 7 against a test server that plays
// the install API of this package: the plan, and the artifacts with the install grants. The script runs in staging mode (-Root), so
// it changes nothing on the machine; its verifier is a stand-in for the myriarch-updater.exe of the installer bundle (the script takes
// the verifier from the bundle it runs from) and the collector is a stand-in too: both are one small Go program, built when the tests
// start, so that the same rig runs on Unix and on Windows. On Windows every case runs with Windows PowerShell 5.1 (powershell.exe,
// which the command of the web UI always starts) and with PowerShell 7; elsewhere with PowerShell 7. What needs a real Windows
// host (services, ACLs, the registry, Group Policy, App Control) is not part of staging mode. They check
//   - that every option which the rendered commands (the golden files) pass is accepted, and that the whole run works,
//   - that the plan is read as data (ADR-0046): unknown keys, too many lines or bytes, a URL of another origin, http, a redirect to
//     another origin are all refused, and nothing is installed when the plan is refused,
//   - every error code of the server mode: plan_failed, plan_invalid, plan_rejected, plan_unavailable, https_required,
//     conflicting_install, supervisor_missing and language_mode_unsupported,
//   - that the token and the grants reach the server as request headers only.
//
// The tests skip without PowerShell (MYRIARCH_PWSH, the pinned tool of make tools, or pwsh in the PATH; powershell.exe on Windows).
// A test that needs a TLS server runs on Linux only: PowerShell trusts the CA of the test server there through SSL_CERT_FILE, and
// elsewhere it would need the certificate store of the machine.

type winRig struct {
	t         *testing.T
	pwsh      string
	secure    bool
	root      string // the staging root of the installer
	bundle    string // the unpacked installer bundle: deploy/ and the stand-in for myriarch-updater.exe
	logs      string
	tmp       string
	home      string // the profile directory of PowerShell
	token     string
	tokenFile string
	caFile    string
	srv       *httptest.Server

	mu       sync.Mutex
	requests []recorded
	plan     func(w http.ResponseWriter, r *http.Request, n int)
	plans    int
	// wrongSHA makes the server answer the .sha256 of the supervisor with another hash.
	wrongSHA bool
	// tampered makes every .sigstore.json say TAMPERED, which the stand-in verifier refuses.
	tampered bool
	// pathFront is a directory that env puts in front of the PATH of the rig's processes (Windows only).
	pathFront string
}

func newWinRig(t *testing.T, secure bool) *winRig {
	t.Helper()
	if secure && runtime.GOOS != "linux" {
		t.Skip("a TLS server of a test is trusted through SSL_CERT_FILE on Linux only")
	}
	r := &winRig{t: t, pwsh: findWinShell(t), secure: secure, token: testToken}
	dir := t.TempDir()
	r.root, r.bundle, r.logs, r.tmp, r.home = filepath.Join(dir, "root"), filepath.Join(dir, "bundle"), filepath.Join(dir, "logs"), filepath.Join(dir, "tmp"), filepath.Join(dir, "home")
	for _, d := range []string{r.root, r.logs, r.tmp, r.home} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	r.tokenFile = filepath.Join(dir, "enrollment-token")
	require.NoError(t, os.WriteFile(r.tokenFile, []byte(r.token+"\r\n"), 0o600))
	r.buildBundle()

	h := http.HandlerFunc(r.serve)
	r.caFile = filepath.Join(dir, "ca.pem")
	if secure {
		r.setServer(httptest.NewTLSServer(h))
		r.trust(r.srv)
	} else {
		r.setServer(httptest.NewServer(h))
	}
	t.Cleanup(func() { r.srv.Close() })
	return r
}

// setServer sets the server under the lock: the handler goroutines read it (planText) while the test goroutine sets it.
func (r *winRig) setServer(srv *httptest.Server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.srv = srv
}

// origin is the URL of the server, for the handler goroutines.
func (r *winRig) origin() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.srv.URL
}

// trust makes PowerShell trust the certificate of a test server.
func (r *winRig) trust(srv *httptest.Server) {
	require.NoError(r.t, os.WriteFile(r.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644))
}

// buildBundle lays out what the installer bundle holds: the script, its templates and a stand-in for myriarch-updater.exe.
func (r *winRig) buildBundle() {
	root := repoRoot(r.t)
	for _, rel := range []string{"deploy/scripts/install-windows.ps1", "deploy/supervisor", "deploy/updater", "deploy/windows"} {
		copyTree(r.t, filepath.Join(root, rel), filepath.Join(r.bundle, rel))
	}
	// The stand-in for verify-bundle (see standInSource); the installer takes it as the verifier and as the updater to install.
	require.NoError(r.t, os.WriteFile(filepath.Join(r.bundle, "myriarch-updater.exe"), standInBinary(r.t), 0o755))
}

func (r *winRig) collectorFile() string {
	return "otelcol-contrib_" + rigVersion + "_windows_amd64.tar.gz"
}
func (r *winRig) supervisorFile() string {
	return "opampsupervisor_" + rigVersion + "_windows_amd64.exe"
}

func (r *winRig) collectorArchive() []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// a program that prints its version, like the collector does
	body := standInBinary(r.t)
	require.NoError(r.t, tw.WriteHeader(&tar.Header{Name: "otelcol-contrib.exe", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write(body)
	require.NoError(r.t, err)
	require.NoError(r.t, tw.Close())
	require.NoError(r.t, gz.Close())
	return buf.Bytes()
}

func (r *winRig) artifactFor(path string) (name string, content []byte, grant, file string, ok bool) {
	switch {
	case strings.HasPrefix(path, "/api/v1/artifacts/c0/content/"):
		return r.collectorFile(), r.collectorArchive(), rigCollectorGrant, strings.TrimPrefix(path, "/api/v1/artifacts/c0/content/"), true
	case strings.HasPrefix(path, "/api/v1/artifacts/s0/content/"):
		return r.supervisorFile(), []byte("MZ-fake-supervisor"), rigSupervisorGrant, strings.TrimPrefix(path, "/api/v1/artifacts/s0/content/"), true
	}
	return "", nil, "", "", false
}

func (r *winRig) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.requests = append(r.requests, recorded{Method: req.Method, Path: req.URL.Path, Auth: req.Header.Get("Authorization"), Body: string(body)})
	var n int
	if req.URL.Path == "/api/v1/install/plan" {
		r.plans++
		n = r.plans
	}
	plan, wrongSHA, tampered := r.plan, r.wrongSHA, r.tampered
	r.mu.Unlock()
	switch {
	case req.URL.Path == "/api/v1/install/plan" && req.Method == http.MethodPost:
		if req.Header.Get("Authorization") != "Bearer "+r.token {
			problem(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		if req.Header.Get("Accept") != "text/plain" {
			http.Error(w, "the plan is asked for as text/plain", http.StatusNotAcceptable)
			return
		}
		if plan == nil {
			plan = func(w http.ResponseWriter, _ *http.Request, _ int) { servePlan(w, r.planText(nil)) }
		}
		plan(w, req, n)
	case strings.HasPrefix(req.URL.Path, "/api/v1/artifacts/"):
		name, content, grant, file, ok := r.artifactFor(req.URL.Path)
		if !ok {
			http.NotFound(w, req)
			return
		}
		if req.Header.Get("Authorization") != "Bearer "+grant {
			problem(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		switch file {
		case name:
			_, _ = w.Write(content)
		case name + ".sha256":
			sum := sha256.Sum256(content)
			if wrongSHA && name == r.supervisorFile() {
				sum = sha256.Sum256([]byte("another file"))
			}
			_, _ = io.WriteString(w, hex.EncodeToString(sum[:])+"  "+name+"\n")
		case name + ".sigstore.json":
			if tampered {
				_, _ = io.WriteString(w, "TAMPERED")
				return
			}
			_, _ = io.WriteString(w, `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)
		default:
			http.NotFound(w, req)
		}
	default:
		http.NotFound(w, req)
	}
}

// planText is a valid plan for this server; over replaces the value of a key (or, with an empty value, drops the line).
func (r *winRig) planText(over map[string]string) string {
	u := r.origin()
	scheme := map[bool]string{true: "wss", false: "ws"}[r.secure]
	vals := map[string]string{
		KeyFormat: "1", KeyFleet: "windows-hosts", KeyVersionSource: "git",
		KeyOpAMPEndpoint:    scheme + "://" + strings.SplitN(u, "://", 2)[1] + "/v1/opamp",
		KeyCollectorVersion: rigVersion, KeyCollectorURL: u + "/api/v1/artifacts/c0/content/" + r.collectorFile(), KeyCollectorGrant: rigCollectorGrant,
		KeySupervisorVersion: rigVersion, KeySupervisorURL: u + "/api/v1/artifacts/s0/content/" + r.supervisorFile(), KeySupervisorGrant: rigSupervisorGrant,
		KeyUpdaterInstall: "true",
	}
	for k, v := range over {
		vals[k] = v
	}
	var b strings.Builder
	for _, k := range PlanKeys {
		if vals[k] != "" {
			b.WriteString(k + "=" + vals[k] + "\n")
		}
	}
	return b.String()
}

func (r *winRig) setPlan(plan func(w http.ResponseWriter, r *http.Request, n int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan = plan
}

func (r *winRig) answerWith(text string) {
	r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) { servePlan(w, text) })
}

func (r *winRig) seen() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.requests...)
}

// env is the environment of every PowerShell process of the rig: nothing of the machine's profile, a private temporary directory
// (the working directory of a staging run is made there) and the CA of the test server.
func (r *winRig) env() []string {
	var env []string
	if runtime.GOOS == "windows" {
		// Windows PowerShell and .NET need SystemRoot, the PATH and more; the profile and the temporary directory are the rig's
		for _, kv := range os.Environ() {
			k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
			if k != "TEMP" && k != "TMP" && k != "USERPROFILE" && k != "HOME" && k != "APPDATA" && k != "LOCALAPPDATA" && !strings.HasPrefix(k, "SSL_") {
				env = append(env, kv)
			}
		}
		if r.pathFront != "" {
			env = append(env, "PATH="+r.pathFront+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		env = append(env, "TEMP="+r.tmp, "TMP="+r.tmp, "USERPROFILE="+r.home, "HOME="+r.home, "APPDATA="+r.home, "LOCALAPPDATA="+r.home, "LOGDIR="+r.logs)
	} else {
		env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.home, "TMPDIR=" + r.tmp, "LOGDIR=" + r.logs, "LANG=C", "NO_PROXY=127.0.0.1,localhost"}
	}
	env = append(env, "POWERSHELL_TELEMETRY_OPTOUT=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1", "NO_PROXY=127.0.0.1,localhost")
	if r.secure {
		env = append(env, "SSL_CERT_FILE="+r.caFile)
	}
	return env
}

func (r *winRig) script() string {
	return filepath.Join(r.bundle, "deploy", "scripts", "install-windows.ps1")
}

func (r *winRig) run(args ...string) installerResult {
	r.t.Helper()
	cmd := exec.Command(r.pwsh, append([]string{"-NoProfile", "-NonInteractive", "-File", r.script()}, args...)...)
	cmd.Env = r.env()
	return runWithin(r.t, cmd, 3*time.Minute)
}

func runWithin(t *testing.T, cmd *exec.Cmd, limit time.Duration) installerResult {
	t.Helper()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		t.Fatalf("the installer did not end: %s", out.String())
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		require.ErrorAs(t, err, &ee, out.String())
		code = ee.ExitCode()
	}
	return installerResult{Out: out.String(), Code: code}
}

// baseArgs are the options of a plain command: the server, the token file and the site; a server over http needs -Insecure, which the
// lab variant of the command passes.
func (r *winRig) baseArgs() []string {
	args := []string{"-Root", r.root, "-Arch", "amd64", "-Server", r.srv.URL, "-TokenFile", r.tokenFile, "-SiteType", "edge", "-SiteId", "042"}
	if !r.secure {
		args = append(args, "-Insecure")
	}
	return args
}

func (r *winRig) runWith(more ...string) installerResult {
	r.t.Helper()
	return r.run(append(r.baseArgs(), more...)...)
}

// installed tells whether the installer put anything below its staging root.
func (r *winRig) installed() bool {
	entries, err := os.ReadDir(r.root)
	return err != nil || len(entries) > 0
}

func (r *winRig) updaterCalls() string {
	b, _ := os.ReadFile(filepath.Join(r.logs, "updater.argv"))
	return string(b)
}

func (r *winRig) pf(rel string) string {
	return filepath.Join(r.root, "ProgramFiles", "Myriarch", filepath.FromSlash(rel))
}

func (r *winRig) assertSecretsOnlyInHeaders(out string) {
	r.t.Helper()
	for _, s := range []string{r.token, rigCollectorGrant, rigSupervisorGrant} {
		assert.NotContains(r.t, out, s, "the output of the installer")
	}
	for _, rec := range r.seen() {
		for _, s := range []string{r.token, rigCollectorGrant, rigSupervisorGrant} {
			assert.NotContains(r.t, rec.Path, s, "the URL of a request")
			assert.NotContains(r.t, rec.Body, s, "the body of a request")
		}
	}
	// the plan request is the only one that carries the token, and each artifact only its own grant
	for _, rec := range r.seen() {
		switch {
		case rec.Path == "/api/v1/install/plan":
			assert.Equal(r.t, "Bearer "+r.token, rec.Auth)
		case strings.Contains(rec.Path, "/c0/"):
			assert.Equal(r.t, "Bearer "+rigCollectorGrant, rec.Auth)
		case strings.Contains(rec.Path, "/s0/"):
			assert.Equal(r.t, "Bearer "+rigSupervisorGrant, rec.Auth)
		}
	}
}

// ------------------------------------------------------------------------ the tests ----

func testWindowsServerModeInstallsFromThePlan(t *testing.T) {
	r := newWinRig(t, false)
	res := r.runWith("-Fleet", "windows-hosts", "-SiteName", `Edge "north"`, "-Environment", "prod")
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, res.Out, "plan: fleet windows-hosts, collector v0.162.0 and supervisor v0.162.0 (version from git), updater: true")
	assert.Contains(t, res.Out, "Sigstore signature of "+r.collectorFile()+" ok")
	assert.Contains(t, res.Out, "Sigstore signature of "+r.supervisorFile()+" ok")

	// what the server saw: the token on the plan, the grant of each artifact on its files, and a body without a secret
	var plans, artifacts int
	for _, rec := range r.seen() {
		switch {
		case rec.Path == "/api/v1/install/plan":
			plans++
			assert.Equal(t, "POST", rec.Method)
			assert.JSONEq(t, fmt.Sprintf(`{"os":"windows","arch":"amd64","fleet":"windows-hosts","attributes":{"site.type":"edge","site.id":"042","site.name":"Edge \"north\"","deployment.environment.name":"prod"%s}}`, hostAttr(rec.Body)), rec.Body)
		case strings.Contains(rec.Path, "/c0/"), strings.Contains(rec.Path, "/s0/"):
			artifacts++
		}
	}
	assert.Equal(t, 1, plans)
	assert.Equal(t, 6, artifacts, "archive, .sha256 and .sigstore.json of both artifacts")
	r.assertSecretsOnlyInHeaders(res.Out)

	// the host: collector at the version of the plan, the supervisor, the endpoint of the plan, the updater of the bundle
	assert.FileExists(t, r.pf("collector/versions/"+rigVersion+"/otelcol-contrib.exe"))
	assert.FileExists(t, r.pf("collector/bin/otelcol-contrib.exe"))
	assert.FileExists(t, r.pf("supervisor/opampsupervisor.exe"))
	cfg, err := os.ReadFile(filepath.Join(r.root, "ProgramData", "Myriarch", "config", "supervisor.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), winEndpointOfPlan(r))
	updaterCfg, err := os.ReadFile(filepath.Join(r.root, "ProgramData", "Myriarch", "config", "updater.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(updaterCfg), winEndpointOfPlan(r))
	want, err := os.ReadFile(filepath.Join(r.bundle, "myriarch-updater.exe"))
	require.NoError(t, err)
	got, err := os.ReadFile(r.pf("updater/myriarch-updater.exe"))
	require.NoError(t, err)
	assert.Equal(t, want, got, "the updater of the installer bundle is installed")
	tok, err := os.ReadFile(filepath.Join(r.root, "ProgramData", "Myriarch", "config", "enrollment-token"))
	require.NoError(t, err)
	assert.Equal(t, r.token+"\n", string(tok), "the token of the file, without the line end of the file")

	// the identity, the issuer and the version are the installer's own; the verifier is the updater of the bundle, started from its
	// copy in the working directory of the run and not from the bundle
	calls := r.updaterCalls()
	assert.Contains(t, calls, `--identity-regexp ^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0$`)
	assert.Contains(t, calls, `--identity-regexp ^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-binary-release\.yaml@refs/tags/v0\.162\.0$`)
	assert.Equal(t, 2, strings.Count(calls, "--issuer https://token.actions.githubusercontent.com"))
	assert.Equal(t, 2, strings.Count(calls, "--version v0.162.0"))
	assert.Equal(t, 2, strings.Count(calls, "--tuf-cache "), "the TUF cache of the check is in the working directory of the installer")
	assert.NotContains(t, calls, "--trusted-root", "the trust root is the verifier's own unless the operator names a file")
	for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
		assert.True(t, strings.HasPrefix(line, r.tmp+string(os.PathSeparator)) || strings.HasPrefix(line, resolvedTmp(t, r.tmp)), "the verifier runs from the working directory: %s", line)
	}
	left, err := filepath.Glob(filepath.Join(r.tmp, "myriarch-install-*"))
	require.NoError(t, err)
	assert.Empty(t, left, "the working directory is removed")

	// a second run changes nothing and asks for a plan again
	res = r.runWith("-Fleet", "windows-hosts", "-SiteName", `Edge "north"`, "-Environment", "prod")
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, res.Out, "Collector v0.162.0 is already installed")
}

// testWindowsServerModeIgnoresATarOfThePath puts a tar.exe and a tar.cmd in front of the PATH that both fail, as the GNU tar of Git
// for Windows does with a path like C:\..., which it reads as host:path. The installer takes the bsdtar of System32 and installs.
func testWindowsServerModeIgnoresATarOfThePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the tar of System32 exists on Windows only")
	}
	r := newWinRig(t, false)
	front := filepath.Join(t.TempDir(), "fake-tar")
	require.NoError(t, os.MkdirAll(front, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(front, "tar.exe"), []byte("this is not a program"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(front, "tar.cmd"), []byte("@echo tar (child): Cannot connect to C: resolve failed 1>&2\r\n@exit /b 2\r\n"), 0o755))
	r.pathFront = front
	res := r.runWith()
	require.Equal(t, 0, res.Code, res.Out)
	assert.NotContains(t, res.Out, "Cannot connect to C:")
	assert.True(t, r.installed())
}

func TestWindowsServerModeIgnoresATarOfThePath(t *testing.T) {
	eachWinShell(t, testWindowsServerModeIgnoresATarOfThePath)
}

func TestWindowsServerModeInstallsFromThePlan(t *testing.T) {
	eachWinShell(t, testWindowsServerModeInstallsFromThePlan)
}

// resolvedTmp is the temporary directory the way PowerShell reports it (on macOS /var is a link to /private/var).
func resolvedTmp(t *testing.T, dir string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return p + string(os.PathSeparator)
}

func winEndpointOfPlan(r *winRig) string {
	for _, l := range strings.Split(r.planText(nil), "\n") {
		if v, ok := strings.CutPrefix(l, KeyOpAMPEndpoint+"="); ok {
			return v
		}
	}
	return ""
}

func testWindowsServerModeWithoutUpdaterInPlan(t *testing.T) {
	r := newWinRig(t, false)
	r.answerWith(r.planText(map[string]string{KeyUpdaterInstall: "false"}))
	res := r.runWith()
	require.Equal(t, 0, res.Code, res.Out)
	assert.NoFileExists(t, r.pf("updater/myriarch-updater.exe"))
	assert.NoFileExists(t, filepath.Join(r.root, "ProgramData", "Myriarch", "config", "updater.yaml"))
	assert.FileExists(t, r.pf("supervisor/opampsupervisor.exe"))
}

func TestWindowsServerModeWithoutUpdaterInPlan(t *testing.T) {
	eachWinShell(t, testWindowsServerModeWithoutUpdaterInPlan)
}

func testWindowsServerModeAsksForTheVersionOfTheOperator(t *testing.T) {
	r := newWinRig(t, false)
	res := r.runWith("-CollectorVersion", "v"+rigVersion)
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, r.seen()[0].Body, `"collectorVersion":"`+rigVersion+`"`)

	// a server that answers another version than the one asked for is not followed
	r2 := newWinRig(t, false)
	r2.answerWith(r2.planText(map[string]string{KeyCollectorVersion: "0.161.0"}))
	res = r2.runWith("-CollectorVersion", rigVersion)
	assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	assert.False(t, r2.installed())
}

func TestWindowsServerModeAsksForTheVersionOfTheOperator(t *testing.T) {
	eachWinShell(t, testWindowsServerModeAsksForTheVersionOfTheOperator)
}

func testWindowsServerModeTheFleetOfThePlanMustBeTheOneAskedFor(t *testing.T) {
	r := newWinRig(t, false)
	r.answerWith(r.planText(map[string]string{KeyFleet: "other-hosts"}))
	res := r.runWith("-Fleet", "windows-hosts")
	assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	assert.False(t, r.installed())
}

func TestWindowsServerModeTheFleetOfThePlanMustBeTheOneAskedFor(t *testing.T) {
	eachWinShell(t, testWindowsServerModeTheFleetOfThePlanMustBeTheOneAskedFor)
}

// goldenWindowsOptions reads the options that the rendered Windows command hands to the installer after -Server and -TokenFile.
func goldenWindowsOptions(t *testing.T, command string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^  InstallerArgs = @\((.*)\)$`).FindStringSubmatch(command)
	require.NotNil(t, m, "the command has the options of the installer")
	var opts []string
	for _, w := range regexp.MustCompile(`'((?:[^']|'')*)'`).FindAllStringSubmatch(m[1], -1) {
		opts = append(opts, strings.ReplaceAll(w[1], "''", "'"))
	}
	return opts
}

// The commands of the web UI start the installer as
//
//	powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File <installer> -Server <server> -TokenFile <file> <options>
//
// and every option that they pass has to be accepted by the script: this runs the script with the options of each golden file.
func testWindowsInstallerAcceptsEveryOptionOfTheRenderedWindowsCommands(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "commands", "*_windows_*.txt"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 3, "the golden files of the Windows commands")
	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			text, err := os.ReadFile(file)
			require.NoError(t, err)
			opts := goldenWindowsOptions(t, string(text))
			r := newWinRig(t, false)
			r.answerWith(r.planText(map[string]string{KeyFleet: "linux-hosts"})) // the fleet that the options name
			args := []string{"-Root", r.root, "-Arch", "amd64", "-Server", r.srv.URL, "-TokenFile", r.tokenFile}
			insecure := false
			for _, o := range opts {
				insecure = insecure || o == "-Insecure"
			}
			if !insecure {
				args = append(args, "-Insecure") // the server of the test is plain http
			}
			res := r.run(append(args, opts...)...)
			require.Equal(t, 0, res.Code, res.Out)
			assert.NotContains(t, res.Out, "WARNING")
			body := r.seen()[0].Body
			for _, want := range []string{`"fleet":"linux-hosts"`, `"collectorVersion":"0.162.0"`, `"site.type":"edge"`, `"site.id":"042"`,
				`"site.name":"Edge site 42 (north)"`, `"site.country":"NL"`, `"site.region":"north"`, `"site.timezone":"Europe/Amsterdam"`, `"deployment.environment.name":"prod"`} {
				assert.Contains(t, body, want)
			}
			r.assertSecretsOnlyInHeaders(res.Out)
		})
	}
}

func TestWindowsInstallerAcceptsEveryOptionOfTheRenderedWindowsCommands(t *testing.T) {
	eachWinShell(t, testWindowsInstallerAcceptsEveryOptionOfTheRenderedWindowsCommands)
}

// Every option that the commands can pass is a parameter of the script.
func TestWindowsInstallerParametersCoverTheOptionsOfTheCommands(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy/scripts/install-windows.ps1"))
	require.NoError(t, err)
	_, block, ok := strings.Cut(string(script), "\nparam(\n")
	require.True(t, ok)
	block, _, ok = strings.Cut(block, "\n)\n")
	require.True(t, ok)
	in := &commandInput{variant: VariantLab, fleet: "windows-hosts", collectorVersion: "0.162.0", siteType: "edge", siteID: "042", siteName: "Edge",
		siteCountry: "NL", siteRegion: "north", siteTimezone: "Europe/Amsterdam", env: "prod"}
	flags := map[string]bool{"-Server": true, "-TokenFile": true}
	for _, o := range in.installerOptions(true) {
		if strings.HasPrefix(o, "-") {
			flags[o] = true
		}
	}
	// the installer line of the script of the command
	_, tail, ok := strings.Cut(windowsScript, "'-File', $installer")
	require.True(t, ok)
	line, _, _ := strings.Cut(tail, "\n")
	for _, f := range regexp.MustCompile(`'(-[A-Za-z]+)'`).FindAllStringSubmatch(line, -1) {
		flags[f[1]] = true
	}
	require.GreaterOrEqual(t, len(flags), 11, "the options of the commands: %v", sortedFlags(flags))
	for f := range flags {
		assert.Regexp(t, `(?m)^\s+\[(string|switch)\]\$`+regexp.QuoteMeta(strings.TrimPrefix(f, "-"))+`\b`, block, "install-windows.ps1 has the parameter %s", f)
	}
}

// ----------------------------------------------------------------- the plan as data ----

func testWindowsServerModeRefusesAPlanThatBreaksTheRules(t *testing.T) {
	long := strings.Repeat("a", 9000)
	var many strings.Builder
	for i := 0; i < 65; i++ {
		fmt.Fprintf(&many, "x%d=1\n", i)
	}
	cases := []struct {
		name string
		text func(r *winRig) string
		why  string
	}{
		{"unknown key", func(r *winRig) string { return r.planText(nil) + "run=true\n" }, "key this installer does not know"},
		{"unknown key between the others", func(r *winRig) string {
			return strings.Replace(r.planText(nil), KeyFleet+"=", "extra=1\n"+KeyFleet+"=", 1)
		}, "key this installer does not know"},
		{"a key in another case", func(r *winRig) string { return strings.Replace(r.planText(nil), KeyFleet+"=", "Fleet=", 1) }, "key this installer does not know"},
		{"too many lines", func(r *winRig) string { return r.planText(nil) + many.String() }, "more than 64 lines"},
		{"too many bytes", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: long}) }, "longer than 8192 bytes"},
		{"empty", func(r *winRig) string { return "" }, "is empty"},
		{"repeated key", func(r *winRig) string { return r.planText(nil) + "fleet=windows-hosts\n" }, "repeated"},
		{"missing key", func(r *winRig) string { return r.planText(map[string]string{KeyCollectorGrant: ""}) }, "lacks the key collector.grant"},
		{"format first", func(r *winRig) string {
			return strings.Replace(r.planText(nil), "format=1\n", "", 1) + "format=1\n"
		}, "does not start with the key format"},
		{"another format", func(r *winRig) string { return strings.Replace(r.planText(nil), "format=1", "format=2", 1) }, "format 2"},
		{"no trailing line end", func(r *winRig) string { return strings.TrimSuffix(r.planText(nil), "\n") }, "line end"},
		{"carriage return", func(r *winRig) string { return strings.ReplaceAll(r.planText(nil), "\n", "\r\n") }, "character that a plan may not have"},
		{"a space", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: "windows hosts"}) }, "character that a plan may not have"},
		{"a dollar sign", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: "win$(id)"}) }, "character that a plan may not have"},
		{"a backtick", func(r *winRig) string { return r.planText(map[string]string{KeyCollectorGrant: "myrd_`id`.x"}) }, "character that a plan may not have"},
		{"a semicolon", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: "win;id"}) }, "character that a plan may not have"},
		{"a non-ASCII letter", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: "w\u00efn"}) }, "character that a plan may not have"},
		{"a NUL byte", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: "win\x00"}) }, "character that a plan may not have"},
		{"an empty line", func(r *winRig) string { return r.planText(nil) + "\n" }, "has no ="},
		{"an empty value", func(r *winRig) string { return strings.Replace(r.planText(nil), "fleet=windows-hosts", "fleet=", 1) }, "no value for fleet"},
		{"an upper case fleet", func(r *winRig) string { return r.planText(map[string]string{KeyFleet: "Windows"}) }, "not the name of a fleet"},
		{"an unknown source", func(r *winRig) string { return r.planText(map[string]string{KeyVersionSource: "mirror"}) }, "version.source"},
		{"a version with two parts", func(r *winRig) string { return r.planText(map[string]string{KeyCollectorVersion: "0.162"}) }, "collector.version"},
		{"a version with a leading v", func(r *winRig) string { return r.planText(map[string]string{KeySupervisorVersion: "v0.162.0"}) }, "supervisor.version"},
		{"a version with build metadata", func(r *winRig) string {
			return r.planText(map[string]string{KeyCollectorVersion: "0.162.0+build"})
		}, "cannot install"},
		{"a grant that is not one", func(r *winRig) string { return r.planText(map[string]string{KeySupervisorGrant: "abc"}) }, "supervisor.grant"},
		{"updater.install not a boolean", func(r *winRig) string { return r.planText(map[string]string{KeyUpdaterInstall: "yes"}) }, "updater.install"},
		{"updater.install in another case", func(r *winRig) string { return r.planText(map[string]string{KeyUpdaterInstall: "True"}) }, "updater.install"},
		{"an endpoint that is not a URL", func(r *winRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "fleet.example.com"})
		}, "opamp.endpoint"},
		{"an endpoint of another host", func(r *winRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "ws://collect.example.com/v1/opamp"})
		}, "another host or port"},
		{"an endpoint of the host but another port", func(r *winRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "ws://127.0.0.1:1/v1/opamp"})
		}, "another host or port"},
		{"an endpoint with the wrong scheme", func(r *winRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "wss://" + strings.SplitN(r.srv.URL, "://", 2)[1] + "/v1/opamp"})
		}, "scheme"},
		{"an artifact of another origin", func(r *winRig) string {
			return r.planText(map[string]string{KeyCollectorURL: "http://downloads.example.com/api/v1/artifacts/c0/content/" + r.collectorFile()})
		}, "not on the origin of -Server"},
		{"an artifact on a lookalike host", func(r *winRig) string {
			return r.planText(map[string]string{KeySupervisorURL: strings.Replace(r.srv.URL, "127.0.0.1", "127.0.0.1.evil.example.com", 1) + "/api/v1/artifacts/s0/content/" + r.supervisorFile()})
		}, "not on the origin of -Server"},
		{"an artifact with a path that climbs", func(r *winRig) string {
			return r.planText(map[string]string{KeyCollectorURL: r.srv.URL + "/api/v1/artifacts/c0/content/../c0/content/" + r.collectorFile()})
		}, "path that is not plain"},
		{"an artifact with another file name", func(r *winRig) string {
			return r.planText(map[string]string{KeyCollectorURL: r.srv.URL + "/api/v1/artifacts/c0/content/otelcol-contrib_0.161.0_windows_amd64.tar.gz"})
		}, "does not end in the file name"},
		{"an artifact for another platform", func(r *winRig) string {
			return r.planText(map[string]string{KeySupervisorURL: r.srv.URL + "/api/v1/artifacts/s0/content/opampsupervisor_" + rigVersion + "_linux_amd64"})
		}, "does not end in the file name"},
		{"an artifact URL with a query", func(r *winRig) string {
			return r.planText(map[string]string{KeyCollectorURL: r.srv.URL + "/api/v1/artifacts/c0/content/" + r.collectorFile() + "?x=1"})
		}, "character that a plan may not have"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := newWinRig(t, false)
			r.answerWith(tc.text(r))
			res := r.runWith()
			assert.Equal(t, 1, res.Code, res.Out)
			assert.Equal(t, "plan_invalid", res.failure(), res.Out)
			assert.Contains(t, res.Out, tc.why)
			assert.False(t, r.installed(), "nothing is installed when the plan is refused")
			assert.Empty(t, r.updaterCalls(), "nothing runs before the check")
			for _, rec := range r.seen() {
				assert.Equal(t, "/api/v1/install/plan", rec.Path, "no artifact is downloaded for a plan that was refused")
			}
		})
	}
}

func TestWindowsServerModeRefusesAPlanThatBreaksTheRules(t *testing.T) {
	eachWinShell(t, testWindowsServerModeRefusesAPlanThatBreaksTheRules)
}

// A plan is text: the same words in the form of JSON or HTML are not a plan.
func testWindowsServerModeDoesNotReadAPlanThatIsNotText(t *testing.T) {
	r := newWinRig(t, false)
	r.answerWith(`{"format":1,"fleet":"windows-hosts"}` + "\n")
	res := r.runWith()
	assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	assert.False(t, r.installed())
}

func TestWindowsServerModeDoesNotReadAPlanThatIsNotText(t *testing.T) {
	eachWinShell(t, testWindowsServerModeDoesNotReadAPlanThatIsNotText)
}

func testWindowsServerModeRefusesAnArtifactOverHTTP(t *testing.T) {
	r := newWinRig(t, true)
	r.answerWith(r.planText(map[string]string{KeyCollectorURL: "http://" + strings.SplitN(r.srv.URL, "://", 2)[1] + "/api/v1/artifacts/c0/content/" + r.collectorFile()}))
	res := r.runWith()
	assert.Equal(t, "https_required", res.failure(), res.Out)
	assert.False(t, r.installed())
}

func TestWindowsServerModeRefusesAnArtifactOverHTTP(t *testing.T) {
	eachWinShell(t, testWindowsServerModeRefusesAnArtifactOverHTTP)
}

func testWindowsServerModeAnEndpointWithoutTLSIsRefused(t *testing.T) {
	r := newWinRig(t, true)
	r.answerWith(r.planText(map[string]string{KeyOpAMPEndpoint: "ws://" + strings.SplitN(r.srv.URL, "://", 2)[1] + "/v1/opamp"}))
	res := r.runWith()
	assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	assert.Contains(t, res.Out, "scheme")
}

func TestWindowsServerModeAnEndpointWithoutTLSIsRefused(t *testing.T) {
	eachWinShell(t, testWindowsServerModeAnEndpointWithoutTLSIsRefused)
}

func testWindowsServerModeHTTPServerNeedsInsecure(t *testing.T) {
	r := newWinRig(t, false)
	res := r.run("-Root", r.root, "-Arch", "amd64", "-Server", r.srv.URL, "-TokenFile", r.tokenFile, "-SiteType", "edge", "-SiteId", "042")
	assert.Equal(t, "https_required", res.failure(), res.Out)
	assert.Empty(t, r.seen(), "nothing is sent to a server that is reached over plain http")

	// the lab variant of the command allows it, and sends the same requests
	res = r.runWith()
	assert.Equal(t, 0, res.Code, res.Out)
}

func TestWindowsServerModeHTTPServerNeedsInsecure(t *testing.T) {
	eachWinShell(t, testWindowsServerModeHTTPServerNeedsInsecure)
}

// ---------------------------------------------------------------------- redirects ----

func testWindowsServerModeRedirects(t *testing.T) {
	t.Run("to another origin", func(t *testing.T) {
		other := newWinRig(t, false)
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			http.Redirect(w, req, other.srv.URL+"/api/v1/install/plan", http.StatusTemporaryRedirect)
		})
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
		assert.Contains(t, res.Out, "another origin")
		assert.Empty(t, other.seen(), "the token does not follow a redirect to another origin")
		assert.False(t, r.installed())
	})
	t.Run("to http", func(t *testing.T) {
		r := newWinRig(t, true)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			http.Redirect(w, req, "http://"+strings.SplitN(r.srv.URL, "://", 2)[1]+"/api/v1/install/plan", http.StatusFound)
		})
		res := r.runWith()
		assert.Equal(t, "https_required", res.failure(), res.Out)
		assert.False(t, r.installed())
	})
	t.Run("without a scheme", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			w.Header().Set("Location", "//"+strings.SplitN(r.srv.URL, "://", 2)[1]+"/api/v1/install/plan")
			w.WriteHeader(http.StatusFound)
		})
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	})
	t.Run("to something that is not a URL", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			w.Header().Set("Location", "file:///etc/passwd")
			w.WriteHeader(http.StatusFound)
		})
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	})
	t.Run("without a Location", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) { w.WriteHeader(http.StatusFound) })
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	})
	t.Run("in a loop", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			http.Redirect(w, req, "/api/v1/install/plan", http.StatusTemporaryRedirect)
		})
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
		assert.Contains(t, res.Out, "more than three times")
	})
	t.Run("on the same origin", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, req *http.Request, n int) {
			if n == 1 {
				http.Redirect(w, req, r.srv.URL+"/api/v1/install/plan", http.StatusTemporaryRedirect)
				return
			}
			servePlan(w, r.planText(nil))
		})
		res := r.runWith()
		assert.Equal(t, 0, res.Code, res.Out)
		var auths []string
		for _, rec := range r.seen() {
			if rec.Path == "/api/v1/install/plan" {
				auths = append(auths, rec.Auth)
			}
		}
		assert.Equal(t, []string{"Bearer " + r.token, "Bearer " + r.token}, auths)
	})
	t.Run("of an artifact to another origin", func(t *testing.T) {
		other := newWinRig(t, false)
		r := newWinRig(t, false)
		// the artifact route of the server redirects
		redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasPrefix(req.URL.Path, "/api/v1/artifacts/") {
				http.Redirect(w, req, other.srv.URL+req.URL.Path, http.StatusFound)
				return
			}
			r.serve(w, req)
		}))
		defer redirecting.Close()
		r.setServer(redirecting)
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
		assert.Empty(t, other.seen(), "the grant does not follow a redirect to another origin")
	})
}

func TestWindowsServerModeRedirects(t *testing.T) { eachWinShell(t, testWindowsServerModeRedirects) }

// ---------------------------------------------------------------- the error codes ----

func testWindowsServerModePlanRejected(t *testing.T) {
	cases := []struct {
		status        int
		class, reason string
		hint          string
	}{
		{http.StatusConflict, "conflict", "fleet_mismatch", "leave -Fleet out"},
		{http.StatusConflict, "conflict", "no_fleet", "no fleet selects this host"},
		{http.StatusConflict, "conflict", "fleet_ambiguous", "different priorities"},
		{http.StatusConflict, "conflict", "rollout_blocking", "version rollout of the fleet"},
		{http.StatusPreconditionFailed, "precondition_failed", "no_target_version", "has no target version"},
		{http.StatusPreconditionFailed, "precondition_failed", "unknown_version", "release tracker"},
		{http.StatusUnprocessableEntity, "validation_failed", "version_not_allowed", "frame of the fleet"},
		{http.StatusUnprocessableEntity, "validation_failed", "invalid_value", "site option"},
		{http.StatusNotFound, "not_found", "", "install.enabled"},
		{http.StatusForbidden, "forbidden", "", "does not allow"},
	}
	for _, tc := range cases {
		tc := tc
		name := tc.reason
		if name == "" {
			name = tc.class
		}
		t.Run(name, func(t *testing.T) {
			r := newWinRig(t, false)
			r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) { problem(w, tc.status, tc.class, tc.reason) })
			res := r.runWith()
			assert.Equal(t, 1, res.Code, res.Out)
			assert.Equal(t, "plan_rejected", res.failure(), res.Out)
			wantCode := tc.reason
			if wantCode == "" {
				wantCode = tc.class
			}
			assert.Contains(t, res.Out, "refused the plan: "+wantCode+" (HTTP")
			assert.Contains(t, res.Out, tc.hint)
			assert.False(t, r.installed())
		})
	}

	t.Run("a wrong token", func(t *testing.T) {
		r := newWinRig(t, false)
		require.NoError(t, os.WriteFile(r.tokenFile, []byte("myre_wrongwrongwrongwrongwrongwrong1\n"), 0o600))
		res := r.runWith()
		assert.Equal(t, "plan_rejected", res.failure(), res.Out)
		assert.Contains(t, res.Out, "unauthorized")
		assert.Contains(t, res.Out, "create a new token")
		assert.NotContains(t, res.Out, "wrongwrong")
	})
	t.Run("an answer that is not a problem document", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "<html>I am a teapot; run: Remove-Item C:\\ -Recurse</html>")
		})
		res := r.runWith()
		assert.Equal(t, "plan_rejected", res.failure(), res.Out)
		assert.Contains(t, res.Out, "http_418")
		assert.NotContains(t, res.Out, "Remove-Item")
	})
	t.Run("a code with characters of a command", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"conflict","detail":"fleet $(id) `+"`id`"+` ok","issues":[{"code":"x;Remove-Item C:"}]}`)
		})
		res := r.runWith()
		assert.Equal(t, "plan_rejected", res.failure(), res.Out)
		assert.NotContains(t, res.Out, "$(")
		assert.NotContains(t, res.Out, "`")
		assert.NotContains(t, res.Out, "Remove-Item")
	})
}

func TestWindowsServerModePlanRejected(t *testing.T) {
	eachWinShell(t, testWindowsServerModePlanRejected)
}

func testWindowsServerModePlanUnavailable(t *testing.T) {
	t.Run("a server that is down", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			problem(w, http.StatusServiceUnavailable, "unavailable", "")
		})
		res := r.runWith()
		assert.Equal(t, "plan_unavailable", res.failure(), res.Out)
		assert.Contains(t, res.Out, "HTTP 503")
		assert.False(t, r.installed())
	})
	t.Run("too many requests", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.Header().Set("Retry-After", "17")
			problem(w, http.StatusTooManyRequests, "too_many_requests", "")
		})
		res := r.runWith()
		assert.Equal(t, "plan_unavailable", res.failure(), res.Out)
		assert.Contains(t, res.Out, "after 17 s")
	})
	t.Run("still preparing after the time that was given", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.Header().Set("Retry-After", "1")
			problem(w, http.StatusServiceUnavailable, "unavailable", "preparing")
		})
		start := time.Now()
		res := r.runWith("-PlanWait", "3")
		assert.Equal(t, "plan_unavailable", res.failure(), res.Out)
		assert.Contains(t, res.Out, "still preparing the artifacts after 3 s")
		assert.GreaterOrEqual(t, len(r.seen()), 3, "it asked again after Retry-After")
		assert.Less(t, time.Since(start), 60*time.Second)
	})
	t.Run("preparing and then ready", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, n int) {
			if n < 3 {
				w.Header().Set("Retry-After", "1")
				problem(w, http.StatusServiceUnavailable, "unavailable", "preparing")
				return
			}
			servePlan(w, r.planText(nil))
		})
		res := r.runWith()
		require.Equal(t, 0, res.Code, res.Out)
		assert.Equal(t, 2, strings.Count(res.Out, "the server is preparing the artifacts"))
	})
}

func TestWindowsServerModePlanUnavailable(t *testing.T) {
	eachWinShell(t, testWindowsServerModePlanUnavailable)
}

func testWindowsServerModePlanFailed(t *testing.T) {
	t.Run("a server that does not answer", func(t *testing.T) {
		if testing.Short() {
			t.Skip("the installer asks again for a few seconds first")
		}
		r := newWinRig(t, false)
		args := r.baseArgs()
		r.srv.Close()
		res := r.run(args...)
		assert.Equal(t, "plan_failed", res.failure(), res.Out)
		assert.Contains(t, res.Out, "certificate store")
		assert.False(t, r.installed())
	})
	t.Run("a certificate that is not trusted", func(t *testing.T) {
		r := newWinRig(t, true)
		// the CA of the test server is not known to this process
		cmd := exec.Command(r.pwsh, append([]string{"-NoProfile", "-NonInteractive", "-File", r.script()}, r.baseArgs()...)...)
		cmd.Env = append(r.env(), "SSL_CERT_FILE="+filepath.Join(t.TempDir(), "none.pem"), "SSL_CERT_DIR="+t.TempDir())
		res := runWithin(t, cmd, 2*time.Minute)
		assert.Equal(t, "plan_failed", res.failure(), res.Out)
		assert.Empty(t, r.seen(), "the token is not sent over a connection that was not verified")
		assert.False(t, r.installed())
	})
	t.Run("a server error", func(t *testing.T) {
		if testing.Short() {
			t.Skip("the installer asks again for a few seconds first")
		}
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
		res := r.runWith()
		assert.Equal(t, "plan_failed", res.failure(), res.Out)
		assert.Contains(t, res.Out, "HTTP 500")
	})
	t.Run("an answer that is too long", func(t *testing.T) {
		r := newWinRig(t, false)
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			_, _ = io.WriteString(w, strings.Repeat("a=1\n", 400000))
		})
		res := r.runWith()
		assert.Equal(t, "plan_failed", res.failure(), res.Out)
		assert.False(t, r.installed())
	})
}

func TestWindowsServerModePlanFailed(t *testing.T) { eachWinShell(t, testWindowsServerModePlanFailed) }

func testWindowsServerModeVerifiesBeforeItInstallsAnything(t *testing.T) {
	r := newWinRig(t, false)
	r.mu.Lock()
	r.tampered = true
	r.mu.Unlock()
	res := r.runWith()
	assert.Equal(t, 1, res.Code, res.Out)
	assert.Equal(t, "sigstore_verification_failed", res.failure(), res.Out)
	assert.NoDirExists(t, r.pf("collector/versions/"+rigVersion))
	assert.NoFileExists(t, r.pf("supervisor/opampsupervisor.exe"))
	assert.NoFileExists(t, r.pf("updater/myriarch-updater.exe"))
}

func TestWindowsServerModeVerifiesBeforeItInstallsAnything(t *testing.T) {
	eachWinShell(t, testWindowsServerModeVerifiesBeforeItInstallsAnything)
}

func testWindowsServerModeChecksTheSHA256OfTheArtifact(t *testing.T) {
	r := newWinRig(t, false)
	r.mu.Lock()
	r.wrongSHA = true
	r.mu.Unlock()
	res := r.runWith()
	assert.Equal(t, "checksum_mismatch", res.failure(), res.Out)
	assert.NoFileExists(t, r.pf("supervisor/opampsupervisor.exe"))
}

func TestWindowsServerModeChecksTheSHA256OfTheArtifact(t *testing.T) {
	eachWinShell(t, testWindowsServerModeChecksTheSHA256OfTheArtifact)
}

func testWindowsServerModeAnExpiredGrantStopsTheDownload(t *testing.T) {
	r := newWinRig(t, false)
	r.answerWith(r.planText(map[string]string{KeyCollectorGrant: "myrd_expired.grant"}))
	res := r.runWith()
	assert.Equal(t, "download_failed", res.failure(), res.Out)
	assert.Contains(t, res.Out, "HTTP 401")
	assert.Contains(t, res.Out, "run the command again")
	assert.NotContains(t, res.Out, "myrd_expired")
}

func TestWindowsServerModeAnExpiredGrantStopsTheDownload(t *testing.T) {
	eachWinShell(t, testWindowsServerModeAnExpiredGrantStopsTheDownload)
}

func testWindowsServerModeNeedsTheUpdaterOfTheBundleAsVerifier(t *testing.T) {
	r := newWinRig(t, false)
	require.NoError(t, os.Remove(filepath.Join(r.bundle, "myriarch-updater.exe")))
	res := r.runWith()
	assert.Equal(t, "sigstore_verifier_missing", res.failure(), res.Out)
	assert.Contains(t, res.Out, "installer bundle")
	assert.False(t, r.installed())

	// a link is not the updater of the archive (creating a link needs a privilege on Windows: without it this part is not run)
	target := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(target, []byte("not the updater"), 0o755))
	link := filepath.Join(r.bundle, "myriarch-updater.exe")
	if err := os.Symlink(target, link); err == nil {
		res = r.runWith()
		assert.Equal(t, "sigstore_verifier_missing", res.failure(), res.Out)
		require.NoError(t, os.Remove(link))
	}

	// a verifier of the operator replaces the one of the bundle for the checks, but the updater that the plan asks for has to come
	// from the bundle
	verifier := filepath.Join(t.TempDir(), "cosign")
	require.NoError(t, os.WriteFile(verifier, []byte("not a cosign"), 0o755))
	res = r.runWith("-Verifier", verifier)
	assert.Equal(t, "file_unreadable", res.failure(), res.Out)
	assert.False(t, r.installed())
}

func TestWindowsServerModeNeedsTheUpdaterOfTheBundleAsVerifier(t *testing.T) {
	eachWinShell(t, testWindowsServerModeNeedsTheUpdaterOfTheBundleAsVerifier)
}

func testWindowsServerModeOptionsThatDoNotCombine(t *testing.T) {
	r := newWinRig(t, false)
	for _, tc := range []struct {
		name string
		more []string
		code string
	}{
		{"with the updater", []string{"-WithUpdater"}, "option_conflict"},
		{"updater url", []string{"-UpdaterUrl", "https://x.example.com/myriarch-updater_0.2.0_windows_amd64.exe"}, "option_conflict"},
		{"updater checksums", []string{"-UpdaterChecksumsUrl", "https://x.example.com/checksums.txt"}, "option_conflict"},
		{"release base url", []string{"-ReleaseBaseUrl", "https://mirror.example.com"}, "option_conflict"},
		{"supervisor version", []string{"-SupervisorVersion", "0.162.0"}, "option_conflict"},
		{"skipped signatures", []string{"-VerifySignatures", "skip"}, "option_conflict"},
		{"latest", []string{"-CollectorVersion", "latest"}, "option_invalid"},
		{"a fleet that is no name", []string{"-Fleet", "Windows Hosts"}, "option_invalid"},
		{"a wait of zero", []string{"-PlanWait", "0"}, "option_invalid"},
		{"a wait that is text", []string{"-PlanWait", "soon"}, "option_invalid"},
		{"an endpoint without a scheme of the allowed ones", []string{"-OpampEndpoint", "ftp://x.example.com"}, "option_invalid"},
	} {
		res := r.runWith(tc.more...)
		assert.Equal(t, tc.code, res.failure(), "%s: %s", tc.name, res.Out)
	}
	for _, server := range []string{r.srv.URL + "/api", "ftp://x.example.com", r.srv.URL + "?x=1", "x.example.com", r.srv.URL + "/"} {
		res := r.run("-Root", r.root, "-Insecure", "-Server", server, "-TokenFile", r.tokenFile, "-SiteType", "edge", "-SiteId", "1")
		assert.Equal(t, "option_invalid", res.failure(), "%s: %s", server, res.Out)
	}
	assert.Empty(t, r.seen())

	// the server mode options mean nothing without -Server
	base := []string{"-Root", r.root, "-OpampEndpoint", "wss://fleet.example.com/v1/opamp", "-TokenFile", r.tokenFile, "-SiteType", "edge", "-SiteId", "1", "-CollectorVersion", rigVersion}
	res := r.run(append(base, "-Fleet", "windows-hosts")...)
	assert.Equal(t, "option_conflict", res.failure(), res.Out)
	res = r.run(append(base, "-PlanWait", "5")...)
	assert.Equal(t, "option_conflict", res.failure(), res.Out)
}

func TestWindowsServerModeOptionsThatDoNotCombine(t *testing.T) {
	eachWinShell(t, testWindowsServerModeOptionsThatDoNotCombine)
}

func testWindowsServerModeNeedsTheSiteAndTheToken(t *testing.T) {
	r := newWinRig(t, false)
	res := r.run("-Root", r.root, "-Insecure", "-Server", r.srv.URL, "-TokenFile", r.tokenFile, "-SiteId", "1")
	assert.Equal(t, "option_missing", res.failure(), res.Out)
	res = r.run("-Root", r.root, "-Insecure", "-Server", r.srv.URL, "-SiteType", "edge", "-SiteId", "1")
	assert.Equal(t, "option_missing", res.failure(), res.Out)
	assert.Contains(t, res.Out, "-Token")
	res = r.run("-Root", r.root, "-Insecure", "-Server", r.srv.URL, "-TokenFile", filepath.Join(r.root, "none"), "-SiteType", "edge", "-SiteId", "1")
	assert.Equal(t, "file_unreadable", res.failure(), res.Out)
	assert.Empty(t, r.seen())
}

func TestWindowsServerModeNeedsTheSiteAndTheToken(t *testing.T) {
	eachWinShell(t, testWindowsServerModeNeedsTheSiteAndTheToken)
}

// The token is a header and nothing else: a token with a character that a header cannot carry is refused before it is sent.
func testWindowsServerModeRefusesATokenThatCouldSplitAHeader(t *testing.T) {
	r := newWinRig(t, false)
	for _, tok := range []string{"myre_abc\r\nX-Evil: 1", "myre abc", "myre_abc\"", "myre_abc\nmyre_def"} {
		res := r.run("-Root", r.root, "-Arch", "amd64", "-Insecure", "-Server", r.srv.URL, "-SiteType", "edge", "-SiteId", "042", "-Token", tok)
		assert.Equal(t, "option_invalid", res.failure(), "%q: %s", tok, res.Out)
	}
	require.NoError(t, os.WriteFile(r.tokenFile, []byte("myre_abc\r\nX-Evil: 1\r\n"), 0o600))
	res := r.runWith()
	assert.Equal(t, "option_invalid", res.failure(), res.Out)
	assert.Empty(t, r.seen())
}

func TestWindowsServerModeRefusesATokenThatCouldSplitAHeader(t *testing.T) {
	eachWinShell(t, testWindowsServerModeRefusesATokenThatCouldSplitAHeader)
}

// ---------------------------------------------- the checks that run before the server is asked ----

// The language mode is the first check of the run, in server mode as well: nothing is read, asked or written in it.
func testWindowsServerModeStopsFirstInConstrainedLanguageMode(t *testing.T) {
	r := newWinRig(t, false)
	var quoted []string
	for _, a := range r.baseArgs() {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "''")+"'")
	}
	command := "$ExecutionContext.SessionState.LanguageMode = 'ConstrainedLanguage'; & $env:MYRIARCH_TEST_SCRIPT " + strings.Join(quoted, " ") + "; exit $LASTEXITCODE"
	cmd := exec.Command(r.pwsh, "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Env = append(r.env(), "MYRIARCH_TEST_SCRIPT="+r.script())
	res := runWithin(t, cmd, time.Minute)
	assert.Equal(t, 1, res.Code, res.Out)
	assert.Equal(t, "language_mode_unsupported", res.failure(), res.Out)
	assert.Contains(t, res.Out, "hash rule")
	assert.Empty(t, r.seen(), "the server is not asked and no token is sent")
	assert.False(t, r.installed())
}

func TestWindowsServerModeStopsFirstInConstrainedLanguageMode(t *testing.T) {
	eachWinShell(t, testWindowsServerModeStopsFirstInConstrainedLanguageMode)
}

// The services of the upstream packages stop the run before the server is asked, a work directory is made or a token is sent. The
// run is the real Invoke-Main with a Get-Service that lists the service of a made-up host, and with every function that talks to the
// server or writes something replaced by one that fails.
func testWindowsServerModeConflictingInstallStopsBeforeTheServerIsAsked(t *testing.T) {
	r := newWinRig(t, false)
	harness := `
param([Parameter(Mandatory)][string]$Installer, [Parameter(Mandatory)][string]$Templates, [Parameter(Mandatory)][string]$TokenFile, [Parameter(Mandatory)][string]$Server)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
. $Installer -Server $Server -Insecure -TokenFile $TokenFile -SiteType edge -SiteId 1 -Arch amd64 -TemplatesDir $Templates
$ErrorActionPreference = 'Stop'
$script:Calls = New-Object System.Collections.Generic.List[string]
function Test-Administrator { return $true }
function Get-Service {
  [CmdletBinding()] param([string]$Name)
  if ($Name -eq $env:MYRIARCH_TEST_SERVICE) { return [pscustomobject]@{ Name = $Name; Status = 'Running' } }
}

foreach ($name in @('New-WorkDirectory', 'Get-PlanFile', 'Send-ServerRequest', 'Invoke-Download', 'Stop-ServiceAndWait', 'Resolve-Verifier', 'Initialize-DataDirectories',
    'Install-Collector', 'Install-SupervisorBinary', 'Install-SupervisorService')) {
  Set-Item -Path "function:$name" -Value ([scriptblock]::Create("param() [void]` + "`" + `$script:Calls.Add('$name'); throw ('unexpected call of $name')"))
}
try { Invoke-Main; Write-Host 'result: ok' } catch { Write-Host ('result: ' + $_.Exception.Message) }
Write-Host ('calls: ' + $script:Calls.Count)
`
	script := filepath.Join(t.TempDir(), "conflict.ps1")
	require.NoError(t, os.WriteFile(script, []byte(harness), 0o600))
	templates := filepath.Join(r.bundle, "deploy")
	for _, tc := range []struct{ service, shown string }{
		{"opampsupervisor", "service opampsupervisor (Running)"},
		{"otelcol-contrib", "service otelcol-contrib (Running)"},
	} {
		cmd := exec.Command(r.pwsh, "-NoProfile", "-NonInteractive", "-File", script, "-Installer", r.script(), "-Templates", templates, "-TokenFile", r.tokenFile, "-Server", r.srv.URL)
		cmd.Env = append(r.env(), "MYRIARCH_TEST_SERVICE="+tc.service)
		res := runWithin(t, cmd, time.Minute)
		assert.Contains(t, res.Out, "result: [conflicting_install] ", res.Out)
		assert.Contains(t, res.Out, tc.shown)
		assert.Contains(t, res.Out, "removes nothing")
		assert.Contains(t, res.Out, "calls: 0", "no work directory, plan request, download or installation step was reached")
	}
	assert.Empty(t, r.seen(), "the server is not asked and no token is sent")
	assert.False(t, r.installed(), "nothing is created")
}

func TestWindowsServerModeConflictingInstallStopsBeforeTheServerIsAsked(t *testing.T) {
	eachWinShell(t, testWindowsServerModeConflictingInstallStopsBeforeTheServerIsAsked)
}

// The supervisor of the server mode goes to the path of the service, and the run stops with supervisor_missing when a security tool
// took it away right after the copy: the file that was verified has to be the one that is there. The copy is replaced by one that
// removes its target, the way a quarantine does.
func testWindowsServerModeSupervisorMissing(t *testing.T) {
	r := newWinRig(t, false)
	harness := `
param([Parameter(Mandatory)][string]$Installer, [Parameter(Mandatory)][string]$Templates, [Parameter(Mandatory)][string]$TokenFile, [Parameter(Mandatory)][string]$Server, [Parameter(Mandatory)][string]$Root)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
. $Installer -Server $Server -Insecure -TokenFile $TokenFile -SiteType edge -SiteId 1 -Arch amd64 -TemplatesDir $Templates -Root $Root
$ErrorActionPreference = 'Stop'
$original = ${function:Copy-VerifiedFile}
function Copy-VerifiedFile {
  param([string]$Source, [string]$Destination, [string]$ExpectedHash)
  & $original -Source $Source -Destination $Destination -ExpectedHash $ExpectedHash
  if ($Destination -like '*opampsupervisor.exe') { Remove-Item -LiteralPath $Destination -Force }
}

try { Invoke-Main; Write-Host 'result: ok' } catch { Write-Host ('result: ' + $_.Exception.Message) }
`
	script := filepath.Join(t.TempDir(), "missing.ps1")
	require.NoError(t, os.WriteFile(script, []byte(harness), 0o600))
	cmd := exec.Command(r.pwsh, "-NoProfile", "-NonInteractive", "-File", script, "-Installer", r.script(), "-Templates", filepath.Join(r.bundle, "deploy"),
		"-TokenFile", r.tokenFile, "-Server", r.srv.URL, "-Root", r.root)
	cmd.Env = r.env()
	res := runWithin(t, cmd, time.Minute)
	assert.Contains(t, res.Out, "result: [supervisor_missing] the supervisor binary does not exist: ", res.Out)
	assert.Contains(t, res.Out, "opampsupervisor.exe")
	assert.Contains(t, res.Out, "protection history", "the message names the next step")
	assert.NoFileExists(t, r.pf("supervisor/VERSION"), "a run that stops here leaves no marker, so the next run fetches the file again")

	// a directory at the path of the service stops the run before anything is fetched
	r2 := newWinRig(t, false)
	require.NoError(t, os.MkdirAll(r2.pf("supervisor/opampsupervisor.exe"), 0o755))
	res2 := r2.runWith()
	assert.Equal(t, "supervisor_missing", res2.failure(), res2.Out)
	assert.Contains(t, res2.Out, "is not a regular file")
	assert.Contains(t, res2.Out, "remove the item")
}

func TestWindowsServerModeSupervisorMissing(t *testing.T) {
	eachWinShell(t, testWindowsServerModeSupervisorMissing)
}

// The rules for URLs and redirects, run against the functions of the installer for an https server: no TLS server is needed, so they
// run on every platform where PowerShell 7 does (the tests with a TLS server above run on Linux only).
func testWindowsServerModeRulesOfHTTPSAndOrigin(t *testing.T) {
	r := newWinRig(t, false)
	harness := `
param([Parameter(Mandatory)][string]$Installer, [Parameter(Mandatory)][string]$Templates, [Parameter(Mandatory)][string]$TokenFile)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
. $Installer -Server https://fleet.example.com -TokenFile $TokenFile -SiteType edge -SiteId 1 -Arch amd64 -TemplatesDir $Templates
$ErrorActionPreference = 'Stop'
$script:failures = 0
function Check { param([string]$Title, [bool]$Ok) if ($Ok) { Write-Host "  ok    $Title" } else { $script:failures++; Write-Host "  FAIL  $Title" } }
function Message-Of { param([scriptblock]$Block) try { & $Block; return '' } catch { return $_.Exception.Message } }
function Redirect { param([string]$Location) return [pscustomobject]@{ Status = 302; Location = $Location; RetryAfter = '' } }
$script:Server = 'https://fleet.example.com'

Check 'a redirect on the same origin is followed' ((Get-RedirectTarget -Result (Redirect '/api/v1/install/plan')) -ceq 'https://fleet.example.com/api/v1/install/plan')
Check 'a redirect to the same origin with a full URL is followed' ((Get-RedirectTarget -Result (Redirect 'https://fleet.example.com/x?y=1')) -ceq 'https://fleet.example.com/x?y=1')
Check 'a redirect to http is https_required' ((Message-Of { Get-RedirectTarget -Result (Redirect 'http://fleet.example.com/x') }) -match '^\[https_required\] ')
Check 'a redirect to another host is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect 'https://evil.example.com/x') }) -match '^\[plan_invalid\] .*another origin')
Check 'a redirect to a host that starts like the server is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect 'https://fleet.example.com.evil.example.com/x') }) -match '^\[plan_invalid\] ')
Check 'a redirect to another port is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect 'https://fleet.example.com:8443/x') }) -match '^\[plan_invalid\] ')
Check 'a redirect with user information is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect 'https://fleet.example.com@evil.example.com/x') }) -match '^\[plan_invalid\] ')
Check 'a redirect without a scheme is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect '//fleet.example.com/x') }) -match '^\[plan_invalid\] ')
Check 'a redirect to a file is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect 'file:///etc/passwd') }) -match '^\[plan_invalid\] ')
Check 'a redirect with a character that no URL of the installation has is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect '/x y') }) -match '^\[plan_invalid\] ')
Check 'a redirect without a Location is plan_invalid' ((Message-Of { Get-RedirectTarget -Result (Redirect '') }) -match '^\[plan_invalid\] ')

$file = 'otelcol-contrib_0.162.0_windows_amd64.tar.gz'
Check 'an artifact URL on the origin is accepted' ((Message-Of { Assert-PlanUrl -Name collector -Url "https://fleet.example.com/api/v1/artifacts/c0/content/$file" -File $file }) -eq '')
Check 'an artifact URL over http is https_required' ((Message-Of { Assert-PlanUrl -Name collector -Url "http://fleet.example.com/api/v1/artifacts/c0/content/$file" -File $file }) -match '^\[https_required\] ')
Check 'an artifact URL on another origin is plan_invalid' ((Message-Of { Assert-PlanUrl -Name collector -Url "https://evil.example.com/api/v1/artifacts/c0/content/$file" -File $file }) -match '^\[plan_invalid\] ')

Check 'an endpoint of the server over wss is accepted' ((Message-Of { Assert-PlanEndpoint -Url 'wss://fleet.example.com/v1/opamp' }) -eq '')
Check 'an endpoint of the server over https is accepted' ((Message-Of { Assert-PlanEndpoint -Url 'https://fleet.example.com/v1/opamp' }) -eq '')
Check 'an endpoint over ws is plan_invalid' ((Message-Of { Assert-PlanEndpoint -Url 'ws://fleet.example.com/v1/opamp' }) -match '^\[plan_invalid\] ')
Check 'an endpoint of another host is plan_invalid' ((Message-Of { Assert-PlanEndpoint -Url 'wss://evil.example.com/v1/opamp' }) -match '^\[plan_invalid\] ')

# an IPv6 literal and a port
$script:Server = 'https://[2001:db8::1]:8443'
Check 'an endpoint with an IPv6 literal and a port is accepted' ((Message-Of { Assert-PlanEndpoint -Url 'wss://[2001:db8::1]:8443/v1/opamp' }) -eq '')
Check 'an endpoint with another port is plan_invalid' ((Message-Of { Assert-PlanEndpoint -Url 'wss://[2001:db8::1]:8444/v1/opamp' }) -match '^\[plan_invalid\] ')

Write-Host "checks: failures $($script:failures)"
if ($script:failures -gt 0) { exit 1 }
`
	script := filepath.Join(t.TempDir(), "rules.ps1")
	require.NoError(t, os.WriteFile(script, []byte(harness), 0o600))
	cmd := exec.Command(r.pwsh, "-NoProfile", "-NonInteractive", "-File", script, "-Installer", r.script(), "-Templates", filepath.Join(r.bundle, "deploy"), "-TokenFile", r.tokenFile)
	cmd.Env = r.env()
	res := runWithin(t, cmd, time.Minute)
	assert.Equal(t, 0, res.Code, res.Out)
	assert.NotContains(t, res.Out, "FAIL")
	assert.Contains(t, res.Out, "checks: failures 0")
}

func TestWindowsServerModeRulesOfHTTPSAndOrigin(t *testing.T) {
	eachWinShell(t, testWindowsServerModeRulesOfHTTPSAndOrigin)
}

// ------------------------------------------------------------------------ the shells and the stand-ins ----

// eachWinShell runs a test with every shell that the installer meets: PowerShell 7 and, on Windows, Windows PowerShell 5.1 (the
// powershell.exe that the command of the web UI starts). newWinRig reads the shell from the name of the test.
func eachWinShell(t *testing.T, fn func(*testing.T)) {
	t.Helper()
	shells := []string{"pwsh"}
	if runtime.GOOS == "windows" {
		shells = []string{"powershell.exe", "pwsh"}
	}
	for _, name := range shells {
		t.Run(name, fn)
	}
}

func findWinShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return findPwsh(t)
	}
	if strings.Contains(t.Name(), "/powershell.exe") {
		p, err := exec.LookPath("powershell.exe")
		if err != nil {
			t.Skip("powershell.exe is not found")
		}
		return p
	}
	if p := os.Getenv("MYRIARCH_PWSH"); p != "" {
		return p
	}
	p, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is not installed: the cases with PowerShell 7 are not run")
	}
	return p
}

// standInSource is the program behind the stand-ins: as myriarch-updater.exe it writes down where it runs from and its arguments and
// refuses a Sigstore bundle file that says TAMPERED (the verify-bundle of the real updater); as otelcol-contrib.exe it prints a version.
const standInSource = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if strings.HasPrefix(filepath.Base(os.Args[0]), "otelcol") {
		fmt.Println("otelcol-contrib version @VERSION@")
		return
	}
	if dir := os.Getenv("LOGDIR"); dir != "" {
		if f, err := os.OpenFile(filepath.Join(dir, "updater.argv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s\n", os.Args[0], strings.Join(os.Args[1:], " "))
			f.Close()
		}
	}
	if len(os.Args) < 2 || os.Args[1] != "verify-bundle" {
		os.Exit(2)
	}
	for i, a := range os.Args {
		if a == "--bundle" && i+1 < len(os.Args) {
			if b, err := os.ReadFile(os.Args[i+1]); err == nil && strings.Contains(string(b), "TAMPERED") {
				fmt.Println("bundle refused")
				os.Exit(1)
			}
		}
	}
}
`

var standIn struct {
	once sync.Once
	dir  string
	exe  string
	err  error
}

// standInBinary builds the stand-in program once for all tests and returns its content.
func standInBinary(t *testing.T) []byte {
	t.Helper()
	standIn.once.Do(func() {
		standIn.dir, standIn.err = os.MkdirTemp("", "myriarch-standin-")
		if standIn.err != nil {
			return
		}
		src := strings.ReplaceAll(standInSource, "@VERSION@", rigVersion)
		if standIn.err = os.WriteFile(filepath.Join(standIn.dir, "main.go"), []byte(src), 0o600); standIn.err != nil {
			return
		}
		if standIn.err = os.WriteFile(filepath.Join(standIn.dir, "go.mod"), []byte("module standin\n\ngo 1.21\n"), 0o600); standIn.err != nil {
			return
		}
		standIn.exe = filepath.Join(standIn.dir, "standin.exe")
		cmd := exec.Command("go", "build", "-o", standIn.exe, ".")
		cmd.Dir = standIn.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			standIn.err = fmt.Errorf("build the stand-in: %w: %s", err, out)
		}
	})
	require.NoError(t, standIn.err)
	b, err := os.ReadFile(standIn.exe)
	require.NoError(t, err)
	return b
}

func TestMain(m *testing.M) {
	code := m.Run()
	if standIn.dir != "" {
		_ = os.RemoveAll(standIn.dir)
	}
	os.Exit(code)
}

// ------------------------------------------------------------------------ the retry policy and the bundle directory ----

// Windows PowerShell 5.1 wraps what a .NET method throws in a MethodInvocationException, and the WebException of .NET Framework has no
// inner exception: a timeout or a failed name resolution is retried only when the policy sees the WebException.
func testWindowsServerModeRetriesWhatTheShellWraps(t *testing.T) {
	r := newWinRig(t, false)
	// a port nobody listens on
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := l.Addr().String()
	require.NoError(t, l.Close())
	harness := `
param([Parameter(Mandatory)][string]$Installer, [Parameter(Mandatory)][string]$Templates, [Parameter(Mandatory)][string]$TokenFile, [Parameter(Mandatory)][string]$Closed)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
. $Installer -Server https://fleet.example.com -TokenFile $TokenFile -SiteType edge -SiteId 1 -Arch amd64 -TemplatesDir $Templates
$ErrorActionPreference = 'Stop'
$script:failures = 0
function Check { param([string]$Title, [bool]$Ok) if ($Ok) { Write-Host "  ok    $Title" } else { $script:failures++; Write-Host "  FAIL  $Title" } }
function Web { param([string]$Status) return New-Object System.Net.WebException('failed', [System.Net.WebExceptionStatus]$Status) }
function Wrap { param($Inner) return New-Object System.Management.Automation.MethodInvocationException('Exception calling "GetResponse" with "0" argument(s): failed', $Inner) }

foreach ($status in @('Timeout', 'NameResolutionFailure', 'ConnectFailure', 'ConnectionClosed')) {
  Check "a WebException $status is retried" (Test-TransientDownloadError (Web $status))
  Check "a wrapped WebException $status is retried" (Test-TransientDownloadError (Wrap (Web $status)))
}
Check 'a WebException TrustFailure is not retried' (-not (Test-TransientDownloadError (Web 'TrustFailure')))
Check 'a wrapped WebException TrustFailure is not retried' (-not (Test-TransientDownloadError (Wrap (Web 'TrustFailure'))))

$r = Send-ServerRequest -Method GET -Url "http://$Closed/x" -OutFile (Join-Path ([IO.Path]::GetTempPath()) 'answer.bin') -Bearer '' -MaxBytes 1024 -TimeoutSeconds 10
Check 'a request to a closed port has no answer' ($r.Status -eq 0)
Check 'the exception of that request is not wrapped by the shell' (-not ($r.Exception -is [System.Management.Automation.MethodInvocationException]))
Check 'a refused connection is retried' (Test-TransientDownloadError $r.Exception)

Write-Host "checks: failures $($script:failures)"
if ($script:failures -gt 0) { exit 1 }
`
	script := filepath.Join(t.TempDir(), "retry.ps1")
	require.NoError(t, os.WriteFile(script, []byte(harness), 0o600))
	cmd := exec.Command(r.pwsh, "-NoProfile", "-NonInteractive", "-File", script, "-Installer", r.script(), "-Templates", filepath.Join(r.bundle, "deploy"), "-TokenFile", r.tokenFile, "-Closed", closed)
	cmd.Env = r.env()
	res := runWithin(t, cmd, time.Minute)
	assert.Equal(t, 0, res.Code, res.Out)
	assert.NotContains(t, res.Out, "FAIL")
	assert.Contains(t, res.Out, "checks: failures 0")
}

func TestWindowsServerModeRetriesWhatTheShellWraps(t *testing.T) {
	eachWinShell(t, testWindowsServerModeRetriesWhatTheShellWraps)
}

// The updater of the bundle verifies what a SYSTEM run installs and is installed as a SYSTEM service: the bundle has to be in a
// directory that unprivileged accounts cannot change, checked before the server is asked, outside staging mode.
func testWindowsServerModeBundleDirectoryMustBeAdminOnly(t *testing.T) {
	r := newWinRig(t, false)
	harness := `
param([Parameter(Mandatory)][string]$Installer, [Parameter(Mandatory)][string]$Templates, [Parameter(Mandatory)][string]$TokenFile)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
. $Installer -Server https://fleet.example.com -TokenFile $TokenFile -SiteType edge -SiteId 1 -Arch amd64 -TemplatesDir $Templates
$ErrorActionPreference = 'Stop'
$script:failures = 0
function Check { param([string]$Title, [bool]$Ok) if ($Ok) { Write-Host "  ok    $Title" } else { $script:failures++; Write-Host "  FAIL  $Title" } }
function Message-Of { param([scriptblock]$Block) try { & $Block; return '' } catch { return $_.Exception.Message } }
$script:AdminOnly = $false
$script:Checked = ''
function Test-AclEnforced { return $true }
function Test-AdminOnlyDirectory { param([string]$Path) $script:Checked = $Path; return $script:AdminOnly }

$script:StagingMode = $false
$msg = Message-Of { Assert-BundleDirectory }
Check 'a bundle in a directory that others can change is directory_unsafe' ($msg -match '^\[directory_unsafe\] ')
Check 'the message names the directory and the next step' ($msg.Contains($script:Checked) -and $msg -match 'only Administrators can write')
Check 'the directory is the one of myriarch-updater.exe' ((Get-BundleUpdaterPath) -ceq (Join-Path $script:Checked 'myriarch-updater.exe'))
$script:AdminOnly = $true
Check 'a bundle in an administrators-only directory is accepted' ((Message-Of { Assert-BundleDirectory }) -eq '')
$script:AdminOnly = $false
$script:StagingMode = $true
Check 'staging mode does not check the directory' ((Message-Of { Assert-BundleDirectory }) -eq '')

Write-Host "checks: failures $($script:failures)"
if ($script:failures -gt 0) { exit 1 }
`
	script := filepath.Join(t.TempDir(), "bundledir.ps1")
	require.NoError(t, os.WriteFile(script, []byte(harness), 0o600))
	cmd := exec.Command(r.pwsh, "-NoProfile", "-NonInteractive", "-File", script, "-Installer", r.script(), "-Templates", filepath.Join(r.bundle, "deploy"), "-TokenFile", r.tokenFile)
	cmd.Env = r.env()
	res := runWithin(t, cmd, time.Minute)
	assert.Equal(t, 0, res.Code, res.Out)
	assert.NotContains(t, res.Out, "FAIL")
	assert.Contains(t, res.Out, "checks: failures 0")

	// the check comes before the work directory and the first request of the run
	text, err := os.ReadFile(r.script())
	require.NoError(t, err)
	main := string(text[strings.Index(string(text), "function Invoke-Main"):])
	check := strings.Index(main, "Assert-BundleDirectory")
	assert.Less(t, strings.Index(main, "Assert-NoConflictingInstall"), check)
	assert.Less(t, check, strings.Index(main, "New-WorkDirectory -Staging"))
	assert.Less(t, check, strings.Index(main, "Get-PlanFile -Arch"))
}

func TestWindowsServerModeBundleDirectoryMustBeAdminOnly(t *testing.T) {
	eachWinShell(t, testWindowsServerModeBundleDirectoryMustBeAdminOnly)
}
