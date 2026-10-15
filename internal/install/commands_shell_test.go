// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
)

// These tests run the command for Linux in a real shell, with stand-ins for sudo, id and mktemp (the tests are not
// root) and, unless a test says otherwise, for curl. The installer in the archive is a script that writes down how it
// was called. They check what the command does and what it leaves behind, and that the token is in no argument vector,
// no environment and no file once the command has ended.

// testToken has the form of an enrollment token and is made up, so that a secret scanner finds nothing in the source.
var testToken = "myre_" + strings.Repeat("Ab1x", 8)

// linuxFixture is an installer bundle for Linux with a stand-in installer.
type linuxFixture struct {
	archive []byte
	sum     string
	name    string
}

const installerStub = `#!/usr/bin/env bash
{ for a in "$@"; do printf '%s\0' "$a"; done; } > "$LOGDIR/installer.argv"
env > "$LOGDIR/installer.env"
pwd > "$LOGDIR/installer.cwd"
cat > "$LOGDIR/installer.stdin"
prev=
for a in "$@"; do
  if [ "$prev" = "--token-file" ]; then
    cat "$a" > "$LOGDIR/installer.token"
    { stat -c %a "$a" 2>/dev/null || stat -f %Lp "$a"; } > "$LOGDIR/installer.tokenmode"
    dirname "$a" > "$LOGDIR/installer.tokendir"
  fi
  prev=$a
done
echo "the installer ran"
exit "${INSTALLER_EXIT:-0}"
`

func newLinuxFixture(t *testing.T) linuxFixture {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, body string) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	add("LICENSE", 0o644, "license")
	add("deploy/scripts/install-linux.sh", 0o755, installerStub)
	add("deploy/supervisor/linux/supervisor.yaml", 0o644, "server: {}")
	add(brand.UpdaterBinary, 0o755, "binary")
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return linuxFixture{archive: buf.Bytes(), sum: sumHex(buf.Bytes()), name: BundleFilename("0.2.0", Platform{"linux", "amd64"})}
}

type linuxHarness struct {
	t       *testing.T
	root    string // the file system of the test: root/root is the home of root
	bin     string
	logs    string
	fixture linuxFixture
	env     []string
	curl    string // "stub" or the real curl
}

func skipWithoutShell(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "sh", "tar", "gzip", "tr", "rm", "cat", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// newLinuxHarness builds the stand-ins. sh names the shell for `sh -c` ("" is the system one).
func newLinuxHarness(t *testing.T, sh string) *linuxHarness {
	t.Helper()
	skipWithoutShell(t)
	h := &linuxHarness{t: t, root: t.TempDir(), bin: t.TempDir(), logs: t.TempDir(), fixture: newLinuxFixture(t), curl: "stub"}
	require.NoError(t, os.MkdirAll(filepath.Join(h.root, "root"), 0o755))
	archive := filepath.Join(h.logs, "fixture.tar.gz")
	require.NoError(t, os.WriteFile(archive, h.fixture.archive, 0o644))
	realMktemp, err := exec.LookPath("mktemp")
	require.NoError(t, err)
	script := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(h.bin, name), []byte("#!/bin/sh\n"+body), 0o755))
	}
	argv := func(name string) string {
		return `for a in "$@"; do printf '%s\0' "$a"; done >> "$LOGDIR/` + name + `.argv"` + "\n"
	}
	script("sudo", argv("sudo")+`exec "$@"`+"\n")
	script("id", `if [ "$1" = "-u" ]; then echo "${FAKE_UID:-0}"; else exec /usr/bin/id "$@"; fi`+"\n")
	script("mktemp", argv("mktemp")+`for last; do :; done
exec "$REAL_MKTEMP" -d "$FAKE_ROOT/root/${last##*/}"`+"\n")
	script("curl", argv("curl")+`cat > "$LOGDIR/curl.stdin"
if [ -n "${CURL_FAIL:-}" ]; then echo "curl: (22) The requested URL returned error: 401" >&2; exit 22; fi
out=; while [ $# -gt 0 ]; do case $1 in -o) out=$2; shift 2 ;; *) shift ;; esac; done
cp "$FIXTURE" "$out"`+"\n")
	if _, err := exec.LookPath("sha256sum"); err != nil {
		script("sha256sum", `exec shasum -a 256 "$@"`+"\n")
	}
	if sh != "" {
		path, err := exec.LookPath(sh)
		if err != nil {
			t.Skipf("%s is not installed", sh)
		}
		shdir := t.TempDir()
		require.NoError(t, os.Symlink(path, filepath.Join(shdir, "sh")))
		h.bin = shdir + string(os.PathListSeparator) + h.bin
	}
	h.env = []string{
		"PATH=" + h.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"LOGDIR=" + h.logs, "FAKE_ROOT=" + h.root, "FIXTURE=" + archive, "REAL_MKTEMP=" + realMktemp,
		"HOME=" + h.root + "/home", "LANG=C",
	}
	return h
}

