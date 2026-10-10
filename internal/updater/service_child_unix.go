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
	"sync"
	"syscall"
	"time"
)

// ChildController is the ServiceController of container mode: the "service"
// is the supervisor started as a child process of myriarch-updater (PID 1 of the
// container) in its own process group. Stop/Start are the operations the
// Installer needs for the binary swap; an unexpected end of the child is
// answered with a restart with backoff.
//
// The controller never calls Wait: the reaper collects the exit statuses.
type ChildController struct {
	ChildOptions

	mu       sync.Mutex
	wanted   bool
	proc     *childProc
	timer    *time.Timer
	crashes  int         // consecutive unexpected exits
	restarts []time.Time // unexpected restarts inside the window
	fatalErr error
	fatal    chan struct{}
	closed   bool
}

// ChildOptions configure a ChildController (zero values select defaults).
type ChildOptions struct {
	Command       []string
	StopTimeout   time.Duration // SIGTERM -> SIGKILL (default 30s)
	RestartDelay  time.Duration // first restart delay, doubles up to 60s (default 2s)
	MaxRestarts   int           // unexpected restarts within RestartWindow before giving up (default 5)
	RestartWindow time.Duration // default 10m
	Stdout        *os.File      // default os.Stdout
	Stderr        *os.File      // default os.Stderr
	Log           *slog.Logger
	Reaper        *reaper

	// stableAfter: a child that ran this long resets the backoff (default 60s).
	stableAfter time.Duration
}

type childProc struct {
	pid     int
	started time.Time
	done    chan struct{} // closed after the exit was processed
	status  exitStatus
}

const maxRestartDelay = 60 * time.Second

var _ ServiceController = (*ChildController)(nil)

// NewChildController returns a controller with defaults filled in.
func NewChildController(c ChildOptions) *ChildController {
	if c.StopTimeout <= 0 {
		c.StopTimeout = 30 * time.Second
	}
	if c.RestartDelay <= 0 {
		c.RestartDelay = 2 * time.Second
	}
	if c.MaxRestarts <= 0 {
		c.MaxRestarts = 5
	}
	if c.RestartWindow <= 0 {
		c.RestartWindow = 10 * time.Minute
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.stableAfter <= 0 {
		c.stableAfter = 60 * time.Second
	}
	return &ChildController{ChildOptions: c, fatal: make(chan struct{})}
}

// Fatal is closed when the child crashed more often than allowed; Err then
// says why.
func (c *ChildController) Fatal() <-chan struct{} { return c.fatal }

// Err returns the reason Fatal was closed.
func (c *ChildController) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fatalErr
}

// Exists implements ServiceController.
func (c *ChildController) Exists(context.Context, string) (bool, error) { return true, nil }

// State implements ServiceController.
func (c *ChildController) State(context.Context, string) (ServiceState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.proc != nil && !c.wanted:
		return ServiceStopping, nil
	case c.proc != nil:
		return ServiceRunning, nil
	case c.wanted:
		return ServiceStarting, nil // waiting for the restart backoff
	default:
		return ServiceStopped, nil
	}
}

// Environment implements ServiceController: the child runs with the
// updater's own environment.
func (c *ChildController) Environment(context.Context, string) ([]string, error) {
	return os.Environ(), nil
}

// Pid returns the pid of the running child (0 if none).
func (c *ChildController) Pid() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == nil {
		return 0
	}
	return c.proc.pid
}

// Start implements ServiceController: the child runs from now on (and is
// restarted when it ends unexpectedly). It returns once the process exists.
func (c *ChildController) Start(_ context.Context, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("child controller is shut down")
	}
	c.wanted = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.proc != nil {
		return nil
	}
	return c.spawnLocked()
}

func (c *ChildController) spawnLocked() error {
	if len(c.Command) == 0 {
		return errors.New("no supervisor command configured")
	}
	pid, exit, err := c.Reaper.Spawn(spawnSpec{
		Path: c.Command[0], Args: c.Command[1:], Stdout: c.Stdout, Stderr: c.Stderr,
	})
	if err != nil {
		return fmt.Errorf("start %s: %w", c.Command[0], err)
	}
	p := &childProc{pid: pid, started: time.Now(), done: make(chan struct{})}
	c.proc = p
	c.Log.Info("supervisor child started", "pid", pid, "command", c.Command[0])
	go c.await(p, exit)
	return nil
}

