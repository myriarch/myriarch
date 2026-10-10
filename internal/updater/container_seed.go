// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Masterminds/semver/v3"
)

// Seed decisions (logged as "seed decision").
const (
	SeedKeepActive     = "keep-active"
	SeedActivateImage  = "activate-image"
	SeedSkippedNoImage = "skipped-image-unusable"
)

// seedResult is the outcome of seedFromImage.
type seedResult struct {
	// ImageVersion is the collector version baked into the image ("" if it
	// could not be determined).
	ImageVersion string
	// Active is the version that is active after seeding.
	Active string
	// Before is the version that was active before.
	Before   string
	Decision string
	Reason   string
}

// seedVersionRE finds the version in `otelcol-contrib --version` output
// ("otelcol-contrib version 0.161.0").
var seedVersionRE = regexp.MustCompile(`(?m)\bversion\s+v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?)\b`)

// imageVersion runs the image binary with --version.
func imageVersion(ctx context.Context, exe Executor, binary string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	vctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exe(vctx, nil, binary, "--version")
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w: %s", binary, err, trimOutput(out))
	}
	m := seedVersionRE.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("cannot find a version in the output of %s --version: %q", binary, trimOutput(out))
	}
	v, err := normalizeVersion(string(m[1]))
	if err != nil {
		return "", err
	}
	return v, nil
}

// activeIntact reports whether the binary of version v is present, executable
// and (if the install marker records a hash) unmodified.
func (l *Layout) activeIntact(v string) bool {
	st, err := os.Stat(l.BinaryPath(v))
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return false
	}
	m, err := l.readMarker(v)
	if err != nil || m.BinaryHash == "" {
		return true // nothing to compare against
	}
	got, err := fileSHA256(l.BinaryPath(v))
	return err == nil && hex.EncodeToString(got) == m.BinaryHash
}

// seedFromImage makes sure the collector binary of the container image is
// part of the persistent volume and decides which version is active (README
// 8, "Seeding/Vorrang"):
//
//   - no active version: the image version is activated;
//   - seedPolicy image: the image version is activated (emergency switch);
//   - seedPolicy newest: the higher version wins, a tie keeps the volume; an
//     active binary that is missing or modified is replaced by the image one.
//
// The image binary is copied only when it is about to be activated. If the
// image binary cannot be inspected but a version is active, the volume is
// used as it is.
//
// With holdActive (an update journal of an interrupted update is pending) an
// existing active version is never replaced: recovery has to run first, then
// seeding is evaluated again.
func seedFromImage(ctx context.Context, log *slog.Logger, cfg Config, layout *Layout, exe Executor, now func() time.Time, holdActive bool) (seedResult, error) {
	var res seedResult
	cur, err := layout.CurrentVersion()
	if err != nil {
		return res, err
	}
	res.Before, res.Active = cur, cur

	imgVer, err := imageVersion(ctx, exe, cfg.Container.SeedBinary, cfg.Update.PreflightTimeout.D())
	if err != nil {
		if cur == "" {
			return res, fmt.Errorf("no collector version is installed and the image binary is unusable: %w", err)
		}
		res.Decision, res.Reason = SeedSkippedNoImage, err.Error()
		log.Error("seed decision", "decision", res.Decision, "active", cur, "err", err)
		return res, nil
	}
	res.ImageVersion = imgVer
	imgHash, err := fileSHA256(cfg.Container.SeedBinary)
	if err != nil {
		if cur == "" {
			return res, fmt.Errorf("hash image binary: %w", err)
		}
		res.Decision, res.Reason = SeedSkippedNoImage, err.Error()
		log.Error("seed decision", "decision", res.Decision, "active", cur, "err", err)
		return res, nil
	}

	hexImg := hex.EncodeToString(imgHash)
	activate, reason := decideSeed(cfg.Container.SeedPolicy, cur, imgVer, hexImg, layout)
	res.Reason = reason
	if activate && holdActive && cur != "" {
		activate = false
		res.Reason = "update journal pending, seeding is deferred until recovery has run (" + reason + ")"
		reason = res.Reason
	}
	if !activate {
		res.Decision = SeedKeepActive
		log.Info("seed decision", "imageVersion", imgVer, "active", cur, "decision", res.Decision, "reason", reason, "policy", cfg.Container.SeedPolicy)
		return res, nil
	}

	// Reuse an intact version directory, otherwise copy the image binary.
	reuse := false
	if m, err := layout.readMarker(imgVer); err == nil && layout.activeIntact(imgVer) {
		switch {
		case m.Source == SourceImage && m.BinaryHash != hexImg:
			// the image was rebuilt with another binary of the same version
		case cfg.Container.SeedPolicy == SeedPolicyImage && m.Source != SourceImage:
			// the emergency switch: the image binary wins over a downloaded one
		default:
			reuse = true
		}
	}
	previous := cur
	if cur == imgVer {
		previous = layout.previousOf(imgVer)
	}
	if reuse {
		if m, err := layout.readMarker(imgVer); err == nil && m.PreviousVersion != previous && previous != "" {
			m.PreviousVersion = previous
			if err := writeMarkerFile(layout.VersionDir(imgVer), m); err != nil {
				log.Warn("update seed marker failed", "err", err)
			}
		}
	} else if err := installSeed(layout, cfg, imgVer, previous, imgHash, now()); err != nil {
		return res, fmt.Errorf("seed %s from the image: %w", imgVer, err)
	}
	if err := layout.Activate(imgVer); err != nil {
		return res, fmt.Errorf("activate seeded version %s: %w", imgVer, err)
	}
	res.Decision, res.Active = SeedActivateImage, imgVer
	log.Info("seed decision", "imageVersion", imgVer, "active", imgVer, "before", cur, "decision", res.Decision, "reason", reason,
		"policy", cfg.Container.SeedPolicy, "copied", !reuse)
	if err := layout.Prune(cfg.Collector.KeepVersions, imgVer, cur); err != nil {
		log.Warn("prune old versions", "err", err)
	}
	return res, nil
}

