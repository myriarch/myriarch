// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	require.NoError(t, err)
	return l
}

// localTime builds an instant from wall-clock time in a zone.
func localTime(t *testing.T, zone string, y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, mustLoc(t, zone)).UTC()
}

func policy(ws ...domain.MaintenanceWindow) domain.RolloutPolicySpec {
	return domain.RolloutPolicySpec{MaintenanceWindows: ws, TimezoneAttribute: "site.timezone", DefaultTimezone: "Europe/Berlin"}
}

var edgeCareful = policy(
	domain.MaintenanceWindow{Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}, Start: "21:30", End: "06:30", Timezone: "agent"},
	domain.MaintenanceWindow{Days: []string{"Sun"}, Start: "00:00", End: "00:00", Timezone: "agent"},
)

func TestWindowNoWindowsIsAlwaysOpen(t *testing.T) {
	assert.True(t, windowOpen(domain.RolloutPolicySpec{}, nil, time.Now(), "UTC"))
}

func TestWindowMidnightCrossingBelongsToStartDay(t *testing.T) {
	view := domain.AttributeView{"site.timezone": "Europe/Berlin"}
	// 2026-10-10 is a Saturday.
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"Fri 21:29 closed", localTime(t, "Europe/Berlin", 2026, 10, 9, 21, 29), false},
		{"Fri 21:30 opens (start day Fri)", localTime(t, "Europe/Berlin", 2026, 10, 9, 21, 30), true},
		{"Sat 02:00 open (started Fri)", localTime(t, "Europe/Berlin", 2026, 10, 10, 2, 0), true},
		{"Sat 06:29 open", localTime(t, "Europe/Berlin", 2026, 10, 10, 6, 29), true},
		{"Sat 06:30 closed (end exclusive)", localTime(t, "Europe/Berlin", 2026, 10, 10, 6, 30), false},
		{"Sat 12:00 closed", localTime(t, "Europe/Berlin", 2026, 10, 10, 12, 0), false},
		{"Sat 23:00 open (start day Sat)", localTime(t, "Europe/Berlin", 2026, 10, 10, 23, 0), true},
		{"Sun 03:00 open (started Sat) and Sunday window", localTime(t, "Europe/Berlin", 2026, 10, 11, 3, 0), true},
		{"Sun 12:00 open: 24h window of Sunday", localTime(t, "Europe/Berlin", 2026, 10, 11, 12, 0), true},
		{"Sun 23:59 open", localTime(t, "Europe/Berlin", 2026, 10, 11, 23, 59), true},
		// Sunday is not a start day of the 21:30 window: Monday morning is closed
		{"Mon 00:00 closed (24h window ended)", localTime(t, "Europe/Berlin", 2026, 10, 12, 0, 0), false},
		{"Mon 05:00 closed (Sunday does not start the night window)", localTime(t, "Europe/Berlin", 2026, 10, 12, 5, 0), false},
		{"Mon 22:00 open", localTime(t, "Europe/Berlin", 2026, 10, 12, 22, 0), true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, windowOpen(edgeCareful, view, tc.at, "Europe/Berlin"), tc.name)
	}
}

func TestWindowStartEqualsEndIs24Hours(t *testing.T) {
	p := policy(domain.MaintenanceWindow{Days: []string{"Wed"}, Start: "08:00", End: "08:00", Timezone: "UTC"})
	// 2026-10-07 is a Wednesday
	assert.False(t, windowOpen(p, nil, time.Date(2026, 10, 7, 7, 59, 0, 0, time.UTC), "UTC"))
	assert.True(t, windowOpen(p, nil, time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC), "UTC"))
	assert.True(t, windowOpen(p, nil, time.Date(2026, 10, 8, 7, 59, 0, 0, time.UTC), "UTC"), "until Thursday 08:00")
	assert.False(t, windowOpen(p, nil, time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC), "UTC"))
}

