package helpers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// An empty week renders a stable "nothing yet" personal-best embed without
// touching the DB - the path /reset-week relies on so the thread clears
// instead of crashing.
func TestBuildWeekPBEmbedEmpty(t *testing.T) {
	e := buildWeekPBEmbed(nil, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), nil)
	if e == nil {
		t.Fatal("buildWeekPBEmbed returned nil")
	}
	if e.Title != "New personal bests this week :tada:" {
		t.Errorf("PB embed title = %q", e.Title)
	}
	if e.Description != "No scores recorded yet this week." {
		t.Errorf("empty-week PB description = %q", e.Description)
	}
	if e.Color != weekEmbedColor {
		t.Errorf("PB embed color = %d, want %d", e.Color, weekEmbedColor)
	}
}

// The channel summary is an embed: title "Week of <date>", guild total in the
// description, the top three as a 2-column podium grid (medal/place + backticked
// name / score), and a trailing full-width coverage field.
func TestBuildWeeklySummaryEmbed(t *testing.T) {
	rows := []weekScore{
		{Name: "Alpha", Score: 300, Rank: 1},
		{Name: "Beta", Score: 200, Rank: 2},
	}

	curWeek := cmdhelpers.CurrentCulvertWeek(time.Now())
	curStr := curWeek.Format(time.DateOnly)
	e := buildWeeklySummary(curWeek, curStr, 3, rows)
	if want := "Week of " + curStr; e.Title != want {
		t.Errorf("title = %q, want %q", e.Title, want)
	}
	if e.Color != weekEmbedColor {
		t.Errorf("summary color = %d, want %d", e.Color, weekEmbedColor)
	}
	if e.Footer == nil || e.Footer.Text != "See /culvert-help for more information" {
		t.Errorf("summary footer = %+v", e.Footer)
	}
	if !strings.Contains(e.Description, "Guild total: **500**") {
		t.Errorf("summary description = %q, want guild total", e.Description)
	}
	// Podium grid (2 inline columns) + a trailing full-width coverage field.
	if len(e.Fields) != 3 {
		t.Fatalf("expected 3 fields (podium + coverage), got %d", len(e.Fields))
	}
	if !e.Fields[0].Inline || !e.Fields[1].Inline {
		t.Errorf("podium columns must be inline")
	}
	if e.Fields[2].Inline {
		t.Errorf("coverage field must be full-width (not inline)")
	}
	if want := ":first_place: `Alpha`\n:second_place: `Beta`"; e.Fields[0].Value != want {
		t.Errorf("place+character column = %q, want %q", e.Fields[0].Value, want)
	}
	if e.Fields[1].Value != "300\n200" {
		t.Errorf("score column = %q, want 300/200", e.Fields[1].Value)
	}
	if !strings.Contains(e.Fields[2].Value, "2 of 3 members submitted") {
		t.Errorf("coverage field = %q", e.Fields[2].Value)
	}

	// A past week -> title is still just "Week of <date>".
	pastStr := "2020-01-01"
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if p := buildWeeklySummary(past, pastStr, 3, rows); p.Title != "Week of "+pastStr {
		t.Errorf("past-week title = %q", p.Title)
	}

	// An empty week still renders coverage (in the trailing field), not a crash.
	em := buildWeeklySummary(past, pastStr, 5, nil)
	foundCoverage := false
	for _, f := range em.Fields {
		if strings.Contains(f.Value, "0 of 5 members submitted") {
			foundCoverage = true
		}
	}
	if !foundCoverage {
		t.Errorf("empty-week coverage missing: fields=%+v desc=%q", em.Fields, em.Description)
	}
}

// BuildWeekTableEmbed renders the board as a 2-column inline-field grid
// ("rank + character" and score), capping at the top 25 rows while the footer's
// coverage still counts EVERY submitter and flags the cap.
func TestBuildWeekTableEmbedTop25Cap(t *testing.T) {
	rows := make([]weekScore, 0, 30)
	for rank := 1; rank <= 30; rank++ {
		rows = append(rows, weekScore{
			CharacterID: int64(rank),
			Name:        namePad(rank),
			Score:       10000 - rank, // strictly descending
			Rank:        rank,
		})
	}

	e := BuildWeekTableEmbed("Top 25", 30, rows, nil)

	// Coverage counts ALL submitters and notes the cap.
	if e.Footer == nil || e.Footer.Text != "30 of 30 members submitted • top 25 shown" {
		t.Errorf("coverage footer = %+v", e.Footer)
	}
	// Two side-by-side inline columns: "rank + character" and score.
	if len(e.Fields) != 2 {
		t.Fatalf("expected 2 inline fields, got %d", len(e.Fields))
	}
	for _, f := range e.Fields {
		if !f.Inline {
			t.Errorf("field %q must be inline for a grid layout", f.Name)
		}
		if got := strings.Count(f.Value, "\n") + 1; got != 25 {
			t.Errorf("field %q rendered %d rows, want 25 (cap)", f.Name, got)
		}
	}
	// Rank + backticked name live in the first column; rank 25 rendered (with
	// its number and backticks), rank 26 capped.
	col := e.Fields[0].Value
	if !strings.Contains(col, "25 `"+namePad(25)+"`") {
		t.Errorf("rank 25 row (25 `%s`) should be rendered:\n%s", namePad(25), col)
	}
	if strings.Contains(col, namePad(26)) {
		t.Errorf("rank 26 (%s) should be capped out", namePad(26))
	}

	// 25 rows exactly -> no cap note in the footer.
	if e25 := BuildWeekTableEmbed("Top 25", 25, rows[:25], nil); strings.Contains(e25.Footer.Text, "top 25 shown") {
		t.Errorf("exactly 25 rows must not flag the cap: %q", e25.Footer.Text)
	}
}

func namePad(rank int) string {
	return fmt.Sprintf("Char%02d", rank)
}
