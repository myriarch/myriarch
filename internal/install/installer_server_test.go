// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/testutil/linuxhost"
)

// These tests run deploy/scripts/install-linux.sh in its server mode (--server) against a test server that plays the install API
// of internal/install: the plan, and the artifacts with the install grants. The script runs in staging mode (--root), so it needs
// no root and changes nothing on the machine; its verifier is a stand-in for the myriarch-updater of the installer bundle (the
// script takes the verifier from the bundle it runs from). They check
//   - that every option which the rendered commands (the golden files) pass is accepted, and that the whole run works,
//   - that the plan is read as data (ADR-0046): unknown keys, too many lines or bytes, a URL of another origin, http, a redirect
//     to another origin are all refused, and nothing is installed when the plan is refused,
//   - every error code of the server mode: plan_failed, plan_invalid, plan_rejected, plan_unavailable, https_required,
//     conflicting_install and supervisor_missing,
//   - that the token and the grants reach the server as request headers only.

const (
	rigCollectorGrant  = "myrd_collectorgrant.abc123"
	rigSupervisorGrant = "myrd_supervisorgrant.def456"
	rigVersion         = "0.162.0"
)

// recorded is one request that the test server saw.
type recorded struct {
	Method, Path, Auth, Body string
}

type installerRig struct {
	t         *testing.T
	arch      string
	root      string // the staging root of the installer
	bundle    string // the unpacked installer bundle: deploy/ and the stand-in for myriarch-updater
	bin       string // stand-ins on the PATH
	logs      string
	tmp       string
	token     string
	tokenFile string
	caFile    string
	srv       *httptest.Server
	secure    bool

	mu       sync.Mutex
	requests []recorded
	// plan answers the request for the plan; the default serves planText().
	plan  func(w http.ResponseWriter, r *http.Request, n int)
	plans int
}

func newInstallerRig(t *testing.T, secure bool, arch string) *installerRig {
	t.Helper()
	linuxhost.SkipWithoutShell(t)
	for _, tool := range []string{"curl", "awk", "od", "wc", "install", "cmp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	r := &installerRig{t: t, arch: arch, token: testToken, secure: secure}
	dir := t.TempDir()
	r.root, r.bundle, r.bin, r.logs, r.tmp = filepath.Join(dir, "root"), filepath.Join(dir, "bundle"), filepath.Join(dir, "bin"), filepath.Join(dir, "logs"), filepath.Join(dir, "tmp")
	for _, d := range []string{r.root, r.bin, r.logs, r.tmp} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	r.tokenFile = filepath.Join(dir, "enrollment-token")
	require.NoError(t, os.WriteFile(r.tokenFile, []byte(r.token+"\n"), 0o600))
	r.buildBundle()
	r.buildStandIns()

	h := http.HandlerFunc(r.serve)
	if secure {
		r.srv = httptest.NewTLSServer(h)
		r.caFile = filepath.Join(dir, "ca.pem")
		require.NoError(t, os.WriteFile(r.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.srv.Certificate().Raw}), 0o644))
	} else {
		r.srv = httptest.NewServer(h)
		r.caFile = filepath.Join(dir, "ca.pem")
		require.NoError(t, os.WriteFile(r.caFile, []byte("not used over http\n"), 0o644))
	}
	t.Cleanup(r.srv.Close)
	return r
}

// buildBundle lays out what the installer bundle holds: the script, its templates and a stand-in for myriarch-updater.
func (r *installerRig) buildBundle() {
	root := repoRoot(r.t)
	for _, rel := range []string{"deploy/scripts/install-linux.sh", "deploy/supervisor", "deploy/systemd", "deploy/updater"} {
		copyTree(r.t, filepath.Join(root, rel), filepath.Join(r.bundle, rel))
	}
	// The stand-in for verify-bundle: it writes down its arguments and refuses a bundle file that says TAMPERED.
	stub := `#!/bin/sh
printf '%s\n' "$*" >> "$LOGDIR/updater.argv"
[ "$1" = verify-bundle ] || exit 2
bundle=
while [ $# -gt 0 ]; do
  [ "$1" = --bundle ] && bundle=$2
  shift
done
if grep -q TAMPERED "$bundle" 2>/dev/null; then exit 1; fi
exit 0
`
	require.NoError(r.t, os.WriteFile(filepath.Join(r.bundle, "myriarch-updater"), []byte(stub), 0o755))
}

// buildStandIns puts a curl on the PATH that writes down its arguments and then runs the real one. It also answers the lookup of
// the newest release (--head) the way GitHub does when the test sets NEWEST.
func (r *installerRig) buildStandIns() {
	real, err := exec.LookPath("curl")
	require.NoError(r.t, err)
	script := `#!/bin/sh
for a in "$@"; do printf '%s\0' "$a"; done >> "$LOGDIR/curl.argv"
case " $* " in
  *" --head "*)
    if [ -n "$NEWEST" ]; then
      printf 'HTTP/2 302\r\nlocation: https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v%s\r\n\r\n' "$NEWEST"
      exit 0
    fi ;;
esac
exec ` + real + ` "$@"
`
	require.NoError(r.t, os.WriteFile(filepath.Join(r.bin, "curl"), []byte(script), 0o755))
}

// withSystemd puts a systemctl on the PATH that reports the given units of the upstream packages as enabled.
func (r *installerRig) withSystemd(units ...string) {
	var cases strings.Builder
	for _, u := range units {
		fmt.Fprintf(&cases, "  *is-enabled*%s*) echo enabled; exit 0 ;;\n", u)
	}
	script := "#!/bin/sh\ncase \"$*\" in\n" + cases.String() + "esac\nexit 1\n"
	require.NoError(r.t, os.WriteFile(filepath.Join(r.bin, "systemctl"), []byte(script), 0o755))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		rel, err := filepath.Rel(src, path)
		require.NoError(t, err)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	}))
}

