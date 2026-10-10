// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

// Installer performs verified, atomic collector binary updates with health
// check and automatic rollback.
type Installer struct {
	Cfg      Config
	Log      *slog.Logger
	Layout   *Layout
	Services ServiceController
	Health   *HealthChecker
	Verifier SignatureVerifier
	// Exec overrides how programs are run (tests). Nil = the OS, and for the
	// config preflight as the supervisor's service account when possible.
	Exec Executor
	Now  func() time.Time
	// Progress receives the steps of every attempt (nil = no reporting).
	Progress Progress

	// privilegedHost reports whether the updater runs with more privileges
	// than the supervisor's service account without being able to drop them
	// (Windows: SYSTEM). Nil = runtime.GOOS == "windows".
	privilegedHost func() bool

	mu sync.Mutex // serializes installs and recovery
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now().UTC()
	}
	return time.Now().UTC()
}

func (in *Installer) downloadsDir() string { return filepath.Join(in.Cfg.StateDir, "downloads") }

// InstallRequest is one collector update.
type InstallRequest struct {
	// Version as offered ("v0.163.0").
	Version     string
	DownloadURL string
	// Data is the download stream (closed by the caller).
	Data        io.Reader
	ContentHash []byte
	Signature   []byte
}

// Install runs the complete update. The order is deliberate: nothing on the
// host is touched before the SHA-256 and the signature of the downloaded
// archive were verified and the new binary passed the preflight. The
// supervisor is only stopped for the swap, and any failure after the stop is
// rolled back.
func (in *Installer) Install(ctx context.Context, req InstallRequest) (err error) {
	in.mu.Lock()
	defer in.mu.Unlock()

	run := in.newRun()
	defer func() { run.finish(err) }()

	version, err := normalizeVersion(req.Version)
	if err != nil {
		if run.p != nil {
			run.p.Begin(strings.TrimPrefix(req.Version, "v"), "")
		}
		run.stage = StepVerifying
		return err
	}
	log := in.Log.With("version", "v"+version)
	log.Info("update started", "url", req.DownloadURL)

	// Host-side anti-rollback policy: decided before anything is downloaded.
	current, cerr := in.Layout.CurrentVersion()
	if run.p != nil {
		run.p.Begin(version, current)
	}
	if cerr != nil {
		run.stage = StepVerifying
		return fmt.Errorf("determine current version: %w", cerr)
	}
	if err := in.checkVersionPolicy(version, current); err != nil {
		run.stage = StepVerifying
		run.skip(StepDownloading)
		return err
	}

	if ok, err := in.Services.Exists(ctx, in.Cfg.Supervisor.ServiceName); err != nil {
		run.stage = StepVerifying
		run.skip(StepDownloading)
		return fmt.Errorf("check supervisor service %q: %w", in.Cfg.Supervisor.ServiceName, err)
	} else if !ok {
		run.stage = StepVerifying
		run.skip(StepDownloading)
		return fmt.Errorf("supervisor service %q does not exist on this host", in.Cfg.Supervisor.ServiceName)
	}

	// 1. Download to disk and hash.
	run.begin(StepDownloading)
	archive, digest, err := in.spool(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()
	run.done(StepDownloading)

	// 2. Hash, signature, extraction and preflight: the "verifying" step.
	run.begin(StepVerifying)
	if len(req.ContentHash) != 32 {
		return fmt.Errorf("package offer carries no valid SHA-256 content hash (%d bytes)", len(req.ContentHash))
	}
	if !digestEqual(digest, req.ContentHash) {
		return fmt.Errorf("SHA-256 mismatch: offered %x, downloaded %x", req.ContentHash, digest)
	}
	log.Info("sha256 verified")

	if err := in.Verifier.Verify(req.Signature, digest, version); err != nil {
		return fmt.Errorf("signature verification: %w", err)
	}
	log.Info("signature verified", "present", len(req.Signature) > 0)

	// 3. Extract into a staging directory.
	if err := os.MkdirAll(in.Layout.VersionsDir(), 0o755); err != nil {
		return fmt.Errorf("create versions dir: %w", err)
	}
	staging := filepath.Join(in.Layout.VersionsDir(), version+".tmp-"+randSuffix())
	if err := os.Mkdir(staging, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	res, err := extractBinary(archive, staging, in.Cfg.Collector.BinaryName, in.Cfg.Update.MaxBinaryBytes)
	if err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}
	log.Info("archive extracted", "bytes", res.Size)

	// 4. Preflight (before anything is changed).
	if err := in.preflight(ctx, filepath.Join(staging, in.Cfg.Collector.BinaryName), version); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	run.done(StepVerifying)

	// 5. Place the version directory.
	if current == version {
		// Already active: only accept if the binary on disk is what we just verified.
		onDisk, err := fileSHA256(in.Layout.BinaryPath(version))
		if err != nil || !digestEqual(onDisk, res.SHA256) {
			run.stage = StepInstalling
			return fmt.Errorf("version v%s is already active but its binary differs from the verified package; refusing to overwrite the running version", version)
		}
		if err := in.Layout.writeMarker(in.Layout.VersionDir(version), version, in.Layout.previousOf(version), digest, res.SHA256, in.now()); err != nil {
			log.Warn("write install marker failed", "err", err)
		}
		log.Info("version already active with identical binary; nothing to do")
		run.skip(StepInstalling, StepRestarting, StepHealthCheck)
		return nil
	}
	run.begin(StepInstalling)
	if err := in.Layout.writeMarker(staging, version, current, digest, res.SHA256, in.now()); err != nil {
		return fmt.Errorf("write install marker: %w", err)
	}
	final := in.Layout.VersionDir(version)
	if _, err := os.Lstat(final); err == nil {
		if err := os.RemoveAll(final); err != nil {
			return fmt.Errorf("remove stale version dir: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		return fmt.Errorf("activate staging dir: %w", err)
	}
	keepStaging = true

	// 6. From here on a half-finished state must never be left behind: do
	// not honor ctx cancellation (service shutdown) until the host is in a
	// consistent state again. The journal covers hard kills.
	crit := context.WithoutCancel(ctx)
	if rolledBack, err := in.swapAndVerify(crit, log, run, current, version); err != nil {
		if rolledBack {
			// The host runs the previous version again: the failed version has no value on disk.
			_ = os.RemoveAll(final)
		}
		return err
	}

	// 7. Success: clean up old versions.
	if err := in.Layout.Prune(in.Cfg.Collector.KeepVersions, version, current); err != nil {
		log.Warn("prune old versions", "err", err)
	}
	log.Info("update finished", "previous", current)
	return nil
}

// swapAndVerify stops the supervisor, activates the new version, starts the
// supervisor and waits for health; on any error it rolls back.
func (in *Installer) swapAndVerify(ctx context.Context, log *slog.Logger, run *installRun, from, to string) (rolledBack bool, err error) {
	svc := in.Cfg.Supervisor.ServiceName
	if err := writeJournal(in.Cfg.StateDir, journal{From: from, To: to, Phase: "swapping", StartedAt: in.now()}); err != nil {
		return true, fmt.Errorf("write update journal: %w", err)
	}

	log.Info("stopping supervisor service", "service", svc)
	if err := in.Services.Stop(ctx, svc); err != nil {
		// Nothing was changed. Make sure the service runs again.
		if serr := in.Services.Start(ctx, svc); serr != nil {
			log.Warn("restart after failed stop failed", "err", serr)
		}
		_ = clearJournal(in.Cfg.StateDir)
		return true, fmt.Errorf("update to v%s aborted: stop supervisor service: %w (nothing was changed)", to, err)
	}

	cause := in.activateStartVerify(ctx, log, run, from, to)
	if cause == nil {
		_ = clearJournal(in.Cfg.StateDir)
		return false, nil
	}
	log.Error("new version failed; rolling back", "err", cause, "rollbackTo", from)
	run.fail(run.stage, cause) // the step that broke (installing, restarting or health_check)
	return in.rollback(ctx, log, run, from, to, cause)
}

func (in *Installer) activateStartVerify(ctx context.Context, log *slog.Logger, run *installRun, from, version string) error {
	svc := in.Cfg.Supervisor.ServiceName
	if err := in.Layout.Activate(version); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	run.done(StepInstalling)
	run.begin(StepRestarting)
	log.Info("starting supervisor service", "service", svc)
	if err := in.Services.Start(ctx, svc); err != nil {
		return fmt.Errorf("start supervisor service: %w", err)
	}
	run.done(StepRestarting)
	run.begin(StepHealthCheck)
	_ = writeJournal(in.Cfg.StateDir, journal{From: from, To: version, Phase: "verifying", StartedAt: in.now()})
	log.Info("waiting for health", "checks", in.Health.describe(), "timeout", in.Health.Timeout)
	if err := in.Health.WaitHealthy(ctx); err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	run.done(StepHealthCheck)
	return nil
}

// rollback reactivates the previous version, restarts the service and checks
// health. It returns the error to report for the failed update.
func (in *Installer) rollback(ctx context.Context, log *slog.Logger, run *installRun, from, to string, cause error) (bool, error) {
	if from == "" {
		_ = clearJournal(in.Cfg.StateDir)
		run.outcome, run.healthy = OutcomeFailed, false
		return false, fmt.Errorf("update to v%s failed: %w; no previous version to roll back to (rollback also failed)", to, cause)
	}
	svc := in.Cfg.Supervisor.ServiceName
	_ = writeJournal(in.Cfg.StateDir, journal{From: from, To: to, Phase: "rollingBack", StartedAt: in.now()})
	run.begin(StepRollingBack)
	var rbErr error
	if err := in.Services.Stop(ctx, svc); err != nil {
		// Continue: with the symlink strategy the swap works on a running
		// service, and on Windows Activate fails loudly if the file is locked.
		log.Warn("stop before rollback failed", "err", err)
	}
	if err := in.Layout.Activate(from); err != nil {
		rbErr = fmt.Errorf("reactivate v%s: %w", from, err)
	} else if err := in.Services.Start(ctx, svc); err != nil {
		rbErr = fmt.Errorf("start supervisor service: %w", err)
	} else if err := in.Health.WaitHealthy(ctx); err != nil {
		rbErr = fmt.Errorf("health check: %w", err)
	}
	if rbErr != nil {
		// Keep the journal: the next start of the updater tries again.
		log.Error("rollback failed", "err", rbErr)
		run.fail(StepRollingBack, rbErr)
		run.outcome, run.healthy = OutcomeFailed, false
		return false, fmt.Errorf("update to v%s failed: %w; rollback to v%s also failed: %v", to, cause, from, rbErr)
	}
	_ = clearJournal(in.Cfg.StateDir)
	run.done(StepRollingBack)
	run.outcome = OutcomeRolledBack
	log.Info("rolled back", "to", "v"+from)
	return true, fmt.Errorf("update to v%s failed: %w; rolled back to v%s", to, cause, from)
}

// spool writes the download to disk (bounded) and returns its path and SHA-256.
func (in *Installer) spool(ctx context.Context, req InstallRequest) (path string, digest []byte, err error) {
	if err := os.MkdirAll(in.downloadsDir(), 0o750); err != nil {
		return "", nil, fmt.Errorf("create downloads dir: %w", err)
	}
	f, err := os.CreateTemp(in.downloadsDir(), "dl-*.part")
	if err != nil {
		return "", nil, fmt.Errorf("create download file: %w", err)
	}
	tmpName := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	h := newSHA256()
	limit := in.Cfg.Update.MaxDownloadBytes
	n, cerr := io.Copy(io.MultiWriter(f, h), io.LimitReader(&ctxReader{ctx: ctx, r: req.Data}, limit+1))
	if cerr != nil {
		_ = f.Close()
		return "", nil, fmt.Errorf("download: %w", cerr)
	}
	if n > limit {
		_ = f.Close()
		return "", nil, fmt.Errorf("download exceeds the size limit of %d bytes", limit)
	}
	if n == 0 {
		_ = f.Close()
		return "", nil, errors.New("download is empty")
	}
	if err := f.Close(); err != nil {
		return "", nil, fmt.Errorf("write download: %w", err)
	}
	return tmpName, h.Sum(nil), nil
}

// ctxReader fails reads once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

var versionLineRE = func(version string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)\bversion\s+v?` + regexp.QuoteMeta(version) + `(\s|$)`)
}

// preflight checks the new binary before the swap: it must run, report the
// expected version and (optionally) accept the supervisor's current
// effective config.
func (in *Installer) preflight(ctx context.Context, binary, version string) error {
	exe := in.Exec
	if exe == nil {
		exe = osExecutor
	}
	timeout := in.Cfg.Update.PreflightTimeout.D()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	vctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exe(vctx, nil, binary, "--version")
	if err != nil {
		return fmt.Errorf("new binary does not run: %w: %s", err, trimOutput(out))
	}
	if !versionLineRE(version).Match(out) {
		return fmt.Errorf("new binary reports %q, expected version %s", trimOutput(out), version)
	}
	if !in.Cfg.Update.PreflightValidate {
		return nil
	}
	cfgPath := in.Cfg.EffectiveConfigPath()
	if _, err := os.Stat(cfgPath); err != nil {
		in.Log.Info("preflight validate skipped: no effective config yet", "path", cfgPath)
		return nil
	}
	val, err := in.prepareValidation(ctx, cfgPath)
	if err != nil {
		return err
	}
	defer val.cleanup()
	if val.skip != "" {
		in.Log.Warn("preflight validate skipped", "reason", val.skip)
		return nil
	}
	env, envErr := in.Services.Environment(ctx, in.Cfg.Supervisor.ServiceName)
	vctx2, cancel2 := context.WithTimeout(ctx, timeout)
	defer cancel2()
	out, err = val.exe(vctx2, env, binary, "validate", "--config", val.path)
	if err != nil {
		// The output may contain resolved config values (secrets): it is
		// kept in the local debug log and never forwarded to the server.
		in.Log.Debug("preflight validate output", "output", trimOutput(out))
		if envErr != nil {
			// We could not reproduce the service environment, so ${env:...}
			// references may fail for reasons unrelated to the new binary.
			in.Log.Warn("preflight validate failed but the service environment could not be determined; continuing", "err", err, "envErr", envErr)
			return nil
		}
		return fmt.Errorf("new binary rejects the current effective config (%w); details are in the updater's debug log", err)
	}
	return nil
}

// validation is the prepared `validate` run.
type validation struct {
	exe     Executor
	path    string
	cleanup func()
	skip    string // non-empty: skip the validation for this reason
}

// nonEnvProviderRE finds confmap provider references other than ${env:...}.
var nonEnvProviderRE = regexp.MustCompile(`\$\{\s*([A-Za-z][A-Za-z0-9+.-]*):`)

// prepareValidation decides how `validate` runs. The effective config is
// written by the supervisor's service account, so it is untrusted for the
// updater's (root/SYSTEM) privileges: the collector resolves confmap
// providers (file, http, ...) while validating. Therefore
//   - as root the check runs under the supervisor's own account;
//   - where privileges cannot be dropped (Windows) a bounded copy is checked
//     and only configs without non-env providers are validated;
//   - otherwise (the updater runs unprivileged) the file is used in place.
func (in *Installer) prepareValidation(ctx context.Context, cfgPath string) (validation, error) {
	noop := func() {}
	if in.Exec != nil && in.privilegedHost == nil {
		return validation{exe: in.Exec, path: cfgPath, cleanup: noop}, nil
	}
	privileged := runtime.GOOS == "windows"
	if in.privilegedHost != nil {
		privileged = in.privilegedHost()
	}
	exe := in.Exec
	if exe == nil {
		exe = osExecutor
	}
	if privileged {
		return in.privateCopyValidation(exe, cfgPath)
	}
	if in.Exec == nil && canDropPrivileges() {
		user := ""
		if r, ok := in.Services.(ServiceUserResolver); ok {
			u, err := r.ServiceUser(ctx, in.Cfg.Supervisor.ServiceName)
			if err != nil {
				return validation{cleanup: noop, skip: fmt.Sprintf("cannot determine the supervisor's service account: %v", err)}, nil
			}
			user = u
		} else {
			return validation{cleanup: noop, skip: "the service controller cannot report the supervisor's account"}, nil
		}
		if user != "" && user != "root" && user != "0" {
			as, _, _, err := osExecutorAs(user)
			if err != nil {
				return validation{cleanup: noop, skip: err.Error()}, nil
			}
			return validation{exe: as, path: cfgPath, cleanup: noop}, nil
		}
	}
	return validation{exe: exe, path: cfgPath, cleanup: noop}, nil
}

func (in *Installer) privateCopyValidation(exe Executor, cfgPath string) (validation, error) {
	noop := func() {}
	data, err := readRegularFile(cfgPath, maxSupervisorFileBytes)
	if err != nil {
		return validation{cleanup: noop, skip: fmt.Sprintf("effective config is not a plain readable file: %v", err)}, nil
	}
	for _, m := range nonEnvProviderRE.FindAllSubmatch(data, -1) {
		if string(m[1]) != "env" {
			return validation{cleanup: noop, skip: "effective config uses config providers other than env; they are not resolved with the updater's privileges"}, nil
		}
	}
	dir := in.downloadsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return validation{}, fmt.Errorf("create preflight dir: %w", err)
	}
	tmp := filepath.Join(dir, "preflight-"+randSuffix()+".yaml")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return validation{}, fmt.Errorf("write preflight copy: %w", err)
	}
	return validation{exe: exe, path: tmp, cleanup: func() { _ = os.Remove(tmp) }}, nil
}

// checkVersionPolicy enforces the host-side anti-rollback policy: the
// offered version must not be below update.minimumVersion, and (unless
// update.allowDowngrade is set) not older than the active version - except
// the one-step rollback to the version that was active before it.
func (in *Installer) checkVersionPolicy(version, current string) error {
	nv, err := semver.StrictNewVersion(version)
	if err != nil {
		return fmt.Errorf("invalid version %q: %w", version, err)
	}
	if min := in.Cfg.Update.MinimumVersion; min != "" {
		mv, err := semver.StrictNewVersion(strings.TrimPrefix(min, "v"))
		if err != nil {
			return fmt.Errorf("update.minimumVersion %q is invalid: %w", min, err)
		}
		if nv.LessThan(mv) {
			return fmt.Errorf("version v%s is below the host policy minimum v%s (update.minimumVersion)", version, mv.String())
		}
	}
	if current == "" || in.Cfg.Update.AllowDowngrade {
		return nil
	}
	cv, err := semver.StrictNewVersion(current)
	if err != nil || !nv.LessThan(cv) {
		return nil
	}
	if in.Layout.previousOf(current) == version {
		return nil
	}
	return fmt.Errorf("version v%s is older than the active v%s; downgrades are disabled on this host (update.allowDowngrade)", version, current)
}

func trimOutput(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = s[:2000] + "..."
	}
	return s
}

// Recover repairs the host after the updater (or the machine) died in the
// middle of an update. It is called once at startup.
func (in *Installer) Recover(ctx context.Context) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	j, err := readJournal(in.Cfg.StateDir)
	if err != nil {
		in.Log.Warn("discarding unreadable update journal", "err", err)
		_ = clearJournal(in.Cfg.StateDir)
		return nil
	}
	if j == nil {
		return nil
	}
	log := in.Log.With("journalPhase", j.Phase, "from", j.From, "to", j.To)
	log.Warn("found unfinished update from a previous run; recovering")
	svc := in.Cfg.Supervisor.ServiceName
	cur, err := in.Layout.CurrentVersion()
	if err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	// A crash between the renames of the "copy" activation can leave no (or
	// the wrong) binary behind while current-version still names a version:
	// trust the bytes on disk, not the version file.
	if mismatch, _ := in.Layout.ActiveBinaryMismatch(); mismatch && cur != "" {
		log.Warn("recovery: active collector binary is missing or differs from the installed version; re-activating", "version", "v"+cur)
		_ = in.Services.Stop(ctx, svc)
		if err := in.Layout.Activate(cur); err != nil {
			return fmt.Errorf("recovery: re-activate v%s: %w", cur, err)
		}
	}
	// healthy makes sure the service runs and passes the health check.
	healthy := func() bool {
		if st, err := in.Services.State(ctx, svc); err != nil || st != ServiceRunning {
			if err := in.Services.Start(ctx, svc); err != nil {
				log.Error("recovery: start supervisor service", "err", err)
				return false
			}
		}
		return in.Health.WaitHealthy(ctx) == nil
	}

	switch {
	case j.Phase != "rollingBack" && cur == j.To:
		// The new version was activated before we died.
		if healthy() {
			log.Info("recovery: new version is active and healthy; accepting it")
			_ = clearJournal(in.Cfg.StateDir)
			return nil
		}
		if err := ctx.Err(); err != nil {
			// A cancelled or timed-out recovery says nothing about the health
			// of the new version: keep the journal, the next start decides.
			return fmt.Errorf("recovery interrupted: %w", err)
		}
		log.Error("recovery: new version is active but unhealthy; rolling back")
		return in.rollbackRecovery(ctx, log, j.From)
	case cur == j.From || j.From == "":
		// Old version still (or again) active: only make sure it runs.
		if healthy() {
			_ = clearJournal(in.Cfg.StateDir)
			log.Info("recovery: previous version is active and healthy")
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("recovery interrupted: %w", err)
		}
		// Keep the journal: the next start of the updater tries again.
		return fmt.Errorf("recovery: supervisor service is unhealthy on v%s", cur)
	default:
		return in.rollbackRecovery(ctx, log, j.From)
	}
}

func (in *Installer) rollbackRecovery(ctx context.Context, log *slog.Logger, from string) error {
	if from == "" {
		_ = clearJournal(in.Cfg.StateDir)
		return errors.New("recovery: no previous version to roll back to")
	}
	svc := in.Cfg.Supervisor.ServiceName
	_ = in.Services.Stop(ctx, svc)
	if err := in.Layout.Activate(from); err != nil {
		return fmt.Errorf("recovery rollback: reactivate v%s: %w", from, err)
	}
	if err := in.Services.Start(ctx, svc); err != nil {
		return fmt.Errorf("recovery rollback: start service: %w", err)
	}
	if err := in.Health.WaitHealthy(ctx); err != nil {
		return fmt.Errorf("recovery rollback: %w", err)
	}
	_ = clearJournal(in.Cfg.StateDir)
	log.Info("recovery: rolled back", "to", "v"+from)
	return nil
}