// command renders the command of the variant with the given bundle and puts the token in.
func (h *linuxHarness) command(req CommandsRequest, token string, mut ...func(d *Deps, s *stubBundles)) string {
	h.t.Helper()
	e := newCommandsEnv(h.t, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "linux" && s.list.Items[i].Arch == "amd64" {
				s.list.Items[i].Filename, s.list.Items[i].SHA256 = h.fixture.name, h.fixture.sum
			}
		}
		for _, m := range mut {
			m(d, s)
		}
	})
	req.Platforms = []string{"linux/amd64"}
	c, err := e.render(req)
	require.NoError(h.t, err)
	require.Len(h.t, c.Commands, 1)
	return strings.ReplaceAll(c.Commands[0].Command, brand.EnrollmentTokenPlaceholder, token)
}

type runResult struct {
	out  string
	code int
}

// run executes the command in bash, the way a person pastes it, with the given extra environment.
func (h *linuxHarness) run(command string, extra ...string) runResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Env = append(append([]string(nil), h.env...), extra...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		require.ErrorAs(h.t, err, &ee, out.String())
		code = ee.ExitCode()
	}
	return runResult{out: out.String(), code: code}
}

func (h *linuxHarness) log(name string) string {
	b, err := os.ReadFile(filepath.Join(h.logs, name))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(h.t, err)
	return string(b)
}

func (h *linuxHarness) argv(name string) []string {
	raw := h.log(name + ".argv")
	if raw == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
}

// privateDirs lists what is left of the working directories below the home of root.
func (h *linuxHarness) privateDirs() []string {
	entries, err := os.ReadDir(filepath.Join(h.root, "root"))
	require.NoError(h.t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// theTokenIsNowhere checks every place where it must not be: the argument vectors of all programs, the environment of
// the installer, the output and the files that are left.
func (h *linuxHarness) theTokenIsNowhere(token string, r runResult) {
	h.t.Helper()
	for _, name := range []string{"sudo", "mktemp", "curl", "installer"} {
		for _, a := range h.argv(name) {
			assert.NotContains(h.t, a, token, "%s argv", name)
		}
	}
	assert.NotContains(h.t, h.log("installer.env"), token, "the environment of the installer")
	assert.NotContains(h.t, r.out, token, "the output")
	assert.Empty(h.t, h.log("installer.stdin"), "the installer gets no standard input")
	_ = filepath.Walk(h.root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(path)
			assert.NotContains(h.t, string(b), token, "left behind: %s", path)
		}
		return nil
	})
}

func shells() []string {
	out := []string{""}
	if _, err := exec.LookPath("dash"); err == nil {
		out = append(out, "dash")
	}
	return out
}

func shellName(sh string) string {
	if sh == "" {
		return "sh"
	}
	return sh
}