// ----------------------------------------------------------------------- the server ----

func (r *installerRig) collectorFile() string {
	return fmt.Sprintf("otelcol-contrib_%s_linux_%s.tar.gz", rigVersion, r.arch)
}
func (r *installerRig) supervisorFile() string {
	return fmt.Sprintf("opampsupervisor_%s_linux_%s", rigVersion, r.arch)
}

func (r *installerRig) collectorArchive() []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho \"otelcol-contrib version " + rigVersion + "\"\n"
	require.NoError(r.t, tw.WriteHeader(&tar.Header{Name: "otelcol-contrib", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	require.NoError(r.t, err)
	require.NoError(r.t, tw.Close())
	require.NoError(r.t, gz.Close())
	return buf.Bytes()
}

func (r *installerRig) artifact(path string) ([]byte, string, bool) {
	switch {
	case strings.HasPrefix(path, "/api/v1/artifacts/c0/content/"):
		file := strings.TrimPrefix(path, "/api/v1/artifacts/c0/content/")
		return r.artifactBody(file, r.collectorFile(), r.collectorArchive()), rigCollectorGrant, true
	case strings.HasPrefix(path, "/api/v1/artifacts/s0/content/"):
		file := strings.TrimPrefix(path, "/api/v1/artifacts/s0/content/")
		return r.artifactBody(file, r.supervisorFile(), []byte("#!/bin/sh\necho supervisor\n")), rigSupervisorGrant, true
	}
	return nil, "", false
}

func (r *installerRig) artifactBody(file, name string, content []byte) []byte {
	switch file {
	case name:
		return content
	case name + ".sha256":
		sum := sha256.Sum256(content)
		return []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	case name + ".sigstore.json":
		return []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)
	}
	return nil
}

func (r *installerRig) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.requests = append(r.requests, recorded{Method: req.Method, Path: req.URL.Path, Auth: req.Header.Get("Authorization"), Body: string(body)})
	var n int
	if req.URL.Path == "/api/v1/install/plan" {
		r.plans++
		n = r.plans
	}
	plan := r.plan
	r.mu.Unlock()
	switch {
	case req.URL.Path == "/api/v1/install/plan" && req.Method == http.MethodPost:
		if req.Header.Get("Authorization") != "Bearer "+r.token {
			problem(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		if plan == nil {
			plan = func(w http.ResponseWriter, _ *http.Request, _ int) { servePlan(w, r.planText(nil)) }
		}
		plan(w, req, n)
	case strings.HasPrefix(req.URL.Path, "/api/v1/artifacts/"):
		content, grant, ok := r.artifact(req.URL.Path)
		if !ok || content == nil {
			http.NotFound(w, req)
			return
		}
		if req.Header.Get("Authorization") != "Bearer "+grant {
			problem(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		_, _ = w.Write(content)
	default:
		http.NotFound(w, req)
	}
}

func servePlan(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, text)
}

// problem writes a problem document the way the server does: the class is the code, the reason the code of the first issue.
func problem(w http.ResponseWriter, status int, class, reason string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	issues := ""
	if reason != "" {
		issues = fmt.Sprintf(`,"issues":[{"severity":"error","code":%q,"message":"the reason"}]`, reason)
	}
	_, _ = fmt.Fprintf(w, `{"type":"about:blank","title":"refused","status":%d,"detail":"the server says: %s","code":%q%s}`, status, reason, class, issues)
}

// planText is a valid plan for this server; over replaces the value of a key (or, with an empty value, drops the line).
func (r *installerRig) planText(over map[string]string) string {
	u := r.srv.URL
	scheme := map[bool]string{true: "wss", false: "ws"}[r.secure]
	vals := map[string]string{
		KeyFormat: "1", KeyFleet: "linux-hosts", KeyVersionSource: "git",
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

func (r *installerRig) answerWith(text string) {
	r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) { servePlan(w, text) })
}

func (r *installerRig) seen() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.requests...)
}

// ------------------------------------------------------------------------- running ----

type installerResult struct {
	Out  string
	Code int
}

// failure is the code of the one-line error, or "".
func (res installerResult) failure() string {
	m := regexp.MustCompile(`\[myriarch-install\] ERROR \[([a-z][a-z0-9_]*)\]`).FindStringSubmatch(res.Out)
	if m == nil {
		return ""
	}
	return m[1]
}

func (r *installerRig) baseArgs() []string {
	return []string{"--root", r.root, "--arch", r.arch, "--server", r.srv.URL, "--ca-file", r.caFile, "--token-file", r.tokenFile,
		"--site-type", "edge", "--site-id", "042"}
}

func (r *installerRig) run(args ...string) installerResult {
	r.t.Helper()
	cmd := exec.Command("bash", filepath.Join(r.bundle, "deploy", "scripts", "install-linux.sh"))
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = []string{"PATH=" + r.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + r.root, "TMPDIR=" + r.tmp, "LOGDIR=" + r.logs, "LANG=C"}
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	done := make(chan error, 1)
	require.NoError(r.t, cmd.Start())
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		r.t.Fatalf("the installer did not end: %s", out.String())
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		require.ErrorAs(r.t, err, &ee, out.String())
		code = ee.ExitCode()
	}
	return installerResult{Out: out.String(), Code: code}
}

// runWith runs the installer with the base arguments and some more.
func (r *installerRig) runWith(more ...string) installerResult {
	r.t.Helper()
	return r.run(append(r.baseArgs(), more...)...)
}

func (r *installerRig) installed() bool {
	_, err := os.Stat(filepath.Join(r.root, "opt"))
	return err == nil
}

