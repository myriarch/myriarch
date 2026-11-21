// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServerHasNoKubernetesAPIClient guards the boundary of the gitops-pr
// executor (ADR-0052): the server hands updates over as pull or merge requests
// and never talks to a Kubernetes API, so no Kubernetes client library may be
// linked into it.
//
// k8s.io/klog/v2 is allowed explicitly: it is a logging library that comes in
// through internal/artifact -> sigstore-go -> certificate-transparency-go
// (loglist3) and is not an API client.
func TestServerHasNoKubernetesAPIClient(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go tool is not available")
	}
	cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".")
	cmd.Dir = "."
	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps")

	forbidden := []string{
		"k8s.io/client-go",
		"k8s.io/api",
		"k8s.io/apimachinery",
		"sigs.k8s.io/controller-runtime",
	}
	sawExecutor := false
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == "github.com/myriarch/myriarch/internal/executor/gitopsprexec" {
			sawExecutor = true
		}
		for _, f := range forbidden {
			if pkg == f || strings.HasPrefix(pkg, f+"/") {
				t.Errorf("myriarchd depends on the Kubernetes package %s", pkg)
			}
		}
	}
	require.True(t, sawExecutor, "the dependency list does not contain the gitops-pr executor: the test does not see the server")
}
