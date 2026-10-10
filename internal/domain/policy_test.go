// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"testing"
	"time"
)

func TestPolicyWithDefaults(t *testing.T) {
	p := RolloutPolicySpec{Waves: []WaveSpec{{Name: "pilot", Selector: &Selector{}}, {Name: "half", Percent: 50}}}.
		WithDefaults(RolloutKindVersion, "Europe/Amsterdam")
	if len(p.Waves) != 3 || p.Waves[2].Name != "rest" || p.Waves[2].Percent != 100 {
		t.Fatalf("waves %+v", p.Waves)
	}
	if p.ApplyTimeout.D() != 30*time.Minute || p.HealthWait.D() != time.Minute || p.OfflineGracePeriod.D() != 15*time.Minute {
		t.Fatalf("durations %+v", p)
	}
	if p.FailureThreshold.MaxFailed != 1 || p.OnFailure != FailurePause || p.DefaultTimezone != "Europe/Amsterdam" || p.TimezoneAttribute != AttrSiteTimezone {
		t.Fatalf("defaults %+v", p)
	}
	pct := RolloutPolicySpec{FailureThreshold: FailureThreshold{MaxFailedPercent: 5}}.WithDefaults(RolloutKindConfig, "UTC")
	if pct.FailureThreshold.MaxFailed != 0 || pct.ApplyTimeout.D() != 10*time.Minute {
		t.Fatalf("percent-only threshold must stay percent-only: %+v", pct)
	}
	if pct.FailureThreshold.Reached(4, 100) || !pct.FailureThreshold.Reached(5, 100) {
		t.Fatal("percent rule")
	}
	if !(FailureThreshold{MaxFailed: 1}).Reached(1, 1000) || (FailureThreshold{MaxFailed: 3}).Reached(2, 3) {
		t.Fatal("absolute rule")
	}
	if (FailureThreshold{MaxFailed: 1}).Reached(0, 10) {
		t.Fatal("zero failures")
	}
}