func (r *installerRig) curlArgs() []string {
	b, err := os.ReadFile(filepath.Join(r.logs, "curl.argv"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func (r *installerRig) updaterCalls() string {
	b, _ := os.ReadFile(filepath.Join(r.logs, "updater.argv"))
	return string(b)
}

func (r *installerRig) assertSecretsOnlyInHeaders(out string) {
	r.t.Helper()
	for _, a := range r.curlArgs() {
		assert.NotContains(r.t, a, r.token, "an argument of curl")
		assert.NotContains(r.t, a, rigCollectorGrant, "an argument of curl")
		assert.NotContains(r.t, a, rigSupervisorGrant, "an argument of curl")
	}
	for _, s := range []string{r.token, rigCollectorGrant, rigSupervisorGrant} {
		assert.NotContains(r.t, out, s, "the output of the installer")
	}
	for _, rec := range r.seen() {
		for _, s := range []string{r.token, rigCollectorGrant, rigSupervisorGrant} {
			assert.NotContains(r.t, rec.Path, s, "the URL of a request")
		}
	}
}

// ------------------------------------------------------------------------ the tests ----

func TestServerModeInstallsFromThePlan(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	res := r.runWith("--fleet", "linux-hosts", "--site-name", `Edge "north"`, "--environment", "prod")
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, res.Out, "plan: fleet linux-hosts, collector v0.162.0 and supervisor v0.162.0 (version from git), updater: true")
	assert.Contains(t, res.Out, "Sigstore signature of "+r.collectorFile()+" ok")
	assert.Contains(t, res.Out, "Sigstore signature of "+r.supervisorFile()+" ok")

	// what the server saw: the token on the plan, the grant of each artifact on its files, and a body without a secret
	var plans, artifacts int
	for _, rec := range r.seen() {
		switch {
		case rec.Path == "/api/v1/install/plan":
			plans++
			assert.Equal(t, "POST", rec.Method)
			assert.Equal(t, "Bearer "+r.token, rec.Auth)
			assert.NotContains(t, rec.Body, r.token)
			assert.JSONEq(t, fmt.Sprintf(`{"os":"linux","arch":"amd64","fleet":"linux-hosts","attributes":{"site.type":"edge","site.id":"042","site.name":"Edge \"north\"","deployment.environment.name":"prod"%s}}`, hostAttr(rec.Body)), rec.Body)
		case strings.Contains(rec.Path, "/c0/"):
			artifacts++
			assert.Equal(t, "Bearer "+rigCollectorGrant, rec.Auth)
		case strings.Contains(rec.Path, "/s0/"):
			artifacts++
			assert.Equal(t, "Bearer "+rigSupervisorGrant, rec.Auth)
		}
	}
	assert.Equal(t, 1, plans)
	assert.Equal(t, 6, artifacts, "archive, .sha256 and .sigstore.json of both artifacts")
	r.assertSecretsOnlyInHeaders(res.Out)

	// the host: collector at the version of the plan, the endpoint of the plan, the updater of the bundle
	assert.FileExists(t, filepath.Join(r.root, "opt/myriarch/collector/versions/"+rigVersion+"/otelcol-contrib"))
	cur, err := os.Readlink(filepath.Join(r.root, "opt/myriarch/collector/current"))
	require.NoError(t, err)
	assert.Equal(t, "versions/"+rigVersion, cur)
	cfg, err := os.ReadFile(filepath.Join(r.root, "etc/myriarch/supervisor.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), r.endpointOfPlan())
	want, err := os.ReadFile(filepath.Join(r.bundle, "myriarch-updater"))
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(r.root, "opt/myriarch/updater/myriarch-updater"))
	require.NoError(t, err)
	assert.Equal(t, want, got, "the updater of the installer bundle is installed")

	// the identity, the issuer and the version are the installer's own; the verifier is the updater of the bundle
	calls := r.updaterCalls()
	assert.Contains(t, calls, `--identity-regexp ^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0$`)
	assert.Contains(t, calls, `--identity-regexp ^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-binary-release\.yaml@refs/tags/v0\.162\.0$`)
	assert.Equal(t, 2, strings.Count(calls, "--issuer https://token.actions.githubusercontent.com"))
	assert.Equal(t, 2, strings.Count(calls, "--version v0.162.0"))
	assert.Equal(t, 2, strings.Count(calls, "--tuf-cache "), "the TUF cache of the check is in the working directory of the installer")
	assert.NotContains(t, calls, "--trusted-root", "the trust root is the verifier's own unless the operator names a file")

	// a second run changes nothing and asks for a plan again
	res = r.runWith("--fleet", "linux-hosts", "--site-name", `Edge "north"`, "--environment", "prod")
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, res.Out, "Collector v0.162.0 is already installed")
}

func TestServerModeAcceptsATrustedRootFile(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	file := filepath.Join(t.TempDir(), "trusted_root.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"mediaType":"rig"}`), 0o600))
	res := r.runWith("--trusted-root", file)
	require.Equal(t, 0, res.Code, res.Out)

	// both checks use the file and neither uses a TUF cache
	calls := r.updaterCalls()
	assert.Equal(t, 2, strings.Count(calls, "--trusted-root "+file))
	assert.NotContains(t, calls, "--tuf-cache")

	// the updater keeps verifying against a copy of the file
	got, err := os.ReadFile(filepath.Join(r.root, "etc/myriarch/trusted_root.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"mediaType":"rig"}`, string(got))
	cfg, err := os.ReadFile(filepath.Join(r.root, "etc/myriarch/updater.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), `trustedRootFile: "/etc/myriarch/trusted_root.json"`)
}

func (r *installerRig) endpointOfPlan() string {
	for _, l := range strings.Split(r.planText(nil), "\n") {
		if v, ok := strings.CutPrefix(l, KeyOpAMPEndpoint+"="); ok {
			return v
		}
	}
	return ""
}

// hostAttr is the host.name that the installer sends (the name of this machine when it has a plain one), as a JSON member.
func hostAttr(body string) string {
	m := regexp.MustCompile(`"host\.name":"([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return fmt.Sprintf(`,"host.name":%q`, m[1])
}

func TestServerModeWithoutUpdaterInPlan(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	r.answerWith(r.planText(map[string]string{KeyUpdaterInstall: "false"}))
	res := r.runWith()
	require.Equal(t, 0, res.Code, res.Out)
	assert.NoFileExists(t, filepath.Join(r.root, "opt/myriarch/updater/myriarch-updater"))
	assert.NoFileExists(t, filepath.Join(r.root, "etc/systemd/system/myriarch-updater.service"))
	assert.FileExists(t, filepath.Join(r.root, "etc/systemd/system/otelcol-supervisor.service"))
}

func TestServerModeAsksForTheVersionOfTheOperator(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	res := r.runWith("--collector-version", "v"+rigVersion)
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, r.seen()[0].Body, `"collectorVersion":"`+rigVersion+`"`)

	// a server that answers another version than the one asked for is not followed
	r2 := newInstallerRig(t, true, "amd64")
	r2.answerWith(r2.planText(map[string]string{KeyCollectorVersion: "0.161.0"}))
	res = r2.runWith("--collector-version", rigVersion)
	assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	assert.False(t, r2.installed())
}

func TestServerModeTheFleetOfThePlanMustBeTheOneAskedFor(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	r.answerWith(r.planText(map[string]string{KeyFleet: "other-hosts"}))
	res := r.runWith("--fleet", "linux-hosts")
	assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	assert.False(t, r.installed())
}

func TestInstallerAcceptsEveryOptionOfTheRenderedLinuxCommands(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "commands", "*_linux_*.txt"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 6, "the golden files of the Linux commands")
	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			text, err := os.ReadFile(file)
			require.NoError(t, err)
			fixed, opts := goldenInstallerArguments(t, string(text))
			srv, ca := fixed[0], fixed[2]
			arch := "amd64"
			if strings.Contains(file, "arm64") {
				arch = "arm64"
			}
			r := newInstallerRig(t, strings.HasPrefix(srv, "https://"), arch)
			// what the command passes: --server and --token-file first, then --ca-file when there is a CA, then the options
			args := []string{"--root", r.root, "--arch", arch, "--server", r.srv.URL, "--token-file", r.tokenFile}
			if ca != "" {
				args = append(args, "--ca-file", r.caFile)
			}
			res := r.run(append(args, opts...)...)
			require.Equal(t, 0, res.Code, res.Out)
			assert.NotContains(t, res.Out, "usage_error")
			assert.NotContains(t, res.Out, "WARNING")
			body := r.seen()[0].Body
			assert.Contains(t, body, fmt.Sprintf(`"arch":%q`, arch))
			for _, want := range []string{`"fleet":"linux-hosts"`, `"collectorVersion":"0.162.0"`, `"site.type":"edge"`, `"site.id":"042"`,
				`"site.name":"Edge site 42 (north)"`, `"site.country":"NL"`, `"site.region":"north"`, `"site.timezone":"Europe/Amsterdam"`, `"deployment.environment.name":"prod"`} {
				assert.Contains(t, body, want)
			}
			r.assertSecretsOnlyInHeaders(res.Out)
		})
	}
}

// goldenInstallerArguments reads the words that the command hands to `sh -c`: the six fixed ones (server, curl protocol, CA file,
// token file, bundle, SHA-256) and the options of the installer.
func goldenInstallerArguments(t *testing.T, command string) (fixed, opts []string) {
	t.Helper()
	_, tail, ok := strings.Cut(command, "\n' sh ")
	require.True(t, ok, "the command ends its script with ' sh and the arguments")
	line, _, _ := strings.Cut(tail, "\n")
	line, _, _ = strings.Cut(line, " <<'")
	var words []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(line, -1) {
		words = append(words, m[1])
	}
	require.GreaterOrEqual(t, len(words), 6)
	return words[:6], words[6:]
}

// Every option that the commands can pass is a case of the script, and so are the options that the script of the command passes.
func TestInstallerCasesCoverTheOptionsOfTheCommands(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy/scripts/install-linux.sh"))
	require.NoError(t, err)
	in := &commandInput{variant: VariantLab, fleet: "linux-hosts", collectorVersion: "0.162.0", siteType: "edge", siteID: "042", siteName: "Edge",
		siteCountry: "NL", siteRegion: "north", siteTimezone: "Europe/Amsterdam", env: "prod"}
	flags := map[string]bool{}
	for _, o := range in.installerOptions(false) {
		if strings.HasPrefix(o, "--") {
			flags[o] = true
		}
	}
	// the installer line of the script of the command
	_, tail, ok := strings.Cut(linuxScript, `bash "$d/deploy/scripts/install-linux.sh"`)
	require.True(t, ok)
	line, _, _ := strings.Cut(tail, "\n")
	for _, f := range regexp.MustCompile(`--[a-z-]+`).FindAllString(line, -1) {
		flags[f] = true
	}
	require.GreaterOrEqual(t, len(flags), 11, "the options of the commands: %v", sortedFlags(flags))
	for f := range flags {
		assert.Regexp(t, `(?m)^\s+`+regexp.QuoteMeta(f)+`[ )|]`, string(script), "install-linux.sh handles %s", f)
	}
}

func sortedFlags(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ----------------------------------------------------------------- the plan as data ----

func TestServerModeRefusesAPlanThatBreaksTheRules(t *testing.T) {
	long := strings.Repeat("a", 9000)
	var many strings.Builder
	for i := 0; i < 65; i++ {
		fmt.Fprintf(&many, "x%d=1\n", i)
	}
	cases := []struct {
		name string
		text func(r *installerRig) string
		why  string
	}{
		{"unknown key", func(r *installerRig) string { return r.planText(nil) + "run=true\n" }, "key this installer does not know"},
		{"unknown key between the others", func(r *installerRig) string {
			return strings.Replace(r.planText(nil), KeyFleet+"=", "extra=1\n"+KeyFleet+"=", 1)
		}, "key this installer does not know"},
		{"too many lines", func(r *installerRig) string { return r.planText(nil) + many.String() }, "more than 64 lines"},
		{"too many bytes", func(r *installerRig) string { return r.planText(map[string]string{KeyFleet: long}) }, "longer than 8192 bytes"},
		{"repeated key", func(r *installerRig) string { return r.planText(nil) + "fleet=linux-hosts\n" }, "repeated"},
		{"missing key", func(r *installerRig) string { return r.planText(map[string]string{KeyCollectorGrant: ""}) }, "lacks the key collector.grant"},
		{"format first", func(r *installerRig) string {
			return strings.Replace(r.planText(nil), "format=1\n", "", 1) + "format=1\n"
		}, "does not start with the key format"},
		{"another format", func(r *installerRig) string { return strings.Replace(r.planText(nil), "format=1", "format=2", 1) }, "format 2"},
		{"no trailing line end", func(r *installerRig) string { return strings.TrimSuffix(r.planText(nil), "\n") }, "line end"},
		{"carriage return", func(r *installerRig) string { return strings.ReplaceAll(r.planText(nil), "\n", "\r\n") }, "character that a plan may not have"},
		{"a space", func(r *installerRig) string { return r.planText(map[string]string{KeyFleet: "linux hosts"}) }, "character that a plan may not have"},
		{"a dollar sign", func(r *installerRig) string { return r.planText(map[string]string{KeyFleet: "linux$(id)"}) }, "character that a plan may not have"},
		{"a backtick", func(r *installerRig) string { return r.planText(map[string]string{KeyCollectorGrant: "myrd_`id`.x"}) }, "character that a plan may not have"},
		{"an empty line", func(r *installerRig) string { return r.planText(nil) + "\n" }, "has no ="},
		{"an upper case fleet", func(r *installerRig) string { return r.planText(map[string]string{KeyFleet: "Linux"}) }, "not the name of a fleet"},
		{"an unknown source", func(r *installerRig) string { return r.planText(map[string]string{KeyVersionSource: "mirror"}) }, "version.source"},
		{"a version with two parts", func(r *installerRig) string { return r.planText(map[string]string{KeyCollectorVersion: "0.162"}) }, "collector.version"},
		{"a version with a leading v", func(r *installerRig) string { return r.planText(map[string]string{KeySupervisorVersion: "v0.162.0"}) }, "supervisor.version"},
		{"a version with build metadata", func(r *installerRig) string {
			return r.planText(map[string]string{KeyCollectorVersion: "0.162.0+build"})
		}, "cannot install"},
		{"a grant that is not one", func(r *installerRig) string { return r.planText(map[string]string{KeySupervisorGrant: "abc"}) }, "supervisor.grant"},
		{"updater.install not a boolean", func(r *installerRig) string { return r.planText(map[string]string{KeyUpdaterInstall: "yes"}) }, "updater.install"},
		{"an endpoint that is not a URL", func(r *installerRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "fleet.example.com"})
		}, "opamp.endpoint"},
		{"an endpoint of another host", func(r *installerRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "wss://collect.example.com/v1/opamp"})
		}, "another host or port"},
		{"an endpoint of the host but another port", func(r *installerRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "wss://127.0.0.1:1/v1/opamp"})
		}, "another host or port"},
		{"an endpoint without TLS", func(r *installerRig) string {
			return r.planText(map[string]string{KeyOpAMPEndpoint: "ws://" + strings.SplitN(r.srv.URL, "://", 2)[1] + "/v1/opamp"})
		}, "scheme"},
		{"an artifact of another origin", func(r *installerRig) string {
			return r.planText(map[string]string{KeyCollectorURL: "https://downloads.example.com/api/v1/artifacts/c0/content/" + r.collectorFile()})
		}, "not on the origin of --server"},
		{"an artifact on a lookalike host", func(r *installerRig) string {
			// a host name that starts like the host of the server, and the same port
			return r.planText(map[string]string{KeySupervisorURL: strings.Replace(r.srv.URL, "127.0.0.1", "127.0.0.1.evil.example.com", 1) + "/api/v1/artifacts/s0/content/" + r.supervisorFile()})
		}, "not on the origin of --server"},
		{"an artifact with a path that climbs", func(r *installerRig) string {
			return r.planText(map[string]string{KeyCollectorURL: r.srv.URL + "/api/v1/artifacts/c0/content/../c0/content/" + r.collectorFile()})
		}, "path that is not plain"},
		{"an artifact with another file name", func(r *installerRig) string {
			return r.planText(map[string]string{KeyCollectorURL: r.srv.URL + "/api/v1/artifacts/c0/content/otelcol-contrib_0.161.0_linux_" + r.arch + ".tar.gz"})
		}, "does not end in the file name"},
		{"an artifact URL with a query", func(r *installerRig) string {
			return r.planText(map[string]string{KeyCollectorURL: r.srv.URL + "/api/v1/artifacts/c0/content/" + r.collectorFile() + "?x=1"})
		}, "character that a plan may not have"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := newInstallerRig(t, true, "amd64")
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

func TestServerModeRefusesAnArtifactOverHTTP(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	r.answerWith(r.planText(map[string]string{KeyCollectorURL: "http://" + strings.SplitN(r.srv.URL, "://", 2)[1] + "/api/v1/artifacts/c0/content/" + r.collectorFile()}))
	res := r.runWith()
	assert.Equal(t, "https_required", res.failure(), res.Out)
	assert.False(t, r.installed())
}

func TestServerModeHTTPServerNeedsInsecure(t *testing.T) {
	r := newInstallerRig(t, false, "amd64")
	res := r.runWith()
	assert.Equal(t, "https_required", res.failure(), res.Out)
	assert.Empty(t, r.seen(), "nothing is sent to a server that is reached over plain http")

	// the lab variant of the command allows it, and sends the same requests
	res = r.runWith("--insecure")
	assert.Equal(t, 0, res.Code, res.Out)
}

// ---------------------------------------------------------------------- redirects ----

func TestServerModeRedirects(t *testing.T) {
	t.Run("to another origin", func(t *testing.T) {
		other := newInstallerRig(t, true, "amd64")
		r := newInstallerRig(t, true, "amd64")
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
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			http.Redirect(w, req, "http://"+strings.SplitN(r.srv.URL, "://", 2)[1]+"/api/v1/install/plan", http.StatusFound)
		})
		res := r.runWith()
		assert.Equal(t, "https_required", res.failure(), res.Out)
		assert.False(t, r.installed())
	})
	t.Run("without a scheme", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			w.Header().Set("Location", "//"+strings.SplitN(r.srv.URL, "://", 2)[1]+"/api/v1/install/plan")
			w.WriteHeader(http.StatusFound)
		})
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	})
	t.Run("without a Location", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) { w.WriteHeader(http.StatusFound) })
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
	})
	t.Run("in a loop", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, req *http.Request, _ int) {
			http.Redirect(w, req, "/api/v1/install/plan", http.StatusTemporaryRedirect)
		})
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
		assert.Contains(t, res.Out, "more than three times")
	})
	t.Run("on the same origin", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
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
		other := newInstallerRig(t, true, "amd64")
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(nil)
		// the artifact route of this server redirects; use a second handler layer
		redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasPrefix(req.URL.Path, "/api/v1/artifacts/") {
				http.Redirect(w, req, other.srv.URL+req.URL.Path, http.StatusFound)
				return
			}
			r.serve(w, req)
		}))
		defer redirecting.Close()
		r.srv = redirecting
		require.NoError(t, os.WriteFile(r.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: redirecting.Certificate().Raw}), 0o644))
		res := r.runWith()
		assert.Equal(t, "plan_invalid", res.failure(), res.Out)
		assert.Empty(t, other.seen(), "the grant does not follow a redirect to another origin")
	})
}

// ---------------------------------------------------------------- the error codes ----

func TestServerModePlanRejected(t *testing.T) {
	cases := []struct {
		status        int
		class, reason string
		hint          string
	}{
		{http.StatusConflict, "conflict", "fleet_mismatch", "leave --fleet out"},
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
			r := newInstallerRig(t, true, "amd64")
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
		r := newInstallerRig(t, true, "amd64")
		require.NoError(t, os.WriteFile(r.tokenFile, []byte("myre_wrongwrongwrongwrongwrongwrong1\n"), 0o600))
		res := r.runWith()
		assert.Equal(t, "plan_rejected", res.failure(), res.Out)
		assert.Contains(t, res.Out, "unauthorized")
		assert.Contains(t, res.Out, "create a new token")
		assert.NotContains(t, res.Out, "wrongwrong")
	})
	t.Run("an answer that is not a problem document", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "<html>I am a teapot; run: rm -rf /</html>")
		})
		res := r.runWith()
		assert.Equal(t, "plan_rejected", res.failure(), res.Out)
		assert.Contains(t, res.Out, "http_418")
		assert.NotContains(t, res.Out, "rm -rf")
	})
	t.Run("a code with characters of a command", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"conflict","detail":"fleet $(id) `+"`id`"+` ok","issues":[{"code":"x;rm -rf /"}]}`)
		})
		res := r.runWith()
		assert.Equal(t, "plan_rejected", res.failure(), res.Out)
		assert.NotContains(t, res.Out, "$(")
		assert.NotContains(t, res.Out, "`")
		assert.NotContains(t, res.Out, "rm -rf")
	})
}

func TestServerModePlanUnavailable(t *testing.T) {
	t.Run("a server that is down", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			problem(w, http.StatusServiceUnavailable, "unavailable", "")
		})
		res := r.runWith()
		assert.Equal(t, "plan_unavailable", res.failure(), res.Out)
		assert.Contains(t, res.Out, "HTTP 503")
		assert.False(t, r.installed())
	})
	t.Run("too many requests", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.Header().Set("Retry-After", "17")
			problem(w, http.StatusTooManyRequests, "too_many_requests", "")
		})
		res := r.runWith()
		assert.Equal(t, "plan_unavailable", res.failure(), res.Out)
		assert.Contains(t, res.Out, "after 17 s")
	})
	t.Run("still preparing after the time that was given", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.Header().Set("Retry-After", "1")
			problem(w, http.StatusServiceUnavailable, "unavailable", "preparing")
		})
		start := time.Now()
		res := r.runWith("--plan-wait", "3")
		assert.Equal(t, "plan_unavailable", res.failure(), res.Out)
		assert.Contains(t, res.Out, "still preparing the artifacts after 3 s")
		assert.GreaterOrEqual(t, len(r.seen()), 3, "it asked again after Retry-After")
		assert.Less(t, time.Since(start), 30*time.Second)
	})
	t.Run("preparing and then ready", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
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

func TestServerModePlanFailed(t *testing.T) {
	t.Run("a certificate that is not trusted", func(t *testing.T) {
		r := newInstallerRig(t, true, "amd64")
		// no --ca-file: the certificate of the test server is not known to the system
		res := r.run("--root", r.root, "--arch", "amd64", "--server", r.srv.URL, "--token-file", r.tokenFile, "--site-type", "edge", "--site-id", "042")
		assert.Equal(t, "plan_failed", res.failure(), res.Out)
		assert.Contains(t, res.Out, "--ca-file")
		assert.Empty(t, r.seen(), "the token is not sent over a connection that was not verified")
		assert.False(t, r.installed())
	})
	t.Run("a server error", func(t *testing.T) {
		if testing.Short() {
			t.Skip("the installer asks again for a few seconds first")
		}
		r := newInstallerRig(t, true, "amd64")
		r.setPlan(func(w http.ResponseWriter, _ *http.Request, _ int) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
		res := r.runWith()
		assert.Equal(t, "plan_failed", res.failure(), res.Out)
		assert.Contains(t, res.Out, "HTTP 500")
	})
}

func TestServerModeConflictingInstallStopsBeforeTheServerIsAsked(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	r.withSystemd("otelcol-contrib.service")
	res := r.runWith()
	assert.Equal(t, 1, res.Code, res.Out)
	assert.Equal(t, "conflicting_install", res.failure(), res.Out)
	assert.Contains(t, res.Out, "otelcol-contrib.service (enabled)")
	assert.Contains(t, res.Out, "removes nothing")
	assert.Empty(t, r.seen(), "the server is not asked and no token is sent")
	assert.False(t, r.installed(), "nothing is created")
}

func TestServerModeSupervisorMissing(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	// a directory where the supervisor binary belongs: the installer does not replace it
	require.NoError(t, os.MkdirAll(filepath.Join(r.root, "opt/myriarch/supervisor/opampsupervisor"), 0o755))
	res := r.runWith()
	assert.Equal(t, 1, res.Code, res.Out)
	assert.Equal(t, "supervisor_missing", res.failure(), res.Out)
	assert.Contains(t, res.Out, "is not a regular file")
	assert.Contains(t, res.Out, "/opt/myriarch/supervisor/opampsupervisor")
	assert.Contains(t, res.Out, "remove the item")
}

func TestServerModeVerifiesBeforeItInstallsAnything(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	// a Sigstore bundle that the verifier refuses: the archive of the collector is not installed
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, ".sigstore.json") {
			_, _ = io.WriteString(w, "TAMPERED")
			return
		}
		r.serve(w, req)
	}))
	defer srv.Close()
	r.srv = srv
	require.NoError(t, os.WriteFile(r.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644))
	res := r.runWith()
	assert.Equal(t, 1, res.Code, res.Out)
	assert.Equal(t, "sigstore_verification_failed", res.failure(), res.Out)
	assert.NoDirExists(t, filepath.Join(r.root, "opt/myriarch/collector/versions/"+rigVersion))
	assert.NoFileExists(t, filepath.Join(r.root, "opt/myriarch/supervisor/opampsupervisor"))
}

