// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package kind holds the tests of the shell scripts of hack/kind that run without a cluster: the comparison of the upgrade test
// (hack/kind/lib/upgrade.sh) against fixtures, the refusal to start without a tool and the pins of versions.env.
package kind

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	upgradeLib = "lib/upgrade.sh"
	sha64      = `sha256:[0-9a-f]{64}`
)

// needBash skips a test on Windows and without bash; withJQ also skips without jq.
func needBash(t *testing.T, withJQ bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the shell scripts of hack/kind do not run on Windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	if withJQ {
		if _, err := exec.LookPath("jq"); err != nil {
			t.Skip("jq is not installed")
		}
	}
	return bash
}

// runLib sources lib/upgrade.sh and runs the shell code in script with the arguments args ($2...). It returns the exit code and the
// combined output.
func runLib(t *testing.T, env []string, stdin string, script string, args ...string) (int, string) {
	t.Helper()
	bash := needBash(t, false)
	argv := append([]string{"-c", `. "$1"; ` + script, "bash", upgradeLib}, args...)
	cmd := exec.Command(bash, argv...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return 0, out.String()
	}
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "bash did not run: %s", out.String())
	return ee.ExitCode(), out.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestUpgradeScriptIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no execute bit on Windows")
	}
	st, err := os.Stat("upgrade.sh")
	require.NoError(t, err)
	assert.NotZero(t, st.Mode()&0o111, "hack/kind/upgrade.sh must be executable (chmod +x; ci/check.sh requires it)")
}

func TestUpgradeScriptSyntax(t *testing.T) {
	bash := needBash(t, false)
	for _, f := range []string{"upgrade.sh", upgradeLib} {
		out, err := exec.Command(bash, "-n", f).CombinedOutput()
		assert.NoError(t, err, "bash -n %s: %s", f, out)
	}
}

// ---- fixtures ---------------------------------------------------------------------------------------------------------

func copyFixtures(t *testing.T) (before, after string) {
	t.Helper()
	root := t.TempDir()
	for _, side := range []string{"before", "after"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, side), 0o755))
		for _, f := range []string{"fleets.json", "agents.json", "audit.json"} {
			b, err := os.ReadFile(filepath.Join("testdata", "upgrade", side, f))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, side, f), b, 0o600))
		}
	}
	return filepath.Join(root, "before"), filepath.Join(root, "after")
}

type doc = map[string]any

// mutate loads dir/file, applies fn to the document and writes it back.
func mutate(t *testing.T, dir, file string, fn func(items []any) []any) {
	t.Helper()
	p := filepath.Join(dir, file)
	var d doc
	require.NoError(t, json.Unmarshal([]byte(readFile(t, p)), &d))
	items, ok := d["items"].([]any)
	require.True(t, ok)
	d["items"] = fn(items)
	b, err := json.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, b, 0o600))
}

// edit applies set to the item for which match is true; it fails when no item matches.
func edit(t *testing.T, items []any, match func(doc) bool, set func(doc)) {
	t.Helper()
	for _, it := range items {
		m := it.(doc)
		if match(m) {
			set(m)
			return
		}
	}
	require.Fail(t, "no item matches")
}

func drop(items []any, match func(doc) bool) []any {
	var out []any
	for _, it := range items {
		if !match(it.(doc)) {
			out = append(out, it)
		}
	}
	return out
}

func fleetNamed(name string) func(doc) bool {
	return func(m doc) bool { return m["fleet"].(doc)["name"] == name }
}

func idIs(id any) func(doc) bool { return func(m doc) bool { return m["id"] == id } }

const agent1 = "00000000-0000-4000-8000-000000000001"

