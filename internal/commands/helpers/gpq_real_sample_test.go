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
