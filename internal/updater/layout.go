// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// semverRE restricts versions that come from the server to a strict, path
// safe subset: X.Y.Z with an optional pre-release (letters, digits, dots,
// dashes). Leading "v" is removed before matching.
var semverRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)

// normalizeVersion strips a leading "v" and validates the version.
func normalizeVersion(v string) (string, error) {
	n := strings.TrimPrefix(v, "v")
	if !semverRE.MatchString(n) || len(n) > 64 {
		return "", fmt.Errorf("invalid version %q", v)
	}
	return n, nil
}

// markerFile is written into every version directory after successful
// verification and extraction.
const markerFile = ".myriarch-install.json"

// installMarker records what was verified for a version directory.
type installMarker struct {
	Version     string `json:"version"`
	ContentHash string `json:"contentHash"` // hex SHA-256 of the archive
	BinaryHash  string `json:"binaryHash"`  // hex SHA-256 of the binary
	// PreviousVersion is the version that was active before this one was
	// installed (permits a one-step rollback under the anti-downgrade policy).
	PreviousVersion string    `json:"previousVersion,omitempty"`
	InstalledAt     time.Time `json:"installedAt"`
	// Source says where the version came from: "package" (downloaded and
	// verified by the updater) or "image" (seeded from the container image,
	// container mode). Empty = unknown (older installs).
	Source string `json:"source,omitempty"`
}

// Marker sources.
const (
	SourcePackage = "package"
	SourceImage   = "image"
)

// Layout describes the on-disk collector installation:
//
//	<installDir>/versions/<version>/<binary>
//	<installDir>/current -> versions/<version>      (activation "symlink")
//	<installDir>/bin/<binary> (+ .previous)         (activation "copy")
//	<installDir>/bin/current-version                (activation "copy")
type Layout struct {
	InstallDir string
	BinaryName string
	Activation string // "symlink" | "copy"
}

// VersionsDir is <installDir>/versions.
func (l *Layout) VersionsDir() string { return filepath.Join(l.InstallDir, "versions") }

// VersionDir is the directory of a (normalized) version.
func (l *Layout) VersionDir(v string) string { return filepath.Join(l.VersionsDir(), v) }

// BinaryPath is the binary inside a version directory.
func (l *Layout) BinaryPath(v string) string { return filepath.Join(l.VersionDir(v), l.BinaryName) }

func (l *Layout) currentLink() string { return filepath.Join(l.InstallDir, "current") }
func (l *Layout) binDir() string      { return filepath.Join(l.InstallDir, "bin") }
func (l *Layout) activeBinary() string {
	return filepath.Join(l.binDir(), l.BinaryName)
}
func (l *Layout) activeVersionFile() string { return filepath.Join(l.binDir(), "current-version") }

// CurrentVersion returns the active version, or "" if none is active yet.
func (l *Layout) CurrentVersion() (string, error) {
	switch l.Activation {
	case "symlink":
		target, err := os.Readlink(l.currentLink())
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("%s is not a symlink (%w); migrate it to a symlink into versions/", l.currentLink(), err)
		}
		v := filepath.Base(filepath.Clean(target))
		if _, err := normalizeVersion(v); err != nil {
			return "", fmt.Errorf("%s points to %q which is not a versions/<version> directory", l.currentLink(), target)
		}
		return v, nil
	case "copy":
		data, err := os.ReadFile(l.activeVersionFile())
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		v := strings.TrimSpace(string(data))
		if _, err := normalizeVersion(v); err != nil {
			return "", fmt.Errorf("%s contains invalid version %q", l.activeVersionFile(), v)
		}
		return v, nil
	default:
		return "", fmt.Errorf("unknown activation %q", l.Activation)
	}
}

// Activate makes version v the active one. The version directory must exist.
// The service using the binary must be stopped on Windows ("copy"); on Linux
// ("symlink") the switch is atomic and safe even while running.
func (l *Layout) Activate(v string) error {
	if _, err := normalizeVersion(v); err != nil {
		return err
	}
	src := l.BinaryPath(v)
	if st, err := os.Stat(src); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("version %s is not installed (%s missing)", v, src)
	}
	switch l.Activation {
	case "symlink":
		return l.activateSymlink(v)
	case "copy":
		return l.activateCopy(v)
	default:
		return fmt.Errorf("unknown activation %q", l.Activation)
	}
}

func (l *Layout) activateSymlink(v string) error {
	link := l.currentLink()
	if st, err := os.Lstat(link); err == nil && st.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s exists and is not a symlink; refusing to replace it", link)
	}
	tmp := link + ".tmp-" + randSuffix()
	if err := os.Symlink(filepath.Join("versions", v), tmp); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil { // atomic replace
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", link, err)
	}
	return nil
}

