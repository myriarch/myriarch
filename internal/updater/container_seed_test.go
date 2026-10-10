// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedEnv is a fake container: an image binary (script) and a volume.
type seedEnv struct {
	t      *testing.T
	cfg    Config
	layout *Layout
	image  string
}

func newSeedEnv(t *testing.T, imageVersion string) *seedEnv {
	skipOnWindows(t)
	root := t.TempDir()
	cfg := defaultConfigFor("linux")
	cfg.Collector.InstallDir = filepath.Join(root, "collector")
	cfg.Collector.BinaryName = testBinary
	cfg.StateDir = filepath.Join(root, "state")
	cfg.Container.SeedBinary = filepath.Join(root, "image-otelcol-contrib")
	cfg.Container.SeedPolicy = SeedPolicyNewest
	e := &seedEnv{t: t, cfg: cfg, layout: &Layout{InstallDir: cfg.Collector.InstallDir, BinaryName: testBinary, Activation: "symlink"}}
	e.setImage(imageVersion, "")
	return e
}

// setImage writes the image binary; salt makes the bytes differ for equal versions.
func (e *seedEnv) setImage(version, salt string) {
	body := append(fakeCollectorScript(version, 0), []byte("# "+salt+"\n")...)
	require.NoError(e.t, os.WriteFile(e.cfg.Container.SeedBinary, body, 0o755))
}

// install puts a version on the volume as the updater would (package) and
// optionally activates it.
func (e *seedEnv) install(version, source string, activate bool) {
	dir := e.layout.VersionDir(version)
	require.NoError(e.t, os.MkdirAll(dir, 0o755))
	body := fakeCollectorScript(version, 0)
	require.NoError(e.t, os.WriteFile(e.layout.BinaryPath(version), body, 0o755))
	require.NoError(e.t, writeMarkerFile(dir, installMarker{Version: version, BinaryHash: hexSum(body), Source: source, InstalledAt: time.Now().UTC()}))
	if activate {
		require.NoError(e.t, e.layout.Activate(version))
	}
}

func hexSum(b []byte) string { return hexString(sha(b)) }

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}

func (e *seedEnv) seed() seedResult {
	e.t.Helper()
	res, err := seedFromImage(context.Background(), testLogger(e.t), e.cfg, e.layout, osExecutor, func() time.Time { return time.Now().UTC() }, false)
	require.NoError(e.t, err)
	return res
}

func (e *seedEnv) active() string {
	v, err := e.layout.CurrentVersion()
	require.NoError(e.t, err)
	return v
}

