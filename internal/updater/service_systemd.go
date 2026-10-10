// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CommandRunner runs an external command and returns its stdout. The error
// of a non-zero exit is an *exec.ExitError; stderr is included in the error
// text by the default implementation.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execRunner is the default CommandRunner.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// SystemdController controls systemd units through systemctl. It compiles on
// all platforms (the Linux code path is unit-tested with a fake runner on any
// OS); on hosts without systemd every call fails with a clear error.
type SystemdController struct {
	// Run executes commands (default: os/exec).
	Run CommandRunner
	// Timeout bounds one systemctl stop/start invocation (default 60s).
	Timeout time.Duration
	// ReadFile reads EnvironmentFile contents (default os.ReadFile).
	ReadFile func(string) ([]byte, error)
}

func (s *SystemdController) run(ctx context.Context, args ...string) ([]byte, error) {
	r := s.Run
	if r == nil {
		r = execRunner
	}
	return r(ctx, "systemctl", args...)
}

func (s *SystemdController) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 60 * time.Second
}

func unitName(name string) string {
	if strings.ContainsRune(name, '.') {
		return name
	}
	return name + ".service"
}

// Exists implements ServiceController.
func (s *SystemdController) Exists(ctx context.Context, name string) (bool, error) {
	out, err := s.run(ctx, "show", "--property=LoadState", "--value", unitName(name))
	if err != nil {
		return false, fmt.Errorf("systemctl show %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)) == "loaded", nil
}

// State implements ServiceController.
func (s *SystemdController) State(ctx context.Context, name string) (ServiceState, error) {
	// is-active exits non-zero for everything but "active"; stdout still has the state.
	out, err := s.run(ctx, "is-active", unitName(name))
	state := strings.TrimSpace(string(out))
	if state == "" && err != nil {
		return ServiceUnknown, fmt.Errorf("systemctl is-active %s: %w", name, err)
	}
	switch state {
	case "active", "reloading":
		return ServiceRunning, nil
	case "activating":
		return ServiceStarting, nil
	case "deactivating":
		return ServiceStopping, nil
	case "inactive", "failed":
		return ServiceStopped, nil
	default:
		return ServiceUnknown, nil
	}
}

// Stop implements ServiceController. "systemctl stop" blocks until the unit
// is stopped.
func (s *SystemdController) Stop(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	if _, err := s.run(ctx, "stop", unitName(name)); err != nil {
		return fmt.Errorf("systemctl stop %s: %w", name, err)
	}
	return nil
}

// Start implements ServiceController. A unit that fails to start makes
// "systemctl start" exit non-zero; "activating" units are awaited briefly.
func (s *SystemdController) Start(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	if _, err := s.run(ctx, "start", unitName(name)); err != nil {
		return fmt.Errorf("systemctl start %s: %w", name, err)
	}
	return nil
}

// ServiceUser returns the account the unit runs as (User=); "" means root.
// It implements ServiceUserResolver.
func (s *SystemdController) ServiceUser(ctx context.Context, name string) (string, error) {
	out, err := s.run(ctx, "show", "--property=User", "--value", unitName(name))
	if err != nil {
		return "", fmt.Errorf("systemctl show %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

var envFileRE = regexp.MustCompile(`(\S+) \(ignore_errors=(yes|no)\)`)

// Environment implements ServiceController: Environment= plus
// EnvironmentFile= entries of the unit.
func (s *SystemdController) Environment(ctx context.Context, name string) ([]string, error) {
	out, err := s.run(ctx, "show", "--property=Environment", "--property=EnvironmentFiles", unitName(name))
	if err != nil {
		return nil, fmt.Errorf("systemctl show %s: %w", name, err)
	}
	readFile := s.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	var env []string
	var files []struct {
		path   string
		ignore bool
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Environment="):
			env = append(env, splitSystemdWords(strings.TrimPrefix(line, "Environment="))...)
		case strings.HasPrefix(line, "EnvironmentFiles="):
			for _, m := range envFileRE.FindAllStringSubmatch(strings.TrimPrefix(line, "EnvironmentFiles="), -1) {
				files = append(files, struct {
					path   string
					ignore bool
				}{m[1], m[2] == "yes"})
			}
		}
	}
	var errs []error
	for _, f := range files {
		data, err := readFile(f.path)
		if err != nil {
			if !f.ignore {
				errs = append(errs, fmt.Errorf("read EnvironmentFile %s: %w", f.path, err))
			}
			continue
		}
		env = append(env, parseEnvFile(data)...)
	}
	return env, errors.Join(errs...)
}

// splitSystemdWords splits the value of "systemctl show -p Environment":
// space separated assignments where words containing spaces are double quoted.
func splitSystemdWords(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	esc := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && inQuote:
			esc = true
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// parseEnvFile parses systemd EnvironmentFile syntax (KEY=VALUE, # and ;
// comments, optional single/double quotes).
func parseEnvFile(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out = append(out, strings.TrimSpace(k)+"="+v)
	}
	return out
}