func TestServerModeChecksTheSHA256OfTheArtifact(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, r.supervisorFile()) {
			_, _ = io.WriteString(w, "not the file the checksum is for")
			return
		}
		r.serve(w, req)
	}))
	defer srv.Close()
	r.srv = srv
	require.NoError(t, os.WriteFile(r.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644))
	res := r.runWith()
	assert.Equal(t, "checksum_mismatch", res.failure(), res.Out)
	assert.NoFileExists(t, filepath.Join(r.root, "opt/myriarch/supervisor/opampsupervisor"))
}

func TestServerModeAnExpiredGrantStopsTheDownload(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	r.answerWith(r.planText(map[string]string{KeyCollectorGrant: "myrd_expired.grant"}))
	res := r.runWith()
	assert.Equal(t, "download_failed", res.failure(), res.Out)
	assert.Contains(t, res.Out, "HTTP 401")
	assert.Contains(t, res.Out, "run the command again")
}

func TestServerModeNeedsTheUpdaterOfTheBundleAsVerifier(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	require.NoError(t, os.Remove(filepath.Join(r.bundle, "myriarch-updater")))
	res := r.runWith()
	assert.Equal(t, "sigstore_verifier_missing", res.failure(), res.Out)
	assert.Contains(t, res.Out, "installer bundle")
	assert.False(t, r.installed())

	// a link is not the updater of the archive
	target := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(r.bundle, "myriarch-updater")))
	res = r.runWith()
	assert.Equal(t, "sigstore_verifier_missing", res.failure(), res.Out)
}