func TestWindowEveryDayWhenNoDays(t *testing.T) {
	p := policy(domain.MaintenanceWindow{Start: "01:00", End: "05:00", Timezone: "Europe/Berlin"})
	for d := 5; d <= 11; d++ {
		assert.True(t, windowOpen(p, nil, localTime(t, "Europe/Berlin", 2026, 10, d, 3, 0), "UTC"), "day %d", d)
		assert.False(t, windowOpen(p, nil, localTime(t, "Europe/Berlin", 2026, 10, d, 5, 0), "UTC"), "day %d", d)
	}
}

func TestWindowAgentLocalTimeZones(t *testing.T) {
	// The same instant is night in Vienna but day in Los Angeles.
	p := policy(domain.MaintenanceWindow{Start: "01:00", End: "05:00", Timezone: "agent"})
	at := time.Date(2026, 10, 10, 1, 30, 0, 0, time.UTC) // 03:30 in Vienna (CEST), 18:30 Fri in LA
	assert.True(t, windowOpen(p, domain.AttributeView{"site.timezone": "Europe/Vienna"}, at, "UTC"))
	assert.False(t, windowOpen(p, domain.AttributeView{"site.timezone": "America/Los_Angeles"}, at, "UTC"))
	// Explicit zone of the window beats the agent's attribute.
	p2 := policy(domain.MaintenanceWindow{Start: "01:00", End: "05:00", Timezone: "Europe/Vienna"})
	assert.True(t, windowOpen(p2, domain.AttributeView{"site.timezone": "America/Los_Angeles"}, at, "UTC"))
}

func TestWindowTimezoneFallbackChain(t *testing.T) {
	// 22:30 UTC = 00:30 Berlin (CEST) = 15:30 Los Angeles
	at := time.Date(2026, 10, 10, 22, 30, 0, 0, time.UTC)
	p := policy(domain.MaintenanceWindow{Start: "00:00", End: "01:00"}) // timezone empty = agent
	p.DefaultTimezone = "Europe/Berlin"
	// missing attribute -> policy default
	assert.True(t, windowOpen(p, domain.AttributeView{}, at, "America/Los_Angeles"))
	// invalid attribute -> policy default
	assert.True(t, windowOpen(p, domain.AttributeView{"site.timezone": "Mars/Base"}, at, "America/Los_Angeles"))
	// no policy default -> myriarchd default
	p.DefaultTimezone = ""
	assert.True(t, windowOpen(p, domain.AttributeView{"site.timezone": "garbage"}, at, "Europe/Berlin"))
	assert.False(t, windowOpen(p, domain.AttributeView{}, at, "America/Los_Angeles"))
	// nothing valid at all -> UTC: 22:30 UTC is outside 00:00-01:00
	assert.False(t, windowOpen(p, domain.AttributeView{"site.timezone": "garbage"}, at, "also/garbage"))
	utcOnly := policy(domain.MaintenanceWindow{Start: "22:00", End: "23:00"})
	utcOnly.DefaultTimezone = ""
	assert.True(t, windowOpen(utcOnly, nil, at, ""))
	// "Local" must never resolve to the host zone
	assert.False(t, windowOpen(p, domain.AttributeView{"site.timezone": "Local"}, at, ""))
	// custom attribute name
	p3 := policy(domain.MaintenanceWindow{Start: "00:00", End: "01:00"})
	p3.TimezoneAttribute = "site.tz"
	assert.True(t, windowOpen(p3, domain.AttributeView{"site.tz": "Europe/Berlin", "site.timezone": "America/Los_Angeles"}, at, "UTC"))
}

