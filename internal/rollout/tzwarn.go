// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"log/slog"
	"sort"
	"strings"

	"github.com/myriarch/myriarch/internal/domain"
)

// PoliciesUsingServerTimezone returns the sorted names of the policies that
// silently depend on the server-wide rollouts.defaultTimezone: they have at
// least one maintenance window whose timezone is "" or "agent" (the window
// follows the agent's timezone attribute) and no spec.defaultTimezone that
// would catch agents without a valid timezone attribute.
func PoliciesUsingServerTimezone(policies []domain.RolloutPolicy) []string {
	var names []string
	for _, p := range policies {
		if policyUsesServerTimezone(p.RolloutPolicySpec) {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

func policyUsesServerTimezone(spec domain.RolloutPolicySpec) bool {
	if spec.DefaultTimezone != "" {
		return false
	}
	for _, w := range spec.MaintenanceWindows {
		if w.Timezone == "" || strings.EqualFold(w.Timezone, "agent") {
			return true
		}
	}
	return false
}

const serverTimezoneAdvice = "set spec.defaultTimezone on the policy or rollouts.defaultTimezone in the myriarchd configuration"

// WarnPoliciesUsingServerTimezone logs one warning that lists the policies
// whose maintenance windows fall back to the server default time zone. It
// logs nothing when no policy is affected. myriarchd calls it once at startup.
func WarnPoliciesUsingServerTimezone(log *slog.Logger, policies []domain.RolloutPolicy, effectiveTimezone string) {
	names := PoliciesUsingServerTimezone(policies)
	if len(names) == 0 {
		return
	}
	if effectiveTimezone == "" {
		effectiveTimezone = "UTC"
	}
	log.Warn("rollout policies with maintenance windows fall back to the server default time zone for agents without a valid timezone attribute",
		"policies", names, "rollouts.defaultTimezone", effectiveTimezone, "advice", serverTimezoneAdvice)
}

// warnServerTimezoneOnce logs the same warning the first time the engine
// plans a rollout with the given policy. Policies that arrive through GitOps
// after startup would otherwise never be reported.
func (e *Engine) warnServerTimezoneOnce(policyName string, spec domain.RolloutPolicySpec) {
	if !policyUsesServerTimezone(spec) {
		return
	}
	e.tzWarnMu.Lock()
	if _, done := e.tzWarned[policyName]; done {
		e.tzWarnMu.Unlock()
		return
	}
	if e.tzWarned == nil {
		e.tzWarned = map[string]struct{}{}
	}
	e.tzWarned[policyName] = struct{}{}
	e.tzWarnMu.Unlock()
	e.log.Warn("rollout policy with maintenance windows falls back to the server default time zone for agents without a valid timezone attribute",
		"policy", policyName, "rollouts.defaultTimezone", e.cfg.DefaultTimezone, "advice", serverTimezoneAdvice)
}
