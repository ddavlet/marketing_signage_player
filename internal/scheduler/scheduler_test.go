package scheduler

import (
	"testing"
	"time"

	"github.com/marketing-signage/player/internal/api"
)

func ts(layout, value, tz string) time.Time {
	loc, _ := time.LoadLocation(tz)
	t, _ := time.ParseInLocation(layout, value, loc)
	return t
}

func TestIsOn(t *testing.T) {
	cases := []struct {
		name  string
		sched api.ScreenSchedule
		now   time.Time
		want  bool
	}{
		// No schedule → always on.
		{
			name:  "no schedule",
			sched: api.ScreenSchedule{},
			now:   ts("15:04", "03:00", "UTC"),
			want:  true,
		},
		// Normal window 07:00–23:00, inside.
		{
			name:  "normal window inside",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "UTC"},
			now:   ts("15:04", "12:00", "UTC"),
			want:  true,
		},
		// Normal window, before on.
		{
			name:  "normal window before on",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "UTC"},
			now:   ts("15:04", "06:59", "UTC"),
			want:  false,
		},
		// Normal window, at off time (exclusive).
		{
			name:  "normal window at off",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "UTC"},
			now:   ts("15:04", "23:00", "UTC"),
			want:  false,
		},
		// Overnight window 22:00–06:00, inside (after on).
		{
			name:  "overnight after on",
			sched: api.ScreenSchedule{On: "22:00", Off: "06:00", Timezone: "UTC"},
			now:   ts("15:04", "23:30", "UTC"),
			want:  true,
		},
		// Overnight window, inside (before off).
		{
			name:  "overnight before off",
			sched: api.ScreenSchedule{On: "22:00", Off: "06:00", Timezone: "UTC"},
			now:   ts("15:04", "03:00", "UTC"),
			want:  true,
		},
		// Overnight window, in the gap (should be off).
		{
			name:  "overnight in gap",
			sched: api.ScreenSchedule{On: "22:00", Off: "06:00", Timezone: "UTC"},
			now:   ts("15:04", "12:00", "UTC"),
			want:  false,
		},
		// Timezone: wall clock is 08:00 UTC but schedule is Asia/Tashkent (UTC+5) 07:00–23:00.
		// 08:00 UTC = 13:00 Tashkent → inside.
		{
			name:  "timezone conversion inside",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "Asia/Tashkent"},
			now:   time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC),
			want:  true,
		},
		// 02:00 UTC = 07:00 Tashkent → exactly at on (inclusive).
		{
			name:  "timezone conversion at on",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "Asia/Tashkent"},
			now:   time.Date(2024, 1, 15, 2, 0, 0, 0, time.UTC),
			want:  true,
		},
		// 01:59 UTC = 06:59 Tashkent → one minute before on.
		{
			name:  "timezone conversion before on",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "Asia/Tashkent"},
			now:   time.Date(2024, 1, 15, 1, 59, 0, 0, time.UTC),
			want:  false,
		},
		// Unknown timezone → falls back to UTC.
		{
			name:  "unknown timezone fallback",
			sched: api.ScreenSchedule{On: "07:00", Off: "23:00", Timezone: "Mars/Olympus"},
			now:   ts("15:04", "08:00", "UTC"),
			want:  true,
		},
		// Malformed time strings → safe default (on).
		{
			name:  "malformed on time",
			sched: api.ScreenSchedule{On: "7am", Off: "23:00", Timezone: "UTC"},
			now:   ts("15:04", "08:00", "UTC"),
			want:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsOn(tc.sched, tc.now)
			if got != tc.want {
				t.Errorf("IsOn(%+v, %v) = %v; want %v", tc.sched, tc.now, got, tc.want)
			}
		})
	}
}