func TestLinuxCommandInstallsTheHost(t *testing.T) {
	for _, sh := range shells() {
		t.Run(shellName(sh), func(t *testing.T) {
			h := newLinuxHarness(t, sh)
			cmd := h.command(fullRequest(VariantTokenInline), testToken)
			r := h.run(cmd)
			require.Equal(t, 0, r.code, r.out)

			// the installer ran from the unpacked bundle, with the token file of the private directory
			args := h.argv("installer")
			require.GreaterOrEqual(t, len(args), 6, r.out)
			assert.Equal(t, "--server", args[0])
			assert.Equal(t, "https://fleet.example.com", args[1])
			assert.Equal(t, "--token-file", args[2])
			assert.True(t, strings.HasPrefix(args[3], filepath.Join(h.root, "root", brand.InstallSetupDir+".")), args[3])
			assert.True(t, strings.HasSuffix(args[3], "/enrollment-token"))
			assert.Equal(t, []string{"--ca-file", "/etc/ssl/myriarch-ca.pem", "--fleet", "linux-hosts", "--collector-version", "0.162.0", "--site-type", "edge", "--site-id", "042",
				"--site-name", "Edge site 42 (north)", "--site-country", "NL", "--site-region", "north", "--site-timezone", "Europe/Amsterdam", "--environment", "prod"}, args[4:])
			assert.Equal(t, testToken+"\n", h.log("installer.token"), "the file holds the token")
			assert.Equal(t, "600\n", h.log("installer.tokenmode"), "and only its owner can read it")
			assert.Contains(t, r.out, "the installer ran")

			// curl: https only, no redirects, a header from the standard input, the file in the private directory
			curl := h.argv("curl")
			assert.Equal(t, []string{"-fsSg", "--retry", "3", "--proto", "=https", "--tlsv1.2", "--cacert", "/etc/ssl/myriarch-ca.pem", "-K", "-", "-o"}, curl[:11])
			assert.True(t, strings.HasSuffix(curl[11], "/"+h.fixture.name))
			assert.Equal(t, "https://fleet.example.com/api/v1/install/bundles/"+h.fixture.name, curl[12])
			assert.NotContains(t, curl, "-L")
			assert.Equal(t, "header = \"Authorization: Bearer "+testToken+"\"\n", h.log("curl.stdin"))

			// sudo got the script and the values, not the token; mktemp was asked for a directory of root
			assert.Equal(t, "sh", h.argv("sudo")[0])
			assert.Equal(t, []string{"-d", "/root/" + brand.InstallSetupDir + ".XXXXXX"}, h.argv("mktemp"))

			// and nothing is left: not the directory, not the token
			assert.Empty(t, h.privateDirs())
			h.theTokenIsNowhere(testToken, r)
		})
	}
}

func TestLinuxCommandStopsWhenTheHashIsNotTheOneInTheCommand(t *testing.T) {
	h := newLinuxHarness(t, "")
	cmd := h.command(site(), testToken, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "linux" && s.list.Items[i].Arch == "amd64" {
				s.list.Items[i].SHA256 = strings.Repeat("0", 64)
			}
		}
	})
	r := h.run(cmd)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [checksum_mismatch]")
	assert.Contains(t, r.out, "nothing was unpacked")
	assert.Empty(t, h.log("installer.argv"), "the installer never ran")
	assert.Empty(t, h.privateDirs(), "nothing is left")
	h.theTokenIsNowhere(testToken, r)
}

func TestLinuxCommandStopsWithoutAUsableToken(t *testing.T) {
	cases := map[string]struct {
		token string
		code  string
	}{
		"nothing":                {"", "option_missing"},
		"the placeholder":        {brand.EnrollmentTokenPlaceholder, "option_missing"},
		"a space inside":         {"myre_abc def", "option_invalid"},
		"a quote":                {"myre_abc'def", "option_invalid"},
		"a dollar":               {"myre_$(id)", "option_invalid"},
		"a semicolon":            {"myre_abc;id", "option_invalid"},
		"a backtick":             {"myre_`id`", "option_invalid"},
		"a carriage return":      {"myre_abc\r", "option_invalid"},
		"a double quote":         {"\"myre_abc\"", "option_invalid"},
		"a backslash":            {`myre_abc\n`, "option_invalid"},
		"an asterisk":            {"myre_*", "option_invalid"},
		"the placeholder inside": {"myre_@@abc", "option_missing"},
		"a unicode quote":        {"myre_’abc", "option_invalid"},
		"a token with a tab":     {"myre_abc\tdef", "option_invalid"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newLinuxHarness(t, "")
			r := h.run(h.command(site(), c.token))
			assert.Equal(t, 1, r.code, r.out)
			assert.Contains(t, r.out, "[myriarch-install] ERROR ["+c.code+"]")
			assert.Empty(t, h.argv("curl"), "nothing is downloaded with it")
			assert.Empty(t, h.log("installer.argv"))
			assert.Empty(t, h.privateDirs())
			if c.token != "" && c.token != brand.EnrollmentTokenPlaceholder {
				assert.NotContains(t, r.out, c.token, "the message does not repeat it")
			}
		})
	}
}