func (l *Layout) activateCopy(v string) error {
	active := l.activeBinary()
	if err := os.MkdirAll(l.binDir(), 0o755); err != nil {
		return err
	}
	newBin := active + ".new"
	if err := copyFileAtomic(newBin, l.BinaryPath(v), 0o755); err != nil {
		return fmt.Errorf("copy %s: %w", l.BinaryPath(v), err)
	}
	prev := active + ".previous"
	hadActive := false
	if _, err := os.Stat(active); err == nil {
		hadActive = true
		_ = os.Remove(prev)
		if err := os.Rename(active, prev); err != nil {
			_ = os.Remove(newBin)
			return fmt.Errorf("back up %s: %w (is the service still running?)", active, err)
		}
	}
	if err := os.Rename(newBin, active); err != nil {
		if hadActive {
			_ = os.Rename(prev, active) // restore
		}
		_ = os.Remove(newBin)
		return fmt.Errorf("install %s: %w", active, err)
	}
	syncDir(l.binDir())
	if err := writeFileAtomic(l.activeVersionFile(), []byte(v+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}

// ActiveBinaryMismatch reports whether the active copy of the collector
// binary ("copy" activation) is missing or differs from the binary of the
// version recorded as current - the state a crash between the renames of
// activateCopy leaves behind. It is always false for symlink activation
// (the swap is a single atomic rename) and when the version directory that
// would be authoritative is gone.
func (l *Layout) ActiveBinaryMismatch() (bool, error) {
	if l.Activation != "copy" {
		return false, nil
	}
	cur, err := l.CurrentVersion()
	if err != nil || cur == "" {
		return false, err
	}
	want, err := fileSHA256(l.BinaryPath(cur))
	if err != nil {
		return false, nil // nothing to compare against
	}
	got, err := fileSHA256(l.activeBinary())
	if err != nil {
		return true, nil
	}
	return !digestEqual(got, want), nil
}

// VersionInfo describes an installed version directory.
type VersionInfo struct {
	Version     string
	InstalledAt time.Time
}

// ListVersions returns the installed version directories (newest first).
func (l *Layout) ListVersions() ([]VersionInfo, error) {
	entries, err := os.ReadDir(l.VersionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []VersionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := normalizeVersion(e.Name()); err != nil {
			continue // staging dirs (".tmp-...") and foreign dirs are ignored here
		}
		vi := VersionInfo{Version: e.Name()}
		if m, err := l.readMarker(e.Name()); err == nil {
			vi.InstalledAt = m.InstalledAt
		} else if info, err := e.Info(); err == nil {
			vi.InstalledAt = info.ModTime()
		}
		out = append(out, vi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstalledAt.After(out[j].InstalledAt) })
	return out, nil
}

func (l *Layout) readMarker(v string) (installMarker, error) {
	var m installMarker
	data, err := os.ReadFile(filepath.Join(l.VersionDir(v), markerFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// previousOf returns the recorded predecessor of an installed version ("" if unknown).
func (l *Layout) previousOf(v string) string {
	m, err := l.readMarker(v)
	if err != nil {
		return ""
	}
	return m.PreviousVersion
}

func (l *Layout) writeMarker(dir, v, previous string, contentHash, binaryHash []byte, now time.Time) error {
	return writeMarkerFile(dir, installMarker{Version: v, ContentHash: hex.EncodeToString(contentHash), BinaryHash: hex.EncodeToString(binaryHash),
		InstalledAt: now.UTC(), PreviousVersion: previous, Source: SourcePackage})
}

func writeMarkerFile(dir string, m installMarker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, markerFile), data, 0o644)
}

// ActiveContentHash returns the archive SHA-256 verified when the active
// version was installed (nil if unknown, e.g. installed by the install
// script).
func (l *Layout) ActiveContentHash() []byte {
	cur, err := l.CurrentVersion()
	if err != nil || cur == "" {
		return nil
	}
	m, err := l.readMarker(cur)
	if err != nil {
		return nil
	}
	h, err := hex.DecodeString(m.ContentHash)
	if err != nil {
		return nil
	}
	return h
}

// Prune removes version directories beyond keep (newest first), never the
// protected ones, and stale staging directories. Errors are returned joined
// but do not stop the cleanup.
func (l *Layout) Prune(keep int, protect ...string) error {
	versions, err := l.ListVersions()
	if err != nil {
		return err
	}
	prot := map[string]bool{}
	for _, p := range protect {
		if p != "" {
			prot[p] = true
		}
	}
	kept := 0
	for _, v := range versions {
		if prot[v.Version] {
			kept++
		}
	}
	var errs []error
	for _, v := range versions { // newest first
		if prot[v.Version] {
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		if err := os.RemoveAll(l.VersionDir(v.Version)); err != nil {
			errs = append(errs, err)
		}
	}
	// stale staging directories from crashed installs
	if entries, err := os.ReadDir(l.VersionsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.Contains(e.Name(), ".tmp-") {
				if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
					_ = os.RemoveAll(filepath.Join(l.VersionsDir(), e.Name()))
				}
			}
		}
	}
	return errors.Join(errs...)
}
