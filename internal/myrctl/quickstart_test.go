// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package myrctl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/myrctl"
)

const (
	quickstartDir        = "../../examples/quickstart"
	quickstartComponents = "../../examples/sim/components-otelcol-contrib-0.162.0.json"
)

func quickstartRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = myrctl.Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestQuickstartValidatesStrictly(t *testing.T) {
	code, out, errs := quickstartRun(t, "validate", "--strict", "--components", quickstartComponents, "--output", "json", quickstartDir)
	require.Equal(t, 0, code, out+errs)
	var rep struct {
		Valid    bool           `json:"valid"`
		Errors   int            `json:"errors"`
		Warnings int            `json:"warnings"`
		Objects  map[string]int `json:"objects"`
		Samples  int            `json:"samples"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.True(t, rep.Valid)
	assert.Zero(t, rep.Errors)
	assert.Zero(t, rep.Warnings)
	assert.Equal(t, 3, rep.Objects["Fleet"])
	assert.Equal(t, 2, rep.Objects["RolloutPolicy"])
	assert.Equal(t, 4, rep.Samples)
}

func TestQuickstartSamplesAreAssignedToTheIntendedFleet(t *testing.T) {
	want := map[string]string{
		"linux-edge-01":   "linux-hosts",
		"linux-dc-01":     "linux-hosts",
		"windows-edge-01": "windows-hosts",
		"k8s-gateway-0":   "k8s-gateway",
	}
	entries, err := os.ReadDir(filepath.Join(quickstartDir, "samples", "agents"))
	require.NoError(t, err)
	require.Len(t, entries, len(want))
	for sample, fleet := range want {
		t.Run(sample, func(t *testing.T) {
			// no --fleet: the fleet is chosen by selector and priority, like myriarchd does
			code, out, errs := quickstartRun(t, "render", "--dir", quickstartDir, "--sample", sample,
				"--components", quickstartComponents, "--output", "json")
			require.Equal(t, 0, code, errs)
			var got struct {
				Fleet  string `json:"fleet"`
				Config string `json:"config"`
				Issues []any  `json:"issues"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			assert.Equal(t, fleet, got.Fleet)
			assert.NotEmpty(t, got.Config)
			assert.Empty(t, got.Issues)
		})
	}
}

func TestQuickstartRenderStdoutForWindowsEdgeSample(t *testing.T) {
	code, out, errs := quickstartRun(t, "render", "--dir", quickstartDir, "--fleet", "windows-hosts", "--sample", "windows-edge-01")
	require.Equal(t, 0, code, errs)
	assert.Contains(t, out, "windows_event_log/application")
	assert.Contains(t, out, "gateway.example.com:4317")
}

// The Quickstarts (Compose and kind) show a rollout within about a minute: the demo policies wait 30 seconds, do not limit the
// parallel targets and have no maintenance windows. The production-like values (and their reasons) live in gitops-template.
func TestQuickstartPoliciesAreDemoValues(t *testing.T) {
	for _, name := range []string{"canary-then-all", "config-canary"} {
		raw, err := os.ReadFile(filepath.Join(quickstartDir, "policies", name+".yaml"))
		require.NoError(t, err)
		var policy struct {
			Spec domain.RolloutPolicySpec `yaml:"spec"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &policy))
		spec := policy.Spec
		assert.Equal(t, 30*time.Second, spec.HealthWait.D(), "%s: healthWait", name)
		assert.Equal(t, 0, spec.MaxConcurrent, "%s: maxConcurrent 0 is unlimited", name)
		assert.Empty(t, spec.MaintenanceWindows, "%s: no maintenance windows in a demo", name)
	}
}