// decideSeed implements the precedence rules; it reports whether the image
// version has to be activated and why.
func decideSeed(policy, cur, imgVer, imgHashHex string, layout *Layout) (bool, string) {
	switch {
	case cur == "":
		return true, "no collector version is active yet"
	case policy == SeedPolicyImage:
		if m, err := layout.readMarker(cur); cur == imgVer && err == nil && m.Source == SourceImage && m.BinaryHash == imgHashHex && layout.activeIntact(cur) {
			return false, "seedPolicy image: the image binary is already active"
		}
		return true, "seedPolicy image"
	case !layout.activeIntact(cur):
		return true, fmt.Sprintf("the active binary of v%s is missing, not executable or modified", cur)
	}
	cv, cerr := semver.StrictNewVersion(cur)
	iv, ierr := semver.StrictNewVersion(imgVer)
	switch {
	case cerr != nil || ierr != nil:
		return true, fmt.Sprintf("versions %q and %q are not comparable", cur, imgVer)
	case cv.GreaterThan(iv):
		return false, fmt.Sprintf("active v%s is newer than the image v%s", cur, imgVer)
	case cv.Equal(iv):
		return false, fmt.Sprintf("active v%s equals the image version", cur)
	default:
		return true, fmt.Sprintf("the image v%s is newer than the active v%s", imgVer, cur)
	}
}

// installSeed copies the image binary into versions/<v> (staging, fsync,
// SHA-256 check, install marker, rename).
func installSeed(layout *Layout, cfg Config, v, previous string, imgHash []byte, now time.Time) error {
	if err := os.MkdirAll(layout.VersionsDir(), 0o755); err != nil {
		return err
	}
	staging := filepath.Join(layout.VersionsDir(), v+".tmp-"+randSuffix())
	if err := os.Mkdir(staging, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(staging)
		}
	}()
	dst := filepath.Join(staging, cfg.Collector.BinaryName)
	sum, err := copyFileHashed(dst, cfg.Container.SeedBinary, 0o755)
	if err != nil {
		return err
	}
	if !digestEqual(sum, imgHash) {
		return errors.New("the image binary changed while it was copied")
	}
	if err := writeMarkerFile(staging, installMarker{
		Version: v, BinaryHash: hex.EncodeToString(sum), PreviousVersion: previous,
		InstalledAt: now.UTC(), Source: SourceImage,
	}); err != nil {
		return err
	}
	final := layout.VersionDir(v)
	if _, err := os.Lstat(final); err == nil {
		if err := os.RemoveAll(final); err != nil {
			return fmt.Errorf("remove stale version dir: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		return err
	}
	ok = true
	syncDir(layout.VersionsDir())
	return nil
}

// copyFileHashed copies src to dst (fsync) and returns the SHA-256 of what
// was written.
func copyFileHashed(dst, src string, perm os.FileMode) ([]byte, error) {
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	h := newSHA256()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		_ = out.Close()
		return nil, err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	// umask may have stripped the execute bit
	if err := os.Chmod(dst, perm); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