func TestServerModeOptionsThatDoNotCombine(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	for _, tc := range []struct {
		name string
		more []string
		code string
	}{
		{"with the updater", []string{"--with-updater"}, "option_conflict"},
		{"updater url", []string{"--updater-url", "https://x.example.com/myriarch-updater_0.2.0_linux_amd64"}, "option_conflict"},
		{"release base url", []string{"--release-base-url", "https://mirror.example.com"}, "option_conflict"},
		{"supervisor version", []string{"--supervisor-version", "0.162.0"}, "option_conflict"},
		{"skipped signatures", []string{"--insecure", "--verify-signatures", "skip"}, "option_conflict"},
		{"latest", []string{"--collector-version", "latest"}, "option_conflict"},
		{"a fleet that is no name", []string{"--fleet", "Linux Hosts"}, "option_invalid"},
		{"a wait of zero", []string{"--plan-wait", "0"}, "option_invalid"},
		{"a wait that is text", []string{"--plan-wait", "soon"}, "option_invalid"},
	} {
		res := r.runWith(tc.more...)
		assert.Equal(t, tc.code, res.failure(), "%s: %s", tc.name, res.Out)
	}
	for _, server := range []string{r.srv.URL + "/api", "ftp://x.example.com", r.srv.URL + "?x=1", "x.example.com"} {
		res := r.run("--root", r.root, "--server", server, "--token-file", r.tokenFile, "--site-type", "edge", "--site-id", "1")
		assert.Equal(t, "option_invalid", res.failure(), "%s: %s", server, res.Out)
	}
	assert.Empty(t, r.seen())

	// the server mode options mean nothing without --server
	res := r.run("--root", r.root, "--opamp-endpoint", "wss://fleet.example.com/v1/opamp", "--token-file", r.tokenFile, "--site-type", "edge", "--site-id", "1", "--fleet", "linux-hosts")
	assert.Equal(t, "option_conflict", res.failure(), res.Out)
	res = r.run("--root", r.root, "--opamp-endpoint", "wss://fleet.example.com/v1/opamp", "--token-file", r.tokenFile, "--site-type", "edge", "--site-id", "1", "--plan-wait", "5")
	assert.Equal(t, "option_conflict", res.failure(), res.Out)
}

