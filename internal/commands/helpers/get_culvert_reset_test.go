package helpers

import (
	"testing"
	"time"
)

func mustUTC(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04", value)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", value, err)
	}
	return parsed.UTC()
}

// TestCurrentCulvertWeekBoundary pins the reset INSTANT semantics: the week
// rolls over at Thursday 00:00 UTC, not at Wednesday's calendar date. Weekday
// arithmetic is verified with Go itself, never hand-assumed.
func TestCurrentCulvertWeekBoundary(t *testing.T) {
	cases := []struct {
		name    string
		instant string // UTC
		wantKey string
	}{
		// 2026-08-05 and 2026-08-12 are Wednesdays (asserted below).
		{"Wednesday 23:59 UTC still maps to the PREVIOUS Wednesday key", "2026-08-12 23:59", "2026-08-05"},
		{"Thursday 00:00 UTC exactly rolls onto that Wednesday key", "2026-08-13 00:00", "2026-08-12"},
		{"Thursday 12:00 UTC stays on that Wednesday key", "2026-08-13 12:00", "2026-08-12"},
		{"Saturday maps the same as its preceding Thursday", "2026-08-15 18:30", "2026-08-12"},
		{"Tuesday still belongs to the previous Wednesday key", "2026-08-18 09:00", "2026-08-12"},
		{"Wednesday 00:00 UTC is still the old week (a full day before reset)", "2026-08-12 00:00", "2026-08-05"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CurrentCulvertWeek(mustUTC(t, c.instant)).Format(time.DateOnly)
			if got != c.wantKey {
				t.Errorf("CurrentCulvertWeek(%s) = %s, want %s", c.instant, got, c.wantKey)
			}
		})
	}

	// Verify the weekday assumptions with Go itself.
	for _, wed := range []string{"2026-08-05", "2026-08-12"} {
		d, err := time.Parse(time.DateOnly, wed)
		if err != nil || d.Weekday() != time.Wednesday {
			t.Fatalf("test premise broken: %s is %v, want Wednesday", wed, d.Weekday())
		}
	}
	if d := mustUTC(t, "2026-08-13 00:00"); d.Weekday() != time.Thursday {
		t.Fatalf("test premise broken: 2026-08-13 is %v, want Thursday", d.Weekday())
	}
	if d := mustUTC(t, "2026-08-15 18:30"); d.Weekday() != time.Saturday {
		t.Fatalf("test premise broken: 2026-08-15 is %v, want Saturday", d.Weekday())
	}
}

// TestCurrentCulvertWeekIgnoresLocalTZ feeds the same instant expressed in a
// non-UTC zone and expects the identical key.
func TestCurrentCulvertWeekIgnoresLocalTZ(t *testing.T) {
	// Thursday 00:30 UTC == Thursday 03:30 Israel summer time (UTC+3).
	utc := mustUTC(t, "2026-08-13 00:30")
	israel := utc.In(time.FixedZone("IDT", 3*60*60))
	if got, want := CurrentCulvertWeek(israel).Format(time.DateOnly), "2026-08-12"; got != want {
		t.Errorf("CurrentCulvertWeek(IDT instant) = %s, want %s", got, want)
	}
	// One hour before the reset in a zone where the local date is already
	// Thursday: still the previous key.
	preReset := mustUTC(t, "2026-08-12 23:00").In(time.FixedZone("IDT", 3*60*60))
	if got, want := CurrentCulvertWeek(preReset).Format(time.DateOnly), "2026-08-05"; got != want {
		t.Errorf("CurrentCulvertWeek(pre-reset IDT instant) = %s, want %s", got, want)
	}
}

// TestCurrentCulvertWeekSundayEra covers the pre-2024-08-28 historical rule.
func TestCurrentCulvertWeekSundayEra(t *testing.T) {
	// 2024-06-01 is a Saturday (verified below); the Sunday-era reset instant
	// is Monday 00:00 UTC keyed by the preceding Sunday, 2024-05-26.
	instant := mustUTC(t, "2024-06-01 12:00")
	if instant.Weekday() != time.Saturday {
		t.Fatalf("test premise broken: 2024-06-01 is %v, want Saturday", instant.Weekday())
	}
	got := CurrentCulvertWeek(instant)
	if got.Weekday() != time.Sunday {
		t.Fatalf("Sunday-era key weekday = %v, want Sunday", got.Weekday())
	}
	if want := "2024-05-26"; got.Format(time.DateOnly) != want {
		t.Errorf("CurrentCulvertWeek(sunday era) = %s, want %s", got.Format(time.DateOnly), want)
	}
}

// TestGetCulvertResetDateStaysCalendarBased pins the LABEL helper's behavior:
// a user-typed Wednesday names itself, any later day of the calendar week
// walks back to it.
func TestGetCulvertResetDateStaysCalendarBased(t *testing.T) {
	wed, _ := time.Parse(time.DateOnly, "2026-08-12")
	if got := GetCulvertResetDate(wed).Format(time.DateOnly); got != "2026-08-12" {
		t.Errorf("label of a Wednesday = %s, want itself", got)
	}
	sat, _ := time.Parse(time.DateOnly, "2026-08-15")
	if got := GetCulvertResetDate(sat).Format(time.DateOnly); got != "2026-08-12" {
		t.Errorf("label of the following Saturday = %s, want 2026-08-12", got)
	}
	// Sunday-era historical label.
	old, _ := time.Parse(time.DateOnly, "2024-06-01")
	if got := GetCulvertResetDate(old); got.Weekday() != time.Sunday {
		t.Errorf("pre-changeover label weekday = %v, want Sunday", got.Weekday())
	}
}

func TestGetCulvertPreviousDateIsKeyMinusSevenDays(t *testing.T) {
	key, _ := time.Parse(time.DateOnly, "2026-08-12")
	if got := GetCulvertPreviousDate(key).Format(time.DateOnly); got != "2026-08-05" {
		t.Errorf("previous of 2026-08-12 = %s, want 2026-08-05", got)
	}
}

func TestFormatWeekLabel(t *testing.T) {
	now := mustUTC(t, "2026-08-14 10:00") // Friday; current key = 2026-08-12
	cur, _ := time.Parse(time.DateOnly, "2026-08-12")
	prev, _ := time.Parse(time.DateOnly, "2026-08-05")
	if got, want := FormatWeekLabel(cur, now), "the week of 2026-08-12 (this week)"; got != want {
		t.Errorf("FormatWeekLabel(current) = %q, want %q", got, want)
	}
	if got, want := FormatWeekLabel(prev, now), "the week of 2026-08-05"; got != want {
		t.Errorf("FormatWeekLabel(previous) = %q, want %q", got, want)
	}
	// On Wednesday itself (23:59 UTC) the still-running week is the PREVIOUS
	// key - the label must agree with CurrentCulvertWeek.
	lateWed := mustUTC(t, "2026-08-12 23:59")
	if got, want := FormatWeekLabel(prev, lateWed), "the week of 2026-08-05 (this week)"; got != want {
		t.Errorf("FormatWeekLabel(prev on late Wednesday) = %q, want %q", got, want)
	}
}
