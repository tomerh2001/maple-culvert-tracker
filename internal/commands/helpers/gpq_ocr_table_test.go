package helpers

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// glyphFor returns the first template for r, or nil.
func glyphFor(font *GPQFont, r rune) *gpqGlyph {
	for i := range font.glyphs {
		if font.glyphs[i].r == r {
			return &font.glyphs[i]
		}
	}
	return nil
}

// renderWord draws s at (x, y) using the embedded glyph templates with the
// game's white text colour, returning the x just after the word.
func renderWord(t *testing.T, img *image.RGBA, font *GPQFont, x, y int, s string) int {
	t.Helper()
	white := color.RGBA{255, 255, 255, 255}
	for _, r := range s {
		if r == ' ' {
			x += 4
			continue
		}
		g := glyphFor(font, r)
		if g == nil {
			t.Fatalf("no glyph template for %q", r)
		}
		for gy := 0; gy < g.h; gy++ {
			for gx := 0; gx < g.w; gx++ {
				if g.bits[gy][gx] {
					img.Set(x+gx, y+gy, white)
				}
			}
		}
		x += g.w + 1
	}
	return x
}

// upscaleNearest scales the image by f using nearest-neighbour (crisp UI
// scaling, e.g. Retina or integer game scaling).
func upscaleNearest(img image.Image, f float64) *image.RGBA {
	b := img.Bounds()
	w, h := int(float64(b.Dx())*f), int(float64(b.Dy())*f)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, img.At(b.Min.X+int(float64(x)/f), b.Min.Y+int(float64(y)/f)))
		}
	}
	return out
}

// upscaleBilinear scales the image by f with bilinear filtering (smooth OS or
// client scaling).
func upscaleBilinear(img image.Image, f float64) *image.RGBA {
	b := img.Bounds()
	w, h := int(float64(b.Dx())*f), int(float64(b.Dy())*f)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	at := func(x, y int) (float64, float64, float64) {
		if x >= b.Dx() {
			x = b.Dx() - 1
		}
		if y >= b.Dy() {
			y = b.Dy() - 1
		}
		r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(bl >> 8)
	}
	for y := 0; y < h; y++ {
		sy := float64(y) / f
		y0 := int(sy)
		fy := sy - float64(y0)
		for x := 0; x < w; x++ {
			sx := float64(x) / f
			x0 := int(sx)
			fx := sx - float64(x0)
			r00, g00, b00 := at(x0, y0)
			r10, g10, b10 := at(x0+1, y0)
			r01, g01, b01 := at(x0, y0+1)
			r11, g11, b11 := at(x0+1, y0+1)
			r := r00*(1-fx)*(1-fy) + r10*fx*(1-fy) + r01*(1-fx)*fy + r11*fx*fy
			g := g00*(1-fx)*(1-fy) + g10*fx*(1-fy) + g01*(1-fx)*fy + g11*fx*fy
			bl := b00*(1-fx)*(1-fy) + b10*fx*(1-fy) + b01*(1-fx)*fy + b11*fx*fy
			i := out.PixOffset(x, y)
			out.Pix[i] = uint8(r + 0.5)
			out.Pix[i+1] = uint8(g + 0.5)
			out.Pix[i+2] = uint8(bl + 0.5)
			out.Pix[i+3] = 0xFF
		}
	}
	return out
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func checkEntries(t *testing.T, entries []ScoreEntry, expected map[string]int, keys []string, label string) {
	t.Helper()
	if len(entries) != len(keys) {
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Name+":"+strconv.Itoa(e.Score))
		}
		t.Fatalf("%s: got %d entries, want %d\ngot: %v", label, len(entries), len(keys), got)
	}
	for i, e := range entries {
		if e.Name != keys[i] {
			t.Errorf("%s row %d: name %q, want %q", label, i, e.Name, keys[i])
		}
		if e.Score != expected[keys[i]] {
			t.Errorf("%s row %d (%s): score %d, want %d", label, i, e.Name, e.Score, expected[keys[i]])
		}
	}
}

// TestParticipationAgainstProvided runs the table-locating parser over the
// same pre-cropped fixtures as the legacy parser: it must not regress.
func TestParticipationAgainstProvided(t *testing.T) {
	const total = 12
	expected := loadExpected(t, total)

	memberSet := map[string]bool{}
	for i := 1; i <= total; i++ {
		for name := range expected[i] {
			memberSet[name] = true
		}
	}
	members := make([]string, 0, len(memberSet))
	for name := range memberSet {
		members = append(members, name)
	}

	font, err := LoadGPQFont()
	if err != nil {
		t.Fatalf("load font: %v", err)
	}

	for i := 1; i <= total; i++ {
		data, err := os.ReadFile(filepath.Join(gpqTestsDir, strconv.Itoa(i)+".png"))
		if err != nil {
			t.Fatalf("read %d.png: %v", i, err)
		}
		jsonData, err := os.ReadFile(filepath.Join(gpqTestsDir, strconv.Itoa(i)+".json"))
		if err != nil {
			t.Fatalf("read %d.json: %v", i, err)
		}
		keys := orderedKeys(t, jsonData)
		entries, err := ParseParticipationImage(data, members, font)
		if err != nil {
			t.Fatalf("parse %d.png: %v", i, err)
		}
		checkEntries(t, entries, expected[i], keys, strconv.Itoa(i)+".png")
	}
}

// TestClockExpiryIsPerClock pins the truncation-attribution semantics: one
// pass expiring its own budget (the strict-1x junk pass on a slow machine)
// must not mark candidates governed by a still-healthy clock as truncated.
func TestClockExpiryIsPerClock(t *testing.T) {
	run := &parseRun{}
	expired := run.until(time.Now().Add(-time.Second))
	healthy := run.until(time.Now().Add(time.Hour))

	if !expired.exceeded() {
		t.Fatal("past-deadline clock should report exceeded")
	}
	if !expired.expiredNow() {
		t.Error("expired clock must snapshot as expired")
	}
	if healthy.expiredNow() {
		t.Error("healthy clock must NOT inherit another clock's expiry")
	}
	if !run.truncated.Load() {
		t.Error("run-level flag still records any expiry (nothing-parsed fallback)")
	}
}
