// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// What the rigs of the installers (the Linux script and the Windows script) have in common: it builds on every platform, because the
// tests of the Windows script run on Windows too.

const (
	rigCollectorGrant  = "myrd_collectorgrant.abc123"
	rigSupervisorGrant = "myrd_supervisorgrant.def456"
	rigVersion         = "0.162.0"
)

// recorded is one request that the test server saw.
type recorded struct {
	Method, Path, Auth, Body string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		rel, err := filepath.Rel(src, path)
		require.NoError(t, err)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	}))
}

func servePlan(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, text)
}

// problem writes a problem document the way the server does: the class is the code, the reason the code of the first issue.
func problem(w http.ResponseWriter, status int, class, reason string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	issues := ""
	if reason != "" {
		issues = fmt.Sprintf(`,"issues":[{"severity":"error","code":%q,"message":"the reason"}]`, reason)
	}
	_, _ = fmt.Fprintf(w, `{"type":"about:blank","title":"refused","status":%d,"detail":"the server says: %s","code":%q%s}`, status, reason, class, issues)
}

type installerResult struct {
	Out  string
	Code int
}

// failure is the code of the one-line error, or "".
func (res installerResult) failure() string {
	m := regexp.MustCompile(`\[myriarch-install\] ERROR \[([a-z][a-z0-9_]*)\]`).FindStringSubmatch(res.Out)
	if m == nil {
		return ""
	}
	return m[1]
}

// hostAttr is the host.name that the installer sends (the name of this machine when it has a plain one), as a JSON member.
func hostAttr(body string) string {
	m := regexp.MustCompile(`"host\.name":"([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return fmt.Sprintf(`,"host.name":%q`, m[1])
}

func sortedFlags(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