func TestServerModeNeedsTheSiteAndTheToken(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	res := r.run("--root", r.root, "--server", r.srv.URL, "--ca-file", r.caFile, "--token-file", r.tokenFile, "--site-id", "1")
	assert.Equal(t, "option_missing", res.failure(), res.Out)
	res = r.run("--root", r.root, "--server", r.srv.URL, "--ca-file", r.caFile, "--site-type", "edge", "--site-id", "1")
	assert.Equal(t, "option_missing", res.failure(), res.Out)
	assert.Contains(t, res.Out, "--token")
	assert.Empty(t, r.seen())
}

// ---------------------------------------------------- without --server: the version ----

// A fake release in the layout of the upstream releases, for file:// URLs (lab mode).
func (r *installerRig) fakeMirror(version string) string {
	dir := filepath.Join(r.tmp, "mirror-"+version)
	collector := fmt.Sprintf("otelcol-contrib_%s_linux_%s.tar.gz", version, r.arch)
	supervisor := fmt.Sprintf("opampsupervisor_%s_linux_%s", version, r.arch)
	require.NoError(r.t, os.MkdirAll(filepath.Join(dir, "v"+version), 0o755))
	require.NoError(r.t, os.MkdirAll(filepath.Join(dir, "cmd/opampsupervisor/v"+version), 0o755))
	archive := r.collectorArchiveFor(version)
	write := func(rel string, b []byte) {
		sum := sha256.Sum256(b)
		require.NoError(r.t, os.WriteFile(filepath.Join(dir, rel), b, 0o644))
		require.NoError(r.t, os.WriteFile(filepath.Join(dir, rel+".sha256"), []byte(hex.EncodeToString(sum[:])+"  "+filepath.Base(rel)+"\n"), 0o644))
	}
	write("v"+version+"/"+collector, archive)
	write("cmd/opampsupervisor/v"+version+"/"+supervisor, []byte("#!/bin/sh\necho supervisor\n"))
	return dir
}