// await processes the exit of one child process.
func (c *ChildController) await(p *childProc, exit <-chan exitStatus) {
	st := <-exit
	// Do not leave grandchildren of the group behind (SIGKILL is a no-op
	// when the group is empty).
	_ = killGroup(p.pid, syscall.SIGKILL)

	c.mu.Lock()
	defer c.mu.Unlock()
	p.status = st
	if c.proc == p {
		c.proc = nil
	}
	close(p.done)
	if !c.wanted || c.closed {
		c.Log.Info("supervisor child stopped", "pid", p.pid, "status", st.String())
		return
	}
	// Unexpected end.
	now := time.Now()
	if now.Sub(p.started) >= c.stableAfter {
		c.crashes = 0
	}
	c.crashes++
	cutoff := now.Add(-c.RestartWindow)
	kept := c.restarts[:0]
	for _, t := range c.restarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	c.restarts = append(kept, now)
	if len(c.restarts) > c.MaxRestarts {
		c.fatalErr = fmt.Errorf("supervisor child ended unexpectedly %d times within %s (last: %s)", len(c.restarts), c.RestartWindow, st)
		c.wanted = false
		c.Log.Error("giving up on the supervisor child", "err", c.fatalErr)
		select {
		case <-c.fatal:
		default:
			close(c.fatal)
		}
		return
	}
	delay := c.RestartDelay
	for i := 1; i < c.crashes && delay < maxRestartDelay; i++ {
		delay *= 2
	}
	if delay > maxRestartDelay {
		delay = maxRestartDelay
	}
	c.Log.Warn("supervisor child ended unexpectedly; restarting", "pid", p.pid, "status", st.String(), "restartIn", delay.String(), "restarts", len(c.restarts))
	c.timer = time.AfterFunc(delay, c.restart)
}

func (c *ChildController) restart() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = nil
	if !c.wanted || c.closed || c.proc != nil {
		return
	}
	if err := c.spawnLocked(); err != nil {
		// Counts like a crash: back off and eventually give up.
		c.Log.Error("restart of the supervisor child failed", "err", err)
		now := time.Now()
		c.restarts = append(c.restarts, now)
		c.crashes++
		if len(c.restarts) > c.MaxRestarts {
			c.fatalErr = fmt.Errorf("cannot restart the supervisor child: %w", err)
			c.wanted = false
			select {
			case <-c.fatal:
			default:
				close(c.fatal)
			}
			return
		}
		c.timer = time.AfterFunc(maxRestartDelay/4, c.restart)
	}
}

// Stop implements ServiceController: SIGTERM to the child's process group,
// SIGKILL after StopTimeout. It returns when the child is gone. No restart
// follows until Start is called.
func (c *ChildController) Stop(ctx context.Context, _ string) error {
	c.mu.Lock()
	c.wanted = false
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		return nil
	}
	c.Log.Info("stopping supervisor child", "pid", p.pid, "timeout", c.StopTimeout.String())
	if err := killGroup(p.pid, syscall.SIGTERM); err != nil {
		c.Log.Warn("SIGTERM to the supervisor child failed", "err", err)
	}
	t := time.NewTimer(c.StopTimeout)
	defer t.Stop()
	select {
	case <-p.done:
		return nil
	case <-t.C:
		c.Log.Warn("supervisor child did not stop in time; killing it", "pid", p.pid)
		_ = killGroup(p.pid, syscall.SIGKILL)
	case <-ctx.Done():
		_ = killGroup(p.pid, syscall.SIGKILL)
		return ctx.Err()
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("supervisor child (pid %d) did not end after SIGKILL", p.pid)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Signal forwards a signal to the child (not its group).
func (c *ChildController) Signal(sig syscall.Signal) {
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p != nil {
		_ = syscall.Kill(p.pid, sig)
	}
}

// Shutdown stops the child for good (container exit).
func (c *ChildController) Shutdown(ctx context.Context) error {
	err := c.Stop(ctx, "")
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return err
}
