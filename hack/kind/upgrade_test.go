// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package kind holds the tests of the shell scripts of the kind probe that run without a cluster: the comparison of the upgrade
// test (lib/upgrade.sh) on fixtures, the refusal of upgrade.sh without its tools and the pins of versions.env.
package kind

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// bashOrSkip returns the paths of bash and jq. The scripts run on macOS and Linux; a missing tool skips the test, except in CI,
// where it fails the test.
func bashOrSkip(t *testing.T) (bash, jq string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the kind scripts run on macOS and Linux; jq.exe writes CRLF on Windows")
	}
	for _, name := range []string{"bash", "jq"} {
		p, err := exec.LookPath(name)
		if err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatalf("%s is not installed: %v", name, err)
			}
			t.Skipf("%s is not installed", name)
		}
		if name == "bash" {
			bash = p
		} else {
			jq = p
		}
	}
	return bash, jq
}

// run runs the command and returns stdout, stderr and the exit code.
func run(t *testing.T, cmd *exec.Cmd) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("running %v: %v", cmd.Args, err)
	}
	return out.String(), errb.String(), code
}

func absLib(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("lib", "upgrade.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ---- fixtures: raw answers of GET /fleets, GET /agents and GET /audit (neutral names) -------------------------------------

const fleetsRaw = `{"items":[
 {"fleet":{"name":"windows-hosts","priority":100,"selector":{"matchAttributes":{"os.type":"windows"}},"blueprint":"base","source":{"commit":"aaa"}},"status":{"agents":{"total":2}}},
 {"fleet":{"name":"linux-hosts","priority":100,"selector":{"matchAttributes":{"os.type":"linux"}},"blueprint":"base","description":"hosts","source":{"commit":"aaa"}},"status":{"agents":{"total":3}}}
]}`

const fleetsOneRaw = `{"items":[
 {"fleet":{"name":"linux-hosts","priority":100,"selector":{"matchAttributes":{"os.type":"linux"}},"blueprint":"base","description":"hosts","source":{"commit":"aaa"}},"status":{"agents":{"total":3}}}
]}`

const agentsRaw = `[
 {"items":[
  {"id":"a1","kind":"collector","hostname":"web1.example.com","status":"online","lastSeenAt":"2026-01-01T00:00:00Z","healthy":true,"configSync":"in_sync","version":"0.161.0","fleetName":"linux-hosts"},
  {"id":"a2","kind":"collector","hostname":"gw1.example.com","status":"online","lastSeenAt":"2026-01-01T00:00:00Z","healthy":true,"configSync":"in_sync","version":"0.161.0","fleetName":"k8s-gateway"}
 ],"total":3,"limit":2,"offset":0},
 {"items":[
  {"id":"a3","kind":"updater","hostname":"web1.example.com","status":"online","lastSeenAt":"2026-01-01T00:00:00Z","version":"0.2.1"}
 ],"total":3,"limit":2,"offset":2}
]`

const agentsOnePageRaw = `{"items":[
 {"id":"a1","kind":"collector","hostname":"web1.example.com","status":"online","fleetName":"linux-hosts"},
 {"id":"a2","kind":"collector","hostname":"gw1.example.com","status":"online","fleetName":"k8s-gateway"}
],"total":2,"limit":500,"offset":0}`

const auditRaw = `[
 {"items":[
  {"id":3,"time":"2026-01-01T00:00:03Z","actorType":"user","actorId":"u1","actorName":"admin","action":"fleet.version.select","targetType":"fleet","targetId":"linux-hosts","result":"failure","details":{"reason":"test"},"sourceIp":"192.0.2.1","requestId":"r3"},
  {"id":2,"time":"2026-01-01T00:00:02Z","actorType":"user","actorId":"u1","actorName":"admin","action":"auth.bootstrap.login","result":"success","sourceIp":"192.0.2.1","requestId":"r2"}
 ],"total":3,"limit":2,"offset":0},
 {"items":[
  {"id":1,"time":"2026-01-01T00:00:01Z","actorType":"system","actorId":"s1","actorName":"server","action":"auth.bootstrap.activate","targetType":"bootstrap","targetId":"b1","result":"success","details":{},"requestId":"r1"}
 ],"total":3,"limit":2,"offset":2}
]`

const auditFirstPageRaw = `{"items":[
 {"id":3,"time":"2026-01-01T00:00:03Z","actorType":"user","actorId":"u1","actorName":"admin","action":"fleet.version.select","targetType":"fleet","targetId":"linux-hosts","result":"failure","details":{"reason":"test"}},
 {"id":2,"time":"2026-01-01T00:00:02Z","actorType":"user","actorId":"u1","actorName":"admin","action":"auth.bootstrap.login","result":"success"}
],"total":2,"limit":500,"offset":0}`

// auditExtraRaw is auditRaw plus a newer entry (a login after the upgrade).
const auditExtraRaw = `[
 {"items":[
  {"id":4,"time":"2026-01-01T00:00:04Z","actorType":"user","actorId":"u1","actorName":"admin","action":"auth.session","result":"success"},
  {"id":3,"time":"2026-01-01T00:00:03Z","actorType":"user","actorId":"u1","actorName":"admin","action":"fleet.version.select","targetType":"fleet","targetId":"linux-hosts","result":"failure","details":{"reason":"test"},"sourceIp":"192.0.2.1","requestId":"r3"}
 ],"total":4,"limit":2,"offset":0},
 {"items":[
  {"id":2,"time":"2026-01-01T00:00:02Z","actorType":"user","actorId":"u1","actorName":"admin","action":"auth.bootstrap.login","result":"success","sourceIp":"192.0.2.1","requestId":"r2"},
  {"id":1,"time":"2026-01-01T00:00:01Z","actorType":"system","actorId":"s1","actorName":"server","action":"auth.bootstrap.activate","targetType":"bootstrap","targetId":"b1","result":"success","details":{},"requestId":"r1"}
 ],"total":4,"limit":2,"offset":2}
]`

// answers are the three raw files of one snapshot by kind; a missing key means that the file is not written.
type answers map[string]string

func baseAnswers() answers {
	return answers{"fleets": fleetsRaw, "assignments": agentsRaw, "audit": auditRaw}
}

func (a answers) with(kind, content string) answers {
	b := answers{}
	for k, v := range a {
		b[k] = v
	}
	if content == "" {
		delete(b, kind)
	} else {
		b[kind] = content
	}
	return b
}

func (a answers) write(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for kind, content := range a {
		if err := os.WriteFile(filepath.Join(dir, kind+".json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustReplace(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture lacks %q", old)
	}
	return strings.ReplaceAll(s, old, repl)
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

// ---- tests ---------------------------------------------------------------------------------------------------------------

func TestUpgradeScriptIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows: nothing is checked there")
	}
	fi, err := os.Stat("upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("upgrade.sh is not executable (mode %v)", fi.Mode())
	}
}

func TestUpgradeScriptSyntax(t *testing.T) {
	bash, _ := bashOrSkip(t)
	for _, f := range []string{"upgrade.sh", filepath.Join("lib", "upgrade.sh")} {
		_, stderr, code := run(t, exec.Command(bash, "-n", f))
		if code != 0 {
			t.Errorf("bash -n %s exits %d: %s", f, code, stderr)
		}
	}
}

// upgrade.sh checks its tools before it creates or changes anything: one line names the missing tool.
func TestUpgradeRefusesWithoutTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes carry no meaning on Windows: nothing is checked there")
	}
	bash, _ := bashOrSkip(t)
	required := []string{"kind", "kubectl", "helm", "cosign"}
	for _, missing := range required {
		t.Run(missing, func(t *testing.T) {
			bin := t.TempDir()
			for _, name := range required {
				if name == missing {
					continue
				}
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// the system tools that the scripts use before the check
			for _, name := range []string{"dirname", "date"} {
				p, err := exec.LookPath(name)
				if err != nil {
					t.Skipf("%s is not installed", name)
				}
				if err := os.Symlink(p, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			work := t.TempDir()
			cmd := exec.Command(bash, "upgrade.sh")
			cmd.Env = []string{"PATH=" + bin, "TOOLS_BIN=" + t.TempDir(), "PROBE_WORK=" + work, "HOME=" + t.TempDir(), "NO_COLOR=1"}
			_, stderr, code := run(t, cmd)
			if code == 0 {
				t.Fatalf("upgrade.sh started without %s", missing)
			}
			var lines []string
			for _, l := range strings.Split(stderr, "\n") {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, l)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("want one line on stderr, got %d:\n%s", len(lines), stderr)
			}
			if want := "command '" + missing + "' not found"; !strings.Contains(lines[0], want) {
				t.Errorf("the line %q lacks %q", lines[0], want)
			}
			entries, err := os.ReadDir(work)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("upgrade.sh created files before the tool check: %v", entries)
			}
		})
	}
}

func TestUpgradeNormalize(t *testing.T) {
	bash, _ := bashOrSkip(t)
	lib := absLib(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		kind, file, want string
	}{
		{"fleets", fleetsRaw, `[
 {"name":"linux-hosts","priority":100,"selector":{"matchAttributes":{"os.type":"linux"}},"blueprint":"base"},
 {"name":"windows-hosts","priority":100,"selector":{"matchAttributes":{"os.type":"windows"}},"blueprint":"base"}
]`},
		{"assignments", agentsRaw, `[
 {"id":"a1","kind":"collector","fleetName":"linux-hosts"},
 {"id":"a2","kind":"collector","fleetName":"k8s-gateway"},
 {"id":"a3","kind":"updater","fleetName":""}
]`},
		{"assignments", agentsOnePageRaw, `[
 {"id":"a1","kind":"collector","fleetName":"linux-hosts"},
 {"id":"a2","kind":"collector","fleetName":"k8s-gateway"}
]`},
		{"audit", auditRaw, `[
 {"id":1,"time":"2026-01-01T00:00:01Z","actorType":"system","actorId":"s1","actorName":"server","action":"auth.bootstrap.activate","targetType":"bootstrap","targetId":"b1","result":"success","details":{}},
 {"id":2,"time":"2026-01-01T00:00:02Z","actorType":"user","actorId":"u1","actorName":"admin","action":"auth.bootstrap.login","targetType":"","targetId":"","result":"success","details":{}},
 {"id":3,"time":"2026-01-01T00:00:03Z","actorType":"user","actorId":"u1","actorName":"admin","action":"fleet.version.select","targetType":"fleet","targetId":"linux-hosts","result":"failure","details":{"reason":"test"}}
]`},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			f := write(tc.kind+".json", tc.file)
			stdout, stderr, code := run(t, exec.Command(bash, "-c", `. "$1"; upgrade_normalize "$2" "$3"`, "_", lib, tc.kind, f))
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if got, want := decode(t, stdout), decode(t, tc.want); !reflect.DeepEqual(got, want) {
				t.Errorf("got %s\nwant %s", stdout, tc.want)
			}
		})
	}
	t.Run("unknown kind", func(t *testing.T) {
		f := write("x.json", fleetsRaw)
		_, _, code := run(t, exec.Command(bash, "-c", `. "$1"; upgrade_normalize "$2" "$3"`, "_", lib, "agents", f))
		if code != 2 {
			t.Errorf("exit %d, want 2", code)
		}
	})
}