func (r *installerRig) collectorArchiveFor(version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho \"otelcol-contrib version " + version + "\"\n"
	require.NoError(r.t, tw.WriteHeader(&tar.Header{Name: "otelcol-contrib", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	require.NoError(r.t, err)
	require.NoError(r.t, tw.Close())
	require.NoError(r.t, gz.Close())
	return buf.Bytes()
}

func (r *installerRig) manualArgs(mirror string, more ...string) []string {
	args := []string{"--root", r.root, "--arch", r.arch, "--opamp-endpoint", "wss://fleet.example.com/v1/opamp", "--token-file", r.tokenFile,
		"--site-type", "edge", "--site-id", "042", "--insecure", "--verify-signatures", "skip", "--release-base-url", "file://" + mirror}
	return append(args, more...)
}

// versionsEnvValue reads a pin of ci/versions.env.
func versionsEnvValue(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "ci/versions.env"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^` + name + `=(\S+)$`).FindSubmatch(b)
	require.NotNil(t, m, "%s in ci/versions.env", name)
	return string(m[1])
}

func TestWithoutServerTheTestedVersionIsTheDefault(t *testing.T) {
	want := versionsEnvValue(t, "OTELCOL_VERSION")
	script, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy/scripts/install-linux.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(script), `readonly DEFAULT_COLLECTOR_VERSION="`+want+`"`, "the default of the installer is the OTELCOL_VERSION of ci/versions.env")

	r := newInstallerRig(t, true, "amd64")
	res := r.run(r.manualArgs(r.fakeMirror(want))...)
	require.Equal(t, 0, res.Code, res.Out)
	assert.Contains(t, res.Out, "no --collector-version: installing "+want)
	assert.DirExists(t, filepath.Join(r.root, "opt/myriarch/collector/versions/"+want))
	assert.Empty(t, r.seen())
	for _, a := range r.curlArgs() {
		assert.NotContains(t, a, "github.com", "no lookup of the newest version without a request")
	}
}

func TestWithoutServerLatestAsksGitHubOnRequest(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	mirror := r.fakeMirror("0.99.1")
	cmd := append([]string{"bash", filepath.Join(r.bundle, "deploy/scripts/install-linux.sh")}, r.manualArgs(mirror, "--collector-version", "latest")...)
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = []string{"PATH=" + r.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + r.root, "TMPDIR=" + r.tmp, "LOGDIR=" + r.logs, "LANG=C", "NEWEST=0.99.1"}
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "--collector-version latest: the newest collector release is 0.99.1")
	assert.DirExists(t, filepath.Join(r.root, "opt/myriarch/collector/versions/0.99.1"))
	assert.Contains(t, strings.Join(r.curlArgs(), " "), "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/latest")
}

func TestWithoutServerAnExplicitVersionIsKept(t *testing.T) {
	r := newInstallerRig(t, true, "amd64")
	mirror := r.fakeMirror("0.150.2")
	res := r.run(r.manualArgs(mirror, "--collector-version", "v0.150.2")...)
	require.Equal(t, 0, res.Code, res.Out)
	assert.NotContains(t, res.Out, "no --collector-version")
	assert.DirExists(t, filepath.Join(r.root, "opt/myriarch/collector/versions/0.150.2"))
	res = r.run(r.manualArgs(mirror, "--collector-version", "stable")...)
	assert.Equal(t, "option_invalid", res.failure(), res.Out)
}

func (r *installerRig) setPlan(plan func(w http.ResponseWriter, r *http.Request, n int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan = plan
}
