package helpers

import "time"

// The GMS culvert reset moved from Sunday to Wednesday keys with the patch on
// this date. Dates before it use Sunday-era handling for historical lookups.
var culvertResetChangedFromSundayToWednesday, _ = time.Parse(time.DateOnly, "2024-08-28")

// Culvert weeks are KEYED by a date string (the DB primary key): Wednesday
// since 2024-08-28, Sunday before that. The week keyed K does NOT start at
// K 00:00 - it spans the half-open instant range [K+1d 00:00 UTC, K+8d 00:00
// UTC), because the in-game weekly reset happens at Thursday 00:00 UTC (the
// day AFTER the Wednesday key). Two distinct helpers cover the two questions:
//
//   - CurrentCulvertWeek(now): which week key an INSTANT belongs to. Use it
//     for every "the current week" default keyed off time.Now().
//   - GetCulvertResetDate(d): which week key a user-typed date LABEL names.
//     A typed YYYY-MM-DD is normalized by walking back to the key date on the
//     calendar (a Saturday names the week of the Wednesday before it).

func GetCulvertResetDay(asOf time.Time) time.Weekday {
	if asOf.Before(culvertResetChangedFromSundayToWednesday) {
		return time.Sunday
	}

	return time.Wednesday
}

// GetCulvertResetDate normalizes an EXPLICIT user-typed date to its week key
// by walking back to the key weekday on the calendar. It is a LABEL helper:
// never feed it time.Now() to find the current week - a calendar walk-back
// rolls into the new week a full day early (all of Wednesday UTC), use
// CurrentCulvertWeek for instants instead.
func GetCulvertResetDate(thisWeek time.Time) time.Time {
	for thisWeek.Weekday() != GetCulvertResetDay(thisWeek) {
		thisWeek = thisWeek.Add(time.Hour * -24)
	}
	return thisWeek
}

// GetCulvertPreviousDate returns the week key before the given key (key minus
// 7 days), following the same Sunday-era handling across the 2024-08-28
// boundary.
func GetCulvertPreviousDate(thisWeek time.Time) time.Time {
	thisWeek = thisWeek.Add(-24 * time.Hour)
	for thisWeek.Weekday() != GetCulvertResetDay(thisWeek) {
		thisWeek = thisWeek.Add(time.Hour * -24)
	}
	return thisWeek
}

// CurrentCulvertWeek returns the week KEY the instant `now` belongs to: the
// most recent weekly reset instant (Thursday 00:00 UTC) at or before now,
// minus 24h - i.e. the Wednesday date key. Computed explicitly in time.UTC;
// the process-local timezone never participates. Instants before the
// 2024-08-28 changeover follow the analogous Sunday-era rule (reset instant
// Monday 00:00 UTC, keyed by the Sunday before it).
func CurrentCulvertWeek(now time.Time) time.Time {
	nowUTC := now.UTC()
	key := currentKeyFor(nowUTC, time.Thursday)
	if key.Before(culvertResetChangedFromSundayToWednesday) {
		key = currentKeyFor(nowUTC, time.Monday)
	}
	return key
}

// currentKeyFor finds the most recent `resetDay` 00:00 UTC at or before
// nowUTC and returns the day before it (the week key date).
func currentKeyFor(nowUTC time.Time, resetDay time.Weekday) time.Time {
	reset := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	for reset.Weekday() != resetDay {
		reset = reset.AddDate(0, 0, -1)
	}
	return reset.AddDate(0, 0, -1)
}

// FormatWeekLabel renders a week key as "the week of YYYY-MM-DD", appending
// " (this week)" when it is the culvert week the instant `now` belongs to.
// Every receipt, conflict message and thread note labels weeks through this
// one helper.
func FormatWeekLabel(week, now time.Time) string {
	label := "the week of " + week.Format(time.DateOnly)
	if week.Format(time.DateOnly) == CurrentCulvertWeek(now).Format(time.DateOnly) {
		label += " (this week)"
	}
	return label
}
