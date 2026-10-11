// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// canDropPrivileges reports whether this process is root and can therefore
// run child processes under another account.
func canDropPrivileges() bool { return os.Geteuid() == 0 }

// osExecutorAs returns an Executor that runs the program as the given
// account (name or numeric uid) with that account's primary group and no
// supplementary groups. Used for the config preflight so that the new
// collector binary resolves the supervisor's config with the supervisor's
// own rights, not with root's.
func osExecutorAs(account string) (Executor, error) {
	u, err := user.Lookup(account)
	if err != nil {
		u, err = user.LookupId(account)
	}
	if err != nil {
		return nil, fmt.Errorf("look up service account %q: %w", account, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("service account %q: invalid uid %q", account, u.Uid)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("service account %q: invalid gid %q", account, u.Gid)
	}
	exe := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		var out cappedBuffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		cmd.Dir = "/"
		// Clean environment: nothing of the updater's own (root) environment is handed down.
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + u.HomeDir, "USER=" + u.Username}, env...)
		// The child gets its own process group so that the timeout ends it
		// together with everything it spawned. Signalling a process of another
		// account needs CAP_KILL (part of the unit's capability set); WaitDelay
		// additionally bounds Wait if a descendant keeps the output pipes open.
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), NoSetGroups: false},
			Setpgid:    true,
		}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				return cmd.Process.Kill()
			}
			return nil
		}
		cmd.WaitDelay = 5 * time.Second
		if err := cmd.Run(); err != nil {
			return out.buf.Bytes(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return out.buf.Bytes(), nil
	}
	return exe, nil
}