func TestSeedFirstStartActivatesTheImageVersion(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	res := e.seed()
	assert.Equal(t, SeedActivateImage, res.Decision)
	assert.Equal(t, "0.161.0", res.ImageVersion)
	assert.Equal(t, "0.161.0", e.active())

	m, err := e.layout.readMarker("0.161.0")
	require.NoError(t, err)
	assert.Equal(t, SourceImage, m.Source)
	assert.Equal(t, hexSum(mustReadFile(t, e.cfg.Container.SeedBinary)), m.BinaryHash)
	assert.Empty(t, m.PreviousVersion)
	assert.True(t, e.layout.activeIntact("0.161.0"))
	// the copy is an executable file with the image bytes, and no staging dir is left
	st, err := os.Stat(e.layout.BinaryPath("0.161.0"))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode().Perm()&0o100)
	entries, _ := os.ReadDir(e.layout.VersionsDir())
	for _, en := range entries {
		assert.NotContains(t, en.Name(), ".tmp-")
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

func TestSeedPrecedenceTable(t *testing.T) {
	type tc struct {
		name         string
		image        string
		policy       string
		setup        func(e *seedEnv)
		wantDecision string
		wantActive   string
		wantPrevious string // previousVersion of the active version's marker
	}
	cases := []tc{
		{
			name: "volume has the same version: tie keeps the volume", image: "0.161.0", policy: SeedPolicyNewest,
			setup:        func(e *seedEnv) { e.install("0.161.0", SourcePackage, true) },
			wantDecision: SeedKeepActive, wantActive: "0.161.0",
		},
		{
			name: "volume is newer (updated via the UI): volume wins", image: "0.161.0", policy: SeedPolicyNewest,
			setup:        func(e *seedEnv) { e.install("0.162.0", SourcePackage, true) },
			wantDecision: SeedKeepActive, wantActive: "0.162.0",
		},
		{
			name: "image is newer (image bump): image wins and remembers the predecessor", image: "0.163.0", policy: SeedPolicyNewest,
			setup:        func(e *seedEnv) { e.install("0.162.0", SourcePackage, true) },
			wantDecision: SeedActivateImage, wantActive: "0.163.0", wantPrevious: "0.162.0",
		},
		{
			name: "seedPolicy image overrides a newer volume", image: "0.161.0", policy: SeedPolicyImage,
			setup:        func(e *seedEnv) { e.install("0.162.0", SourcePackage, true) },
			wantDecision: SeedActivateImage, wantActive: "0.161.0", wantPrevious: "0.162.0",
		},
		{
			name: "broken active binary falls back to the image", image: "0.161.0", policy: SeedPolicyNewest,
			setup: func(e *seedEnv) {
				e.install("0.162.0", SourcePackage, true)
				require.NoError(e.t, os.WriteFile(e.layout.BinaryPath("0.162.0"), fakeCollectorScript("0.162.0", 1), 0o755)) // hash differs from the marker
			},
			wantDecision: SeedActivateImage, wantActive: "0.161.0", wantPrevious: "0.162.0",
		},
		{
			name: "missing active binary falls back to the image", image: "0.161.0", policy: SeedPolicyNewest,
			setup: func(e *seedEnv) {
				e.install("0.162.0", SourcePackage, true)
				require.NoError(e.t, os.Remove(e.layout.BinaryPath("0.162.0")))
			},
			wantDecision: SeedActivateImage, wantActive: "0.161.0", wantPrevious: "0.162.0",
		},
		{
			name: "non-executable active binary falls back to the image", image: "0.161.0", policy: SeedPolicyNewest,
			setup: func(e *seedEnv) {
				e.install("0.162.0", SourcePackage, true)
				require.NoError(e.t, os.Chmod(e.layout.BinaryPath("0.162.0"), 0o644))
			},
			wantDecision: SeedActivateImage, wantActive: "0.161.0", wantPrevious: "0.162.0",
		},
		{
			name: "image version already on the volume as older rollback target is reused", image: "0.163.0", policy: SeedPolicyNewest,
			setup: func(e *seedEnv) {
				e.install("0.163.0", SourceImage, false)
				e.install("0.162.0", SourcePackage, true)
			},
			wantDecision: SeedActivateImage, wantActive: "0.163.0", wantPrevious: "0.162.0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSeedEnv(t, c.image)
			e.cfg.Container.SeedPolicy = c.policy
			c.setup(e)
			res := e.seed()
			assert.Equal(t, c.wantDecision, res.Decision, res.Reason)
			assert.Equal(t, c.wantActive, e.active())
			assert.Equal(t, c.wantActive, res.Active)
			assert.Equal(t, c.image, res.ImageVersion)
			if c.wantPrevious != "" {
				assert.Equal(t, c.wantPrevious, e.layout.previousOf(c.wantActive))
			}
			assert.True(t, e.layout.activeIntact(c.wantActive))
			// whatever was decided: the second start changes nothing
			again := e.seed()
			assert.Equal(t, SeedKeepActive, again.Decision, again.Reason)
			assert.Equal(t, c.wantActive, e.active())
		})
	}
}

func TestSeedDoesNotCopyTheImageWhenTheVolumeWins(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	e.install("0.162.0", SourcePackage, true)
	e.seed()
	assert.NoDirExists(t, e.layout.VersionDir("0.161.0"), "the older image binary is only copied when it is needed")
}

