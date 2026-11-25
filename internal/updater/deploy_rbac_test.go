// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rbacKind matches a manifest that creates an RBAC object.
var rbacKind = regexp.MustCompile(`(?m)^\s*kind:\s*["']?(Role|ClusterRole|RoleBinding|ClusterRoleBinding)["']?\s*$`)

// TestChartGrantsNoRBAC: the server hands version updates over as pull or merge
// requests (gitops-pr, ADR-0052) and makes no call to a Kubernetes API, so the
// Helm chart creates no Role, ClusterRole or binding.
func TestChartGrantsNoRBAC(t *testing.T) {
	root := filepath.Join(deployDir, "helm", "myriarch", "templates")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		assert.False(t, rbacKind.Match(b), "%s creates an RBAC object", path)
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, n, 10, "the scan did not find the templates of the chart")
}