func TestLinuxCommandUsesTheFirstLineOfTheHereDocumentOnly(t *testing.T) {
	h := newLinuxHarness(t, "")
	cmd := h.command(site(), testToken+"\nid >/tmp/pwned\nmyre_other")
	r := h.run(cmd)
	require.Equal(t, 0, r.code, r.out)
	assert.Equal(t, testToken+"\n", h.log("installer.token"))
	assert.NoFileExists(t, "/tmp/pwned")
}

func TestLinuxCommandNeedsRoot(t *testing.T) {
	h := newLinuxHarness(t, "")
	r := h.run(h.command(site(), testToken), "FAKE_UID=1000")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [privilege_required]")
	assert.Empty(t, h.argv("mktemp"), "it does nothing before it knows that it is root")
	assert.Empty(t, h.argv("curl"))
}

func TestLinuxCommandReportsADownloadThatFails(t *testing.T) {
	h := newLinuxHarness(t, "")
	r := h.run(h.command(site(), testToken), "CURL_FAIL=1")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [download_failed]")
	assert.Contains(t, r.out, "https://fleet.example.com")
	assert.Empty(t, h.log("installer.argv"))
	assert.Empty(t, h.privateDirs())
	h.theTokenIsNowhere(testToken, r)
}

func TestLinuxCommandKeepsTheExitCodeOfTheInstallerAndCleansUp(t *testing.T) {
	h := newLinuxHarness(t, "")
	r := h.run(h.command(site(), testToken), "INSTALLER_EXIT=3")
	assert.Equal(t, 3, r.code, "the status of the installer is the status of the command")
	assert.Empty(t, h.privateDirs(), "the private directory goes with the token file, whatever the installer did")
	h.theTokenIsNowhere(testToken, r)
}

func TestLinuxCommandStopsWhenTheArchiveCannotBeUnpacked(t *testing.T) {
	h := newLinuxHarness(t, "")
	bad := []byte("this is not a gzip file")
	require.NoError(t, os.WriteFile(filepath.Join(h.logs, "fixture.tar.gz"), bad, 0o644))
	cmd := h.command(site(), testToken, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "linux" && s.list.Items[i].Arch == "amd64" {
				s.list.Items[i].SHA256 = sumHex(bad)
			}
		}
	})
	r := h.run(cmd)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [archive_invalid]")
	assert.Empty(t, h.privateDirs())
}

func TestLinuxCommandCleansUpWhenInterrupted(t *testing.T) {
	h := newLinuxHarness(t, "")
	// the installer waits; the test interrupts the process group, as Ctrl-C does
	slow := newLinuxFixture(t)
	slow.archive = rebuildWithInstaller(t, "#!/usr/bin/env bash\necho started > \"$LOGDIR/installer.started\"\nsleep 30\n")
	slow.sum = sumHex(slow.archive)
	h.fixture = slow
	require.NoError(t, os.WriteFile(filepath.Join(h.logs, "fixture.tar.gz"), slow.archive, 0o644))
	cmd := exec.Command("bash", "-c", h.command(site(), testToken, func(d *Deps, s *stubBundles) {
		for i := range s.list.Items {
			if s.list.Items[i].OS == "linux" && s.list.Items[i].Arch == "amd64" {
				s.list.Items[i].SHA256 = slow.sum
			}
		}
	}))
	cmd.Env = h.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool { return h.log("installer.started") != "" }, 20*time.Second, 20*time.Millisecond, out.String())
	require.NotEmpty(t, h.privateDirs())
	require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGINT))
	_ = cmd.Wait()
	require.Eventually(t, func() bool { return len(h.privateDirs()) == 0 }, 20*time.Second, 20*time.Millisecond, "the private directory is removed after an interrupt")
	h.theTokenIsNowhere(testToken, runResult{out: out.String()})
}

