// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// Policy defaults (see docs/reference/gitops-schema.md, "RolloutPolicy", and
// docs/concepts/rollouts.md, "Which policy a fleet uses").
const (
	DefaultHealthWait         = 60 * time.Second
	DefaultOfflineGracePeriod = 15 * time.Minute
)

// WithDefaults returns a copy of the policy spec with all defaults applied,
// exactly as the rollout engine uses it (and as stored in the rollout's policy
// snapshot):
//
//   - no waves: one wave {name: "all", percent: 100}
//   - last wave is a selector wave or has percent < 100: an implicit final
//     wave {name: "rest", percent: 100} is appended
//   - MaxConcurrent 0 stays 0 (= unlimited)
//   - HealthWait 0 -> 60s; ApplyTimeout 0 -> DefaultApplyTimeout(kind);
//     OfflineGracePeriod 0 -> 15m
//   - FailureThreshold: both fields 0 -> MaxFailed = 1 (a policy that only
//     sets maxFailedPercent uses the percentage rule alone)
//   - OnFailure "" -> pause; TimezoneAttribute "" -> site.timezone;
//     DefaultTimezone "" -> fallbackTZ (myriarchd rollouts.defaultTimezone,
//     default "UTC")
//   - MaintenanceWindow.Timezone "" -> "agent"
func (s RolloutPolicySpec) WithDefaults(kind RolloutKind, fallbackTZ string) RolloutPolicySpec {
	out := s
	out.Waves = append([]WaveSpec(nil), s.Waves...)
	if len(out.Waves) == 0 {
		out.Waves = []WaveSpec{{Name: "all", Percent: 100}}
	}
	if last := out.Waves[len(out.Waves)-1]; last.Selector != nil || last.Percent < 100 {
		out.Waves = append(out.Waves, WaveSpec{Name: "rest", Percent: 100})
	}
	if out.HealthWait == 0 {
		out.HealthWait = Duration(DefaultHealthWait)
	}
	if out.ApplyTimeout == 0 {
		out.ApplyTimeout = Duration(DefaultApplyTimeout(kind))
	}
	if out.OfflineGracePeriod == 0 {
		out.OfflineGracePeriod = Duration(DefaultOfflineGracePeriod)
	}
	if out.FailureThreshold.MaxFailed == 0 && out.FailureThreshold.MaxFailedPercent == 0 {
		out.FailureThreshold.MaxFailed = 1
	}
	if out.OnFailure == "" {
		out.OnFailure = FailurePause
	}
	if out.TimezoneAttribute == "" {
		out.TimezoneAttribute = AttrSiteTimezone
	}
	if out.DefaultTimezone == "" {
		out.DefaultTimezone = fallbackTZ
	}
	out.MaintenanceWindows = append([]MaintenanceWindow(nil), s.MaintenanceWindows...)
	for i := range out.MaintenanceWindows {
		if out.MaintenanceWindows[i].Timezone == "" {
			out.MaintenanceWindows[i].Timezone = "agent"
		}
	}
	return out
}

// Reached reports whether failed targets out of total targets of a wave hit
// the threshold: (MaxFailed > 0 && failed >= MaxFailed) or
// (MaxFailedPercent > 0 && failed*100 >= MaxFailedPercent*total). Apply
// WithDefaults first. failed == 0 never reaches the threshold.
func (t FailureThreshold) Reached(failed, total int) bool {
	if failed <= 0 {
		return false
	}
	if t.MaxFailed > 0 && failed >= t.MaxFailed {
		return true
	}
	return t.MaxFailedPercent > 0 && total > 0 && failed*100 >= t.MaxFailedPercent*total
}
