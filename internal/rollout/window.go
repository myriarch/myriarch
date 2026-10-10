// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"time"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
)

// windowOpen reports whether a target (agent with AttributeView view) may
// START at now according to the policy's maintenance windows
// (docs/concepts/rollouts.md, "Maintenance windows"):
//
//   - no windows: always open;
//   - the time zone of a window is its own zone, or for "agent"/empty the
//     agent attribute policy.TimezoneAttribute (default site.timezone); an
//     invalid or missing zone falls back to policy.DefaultTimezone, then to
//     fallbackTZ (myriarchd rollouts.defaultTimezone), then to UTC;
//   - "days" are the weekdays on which a window STARTS (empty = every day);
//   - end <= start: the window crosses midnight and ends on the next
//     calendar day; start == end therefore means 24 hours from start;
//   - all computations use local wall-clock time, so daylight saving time
//     changes shorten/lengthen the window in absolute time.
//
// Invalid window definitions (rejected by GitOps validation) are ignored.
func windowOpen(policy domain.RolloutPolicySpec, view domain.AttributeView, now time.Time, fallbackTZ string) bool {
	if len(policy.MaintenanceWindows) == 0 {
		return true
	}
	for _, w := range policy.MaintenanceWindows {
		loc := windowLocation(policy, w, view, fallbackTZ)
		if windowContains(w, loc, now) {
			return true
		}
	}
	return false
}

// windowLocation resolves the time zone of a window for an agent.
func windowLocation(policy domain.RolloutPolicySpec, w domain.MaintenanceWindow, view domain.AttributeView, fallbackTZ string) *time.Location {
	var candidates []string
	if w.Timezone != "" && w.Timezone != "agent" {
		candidates = append(candidates, w.Timezone)
	} else {
		attr := policy.TimezoneAttribute
		if attr == "" {
			attr = domain.AttrSiteTimezone
		}
		if tz, ok := view[attr]; ok {
			candidates = append(candidates, tz)
		}
	}
	candidates = append(candidates, policy.DefaultTimezone, fallbackTZ)
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if loc, err := loadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

func loadLocation(name string) (*time.Location, error) {
	if name == "Local" {
		// time.LoadLocation("Local") is the host zone: never accept it, results
		// must not depend on the myriarchd host.
		return nil, errLocal
	}
	return time.LoadLocation(name)
}

type constError string

func (e constError) Error() string { return string(e) }

const errLocal = constError("time zone \"Local\" is not allowed")

// windowContains evaluates one window in loc at instant now.
func windowContains(w domain.MaintenanceWindow, loc *time.Location, now time.Time) bool {
	startMin, ok1 := blueprint.ParseClock(w.Start)
	endMin, ok2 := blueprint.ParseClock(w.End)
	if !ok1 || !ok2 {
		return false
	}
	days := map[time.Weekday]bool{}
	for _, d := range w.Days {
		if wd, ok := blueprint.ParseWeekday(d); ok {
			days[wd] = true
		}
	}
	local := now.In(loc)
	y, m, d := local.Date()
	// A window that started yesterday may still be open (midnight crossing).
	for offset := 0; offset >= -1; offset-- {
		day := time.Date(y, m, d+offset, 12, 0, 0, 0, loc) // noon: safe weekday/date arithmetic
		if len(days) > 0 && !days[day.Weekday()] {
			continue
		}
		dy, dm, dd := day.Date()
		start := time.Date(dy, dm, dd, startMin/60, startMin%60, 0, 0, loc)
		endDay := dd
		if endMin <= startMin {
			endDay = dd + 1
		}
		end := time.Date(dy, dm, endDay, endMin/60, endMin%60, 0, 0, loc)
		if !now.Before(start) && now.Before(end) {
			return true
		}
	}
	return false
}
