package helpers

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRealWorldSample gates the parser against a real user screenshot:
// smooth 2x UI scale, 4-bit-per-channel quantized colors (re-encoded by an
// image host). Requirements: at least 15 of the 17 rows parsed, ZERO wrong
// scores (a misread is worse than a miss), under 10 seconds.
func TestRealWorldSample(t *testing.T) {
	data, err := os.ReadFile("../../../provided/real-sample.png")
	if err != nil {
		t.Skip("no real sample present")
	}
	raw, err := os.ReadFile("../../../provided/real-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	members := make([]string, 0, len(expected))
	for name := range expected {
		members = append(members, name)
	}
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	entries, err := ParseParticipationImage(data, members, font)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %d/%d rows in %s", len(entries), len(expected), elapsed.Round(time.Millisecond))

	correct := 0
	for _, e := range entries {
		want, ok := expected[e.Name]
		if !ok {
			t.Errorf("parsed unknown character %q (score %d)", e.Name, e.Score)
			continue
		}
		if e.Score != want {
			t.Errorf("WRONG SCORE for %s: got %d, want %d", e.Name, e.Score, want)
			continue
		}
		correct++
	}
	if correct < 15 {
		t.Errorf("only %d/17 rows correct, need >= 15", correct)
	}
	if elapsed > 10*time.Second {
		t.Errorf("parse took %s, need < 10s", elapsed)
	}
}

// TestRealWorldSampleProductionRoster replays the bug that shipped: the same
// screenshot parsed with the production 3-name roster {HTomer, Senpapi,
// StarryLeyva}. Most rows must come back raw (unmatched names are allowed to
// stay literal), no matched row may carry a wrong score, and the "Xenpapi"
// row must stay distinct instead of being reconciled onto "Senpapi".
func TestRealWorldSampleProductionRoster(t *testing.T) {
	data, err := os.ReadFile("../../../provided/real-sample.png")
	if err != nil {
		t.Skip("no real sample present")
	}
	raw, err := os.ReadFile("../../../provided/real-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	roster := []string{"HTomer", "Senpapi", "StarryLeyva"}
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}

	entries, err := ParseParticipationImage(data, roster, font)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %d rows with production roster", len(entries))
	for _, e := range entries {
		t.Logf("  %s: %d", e.Name, e.Score)
	}

	if len(entries) < 15 {
		t.Errorf("only %d rows parsed, need >= 15", len(entries))
	}

	rosterSet := map[string]bool{}
	for _, m := range roster {
		rosterSet[m] = true
	}
	byName := map[string]int{}
	for _, e := range entries {
		byName[e.Name] = e.Score
		if rosterSet[e.Name] && e.Score != expected[e.Name] {
			t.Errorf("WRONG SCORE for matched member %s: got %d, want %d", e.Name, e.Score, expected[e.Name])
		}
	}

	if got, ok := byName["Senpapi"]; !ok {
		t.Errorf("no Senpapi row parsed")
	} else if got != 220191 {
		t.Errorf("Senpapi = %d, want 220191", got)
	}
	if got, ok := byName["Xenpapi"]; !ok {
		t.Errorf("no distinct Xenpapi row: the decode must stay raw, not reconcile onto Senpapi")
	} else if got != 214530 {
		t.Errorf("Xenpapi = %d, want 214530", got)
	}
}