func TestUpgradeCompareFixtures(t *testing.T) {
	needBash(t, true)
	tests := []struct {
		name   string
		change func(t *testing.T, before, after string)
		want   []string // prefixes that the output must have; none means exit 0
	}{
		{name: "unchanged"},
		{
			name: "fleet removed",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "fleets.json", func(i []any) []any { return drop(i, fleetNamed("sim-dc")) })
			},
			want: []string{"fleets differ"},
		},
		{
			name: "selector changed",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "fleets.json", func(i []any) []any {
					edit(t, i, fleetNamed("sim-edge"), func(m doc) {
						m["fleet"].(doc)["selector"] = doc{"matchAttributes": doc{"site.type": "other"}}
					})
					return i
				})
			},
			want: []string{"fleets differ"},
		},
		{
			name: "agent fleet changed",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "agents.json", func(i []any) []any {
					edit(t, i, idIs(agent1), func(m doc) { m["fleetName"] = "sim-dc" })
					return i
				})
			},
			want: []string{"assignments differ"},
		},
		{
			name: "agent removed",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "agents.json", func(i []any) []any { return drop(i, idIs(agent1)) })
			},
			want: []string{"assignments differ"},
		},
		{
			name: "old audit event removed",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "audit.json", func(i []any) []any { return drop(i, idIs(float64(2))) })
			},
			want: []string{"audit differs"},
		},
		{
			name: "old audit event changed",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "audit.json", func(i []any) []any {
					edit(t, i, idIs(float64(3)), func(m doc) { m["action"] = "agent.delete" })
					return i
				})
			},
			want: []string{"audit differs"},
		},
		{
			name: "audit file missing",
			change: func(t *testing.T, _, after string) {
				require.NoError(t, os.Remove(filepath.Join(after, "audit.json")))
			},
			want: []string{"audit differs"},
		},
		{
			name: "audit file is not JSON",
			change: func(t *testing.T, _, after string) {
				require.NoError(t, os.WriteFile(filepath.Join(after, "audit.json"), []byte("not json"), 0o600))
			},
			want: []string{"audit differs"},
		},
		{
			name: "several areas differ",
			change: func(t *testing.T, _, after string) {
				mutate(t, after, "fleets.json", func(i []any) []any { return drop(i, fleetNamed("kind-gw")) })
				mutate(t, after, "agents.json", func(i []any) []any { return drop(i, idIs(agent1)) })
				mutate(t, after, "audit.json", func(i []any) []any { return drop(i, idIs(float64(1))) })
			},
			want: []string{"fleets differ", "assignments differ", "audit differs"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before, after := copyFixtures(t)
			if tc.change != nil {
				tc.change(t, before, after)
			}
			code, out := runLib(t, nil, "", `upgrade_compare "$2" "$3"`, before, after)
			if len(tc.want) == 0 {
				assert.Zero(t, code, out)
				assert.Empty(t, strings.TrimSpace(out))
				return
			}
			assert.NotZero(t, code, out)
			for _, w := range tc.want {
				assert.Contains(t, out, w)
			}
		})
	}
}

func TestUpgradeCheckOnline(t *testing.T) {
	needBash(t, true)
	tests := []struct {
		name   string
		change func(t *testing.T, after string)
		want   string
	}{
		{name: "unchanged"},
		{
			name: "an agent is offline",
			change: func(t *testing.T, after string) {
				mutate(t, after, "agents.json", func(i []any) []any {
					edit(t, i, idIs(agent1), func(m doc) { m["status"] = "offline" })
					return i
				})
			},
			want: "agents differ",
		},
		{
			name: "a new id",
			change: func(t *testing.T, after string) {
				mutate(t, after, "agents.json", func(i []any) []any {
					return append(i, doc{"id": "00000000-0000-4000-8000-000000000009", "status": "online"})
				})
			},
			want: "agents differ",
		},
		{
			name: "file missing",
			change: func(t *testing.T, after string) {
				require.NoError(t, os.Remove(filepath.Join(after, "agents.json")))
			},
			want: "agents differ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before, after := copyFixtures(t)
			if tc.change != nil {
				tc.change(t, after)
			}
			code, out := runLib(t, nil, "", `upgrade_check_online "$2" "$3"`, before, after)
			if tc.want == "" {
				assert.Zero(t, code, out)
				return
			}
			assert.NotZero(t, code, out)
			assert.Contains(t, out, tc.want)
		})
	}
}

// ---- refusal without a tool -------------------------------------------------------------------------------------------

func TestUpgradeRefusesWithoutTool(t *testing.T) {
	bash := needBash(t, false)
	tools := []string{"kind", "kubectl", "helm", "cosign"}
	for _, missing := range tools {
		t.Run(missing, func(t *testing.T) {
			bin := t.TempDir()
			for _, name := range []string{"bash", "dirname", "date"} {
				p, err := exec.LookPath(name)
				if err != nil {
					t.Skipf("%s is not installed", name)
				}
				require.NoError(t, os.Symlink(p, filepath.Join(bin, name)))
			}
			for _, name := range tools {
				if name == missing {
					continue
				}
				require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
			}
			cmd := exec.Command(bash, "upgrade.sh")
			cmd.Env = []string{"PATH=" + bin, "TOOLS_BIN=" + t.TempDir(), "PROBE_WORK=" + t.TempDir(), "HOME=" + t.TempDir()}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			require.Error(t, err, "the script must refuse to start; stdout: %s", stdout.String())

			var lines []string
			for _, l := range strings.Split(stderr.String(), "\n") {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, l)
				}
			}
			require.Len(t, lines, 1, "stderr: %s", stderr.String())
			assert.Contains(t, lines[0], "'"+missing+"'")
			for _, other := range tools {
				if other != missing {
					assert.NotContains(t, lines[0], "'"+other+"'")
				}
			}
		})
	}
}

// ---- pins -------------------------------------------------------------------------------------------------------------

