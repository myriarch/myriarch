// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// exitStatus is how a process ended.
type exitStatus struct {
	Code   int
	Signal syscall.Signal // 0 = exited normally
	// Lost is set when the exit status could not be collected (ECHILD): the
	// process is gone but somebody else reaped it.
	Lost bool
}

func (e exitStatus) String() string {
	switch {
	case e.Lost:
		return "exited (status unknown)"
	case e.Signal != 0:
		return fmt.Sprintf("killed by signal %s", e.Signal)
	default:
		return fmt.Sprintf("exited with code %d", e.Code)
	}
}

func exitStatusOf(ws syscall.WaitStatus) exitStatus {
	if ws.Signaled() {
		return exitStatus{Signal: ws.Signal(), Code: -1}
	}
	return exitStatus{Code: ws.ExitStatus()}
}

// maxOrphanExits bounds the memory of exits that were collected before
// anyone watched the pid.
const maxOrphanExits = 256

// reaper is the single place that collects the exit status of child
// processes of this process.
//
// A container's PID 1 inherits every orphaned process and must reap it,
// otherwise zombies pile up. A second Wait elsewhere (os/exec's cmd.Wait)
// would race with a wait4(-1) loop and fail with ECHILD, so in container mode
// every process is started through the reaper (start, executor) and nobody
// else waits. With reapAll the reaper collects all children and orphans
// (Linux PID 1 or subreaper); otherwise it only waits for the pids it was
// asked to watch (macOS, tests).
type reaper struct {
	reapAll bool
	// wait4 is syscall.Wait4 (replaced in tests).
	wait4 func(pid int, ws *syscall.WaitStatus, options int) (int, error)

	mu      sync.Mutex
	watch   map[int]chan exitStatus
	orphans map[int]exitStatus // exits seen before watch() was called
	orphanQ []int
	// reaped counts processes nobody watched (zombies adopted by PID 1).
	reaped int
}

func newReaper(reapAll bool) *reaper {
	return &reaper{
		reapAll: reapAll,
		wait4: func(pid int, ws *syscall.WaitStatus, options int) (int, error) {
			return syscall.Wait4(pid, ws, options, nil)
		},
		watch:   map[int]chan exitStatus{},
		orphans: map[int]exitStatus{},
	}
}

// Watch returns a channel that receives the exit status of pid exactly once.
func (r *reaper) Watch(pid int) <-chan exitStatus {
	ch := make(chan exitStatus, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.orphans[pid]; ok { // exited between start and Watch
		delete(r.orphans, pid)
		ch <- st
		return ch
	}
	r.watch[pid] = ch
	return ch
}

func (r *reaper) deliver(pid int, st exitStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.watch[pid]; ok {
		delete(r.watch, pid)
		ch <- st
		return
	}
	r.reaped++
	if len(r.orphanQ) >= maxOrphanExits {
		delete(r.orphans, r.orphanQ[0])
		r.orphanQ = r.orphanQ[1:]
	}
	r.orphans[pid] = st
	r.orphanQ = append(r.orphanQ, pid)
}

func (r *reaper) watchedPids() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	pids := make([]int, 0, len(r.watch))
	for pid := range r.watch {
		pids = append(pids, pid)
	}
	return pids
}

// Reaped returns how many unwatched (orphaned) processes were collected.
func (r *reaper) Reaped() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reaped
}

// reapOnce collects every process that has exited right now.
func (r *reaper) reapOnce() {
	if r.reapAll {
		for {
			var ws syscall.WaitStatus
			pid, err := r.wait4(-1, &ws, syscall.WNOHANG)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil || pid <= 0 {
				return // ECHILD (no children) or nothing exited
			}
			r.deliver(pid, exitStatusOf(ws))
		}
	}
	for _, pid := range r.watchedPids() {
		var ws syscall.WaitStatus
		got, err := r.wait4(pid, &ws, syscall.WNOHANG)
		switch {
		case errors.Is(err, syscall.EINTR):
		case errors.Is(err, syscall.ECHILD):
			r.deliver(pid, exitStatus{Lost: true})
		case err == nil && got == pid:
			r.deliver(pid, exitStatusOf(ws))
		}
	}
}

// Run reaps on SIGCHLD (and every second as a safety net against coalesced
// signals) until ctx is done.
func (r *reaper) Run(ctx context.Context) {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGCHLD)
	defer signal.Stop(sigs)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	r.reapOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigs:
		case <-tick.C:
		}
		r.reapOnce()
	}
}

// spawnSpec describes a process to start.
type spawnSpec struct {
	Path   string
	Args   []string // argv[1:]
	Env    []string // nil = inherit
	Dir    string
	Stdin  *os.File // nil = /dev/null
	Stdout *os.File
	Stderr *os.File
}

// Spawn starts the process in its own process group and returns its pid
// and the exit channel. Nobody may call Wait on it: the reaper collects it.
func (r *reaper) Spawn(sp spawnSpec) (int, <-chan exitStatus, error) {
	cmd := exec.Command(sp.Path, sp.Args...)
	cmd.Env = sp.Env
	cmd.Dir = sp.Dir
	// A nil *os.File must not be stored in the io interfaces (a non-nil interface with a nil file would close the descriptor in
	// the child instead of connecting /dev/null).
	if sp.Stdin != nil {
		cmd.Stdin = sp.Stdin
	}
	if sp.Stdout != nil {
		cmd.Stdout = sp.Stdout
	}
	if sp.Stderr != nil {
		cmd.Stderr = sp.Stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A reapAll loop may collect the pid before Watch registers it: deliver
	// parks such exits in r.orphans and Watch picks them up.
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release() // never Wait: the reaper owns the exit status
	return pid, r.Watch(pid), nil
}

// killGroup sends sig to the process group of pid (the child is its leader).
func killGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// Executor returns an Executor whose children are collected by the reaper
// (used for the preflight in container mode instead of os/exec's Wait).
// env is merged over the current environment like osExecutor does.
func (r *reaper) Executor() Executor {
	return func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		var childEnv []string
		if len(env) > 0 {
			childEnv = mergeEnv(env)
		}
		pid, exit, err := r.Spawn(spawnSpec{Path: name, Args: args, Env: childEnv, Stdout: pw, Stderr: pw})
		_ = pw.Close() // the child holds its own copy
		if err != nil {
			_ = pr.Close()
			return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		var out cappedBuffer
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			_, _ = io.Copy(&out, pr)
		}()
		var st exitStatus
		select {
		case st = <-exit:
		case <-ctx.Done():
			_ = killGroup(pid, syscall.SIGKILL)
			st = <-exit
		}
		// A descendant may keep the pipe open: do not wait for it forever.
		select {
		case <-copied:
		case <-time.After(2 * time.Second):
			_ = pr.Close()
			<-copied
		}
		_ = pr.Close()
		_ = killGroup(pid, syscall.SIGKILL) // stragglers of the group
		if ctx.Err() != nil {
			return out.buf.Bytes(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), ctx.Err())
		}
		if st.Lost || st.Signal != 0 || st.Code != 0 {
			return out.buf.Bytes(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), st)
		}
		return out.buf.Bytes(), nil
	}
}
