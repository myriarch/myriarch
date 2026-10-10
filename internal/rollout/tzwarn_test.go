// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
)

func tzPolicy(name, defaultTZ string, windows ...domain.MaintenanceWindow) domain.RolloutPolicy {
	return domain.RolloutPolicy{
		ObjectMeta:        domain.ObjectMeta{Name: name},
		RolloutPolicySpec: domain.RolloutPolicySpec{DefaultTimezone: defaultTZ, MaintenanceWindows: windows},
	}
}

func win(tz string) domain.MaintenanceWindow {
	return domain.MaintenanceWindow{Start: "22:00", End: "04:00", Timezone: tz}
}

func TestPoliciesUsingServerTimezone(t *testing.T) {
	policies := []domain.RolloutPolicy{
		tzPolicy("zeta-empty", "", win("")),
		tzPolicy("agent", "", win("agent")),
		tzPolicy("agent-upper", "", win("Agent")),
		tzPolicy("mixed", "", win("Europe/Vienna"), win("agent")),
		tzPolicy("explicit-window", "", win("Europe/Vienna")),
		tzPolicy("policy-default", "Europe/Berlin", win("agent")),
		tzPolicy("no-windows", ""),
	}
	assert.Equal(t, []string{"agent", "agent-upper", "mixed", "zeta-empty"}, PoliciesUsingServerTimezone(policies))
	assert.Empty(t, PoliciesUsingServerTimezone(nil))
	assert.Empty(t, PoliciesUsingServerTimezone([]domain.RolloutPolicy{tzPolicy("ok", "UTC", win(""))}))
}

func TestWarnPoliciesUsingServerTimezoneLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	WarnPoliciesUsingServerTimezone(log, []domain.RolloutPolicy{tzPolicy("ok", "UTC", win(""))}, "UTC")
	assert.Empty(t, buf.String(), "no affected policy, no warning")

	WarnPoliciesUsingServerTimezone(log, []domain.RolloutPolicy{tzPolicy("night", "", win("")), tzPolicy("early", "", win("agent"))}, "")
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "level=WARN"), out)
	assert.Contains(t, out, "early")
	assert.Contains(t, out, "night")
	assert.Contains(t, out, "rollouts.defaultTimezone=UTC")
	assert.Contains(t, out, "spec.defaultTimezone")
}

func TestEngineWarnsOncePerPolicyName(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	e.eng = NewEngine(e.cfg, Deps{
		Store: e.st, Bus: e.bus, Control: e.ctl,
		Executors: executor.MapRegistry{domain.ExecutorUpdater: e.exec, domain.ExecutorWebhook: e.exec, domain.ExecutorAWX: e.exec, domain.ExecutorManual: e.exec},
		Notifier:  e.notif, Audit: auditAdapter{e.st}, Logger: slog.New(slog.NewTextHandler(&buf, nil)),
		Registerer: prometheus.NewRegistry(), Now: e.clk.Now,
	})

	windowed := spec(domain.WaveSpec{Name: "all", Percent: 100})
	windowed.MaintenanceWindows = []domain.MaintenanceWindow{win("agent")}
	e.setPolicy("windowed", windowed)
	e.setPolicy("fixed", func() domain.RolloutPolicySpec {
		s := windowed
		s.DefaultTimezone = "Europe/Berlin"
		return s
	}())
	for _, fleet := range []struct{ name, policy string }{{"a", "windowed"}, {"b", "windowed"}, {"c", "fixed"}} {
		e.addFleet(fleet.name, func(f *domain.Fleet) {
			f.Rollout = domain.FleetRolloutSpec{ConfigPolicy: fleet.policy, VersionPolicy: fleet.policy}
		})
		e.addAgents(2, fleet.name)
	}
	e.publishSnapshot()
	e.plan()
	require.Len(t, e.rollouts(), 3, "all three fleets got a rollout")

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "level=WARN"), out)
	assert.Contains(t, out, "policy=windowed")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level=WARN") {
			assert.NotContains(t, line, "policy=fixed")
		}
	}
	assert.Contains(t, out, "rollouts.defaultTimezone=Europe/Berlin", "the effective server default is named")
}

func TestNewEngineDefaultsToUTC(t *testing.T) {
	eng := NewEngine(config.RolloutsConfig{}, Deps{})
	assert.Equal(t, "UTC", eng.cfg.DefaultTimezone)
}
