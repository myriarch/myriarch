// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package kind holds the tests of the shell scripts in this directory that need no cluster: the comparison of the upgrade test
// against fixtures, the check of the tools, the parsing of the pins and the static rules of the scripts.
package kind

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// needTool returns the path of a tool; a missing tool fails the test in CI and skips it elsewhere.
func needTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("%s is required: %v", name, err)
		}
		t.Skipf("%s is not installed", name)
	}
	return p
}

// runLib calls a function of lib/upgrade.sh and returns stdout, stderr and the exit code.
func runLib(t *testing.T, fn string, args ...string) (string, string, int) {
	t.Helper()
	bash := needTool(t, "bash")
	cmd := exec.Command(bash, append([]string{"-c", ". ./lib/upgrade.sh && " + fn + ` "$@"`, "_"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run %s: %v", fn, err)
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// readEnv parses versions.env (NAME=VALUE, comments and empty lines skipped).
func readEnv(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("versions.env")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("versions.env: line without '=': %q", line)
		}
		env[k] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return env
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const digest64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestUpgradeScriptSyntax(t *testing.T) {
	bash := needTool(t, "bash")
	for _, f := range []string{"upgrade.sh", "lib/upgrade.sh"} {
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", f, err, out)
		}
	}
}

func TestUpgradeScriptExecutable(t *testing.T) {
	fi, err := os.Stat("upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("upgrade.sh is not executable (mode %v); ci/check.sh requires the executable bit", fi.Mode().Perm())
	}
}

func TestUpgradeCompareFixtures(t *testing.T) {
	needTool(t, "jq")
	cases := []struct {
		dir  string
		code int
		part string // the part that differs when code is 1
	}{
		{"before", 0, ""},
		{"after-same", 0, ""},
		{"after-fleet-missing", 1, "fleets"},
		{"after-fleet-changed", 1, "fleets"},
		{"after-assignment-missing", 1, "assignments"},
		{"after-assignment-changed", 1, "assignments"},
		{"after-audit-missing", 1, "audit"},
		{"after-audit-changed", 1, "audit"},
		{"after-missing", 2, ""},
		{"after-invalid", 2, ""},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			stdout, stderr, code := runLib(t, "upgrade_compare", "testdata/upgrade/before", filepath.Join("testdata/upgrade", c.dir))
			if code != c.code {
				t.Fatalf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, stdout, stderr)
			}
			var diffs []string
			for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
				if strings.HasPrefix(l, "DIFF ") {
					diffs = append(diffs, l)
				}
			}
			switch c.code {
			case 1:
				if len(diffs) != 1 || !strings.HasPrefix(diffs[0], "DIFF "+c.part+": ") {
					t.Errorf("want exactly one DIFF line for %s, got %q", c.part, diffs)
				}
			case 0:
				if len(diffs) != 0 || strings.Count(stdout, "SAME ") != 3 {
					t.Errorf("want three SAME lines and no DIFF line, got:\n%s", stdout)
				}
			case 2:
				if strings.TrimSpace(stderr) == "" {
					t.Error("exit code 2 without a message on stderr")
				}
			}
		})
	}
}

func TestUpgradeRequireTools(t *testing.T) {
	bash := needTool(t, "bash")
	all := []string{"kind", "kubectl", "helm", "cosign", "crane", "jq", "curl", "docker"}
	for _, missing := range []string{"kind", "kubectl", "helm", "cosign"} {
		t.Run(missing, func(t *testing.T) {
			stubs := t.TempDir()
			for _, tool := range all {
				if tool == missing {
					continue
				}
				if err := os.WriteFile(filepath.Join(stubs, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bash, "upgrade.sh")
			cmd.Env = []string{"PATH=" + stubs, "TOOLS_BIN=" + t.TempDir(), "HOME=" + t.TempDir()}
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit code 1, got %v\nstderr: %s", err, stderr.String())
			}
			lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "'"+missing+"'") {
				t.Errorf("want one line that names %s, got %q", missing, stderr.String())
			}
		})
	}

	_, stderr, code := runLib(t, "upgrade_require_tools", "no-such-tool-for-the-upgrade-test")
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if n := len(strings.Split(strings.TrimSpace(stderr), "\n")); n != 1 || !strings.Contains(stderr, "no-such-tool-for-the-upgrade-test") {
		t.Errorf("want one line that names the tool, got %q", stderr)
	}
	if _, stderr, code = runLib(t, "upgrade_require_tools", "bash"); code != 0 || stderr != "" {
		t.Errorf("an existing tool: exit code %d, stderr %q", code, stderr)
	}
}

func TestUpgradeSplitRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want []string // repo, tag, digest; nil means exit code 1
	}{
		{"chart", "oci://ghcr.io/example/charts/app:1.2.3@sha256:" + digest64, []string{"oci://ghcr.io/example/charts/app", "1.2.3", "sha256:" + digest64}},
		{"image with registry port", "localhost:5001/x/y:<tag>@sha256:" + digest64, []string{"localhost:5001/x/y", "<tag>", "sha256:" + digest64}},
		{"no digest", "ghcr.io/example/app:1.2.3", nil},
		{"no tag", "ghcr.io/example/app@sha256:" + digest64, nil},
		{"no tag with registry port", "localhost:5001/x/y@sha256:" + digest64, nil},
		{"short digest", "ghcr.io/example/app:1.2.3@sha256:" + digest64[:63], nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runLib(t, "upgrade_split_ref", c.ref)
			if c.want == nil {
				if code != 1 || strings.TrimSpace(stderr) == "" {
					t.Errorf("want exit code 1 with a message, got %d, stderr %q", code, stderr)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit code %d, stderr %q", code, stderr)
			}
			got := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestUpgradeNodeVersion(t *testing.T) {
	env := readEnv(t)
	tagRe := regexp.MustCompile(`:(v[0-9]+\.[0-9]+\.[0-9]+)@sha256:`)
	for _, name := range []string{"KIND_NODE_IMAGE", "KIND_NODE_IMAGE_PREVIOUS"} {
		t.Run(name, func(t *testing.T) {
			m := tagRe.FindStringSubmatch(env[name])
			if m == nil {
				t.Fatalf("%s=%q has no tag of the form vX.Y.Z", name, env[name])
			}
			stdout, stderr, code := runLib(t, "upgrade_node_version", env[name])
			if code != 0 || strings.TrimSpace(stdout) != m[1] {
				t.Errorf("got %q (exit code %d, stderr %q), want %q", stdout, code, stderr, m[1])
			}
		})
	}
	if _, _, code := runLib(t, "upgrade_node_version", "example.com/node:v1.2.3"); code != 1 {
		t.Errorf("a reference without a digest: exit code %d, want 1", code)
	}
	if _, _, code := runLib(t, "upgrade_node_version", "example.com/node:latest@sha256:"+digest64); code != 1 {
		t.Errorf("a reference without a version tag: exit code %d, want 1", code)
	}
}

func TestUpgradeAgentsOnline(t *testing.T) {
	needTool(t, "jq")
	dir := t.TempDir()
	file := filepath.Join(dir, "agents.json")
	body := `{"items":[{"id":"a","status":"online"},{"id":"b","status":"online"},{"id":"c","status":"offline"}],"total":3}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, code := runLib(t, "upgrade_agents_online", file, "2"); code != 0 || strings.TrimSpace(out) != "2" {
		t.Errorf("MIN 2: exit code %d, output %q", code, out)
	}
	if out, _, code := runLib(t, "upgrade_agents_online", file, "3"); code != 1 || strings.TrimSpace(out) != "2" {
		t.Errorf("MIN 3: exit code %d, output %q", code, out)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"items":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runLib(t, "upgrade_agents_online", bad, "1"); code != 2 {
		t.Errorf("invalid JSON: exit code %d, want 2", code)
	}
}

func TestUpgradeVersionsPinned(t *testing.T) {
	env := readEnv(t)
	version := env["UPGRADE_PREVIOUS_VERSION"]
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		t.Fatalf("UPGRADE_PREVIOUS_VERSION=%q is not a version", version)
	}
	q := regexp.QuoteMeta(version)
	checks := map[string]string{
		"UPGRADE_PREVIOUS_CHART":        `^oci://[^@]+:` + q + `@sha256:[0-9a-f]{64}$`,
		"UPGRADE_PREVIOUS_SERVER_IMAGE": `^[^@]+:` + q + `@sha256:[0-9a-f]{64}$`,
		"KIND_NODE_IMAGE":               `@sha256:[0-9a-f]{64}$`,
		"KIND_NODE_IMAGE_PREVIOUS":      `@sha256:[0-9a-f]{64}$`,
	}
	for name, re := range checks {
		if !regexp.MustCompile(re).MatchString(env[name]) {
			t.Errorf("%s=%q does not match %s", name, env[name], re)
		}
	}

	// The scripts carry no pin of their own: no node image name, no floating tag, no version literal (also not in comments).
	literal := regexp.MustCompile(`(?m)(^|[^0-9.])v?[0-9]+\.[0-9]+\.[0-9]+([^0-9.]|$)`)
	for _, f := range []string{"upgrade.sh", "lib/upgrade.sh"} {
		s := readFile(t, f)
		for _, bad := range []string{"kindest/node", ":latest"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", f, bad)
			}
		}
		if m := literal.FindString(s); m != "" {
			t.Errorf("%s contains a version literal: %q", f, m)
		}
	}
	s := readFile(t, "upgrade.sh")
	for _, want := range []string{"UPGRADE_PREVIOUS_VERSION", "UPGRADE_PREVIOUS_CHART", "UPGRADE_PREVIOUS_SERVER_IMAGE", "KIND_NODE_IMAGE_PREVIOUS", "image.digest="} {
		if !strings.Contains(s, want) {
			t.Errorf("upgrade.sh does not mention %s", want)
		}
	}
	if strings.Contains(s, "image.tag") {
		t.Error("upgrade.sh sets image.tag: images are pinned by digest")
	}
}

func TestUpgradeReadme(t *testing.T) {
	s := readFile(t, "README.md")
	_, section, ok := strings.Cut(s, "\n## Upgrade test\n")
	if !ok {
		t.Fatal("README.md has no section '## Upgrade test'")
	}
	if i := strings.Index(section, "\n## "); i >= 0 {
		section = section[:i]
	}
	for _, want := range []string{"hack/kind/upgrade.sh", "Docker"} {
		if !strings.Contains(section, want) {
			t.Errorf("the section 'Upgrade test' does not mention %q", want)
		}
	}
}
