// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package clitest runs the real main function of a binary in a child process, so a test sees the process-level output
// contract of the command line (data and the version on stdout, errors on stderr, the exit code) with the real
// os.Stdout and os.Stderr. A test through a cobra command with SetOut cannot see a command that writes to the wrong
// stream: cobra's Print helpers write to stderr unless SetOut was called, which is how
// `myriarch-sim version` reported nothing on stdout in the image check of v0.1.0-rc.2.
//
// Use it in a package main test:
//
//	func TestMain(m *testing.M) {
//		clitest.RunAsMain(main)
//		os.Exit(m.Run())
//	}
//
//	func TestCLIContract(t *testing.T) { clitest.CheckOutputContract(t, brand.CLIBinary) }
//
// The test binary re-executes itself with the marker environment variable; RunAsMain then runs main instead of the
// tests. The contract is documented in docs/development.md ("CLI output").
package clitest

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/version"
)

// markerEnv tells the re-executed test binary to run main instead of the tests.
const markerEnv = "CLITEST_RUN_MAIN"

// RunAsMain runs mainFn and exits when the test binary was started by Exec; otherwise it returns at once. Call it first
// in TestMain, before anything that needs a database or a flag.
func RunAsMain(mainFn func()) {
	if os.Getenv(markerEnv) == "" {
		return
	}
	mainFn()
	os.Exit(0)
}

// Result is the outcome of one process.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Exec runs the main function of the package under test with args, using separate pipes for stdout and stderr.
func Exec(t *testing.T, args ...string) Result {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("clitest: executable: %v", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), markerEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
	default:
		t.Fatalf("clitest: run %s %v: %v", exe, args, err)
	}
	return res
}

// CheckOutputContract asserts the CLI output rules for the binary of the package under test: `version` prints exactly
// one line with the version as its first field on stdout and nothing on stderr, help is printed on stdout, and an
// unknown command fails with a message prefixed with the binary name on stderr and nothing on stdout.
func CheckOutputContract(t *testing.T, binary string) {
	t.Helper()
	t.Run("version", func(t *testing.T) {
		r := Exec(t, "version")
		if r.Code != 0 {
			t.Fatalf("%s version: exit code %d, stderr %q", binary, r.Code, r.Stderr)
		}
		if r.Stderr != "" {
			t.Errorf("%s version: stderr is %q, want empty", binary, r.Stderr)
		}
		line := strings.TrimSuffix(r.Stdout, "\n")
		if line == "" || strings.Contains(line, "\n") {
			t.Fatalf("%s version: stdout is %q, want exactly one line (the version on stderr, if anything, is the defect)", binary, r.Stdout)
		}
		if first := strings.Fields(line)[0]; first != version.Version {
			t.Errorf("%s version: first field is %q, want %q", binary, first, version.Version)
		}
	})
	t.Run("help", func(t *testing.T) {
		r := Exec(t, "--help")
		if r.Code != 0 || r.Stderr != "" || !strings.Contains(r.Stdout, binary) {
			t.Errorf("%s --help: exit code %d, stdout %q, stderr %q; want help text with the binary name on stdout only", binary, r.Code, r.Stdout, r.Stderr)
		}
	})
	t.Run("unknown command", func(t *testing.T) {
		r := Exec(t, "nosuch")
		if r.Code == 0 {
			t.Errorf("%s nosuch: exit code 0, want a failure", binary)
		}
		if r.Stdout != "" {
			t.Errorf("%s nosuch: stdout is %q, want empty", binary, r.Stdout)
		}
		if !strings.HasPrefix(r.Stderr, binary+":") {
			t.Errorf("%s nosuch: stderr is %q, want a message that starts with %q", binary, r.Stderr, binary+":")
		}
	})
}
