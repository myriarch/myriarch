// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// ContainerOptions configure RunContainer.
type ContainerOptions struct {
	Config Config
	Log    *slog.Logger
	// Deps are optional overrides for the updater's collaborators (tests).
	// Services, Exec, InstallMethod, ImageCollectorVersion and
	// DisableRecovery are set by RunContainer.
	Deps Deps
	// Stdout and Stderr receive the supervisor's output (default: ours).
	Stdout, Stderr *os.File
	// ReapAll overrides the reaper mode (tests). Nil = detect (Linux PID 1
	// or subreaper).
	ReapAll *bool
	// OnListening is called with the address of the probe server.
	OnListening func(addr string)
	// ForwardSignals forwards SIGHUP/SIGUSR1/SIGUSR2 to the child.
	ForwardSignals bool
}

// RunContainer is "myriarch-updater container": it seeds the collector binary
// from the image, starts the supervisor as a child process and runs the
// updater until ctx is cancelled (SIGTERM). It returns an error when the
// child crashes too often (exit 1: the kubelet restarts the container).
func RunContainer(ctx context.Context, opt ContainerOptions) error {
	cfg, log := opt.Config, opt.Log
	if log == nil {
		log = slog.Default()
	}
	if err := cfg.ValidateContainer(); err != nil {
		return err
	}
	ct := cfg.Container

	// 1. Process model: one place collects every exit status.
	reapAll := becomeReaper()
	if opt.ReapAll != nil {
		reapAll = *opt.ReapAll
	}
	rp := newReaper(reapAll)
	reapCtx, stopReaper := context.WithCancel(context.Background())
	reapDone := make(chan struct{})
	go func() { defer close(reapDone); rp.Run(reapCtx) }()
	defer func() { stopReaper(); <-reapDone }()
	log.Info("container mode starting", "pid", os.Getpid(), "reapAll", reapAll, "seedPolicy", ct.SeedPolicy,
		"installDir", cfg.Collector.InstallDir, "stateDir", cfg.StateDir)

	exe := rp.Executor()
	child := NewChildController(ChildOptions{
		Command: ct.SupervisorCommand, StopTimeout: ct.StopTimeout.D(), RestartDelay: ct.RestartDelay.D(),
		MaxRestarts: ct.MaxRestarts, RestartWindow: ct.RestartWindow.D(),
		Stdout: opt.Stdout, Stderr: opt.Stderr, Log: log, Reaper: rp,
	})

	// 2. Probe endpoints first: the startupProbe must see the container while
	// seeding and a long recovery run. /healthz is fed by a startup ticker
	// until the main loop of the updater takes over (Deps.Heartbeat).
	var busy atomic.Pointer[func() bool]
	var mainLoop atomic.Bool
	status := &containerStatus{
		childRunning: func() bool {
			st, _ := child.State(context.Background(), "")
			return st == ServiceRunning
		},
		updateBusy: func() bool {
			if f := busy.Load(); f != nil {
				return (*f)()
			}
			return true // seeding or recovering
		},
		now: func() time.Time { return time.Now() },
	}
	status.heartbeat()
	bootCtx, stopBoot := context.WithCancel(context.Background())
	defer stopBoot()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-bootCtx.Done():
				return
			case <-t.C:
				if !mainLoop.Load() {
					status.heartbeat()
				}
			}
		}
	}()
	if ct.HealthListen != "" {
		addr, stop, err := serveHealth(ct.HealthListen, status.handler())
		if err != nil {
			return err
		}
		defer stop()
		log.Info("probe endpoints listening", "addr", addr)
		if opt.OnListening != nil {
			opt.OnListening(addr)
		}
	}

	// 3. Seeding and precedence between the image and the volume. An update
	// journal means that recovery has to decide first.
	layout := &Layout{InstallDir: cfg.Collector.InstallDir, BinaryName: cfg.Collector.BinaryName, Activation: cfg.Collector.Activation}
	now := opt.Deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	j, jerr := readJournal(cfg.StateDir)
	journalPending := j != nil || jerr != nil // an unreadable journal is discarded by Recover
	seed, err := seedFromImage(ctx, log, cfg, layout, exe, now, journalPending)
	if err != nil {
		return fmt.Errorf("seed collector binary: %w", err)
	}

	// 4. The updater itself.
	deps := opt.Deps
	deps.Services = child
	deps.Exec = exe
	deps.InstallMethod = InstallMethodContainer
	deps.ImageCollectorVersion = seed.ImageVersion
	deps.DisableRecovery = true
	userBeat := deps.Heartbeat
	deps.Heartbeat = func() {
		mainLoop.Store(true)
		status.heartbeat()
		if userBeat != nil {
			userBeat()
		}
	}
	u, err := New(cfg, log, deps)
	if err != nil {
		return err
	}
	ub := u.updateBusy
	busy.Store(&ub)

	// 5. Repair an update that was cut off by the last container exit, then
	// make sure the child runs. Like the service mode this ignores SIGTERM
	// (a half-finished recovery would leave the volume in an undefined state)
	// but is bounded by the worst case of stop + two health waits.
	if journalPending {
		bound := ct.StopTimeout.D()*2 + cfg.Update.HealthTimeout.D()*2 + time.Minute
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), bound)
		err := u.Recover(rctx)
		rcancel()
		if err != nil {
			log.Error("recovery of interrupted update failed", "err", err)
		}
		// The deferred seed decision.
		if again, err := seedFromImage(ctx, log, cfg, layout, exe, now, false); err != nil {
			return fmt.Errorf("seed collector binary: %w", err)
		} else if again.Decision == SeedActivateImage {
			// A running child still executes the old binary.
			if err := child.Stop(context.WithoutCancel(ctx), cfg.Supervisor.ServiceName); err != nil {
				log.Warn("stopping the supervisor child for the seeded version", "err", err)
			}
		}
	}
	if err := child.Start(ctx, cfg.Supervisor.ServiceName); err != nil {
		return err
	}

	// 6. Run until SIGTERM/SIGINT (ctx) or until the child gave up.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-child.Fatal():
				cancel()
				return
			}
		}
	}()
	if opt.ForwardSignals {
		fwd := make(chan os.Signal, 4)
		signal.Notify(fwd, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
		defer signal.Stop(fwd)
		go func() {
			for {
				select {
				case <-runCtx.Done():
					return
				case s := <-fwd:
					if sig, ok := s.(syscall.Signal); ok {
						log.Info("forwarding signal to the supervisor child", "signal", sig.String())
						child.Signal(sig)
					}
				}
			}
		}()
	}
	runErr := u.Run(runCtx)

	// 7. Shutdown: the install critical section has finished (Run waited);
	// stop the child group, SIGKILL after stopTimeout.
	log.Info("container mode stopping")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), ct.StopTimeout.D()+15*time.Second)
	defer stopCancel()
	if err := child.Shutdown(stopCtx); err != nil {
		log.Warn("stopping the supervisor child", "err", err)
	}
	if err := child.Err(); err != nil {
		return err
	}
	return runErr
}

// runContainerCommand loads the configuration and runs container mode with
// signal handling (the cobra command).
func runContainerCommand(cfgPath string) error {
	abs, err := absPath(cfgPath)
	if err != nil {
		return err
	}
	cfg, err := LoadConfig(abs)
	if err != nil {
		return err
	}
	if err := cfg.ValidateContainer(); err != nil {
		return err
	}
	log, closer, err := newLogger(cfg.Log, os.Stderr)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = RunContainer(ctx, ContainerOptions{Config: cfg, Log: log, ForwardSignals: true})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("container mode terminated with error", "err", err)
		return err
	}
	return nil
}
