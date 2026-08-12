package helpers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"testing"
	"time"
)

// This file gates the anchor pipeline across rescaled variants of a REAL
// screenshot. The old gauntlet rescaled a synthetic window built from
// rendered 1x templates - it exercised the retired pitch-guessing ladder and
// modelled nothing a real client produces (real windows carry the smooth-font
// title and header this pipeline anchors on). The new gauntlet takes
// provided/real-sample.png (smooth 2x, quantized colors - the anchor is in
// frame) and upscales it the two ways image hosts and clients do: crisp
// nearest-neighbour and smooth bilinear, at 1.25/1.5/1.75/2.5 of ITS scale.
// The anchor must find the title at the compound scale and recover nearly
// every row.

// scoreEntries counts rows whose (name, score) both match expected.
func scoreEntries(entries []ScoreEntry, expected map[string]int) int {
	got := 0
	seen := map[string]bool{}
	for _, e := range entries {
		if !seen[e.Name] && expected[e.Name] == e.Score {
			seen[e.Name] = true
			got++
		}
	}
	return got
}

// TestAnchorScaleGauntlet parses every rescale variant of the real sample and
// requires >= 15/17 exact rows each (a row counts only when both the name and
// the culvert score are exact).
func TestAnchorScaleGauntlet(t *testing.T) {
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
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	type variant struct {
		label string
		img   image.Image
	}
	variants := []variant{}
	for _, f := range []float64{1.25, 1.5, 1.75, 2.5} {
		variants = append(variants,
			variant{fmt.Sprintf("nearest-%.2fx", f), upscaleNearest(src, f)},
			variant{fmt.Sprintf("bilinear-%.2fx", f), upscaleBilinear(src, f)},
		)
	}

	for _, v := range variants {
		start := time.Now()
		res, err := ParseParticipation(encodePNG(t, v.img), members, font)
		if err != nil {
			t.Fatalf("%s: %v", v.label, err)
		}
		correct := scoreEntries(res.Rows, expected)
		t.Logf("%s: %d/%d rows exact in %.2fs (engine %s, scale %.2f, anchor ncc %.3f)",
			v.label, correct, len(expected), time.Since(start).Seconds(), res.Engine, res.Scale, res.AnchorNCC)
		if !res.AnchorFound {
			t.Errorf("%s: anchor not found", v.label)
		}
		if correct < 15 {
			t.Errorf("%s: %d/%d rows exact, want >= 15", v.label, correct, len(expected))
		}
	}
}
