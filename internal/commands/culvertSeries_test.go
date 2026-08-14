package commands

import (
	"reflect"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

func pt(raw string, score int) data.ChartMakerPoints {
	return data.ChartMakerPoints{RawDate: raw, Score: score, Label: raw[5:10]}
}

func ip(v int) *int { return &v }

// scoreView renders a DataPlot's scores as a comparable []any (ints, nil for
// gaps) so reflect.DeepEqual can check gap placement.
func scoreView(scores []*int) []any {
	out := make([]any, len(scores))
	for i, s := range scores {
		if s == nil {
			out[i] = nil
		} else {
			out[i] = *s
		}
	}
	return out
}

func TestBuildCulvertSeries_AlignsAndOrdersByLatestScore(t *testing.T) {
	series := []culvertCharSeries{
		// Given out of chronological order on purpose.
		{name: "Alpha", points: []data.ChartMakerPoints{pt("2026-01-08", 150), pt("2026-01-01", 100)}},
		{name: "Beta", points: []data.ChartMakerPoints{pt("2026-01-01", 200), pt("2026-01-08", 250)}},
	}

	payload, more, ok := buildCulvertSeries(series, 12)
	if !ok {
		t.Fatal("expected ok=true when characters have data")
	}
	if more != 0 {
		t.Fatalf("more = %d, want 0", more)
	}
	if want := []string{"01-01", "01-08"}; !reflect.DeepEqual(payload.Labels, want) {
		t.Fatalf("labels = %v, want %v", payload.Labels, want)
	}
	// Beta's latest (250) beats Alpha's latest (150): Beta must come first.
	if got := []string{payload.DataPlots[0].CharacterName, payload.DataPlots[1].CharacterName}; !reflect.DeepEqual(got, []string{"Beta", "Alpha"}) {
		t.Fatalf("series order = %v, want [Beta Alpha]", got)
	}
	if got, want := scoreView(payload.DataPlots[0].Scores), []any{200, 250}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Beta scores = %v, want %v", got, want)
	}
	if got, want := scoreView(payload.DataPlots[1].Scores), []any{100, 150}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Alpha scores = %v, want %v", got, want)
	}
}

func TestBuildCulvertSeries_MissingWeeksBecomeNullGaps(t *testing.T) {
	series := []culvertCharSeries{
		{name: "Alpha", points: []data.ChartMakerPoints{pt("2026-01-01", 100), pt("2026-01-08", 150), pt("2026-01-15", 160)}},
		// Beta registered late: only the final week has data.
		{name: "Beta", points: []data.ChartMakerPoints{pt("2026-01-15", 900)}},
	}

	payload, _, ok := buildCulvertSeries(series, 12)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if want := []string{"01-01", "01-08", "01-15"}; !reflect.DeepEqual(payload.Labels, want) {
		t.Fatalf("labels = %v, want %v", payload.Labels, want)
	}
	// Beta (latest 900) is first, with gaps before its only week.
	beta := payload.DataPlots[0]
	if beta.CharacterName != "Beta" {
		t.Fatalf("first series = %q, want Beta", beta.CharacterName)
	}
	if got, want := scoreView(beta.Scores), []any{nil, nil, 900}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Beta scores = %v, want %v", got, want)
	}
	if got, want := scoreView(payload.DataPlots[1].Scores), []any{100, 150, 160}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Alpha scores = %v, want %v", got, want)
	}
}

func TestBuildCulvertSeries_CapsSeriesKeepingHighestLatest(t *testing.T) {
	series := []culvertCharSeries{
		{name: "Low", points: []data.ChartMakerPoints{pt("2026-01-01", 100)}},
		{name: "High", points: []data.ChartMakerPoints{pt("2026-01-01", 300)}},
		{name: "Mid", points: []data.ChartMakerPoints{pt("2026-01-01", 200)}},
	}

	payload, more, ok := buildCulvertSeries(series, 2)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if more != 1 {
		t.Fatalf("more = %d, want 1", more)
	}
	got := []string{payload.DataPlots[0].CharacterName, payload.DataPlots[1].CharacterName}
	if want := []string{"High", "Mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept series = %v, want %v", got, want)
	}
}

func TestBuildCulvertSeries_TieBreaksByName(t *testing.T) {
	series := []culvertCharSeries{
		{name: "Zed", points: []data.ChartMakerPoints{pt("2026-01-01", 500)}},
		{name: "Amy", points: []data.ChartMakerPoints{pt("2026-01-01", 500)}},
	}

	payload, _, ok := buildCulvertSeries(series, 12)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if payload.DataPlots[0].CharacterName != "Amy" {
		t.Fatalf("tie order first = %q, want Amy", payload.DataPlots[0].CharacterName)
	}
}

func TestBuildCulvertSeries_DropsCharactersWithoutData(t *testing.T) {
	series := []culvertCharSeries{
		{name: "Empty", points: nil},
		{name: "Has", points: []data.ChartMakerPoints{pt("2026-01-01", 42)}},
	}

	payload, more, ok := buildCulvertSeries(series, 12)
	if !ok {
		t.Fatal("expected ok=true when at least one character has data")
	}
	if more != 0 {
		t.Fatalf("more = %d, want 0", more)
	}
	if len(payload.DataPlots) != 1 || payload.DataPlots[0].CharacterName != "Has" {
		t.Fatalf("plots = %+v, want a single 'Has' series", payload.DataPlots)
	}
	if got, want := scoreView(payload.DataPlots[0].Scores), []any{42}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Has scores = %v, want %v", got, want)
	}
}

func TestBuildCulvertSeries_NoDataReturnsNotOk(t *testing.T) {
	series := []culvertCharSeries{
		{name: "A", points: nil},
		{name: "B", points: []data.ChartMakerPoints{}},
	}

	if _, _, ok := buildCulvertSeries(series, 12); ok {
		t.Fatal("expected ok=false when no character has data")
	}
	if _, _, ok := buildCulvertSeries(nil, 12); ok {
		t.Fatal("expected ok=false for empty input")
	}
}

// Sanity: a real *int is emitted (pointer identity is per-element, not shared).
func TestBuildCulvertSeries_ScorePointersAreDistinct(t *testing.T) {
	series := []culvertCharSeries{
		{name: "A", points: []data.ChartMakerPoints{pt("2026-01-01", 10), pt("2026-01-08", 20)}},
	}
	payload, _, ok := buildCulvertSeries(series, 12)
	if !ok {
		t.Fatal("expected ok=true")
	}
	got := payload.DataPlots[0].Scores
	if *got[0] == *got[1] || got[0] == got[1] {
		t.Fatalf("expected distinct score pointers, got %d and %d", *got[0], *got[1])
	}
	want := []*int{ip(10), ip(20)}
	if *got[0] != *want[0] || *got[1] != *want[1] {
		t.Fatalf("scores = [%d %d], want [10 20]", *got[0], *got[1])
	}
}