// lockedBuffer collects the output of a process that the test reads while it runs.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func rebuildWithInstaller(t *testing.T, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "deploy/scripts/install-linux.sh", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(script))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestLinuxCommandReportsAMissingTool(t *testing.T) {
	h := newLinuxHarness(t, "")
	// a PATH with the stand-ins and a few tools, but no gzip
	only := t.TempDir()
	for _, tool := range []string{"bash", "sh", "tr", "rm", "cat", "tar", "curl", "sha256sum", "mktemp", "env", "pwd", "dirname", "stat", "cp", "mkdir", "uname"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(only, tool))
		}
	}
	for _, stub := range []string{"sudo", "id", "mktemp", "curl", "sha256sum"} {
		if _, err := os.Stat(filepath.Join(h.bin, stub)); err == nil {
			_ = os.Remove(filepath.Join(only, stub))
			require.NoError(t, os.Symlink(filepath.Join(h.bin, stub), filepath.Join(only, stub)))
		}
	}
	h.env[0] = "PATH=" + only
	r := h.run(h.command(site(), testToken))
	assert.Equal(t, 1, r.code, r.out)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [tool_missing] gzip is required")
	assert.Empty(t, h.argv("mktemp"))
}

func TestLinuxCommandReadsTheTokenFromAFile(t *testing.T) {
	for name, content := range map[string]string{"plain": testToken, "newline": testToken + "\n", "crlf": testToken + "\r\n", "spaces": "  " + testToken + "  \n\n"} {
		t.Run(name, func(t *testing.T) {
			h := newLinuxHarness(t, "")
			tokenFile := filepath.Join(h.root, "etc-token")
			require.NoError(t, os.WriteFile(tokenFile, []byte(content), 0o600))
			req := fullRequest(VariantTokenFile)
			req.TokenFiles = &TokenFiles{Linux: tokenFile}
			r := h.run(h.command(req, ""))
			require.Equal(t, 0, r.code, r.out)
			args := h.argv("installer")
			assert.Equal(t, []string{"--server", "https://fleet.example.com", "--token-file", tokenFile}, args[:4], "the installer reads the file of the operator")
			assert.Equal(t, "header = \"Authorization: Bearer "+testToken+"\"\n", h.log("curl.stdin"))
			assert.Equal(t, content, h.log("installer.token"), "the file is as the operator left it")
			assert.FileExists(t, tokenFile, "and it is still there")
			assert.Empty(t, h.privateDirs())
			for _, a := range h.argv("curl") {
				assert.NotContains(t, a, testToken)
			}
		})
	}
}

func TestLinuxCommandWithAFileNeedsAUsableFile(t *testing.T) {
	t.Run("a file that is not there", func(t *testing.T) {
		h := newLinuxHarness(t, "")
		req := fullRequest(VariantTokenFile)
		req.TokenFiles = &TokenFiles{Linux: filepath.Join(h.root, "missing")}
		r := h.run(h.command(req, ""))
		assert.Equal(t, 1, r.code)
		assert.Contains(t, r.out, "[myriarch-install] ERROR [file_unreadable]")
		assert.Empty(t, h.argv("curl"))
	})
	for name, content := range map[string]string{
		"empty": "", "a semicolon": "myre_abc;id", "a quote": "\"myre_abc\"", "the placeholder": brand.EnrollmentTokenPlaceholder, "a command": "$(id)",
	} {
		t.Run(name, func(t *testing.T) {
			h := newLinuxHarness(t, "")
			tokenFile := filepath.Join(h.root, "token")
			require.NoError(t, os.WriteFile(tokenFile, []byte(content), 0o600))
			req := fullRequest(VariantTokenFile)
			req.TokenFiles = &TokenFiles{Linux: tokenFile}
			r := h.run(h.command(req, ""))
			assert.Equal(t, 1, r.code, r.out)
			assert.Contains(t, r.out, "[myriarch-install] ERROR [option_")
			assert.Empty(t, h.argv("curl"), "nothing is downloaded with it")
			assert.Empty(t, h.log("installer.argv"))
			assert.FileExists(t, tokenFile, "the file of the operator is never touched")
			if content != "" {
				assert.NotContains(t, r.out, content, "the message does not repeat it")
			}
		})
	}
	t.Run("a directory is not a file", func(t *testing.T) {
		h := newLinuxHarness(t, "")
		req := fullRequest(VariantTokenFile)
		req.TokenFiles = &TokenFiles{Linux: h.root}
		r := h.run(h.command(req, ""))
		assert.Contains(t, r.out, "[myriarch-install] ERROR [file_unreadable]")
	})
}