// Europe/Berlin switches to summer time on 2026-03-29 (02:00 -> 03:00) and
// back on 2026-10-25 (03:00 -> 02:00).
func TestWindowSpringForward(t *testing.T) {
	view := domain.AttributeView{"site.timezone": "Europe/Berlin"}
	berlin := mustLoc(t, "Europe/Berlin")
	// Window Sat 21:30 -> Sun 06:30 starts on 2026-03-28 (CET) and ends in CEST: only 8 hours long.
	// 05:59 CEST on the 29th is 03:59 UTC; 06:30 CEST is 04:30 UTC.
	end := time.Date(2026, 3, 29, 6, 30, 0, 0, berlin)
	require.Equal(t, 4, end.UTC().Hour())
	sat := policy(domain.MaintenanceWindow{Days: []string{"Sat"}, Start: "21:30", End: "06:30", Timezone: "agent"})
	assert.True(t, windowOpen(sat, view, end.Add(-time.Minute), "UTC"))
	assert.False(t, windowOpen(sat, view, end, "UTC"), "closes at 06:30 local CEST")
	// the instant 06:30 CET-equivalent (05:30 UTC) is long past the end
	assert.False(t, windowOpen(sat, view, time.Date(2026, 3, 29, 5, 30, 0, 0, time.UTC), "UTC"))
	assert.True(t, windowOpen(sat, view, time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), "UTC"), "01:30 CET")
	// The nightly window 01:00-05:00 is shorter on that night: 01:00 CET .. 05:00 CEST = 3 real hours.
	nightly := policy(domain.MaintenanceWindow{Start: "01:00", End: "05:00", Timezone: "agent"})
	open := 0
	for m := 0; m < 6*60; m++ {
		at := time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
		if windowOpen(nightly, view, at, "UTC") {
			open++
		}
	}
	assert.Equal(t, 3*60, open, "03/29: 3 real hours between 01:00 CET and 05:00 CEST (02:00-03:00 does not exist)")
	// Window inside the non-existing hour: 02:30-04:00 local. The start is normalized by time.Date; the
	// window must still be open at 03:30 CEST and closed at 04:00 CEST.
	gap := policy(domain.MaintenanceWindow{Start: "02:30", End: "04:00", Timezone: "agent"})
	assert.True(t, windowOpen(gap, view, time.Date(2026, 3, 29, 3, 30, 0, 0, berlin), "UTC"))
	assert.False(t, windowOpen(gap, view, time.Date(2026, 3, 29, 4, 0, 0, 0, berlin), "UTC"))
	assert.False(t, windowOpen(gap, view, time.Date(2026, 3, 29, 1, 59, 0, 0, berlin), "UTC"))
}

func TestWindowFallBack(t *testing.T) {
	view := domain.AttributeView{"site.timezone": "Europe/Berlin"}
	nightly := policy(domain.MaintenanceWindow{Start: "01:00", End: "05:00", Timezone: "agent"})
	open := 0
	for m := 0; m < 6*60; m++ {
		at := time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute) // 00:00 CEST on 10/25 ...
		if windowOpen(nightly, view, at, "UTC") {
			open++
		}
	}
	assert.Equal(t, 5*60, open, "10/25: 5 real hours between 01:00 CEST and 05:00 CET (02:00-03:00 happens twice)")
	// The repeated hour 02:30 exists twice and both are inside the window.
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)  // 02:30 CEST
	second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC) // 02:30 CET
	assert.True(t, windowOpen(nightly, view, first, "UTC"))
	assert.True(t, windowOpen(nightly, view, second, "UTC"))
	// A Saturday-start midnight window across the autumn change: 21:30 CEST (Sat 10/24) .. 06:30 CET (Sun 10/25) is 9h 0m... (10h real)
	sat := policy(domain.MaintenanceWindow{Days: []string{"Sat"}, Start: "21:30", End: "06:30", Timezone: "agent"})
	assert.True(t, windowOpen(sat, view, time.Date(2026, 10, 25, 5, 29, 0, 0, time.UTC), "UTC"), "06:29 CET")
	assert.False(t, windowOpen(sat, view, time.Date(2026, 10, 25, 5, 30, 0, 0, time.UTC), "UTC"), "06:30 CET")
}

func TestWindowInvalidDefinitionsNeverOpen(t *testing.T) {
	p := policy(domain.MaintenanceWindow{Start: "25:00", End: "06:00"}, domain.MaintenanceWindow{Start: "xx", End: "yy"})
	assert.False(t, windowOpen(p, nil, time.Now(), "UTC"))
	// unknown day names are ignored; remaining valid days apply
	p2 := policy(domain.MaintenanceWindow{Days: []string{"Funday", "Wed"}, Start: "00:00", End: "00:00", Timezone: "UTC"})
	assert.True(t, windowOpen(p2, nil, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), "UTC"))
	assert.False(t, windowOpen(p2, nil, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), "UTC"))
}
