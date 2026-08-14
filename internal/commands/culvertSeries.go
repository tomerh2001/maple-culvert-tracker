package commands

import (
	"sort"

	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// maxCulvertChartSeries caps how many character lines a single multi-character
// culvert chart draws. Members almost always have 1-4 characters; the cap is an
// edge guard so a member with a huge roster still produces a readable chart.
// The highest-latest-score characters win; the rest are reported as "+N more".
const maxCulvertChartSeries = 12

// culvertCharSeries is one character's score points feeding the multi-series
// chart. points carry RawDate ("YYYY-MM-DD") and Score; order does not matter.
type culvertCharSeries struct {
	name   string
	points []data.ChartMakerPoints
}

// latestScore returns the score at the character's most recent week (max
// RawDate). ok is false when the character has no points.
func (c culvertCharSeries) latestScore() (score int, raw string, ok bool) {
	for _, p := range c.points {
		if !ok || p.RawDate > raw {
			raw = p.RawDate
			score = p.Score
			ok = true
		}
	}
	return score, raw, ok
}

// buildCulvertSeries turns a member's characters and their score points into a
// single multi-line chart payload: one dataset per character aligned to the
// union of week labels, with a nil (JSON null) gap for any week a character has
// no score. Characters with no points at all are dropped. Series are ordered by
// latest score descending (name as a stable tie-break) and capped at maxSeries;
// the number of characters the cap dropped is returned as "more". ok is false
// when no character has any data - there is nothing to chart.
//
// It is pure so the resolution + alignment logic can be unit tested without a
// database or the chartmaker service.
func buildCulvertSeries(series []culvertCharSeries, maxSeries int) (payload data.ChartMakeMultiplePoints, more int, ok bool) {
	// Keep only characters that actually have data in the window.
	type ranked struct {
		s      culvertCharSeries
		latest int
	}
	active := make([]ranked, 0, len(series))
	for _, s := range series {
		if score, _, has := s.latestScore(); has {
			active = append(active, ranked{s: s, latest: score})
		}
	}
	if len(active) == 0 {
		return data.ChartMakeMultiplePoints{}, 0, false
	}

	// Highest latest score first; character name breaks ties deterministically.
	sort.SliceStable(active, func(a, b int) bool {
		if active[a].latest != active[b].latest {
			return active[a].latest > active[b].latest
		}
		return active[a].s.name < active[b].s.name
	})

	if maxSeries > 0 && len(active) > maxSeries {
		more = len(active) - maxSeries
		active = active[:maxSeries]
	}

	// Union of week keys across the charted characters, sorted chronologically.
	// RawDate is "YYYY-MM-DD" so lexical order is chronological order.
	seen := map[string]struct{}{}
	rawDates := []string{}
	for _, r := range active {
		for _, p := range r.s.points {
			if _, dup := seen[p.RawDate]; !dup {
				seen[p.RawDate] = struct{}{}
				rawDates = append(rawDates, p.RawDate)
			}
		}
	}
	sort.Strings(rawDates)

	labels := make([]string, len(rawDates))
	index := make(map[string]int, len(rawDates))
	for i, raw := range rawDates {
		index[raw] = i
		// "YYYY-MM-DD" -> "MM-DD", matching the single-series label format.
		if len(raw) >= 10 {
			labels[i] = raw[5:10]
		} else {
			labels[i] = raw
		}
	}

	plots := make([]data.DataPlot, 0, len(active))
	for _, r := range active {
		scores := make([]*int, len(rawDates))
		for _, p := range r.s.points {
			if i, okIdx := index[p.RawDate]; okIdx {
				v := p.Score
				scores[i] = &v
			}
		}
		plots = append(plots, data.DataPlot{CharacterName: r.s.name, Scores: scores})
	}

	return data.ChartMakeMultiplePoints{Labels: labels, DataPlots: plots}, more, true
}