func TestSeedImageRebuiltWithAnotherBinaryOfTheSameVersion(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	e.seed()
	first := hexSum(mustReadFile(t, e.layout.BinaryPath("0.161.0")))

	// same version, other bytes in the image, volume still on the old seed: tie keeps the volume ...
	e.setImage("0.161.0", "rebuild")
	res := e.seed()
	assert.Equal(t, SeedKeepActive, res.Decision)
	assert.Equal(t, first, hexSum(mustReadFile(t, e.layout.BinaryPath("0.161.0"))))

	// ... unless the volume binary is damaged: then the changed image seed replaces the stale copy
	require.NoError(t, os.WriteFile(e.layout.BinaryPath("0.161.0"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	res = e.seed()
	assert.Equal(t, SeedActivateImage, res.Decision)
	assert.Equal(t, hexSum(mustReadFile(t, e.cfg.Container.SeedBinary)), hexSum(mustReadFile(t, e.layout.BinaryPath("0.161.0"))))
	m, err := e.layout.readMarker("0.161.0")
	require.NoError(t, err)
	assert.Equal(t, SourceImage, m.Source)
}

func TestSeedPolicyImageReplacesADownloadedSameVersion(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	e.install("0.161.0", SourcePackage, true)
	before := hexSum(mustReadFile(t, e.layout.BinaryPath("0.161.0")))
	e.setImage("0.161.0", "image-bytes")
	e.cfg.Container.SeedPolicy = SeedPolicyImage
	res := e.seed()
	assert.Equal(t, SeedActivateImage, res.Decision)
	after := hexSum(mustReadFile(t, e.layout.BinaryPath("0.161.0")))
	assert.NotEqual(t, before, after)
	m, _ := e.layout.readMarker("0.161.0")
	assert.Equal(t, SourceImage, m.Source)
	// and it is stable: no copy at every start
	again := e.seed()
	assert.Equal(t, SeedKeepActive, again.Decision)
}

func TestSeedUnusableImageKeepsAnActiveVolume(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	e.install("0.162.0", SourcePackage, true)
	require.NoError(t, os.WriteFile(e.cfg.Container.SeedBinary, []byte("#!/bin/sh\necho nonsense\n"), 0o755))
	res := e.seed()
	assert.Equal(t, SeedSkippedNoImage, res.Decision)
	assert.Equal(t, "0.162.0", e.active())
}

func TestSeedUnusableImageWithoutVolumeIsAnError(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	require.NoError(t, os.Remove(e.cfg.Container.SeedBinary))
	_, err := seedFromImage(context.Background(), testLogger(t), e.cfg, e.layout, osExecutor, time.Now, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image binary is unusable")
}

func TestSeedRefusesNonSymlinkCurrent(t *testing.T) {
	e := newSeedEnv(t, "0.161.0")
	require.NoError(t, os.MkdirAll(e.cfg.Collector.InstallDir, 0o755))
	require.NoError(t, os.WriteFile(e.layout.currentLink(), []byte("x"), 0o644))
	_, err := seedFromImage(context.Background(), testLogger(t), e.cfg, e.layout, osExecutor, time.Now, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a symlink")
}

func TestSeedAfterImageBumpAllowsOneStepRollbackOnly(t *testing.T) {
	// the anti-downgrade policy of the installer accepts exactly the version
	// the seed recorded as predecessor
	e := newSeedEnv(t, "0.163.0")
	e.install("0.162.0", SourcePackage, true)
	e.install("0.161.0", SourcePackage, false)
	e.seed()
	in := &Installer{Cfg: e.cfg, Layout: e.layout}
	assert.NoError(t, in.checkVersionPolicy("0.162.0", "0.163.0"), "back to the predecessor")
	assert.Error(t, in.checkVersionPolicy("0.161.0", "0.163.0"), "two steps back is refused")
}

func TestSeedPrunesOldVersions(t *testing.T) {
	e := newSeedEnv(t, "0.165.0")
	for i, v := range []string{"0.160.0", "0.161.0", "0.162.0", "0.163.0"} {
		e.install(v, SourcePackage, v == "0.163.0")
		_ = i
		time.Sleep(10 * time.Millisecond)
	}
	e.seed()
	vs, err := e.layout.ListVersions()
	require.NoError(t, err)
	var names []string
	for _, v := range vs {
		names = append(names, v.Version)
	}
	assert.ElementsMatch(t, []string{"0.165.0", "0.163.0"}, names, "active + predecessor are kept (keepVersions 2)")
}
