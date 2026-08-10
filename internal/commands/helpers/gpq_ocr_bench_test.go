package helpers

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This file benchmarks fractional-scale parsing over a gauntlet of synthetic
// full-window screenshots (a real fixture table inside rendered window
// chrome), upscaled the two ways real clients do it: crisp nearest-neighbour
// and smooth bilinear. Two approaches are scored:
//
//   - scaled-template (shipped): ParseParticipationImage upscales the glyph
//     templates to the detected UI scale and matches at native resolution.
//   - downscale+phase (alternative): shrink the image back toward 1x with a
//     phase-swept point/area sampler and match with the 1x templates.
//
// A row counts only if both the name and the culvert score are exact.

var gauntletScales = []float64{1.25, 1.35, 1.5, 1.65, 1.75, 2.0}

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

// downscaleByPhase shrinks img by factor f, point-sampling at offset delta
// (in destination-pixel units) into each source box.
func downscaleByPhase(img image.Image, f, delta float64) *image.RGBA {
	b := img.Bounds()
	w := int(float64(b.Dx()) / f)
	h := int(float64(b.Dy()) / f)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := int((float64(y) + delta) * f)
		if sy >= b.Dy() {
			sy = b.Dy() - 1
		}
		for x := 0; x < w; x++ {
			sx := int((float64(x) + delta) * f)
			if sx >= b.Dx() {
				sx = b.Dx() - 1
			}
			pr, pg, pb, _ := img.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
			i := out.PixOffset(x, y)
			out.Pix[i] = uint8(pr >> 8)
			out.Pix[i+1] = uint8(pg >> 8)
			out.Pix[i+2] = uint8(pb >> 8)
			out.Pix[i+3] = 0xFF
		}
	}
	return out
}

// downscaleArea shrinks img by factor f, averaging each source box.
func downscaleArea(img image.Image, f float64) *image.RGBA {
	b := img.Bounds()
	w := int(float64(b.Dx()) / f)
	h := int(float64(b.Dy()) / f)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy0 := int(float64(y) * f)
		sy1 := int(float64(y+1) * f)
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		if sy1 > b.Dy() {
			sy1 = b.Dy()
		}
		for x := 0; x < w; x++ {
			sx0 := int(float64(x) * f)
			sx1 := int(float64(x+1) * f)
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sx1 > b.Dx() {
				sx1 = b.Dx()
			}
			var r, g, bl, n uint32
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					pr, pg, pb, _ := img.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r += pr >> 8
					g += pg >> 8
					bl += pb >> 8
					n++
				}
			}
			i := out.PixOffset(x, y)
			out.Pix[i] = uint8(r / n)
			out.Pix[i+1] = uint8(g / n)
			out.Pix[i+2] = uint8(bl / n)
			out.Pix[i+3] = 0xFF
		}
	}
	return out
}

// parseViaDownscale is the alternative approach: estimate the scale from the
// row pitch, then search a fine grid of factors and sampling phases, shrink
// the image to 1x each time and run the strict 1x parser on it.
func parseViaDownscale(img image.Image, memberNames []string, font *GPQFont) []ScoreEntry {
	grid := binarizeFull(img)
	best, _ := parseParticipationGrid(grid, font, memberNames)
	est := estimateScaleFromPitch(grid)
	if est < gpqMinScaledEst {
		return best
	}
	for _, df := range []float64{0, -0.01, 0.01, -0.02, 0.02, -0.03, 0.03, -0.04, 0.04} {
		f := est + df
		if f < 1.01 {
			continue
		}
		for _, delta := range []float64{0, 1.0 / 3, 2.0 / 3} {
			e, _ := parseParticipationGrid(binarizeFull(downscaleByPhase(img, f, delta)), font, memberNames)
			if len(e) > len(best) {
				best = e
			}
			if len(e) >= 17 {
				return best
			}
		}
		e, _ := parseParticipationGrid(binarizeFull(downscaleArea(img, f)), font, memberNames)
		if len(e) > len(best) {
			best = e
		}
		if len(e) >= 17 {
			return best
		}
	}
	return best
}

// TestParticipationScaleGauntlet scores both approaches over every scale
// variant and prints the comparison table. The shipped approach must reach
// 16/17 on nearest variants and 15/17 on bilinear ones.
func TestParticipationScaleGauntlet(t *testing.T) {
	expected := loadExpected(t, 1)[1]
	members := make([]string, 0, len(expected))
	for name := range expected {
		members = append(members, name)
	}
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatalf("load font: %v", err)
	}
	tableData, err := os.ReadFile(filepath.Join(gpqTestsDir, "1.png"))
	if err != nil {
		t.Fatalf("read 1.png: %v", err)
	}
	table, _, err := image.Decode(bytes.NewReader(tableData))
	if err != nil {
		t.Fatalf("decode 1.png: %v", err)
	}
	window := buildFullWindow(t, font, table)
	total := len(expected)

	type variant struct {
		label string
		img   image.Image
		kind  string // "nearest" | "bilinear"
	}
	variants := []variant{}
	for _, f := range gauntletScales {
		variants = append(variants,
			variant{fmt.Sprintf("nearest-%.2fx", f), upscaleNearest(window, f), "nearest"},
			variant{fmt.Sprintf("bilinear-%.2fx", f), upscaleBilinear(window, f), "bilinear"},
		)
	}

	t.Logf("%-16s %-22s %-22s", "variant", "scaled-template", "downscale+phase")
	sumScaled, sumDown := 0, 0
	for _, v := range variants {
		start := time.Now()
		entries, err := ParseParticipationImage(encodePNG(t, v.img), members, font)
		if err != nil {
			t.Fatalf("%s: %v", v.label, err)
		}
		scaledScore := scoreEntries(entries, expected)
		scaledDur := time.Since(start)

		downScore := -1
		downLabel := "(skipped -short)"
		if !testing.Short() {
			start = time.Now()
			downScore = scoreEntries(parseViaDownscale(v.img, members, font), expected)
			downLabel = fmt.Sprintf("%2d/%d in %6.2fs", downScore, total, time.Since(start).Seconds())
			sumDown += downScore
		}
		sumScaled += scaledScore
		t.Logf("%-16s %2d/%d in %6.2fs    %s", v.label, scaledScore, total, scaledDur.Seconds(), downLabel)

		minRows := 15
		if v.kind == "nearest" {
			minRows = 16
		}
		if scaledScore < minRows {
			t.Errorf("%s: scaled-template got %d/%d rows, want >= %d", v.label, scaledScore, total, minRows)
		}
	}
	if testing.Short() {
		t.Logf("TOTAL scaled-template %d/%d (downscale skipped)", sumScaled, total*len(variants))
	} else {
		t.Logf("TOTAL scaled-template %d/%d   downscale+phase %d/%d",
			sumScaled, total*len(variants), sumDown, total*len(variants))
	}
}