func TestUpgradeCompare(t *testing.T) {
	bash, _ := bashOrSkip(t)
	lib := absLib(t)
	base := baseAnswers()
	tests := []struct {
		name       string
		after      func(t *testing.T) answers
		wantCode   int
		wantStderr string
	}{
		{"identical", func(t *testing.T) answers { return base }, 0, ""},
		{"only volatile fields changed", func(t *testing.T) answers {
			f := mustReplace(t, fleetsRaw, `"total":2`, `"total":9`)
			f = mustReplace(t, f, `"commit":"aaa"`, `"commit":"bbb"`)
			g := mustReplace(t, agentsRaw, `"status":"online"`, `"status":"offline"`)
			g = mustReplace(t, g, `"lastSeenAt":"2026-01-01T00:00:00Z"`, `"lastSeenAt":"2026-02-02T00:00:00Z"`)
			return base.with("fleets", f).with("assignments", g)
		}, 0, ""},
		{"additional audit entry after", func(t *testing.T) answers { return base.with("audit", auditExtraRaw) }, 0, ""},
		{"fleet missing", func(t *testing.T) answers { return base.with("fleets", fleetsOneRaw) }, 1, "fleets"},
		{"fleet selector changed", func(t *testing.T) answers {
			return base.with("fleets", mustReplace(t, fleetsRaw, `"os.type":"windows"`, `"os.type":"darwin"`))
		}, 1, "fleets"},
		{"agent in another fleet", func(t *testing.T) answers {
			return base.with("assignments", mustReplace(t, agentsRaw, `"fleetName":"k8s-gateway"`, `"fleetName":"linux-hosts"`))
		}, 1, "assignments"},
		{"agent missing", func(t *testing.T) answers { return base.with("assignments", agentsOnePageRaw) }, 1, "assignments"},
		{"audit entry missing", func(t *testing.T) answers { return base.with("audit", auditFirstPageRaw) }, 1, "audit"},
		{"audit entry changed", func(t *testing.T) answers {
			return base.with("audit", mustReplace(t, auditRaw, `"actorName":"server"`, `"actorName":"someone"`))
		}, 1, "audit"},
		{"audit file missing after", func(t *testing.T) answers { return base.with("audit", "") }, 2, "cannot read audit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := base.write(t)
			after := tc.after(t).write(t)
			_, stderr, code := run(t, exec.Command(bash, "-c", `. "$1"; upgrade_compare "$2" "$3"`, "_", lib, before, after))
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d; stderr: %s", code, tc.wantCode, stderr)
			}
			if tc.wantStderr == "" && strings.TrimSpace(stderr) != "" {
				t.Errorf("unexpected stderr: %s", stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr %q lacks %q", stderr, tc.wantStderr)
			}
		})
	}
}