func TestLinuxCommandOfTheLabVariant(t *testing.T) {
	h := newLinuxHarness(t, "")
	req := site(func(r *CommandsRequest) { r.Variant = VariantLab })
	cmd := h.command(req, testToken, func(d *Deps, s *stubBundles) { d.DevMode, d.PublicURL = true, "http://localhost:8080" })
	r := h.run(cmd)
	require.Equal(t, 0, r.code, r.out)
	curl := h.argv("curl")
	assert.Equal(t, []string{"--proto", "=http,https"}, curl[3:5])
	assert.Equal(t, "http://localhost:8080/api/v1/install/bundles/"+h.fixture.name, curl[len(curl)-1])
	args := h.argv("installer")
	assert.Equal(t, "--insecure", args[len(args)-1])
	assert.Equal(t, "http://localhost:8080", args[1])
	h.theTokenIsNowhere(testToken, r)
}

// shellcheck has to be content with the script, as it is for the scripts of the repository.
func TestLinuxScriptPassesShellcheck(t *testing.T) {
	path, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not installed")
	}
	script := "#!/bin/sh\n" + strings.TrimPrefix(scriptMarkers.Replace(linuxScript), "\n")
	file := filepath.Join(t.TempDir(), "setup.sh")
	require.NoError(t, os.WriteFile(file, []byte(script), 0o644))
	out, err := exec.Command(path, "-s", "sh", file).CombinedOutput()
	assert.NoError(t, err, string(out))
}

// The real curl, against a TLS server with its own CA, with the real command: the configuration that carries the
// header, the options and the certificate check work as the command expects.
func TestLinuxCommandWithTheRealCurl(t *testing.T) {
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed")
	}
	h := newLinuxHarness(t, "")
	var mu sync.Mutex
	var gotAuth, gotPath, gotUA string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth, gotPath, gotUA = r.Header.Get("Authorization"), r.URL.Path, r.Header.Get("User-Agent")
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(h.fixture.archive)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile := filepath.Join(h.root, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644))

	// the real curl instead of the stand-in
	require.NoError(t, os.Remove(filepath.Join(h.bin, "curl")))
	require.NoError(t, os.Symlink(curlPath, filepath.Join(h.bin, "curl")))

	req := site(func(r *CommandsRequest) { r.CAFile = caFile })
	cmd := h.command(req, testToken, func(d *Deps, s *stubBundles) { d.PublicURL = srv.URL })
	r := h.run(cmd)
	if r.code != 0 && strings.Contains(r.out, "certificate") && runtime.GOOS == "darwin" {
		t.Skipf("the curl of this system does not use --cacert: %s", r.out)
	}
	require.Equal(t, 0, r.code, r.out)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "Bearer "+testToken, gotAuth, "the header came through the configuration on the standard input")
	assert.Equal(t, "/api/v1/install/bundles/"+h.fixture.name, gotPath)
	assert.NotEmpty(t, gotUA)
	assert.Equal(t, testToken+"\n", h.log("installer.token"))
	assert.Empty(t, h.privateDirs())

	// without the CA the certificate check refuses the server, and nothing is installed
	h2 := newLinuxHarness(t, "")
	require.NoError(t, os.Remove(filepath.Join(h2.bin, "curl")))
	require.NoError(t, os.Symlink(curlPath, filepath.Join(h2.bin, "curl")))
	r = h2.run(h2.command(site(), testToken, func(d *Deps, s *stubBundles) { d.PublicURL = srv.URL }))
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.out, "[myriarch-install] ERROR [download_failed]")
	assert.Empty(t, h2.log("installer.argv"))
}
