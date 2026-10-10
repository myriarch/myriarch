// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testSupervisorSvc = "otelcol-supervisor"
	testBinary        = "otelcol-contrib"
)

func testLogger(t testing.TB) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(string(bytes.TrimRight(p, "\n")))
	return len(p), nil
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses shell scripts as fake collector binaries")
	}
}

// fakeCollectorScript is a shell script that behaves like otelcol-contrib for
// --version and validate.
func fakeCollectorScript(version string, validateExit int) []byte {
	return []byte("#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo \"otelcol-contrib version " + version + "\";;\n" +
		"  validate) echo validating \"$@\"; exit " + strconv.Itoa(validateExit) + ";;\n" +
		"esac\n")
}

type tarEntry struct {
	name     string
	body     []byte
	typeflag byte
	linkname string
	mode     int64
	// sizeOverride lets tests lie in the header (0 = body length)
	sizeOverride int64
}

func makeTarGz(t testing.TB, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		size := int64(len(e.body))
		if e.sizeOverride != 0 {
			size = e.sizeOverride
		}
		if tf != tar.TypeReg {
			size = 0
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tf, Linkname: e.linkname, Mode: mode, Size: size}))
		if tf == tar.TypeReg {
			_, err := tw.Write(e.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func makeZip(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func sha(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func collectorArchive(t testing.TB, version string) []byte {
	return makeTarGz(t,
		tarEntry{name: "README.md", body: []byte("readme"), mode: 0o644},
		tarEntry{name: testBinary, body: fakeCollectorScript(version, 0)},
	)
}

// env is a complete fake host.
type env struct {
	t        testing.TB
	root     string
	cfg      Config
	layout   *Layout
	svc      *FakeServiceController
	inst     *Installer
	verifier *fakeVerifier
	// healthy decides the collector health endpoint answer for the active version.
	healthy   func(active string) bool
	healthSrv *httptest.Server
	execCalls atomic.Int32
}

type fakeVerifier struct {
	mu      sync.Mutex
	err     error
	calls   int
	lastVer string
	lastSig []byte
	lastDig []byte
}

func (f *fakeVerifier) Verify(sig, digest []byte, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastVer, f.lastSig, f.lastDig = version, sig, digest
	return f.err
}

func newEnv(t *testing.T, activation string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, root: root, verifier: &fakeVerifier{}}
	e.healthy = func(string) bool { return true }

	cfg := defaultConfigFor("linux")
	cfg.Server.Endpoint = "ws://127.0.0.1:1/v1/opamp"
	cfg.Server.Token = "myre_test"
	cfg.StateDir = filepath.Join(root, "state")
	cfg.Supervisor.StorageDir = filepath.Join(root, "supervisor")
	cfg.Supervisor.ServiceName = testSupervisorSvc
	cfg.Supervisor.HealthURL = ""
	cfg.Collector.InstallDir = filepath.Join(root, "collector")
	cfg.Collector.BinaryName = testBinary
	cfg.Collector.Activation = activation
	cfg.Update.RequireSignature = false
	cfg.Update.PreflightValidate = false
	cfg.Update.HealthInterval = Duration(10 * time.Millisecond)
	cfg.Update.HealthSuccesses = 2
	cfg.Update.HealthTimeout = Duration(1500 * time.Millisecond)
	cfg.Update.FailedRetryBackoff = Duration(time.Hour)
	require.NoError(t, os.MkdirAll(cfg.StateDir, 0o750))
	require.NoError(t, os.MkdirAll(cfg.Supervisor.StorageDir, 0o750))
	e.cfg = cfg
	e.layout = &Layout{InstallDir: cfg.Collector.InstallDir, BinaryName: testBinary, Activation: activation}

	e.healthSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur, _ := e.layout.CurrentVersion()
		if e.healthy(cur) {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(503)
	}))
	t.Cleanup(e.healthSrv.Close)
	e.cfg.Collector.HealthURL = e.healthSrv.URL
	e.svc = NewFakeServiceController(testSupervisorSvc)
	e.rebuild()
	return e
}

// rebuild re-creates the installer from e.cfg.
func (e *env) rebuild() {
	e.inst = &Installer{
		Cfg: e.cfg, Log: testLogger(e.t), Layout: e.layout, Services: e.svc,
		Health: &HealthChecker{
			Services: e.svc, ServiceName: testSupervisorSvc,
			SupervisorURL: e.cfg.Supervisor.HealthURL, CollectorURL: e.cfg.Collector.HealthURL,
			Interval: e.cfg.Update.HealthInterval.D(), Successes: e.cfg.Update.HealthSuccesses, Timeout: e.cfg.Update.HealthTimeout.D(),
		},
		Verifier: e.verifier,
		Exec: func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
			e.execCalls.Add(1)
			return osExecutor(ctx, env, name, args...)
		},
	}
}

// installOld installs an "old" version on disk and activates it.
func (e *env) installOld(version string) {
	e.t.Helper()
	dir := e.layout.VersionDir(version)
	require.NoError(e.t, os.MkdirAll(dir, 0o755))
	require.NoError(e.t, os.WriteFile(e.layout.BinaryPath(version), fakeCollectorScript(version, 0), 0o755))
	require.NoError(e.t, e.layout.Activate(version))
}

func (e *env) request(version string, archive []byte) InstallRequest {
	return InstallRequest{
		Version:     "v" + version,
		DownloadURL: "https://fleet.example.com/api/v1/artifacts/x/content/otelcol-contrib_" + version + "_linux_amd64.tar.gz",
		Data:        bytes.NewReader(archive),
		ContentHash: sha(archive),
	}
}

func (e *env) active() string {
	e.t.Helper()
	v, err := e.layout.CurrentVersion()
	require.NoError(e.t, err)
	return v
}

func (e *env) noLeftovers() {
	e.t.Helper()
	entries, _ := os.ReadDir(e.layout.VersionsDir())
	for _, en := range entries {
		require.NotContains(e.t, en.Name(), ".tmp-", "staging dir left behind")
	}
	if dl, err := os.ReadDir(filepath.Join(e.cfg.StateDir, "downloads")); err == nil {
		require.Empty(e.t, dl, "download files left behind")
	}
}

func pemEncode(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