func TestUpgradeSplitRef(t *testing.T) {
	bash, _ := bashOrSkip(t)
	lib := absLib(t)
	digest := "sha256:" + strings.Repeat("ab", 32)
	good := "ghcr.io/example/app:1.2.3@" + digest
	stdout, stderr, code := run(t, exec.Command(bash, "-c", `. "$1"; upgrade_split_ref "$2" && echo "$UPGRADE_REF_REPO $UPGRADE_REF_TAG $UPGRADE_REF_DIGEST"`, "_", lib, good))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := "ghcr.io/example/app 1.2.3 " + digest; strings.TrimSpace(stdout) != want {
		t.Errorf("got %q, want %q", strings.TrimSpace(stdout), want)
	}
	for _, bad := range []string{"", "ghcr.io/example/app:1.2.3", "ghcr.io/example/app@" + digest, "ghcr.io/example/app:1.2.3@sha256:abc"} {
		_, _, code := run(t, exec.Command(bash, "-c", `. "$1"; upgrade_split_ref "$2"`, "_", lib, bad))
		if code != 1 {
			t.Errorf("%q: exit %d, want 1", bad, code)
		}
	}
}

// versions.env reads like kind_load_versions: NAME=VALUE lines, comments and empty lines skipped.
func readVersions(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("versions.env")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
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

func TestUpgradePinsByDigest(t *testing.T) {
	env := readVersions(t)
	version := env["UPGRADE_PREVIOUS_VERSION"]
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		t.Fatalf("UPGRADE_PREVIOUS_VERSION is %q, want x.y.z", version)
	}
	ref := regexp.MustCompile(`^[^@:]+:` + regexp.QuoteMeta(version) + `@sha256:[0-9a-f]{64}$`)
	for _, name := range []string{"UPGRADE_PREVIOUS_CHART", "UPGRADE_PREVIOUS_SERVER_IMAGE"} {
		if !ref.MatchString(env[name]) {
			t.Errorf("%s is %q, want repo:%s@sha256:<digest>", name, env[name], version)
		}
	}
	node := regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	for _, name := range []string{"KIND_NODE_IMAGE", "KIND_NODE_IMAGE_PREVIOUS"} {
		if !node.MatchString(env[name]) {
			t.Errorf("%s is %q, want a digest pin", name, env[name])
		}
	}

	src, err := os.ReadFile("upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(src)
	for _, name := range []string{"UPGRADE_PREVIOUS_VERSION", "UPGRADE_PREVIOUS_CHART", "UPGRADE_PREVIOUS_SERVER_IMAGE", "KIND_NODE_IMAGE_PREVIOUS"} {
		if !strings.Contains(script, name) {
			t.Errorf("upgrade.sh does not use %s", name)
		}
	}
	if strings.Contains(script, version) {
		t.Errorf("upgrade.sh contains the version %s as a literal: it belongs in versions.env only", version)
	}
	if regexp.MustCompile(`sha256:[0-9a-f]{64}`).MatchString(script) {
		t.Error("upgrade.sh contains a digest as a literal: the pins belong in versions.env only")
	}
}