// versionPins reads hack/kind/versions.env (NAME=VALUE, comments and empty lines skipped).
func versionPins(t *testing.T) map[string]string {
	t.Helper()
	pins := map[string]string{}
	for _, line := range strings.Split(readFile(t, "versions.env"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		require.True(t, ok, "line %q is not NAME=VALUE", line)
		pins[k] = v
	}
	return pins
}

func TestUpgradePins(t *testing.T) {
	pins := versionPins(t)
	version := pins["UPGRADE_PREVIOUS_VERSION"]
	assert.Regexp(t, `^[0-9]+\.[0-9]+\.[0-9]+$`, version)
	assert.Regexp(t, "^"+sha64+"$", pins["UPGRADE_PREVIOUS_CHART_DIGEST"])
	assert.Regexp(t, `^ghcr\.io/myriarch/myriarch:`+regexp.QuoteMeta(version)+"@"+sha64+"$", pins["UPGRADE_PREVIOUS_SERVER_IMAGE"])
	for _, n := range []string{"KIND_NODE_IMAGE", "KIND_NODE_IMAGE_PREVIOUS"} {
		assert.Regexp(t, `^kindest/node:v\d+\.\d+\.\d+@`+sha64+"$", pins[n], n)
	}

	// the scripts take every version and image from the pins: no literal of them
	src := readFile(t, "upgrade.sh") + "\n" + readFile(t, upgradeLib)
	for _, lit := range []string{version, "kindest/node", "ghcr.io/myriarch/myriarch:"} {
		assert.NotContains(t, src, lit, "the scripts must not hard-code %q", lit)
	}
	for _, name := range []string{"UPGRADE_PREVIOUS_VERSION", "UPGRADE_PREVIOUS_CHART_DIGEST", "UPGRADE_PREVIOUS_SERVER_IMAGE", "KIND_NODE_IMAGE_PREVIOUS"} {
		assert.Contains(t, src, name)
	}
}

func pinEnv(t *testing.T, override map[string]string) []string {
	t.Helper()
	env := []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range versionPins(t) {
		if o, ok := override[k]; ok {
			v = o
		}
		env = append(env, k+"="+v)
	}
	return env
}

func TestUpgradeCheckPins(t *testing.T) {
	code, out := runLib(t, pinEnv(t, nil), "", "upgrade_check_pins")
	assert.Zero(t, code, out)

	tagOnly := map[string]string{"UPGRADE_PREVIOUS_SERVER_IMAGE": "ghcr.io/myriarch/myriarch:" + versionPins(t)["UPGRADE_PREVIOUS_VERSION"]}
	code, out = runLib(t, pinEnv(t, tagOnly), "", "upgrade_check_pins")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, "pin UPGRADE_PREVIOUS_SERVER_IMAGE")

	code, out = runLib(t, pinEnv(t, map[string]string{"KIND_NODE_IMAGE_PREVIOUS": "kindest/node:v1.0.0"}), "", "upgrade_check_pins")
	assert.NotZero(t, code, out)
	assert.Contains(t, out, "pin KIND_NODE_IMAGE_PREVIOUS")
}

func TestUpgradePullDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	code, out := runLib(t, nil, "Pulled: ghcr.io/example.test/charts/demo:1.2.3\nDigest: "+digest+"\n", "upgrade_pull_digest")
	assert.Zero(t, code, out)
	assert.Equal(t, digest, strings.TrimSpace(out))

	code, out = runLib(t, nil, "Pulled: ghcr.io/example.test/charts/demo:1.2.3\n", "upgrade_pull_digest")
	assert.NotZero(t, code, out)

	code, out = runLib(t, nil, "Digest: sha256:short\n", "upgrade_pull_digest")
	assert.NotZero(t, code, out)
}

func TestUpgradeRefHelpers(t *testing.T) {
	ref := "registry.example.test:5000/team/app:1.2.3@sha256:" + strings.Repeat("cd", 32)
	code, out := runLib(t, nil, "", `printf '%s %s' "$(upgrade_ref_repo "$2")" "$(upgrade_ref_digest "$2")"`, ref)
	assert.Zero(t, code, out)
	assert.Equal(t, "registry.example.test:5000/team/app sha256:"+strings.Repeat("cd", 32), out)

	want := "registry.example.test/team/app@sha256:" + strings.Repeat("cd", 32)
	good := "kind: Deployment\n      image: \"" + want + "\"\n      image: " + want + "\n"
	code, out = runLib(t, nil, good, `upgrade_check_template_images "$2"`, want)
	assert.Zero(t, code, out)

	code, out = runLib(t, nil, good+"      image: registry.example.test/team/app:latest\n", `upgrade_check_template_images "$2"`, want)
	assert.NotZero(t, code, out)

	code, out = runLib(t, nil, "kind: Service\n", `upgrade_check_template_images "$2"`, want)
	assert.NotZero(t, code, "a chart without an image is no proof: %s", out)
}
