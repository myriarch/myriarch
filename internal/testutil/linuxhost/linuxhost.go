// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Package linuxhost runs the install command for Linux (internal/install) in a real shell, the way a person pastes
// it, on a host that the test builds: stand-ins for sudo, id and mktemp (a test is not root and has no /root), for
// curl unless the test wants the real one, and an installer in the bundle that writes down how it was called. It is a
// helper of tests; the tests of the install package and of the server use it, so that the command is checked in one way
// everywhere.
package linuxhost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// InstallerStub is the installer in the bundle of a test: it records its arguments, its environment, the file that
// --token-file names (content, mode, directory) and its standard input in the log directory, and ends with
// $INSTALLER_EXIT.
const InstallerStub = `#!/usr/bin/env bash
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

// SHA256Hex is the SHA-256 of b in lower case hex.
func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Bundle is an installer bundle for Linux whose installer is a stand-in.
type Bundle struct {
	Archive []byte
	SHA256  string
}

// NewBundle builds a bundle (a tar.gz with the layout of the release archive) whose installer is script, or
// InstallerStub when script is empty.
func NewBundle(t testing.TB, script string) Bundle {
	t.Helper()
	if script == "" {
		script = InstallerStub
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, body string) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	add("LICENSE", 0o644, "license")
	add("deploy/scripts/install-linux.sh", 0o755, script)
	add("deploy/supervisor/linux/supervisor.yaml", 0o644, "server: {}")
	add("myriarch-updater", 0o755, "binary")
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return Bundle{Archive: buf.Bytes(), SHA256: SHA256Hex(buf.Bytes())}
}

// Host is the machine of a test.
type Host struct {
	t testing.TB
	// Root is the file system of the host: Root/root is the home of root, where the command makes its directory.
	Root string
	// Logs holds what the stand-ins write down.
	Logs string
	bin  string
	env  []string
}

// Options choose the stand-ins.
type Options struct {
	// Shell names the shell for `sh -c` ("dash"); empty is the system one. The test is skipped when it is not installed.
	Shell string
	// Bundle is what the stand-in for curl, and the server for the real curl, deliver. NewBundle(t, "") if zero.
	Bundle Bundle
}

// SkipWithoutShell skips the test when the tools of the command are not installed (not on Windows hosts).
func SkipWithoutShell(t testing.TB) {
	t.Helper()
	for _, tool := range []string{"bash", "sh", "tar", "gzip", "tr", "rm", "cat", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// New builds a host with the stand-ins.
func New(t testing.TB, o Options) *Host {
	t.Helper()
	SkipWithoutShell(t)
	if len(o.Bundle.Archive) == 0 {
		o.Bundle = NewBundle(t, "")
	}
	h := &Host{t: t, Root: t.TempDir(), Logs: t.TempDir(), bin: t.TempDir()}
	require.NoError(t, os.MkdirAll(filepath.Join(h.Root, "root"), 0o755))
	archive := filepath.Join(h.Logs, "fixture.tar.gz")
	require.NoError(t, os.WriteFile(archive, o.Bundle.Archive, 0o644))
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
	path := h.bin
	if o.Shell != "" {
		sh, err := exec.LookPath(o.Shell)
		if err != nil {
			t.Skipf("%s is not installed", o.Shell)
		}
		shdir := t.TempDir()
		require.NoError(t, os.Symlink(sh, filepath.Join(shdir, "sh")))
		path = shdir + string(os.PathListSeparator) + path
	}
	h.env = []string{
		"PATH=" + path + string(os.PathListSeparator) + os.Getenv("PATH"),
		"LOGDIR=" + h.Logs, "FAKE_ROOT=" + h.Root, "FIXTURE=" + archive, "REAL_MKTEMP=" + realMktemp,
		"HOME=" + h.Root + "/home", "LANG=C",
	}
	return h
}

// UseRealCurl replaces the stand-in for curl with the curl of the machine. It skips the test without one.
func (h *Host) UseRealCurl() {
	h.t.Helper()
	curl, err := exec.LookPath("curl")
	if err != nil {
		h.t.Skip("curl is not installed")
	}
	stand := filepath.Join(h.bin, "curl")
	require.NoError(h.t, os.Remove(stand))
	require.NoError(h.t, os.Symlink(curl, stand))
}

// WithPath replaces the PATH of the host.
func (h *Host) WithPath(path string) { h.env[0] = "PATH=" + path }

// Bin is the directory with the stand-ins.
func (h *Host) Bin() string { return h.bin }

// SetBundle replaces the archive that the stand-in for curl delivers.
func (h *Host) SetBundle(b Bundle) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(filepath.Join(h.Logs, "fixture.tar.gz"), b.Archive, 0o644))
}

// Result is the outcome of a command.
type Result struct {
	Out  string
	Code int
}

// Run executes the command in bash with the given extra environment (NAME=value).
func (h *Host) Run(command string, extra ...string) Result {
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
	return Result{Out: out.String(), Code: code}
}

// LockedBuffer collects the output of a process that the test reads while it runs.
type LockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *LockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// String returns what was written.
func (l *LockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Start starts the command in its own process group and returns the process and its output. Interrupt sends the
// signal the way Ctrl-C does.
func (h *Host) Start(command string) (*exec.Cmd, *LockedBuffer) {
	h.t.Helper()
	cmd := exec.Command("bash", "-c", command)
	cmd.Env = h.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &LockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	require.NoError(h.t, cmd.Start())
	return cmd, out
}

// Interrupt sends SIGINT to the process group of a command that Start started.
func Interrupt(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }

// Log returns a file that a stand-in wrote, or "".
func (h *Host) Log(name string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.Logs, name))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(h.t, err)
	return string(b)
}

// Argv returns the arguments with which a stand-in or the installer was called, over all calls.
func (h *Host) Argv(name string) []string {
	h.t.Helper()
	raw := h.Log(name + ".argv")
	if raw == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
}

// PrivateDirs lists what is left of the working directories below the home of root.
func (h *Host) PrivateDirs() []string {
	h.t.Helper()
	entries, err := os.ReadDir(filepath.Join(h.Root, "root"))
	require.NoError(h.t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// AssertTokenNowhere checks every place where the token must not be: the argument vectors of all programs, the
// environment of the installer, the output, the standard input of the installer and every file that is left.
func (h *Host) AssertTokenNowhere(token, out string) {
	h.t.Helper()
	for _, name := range []string{"sudo", "mktemp", "curl", "installer"} {
		for _, a := range h.Argv(name) {
			assert.NotContains(h.t, a, token, "%s argv", name)
		}
	}
	assert.NotContains(h.t, h.Log("installer.env"), token, "the environment of the installer")
	assert.NotContains(h.t, out, token, "the output")
	assert.Empty(h.t, h.Log("installer.stdin"), "the installer gets no standard input")
	_ = filepath.Walk(h.Root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(path)
			assert.NotContains(h.t, string(b), token, "left behind: %s", path)
		}
		return nil
	})
}

// Shells lists the shells to run the command in: the system one, and dash when it is installed.
func Shells() []string {
	out := []string{""}
	if _, err := exec.LookPath("dash"); err == nil {
		out = append(out, "dash")
	}
	return out
}

// ShellName names a shell for a test.
func ShellName(sh string) string {
	if sh == "" {
		return "sh"
	}
	return sh
}
