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

// buildFullWindow pastes a real pre-cropped fixture table into a synthetic
// Guild window: dark chrome, a rendered header row, sidebar button text and a
// TIP box, mimicking a full-window screenshot.
func buildFullWindow(t *testing.T, font *GPQFont, table image.Image) *image.RGBA {
	t.Helper()
	tb := table.Bounds()
	const tableX, tableY = 300, 160
	w := tableX + tb.Dx() + 80
	h := tableY + tb.Dy() + 120
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{32, 33, 36, 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, bg)
		}
	}

	// Table body (real fixture pixels).
	for y := 0; y < tb.Dy(); y++ {
		for x := 0; x < tb.Dx(); x++ {
			out.Set(tableX+x, tableY+y, table.At(tb.Min.X+x, tb.Min.Y+y))
		}
	}

	// Header row above the table: name column starts at tableX; culvert
	// numbers live at tableX+305..tableX+415; flag race right-aligns at the
	// table's right edge.
	hy := tableY - 22
	renderWord(t, out, font, tableX+8, hy, "Name")
	renderWord(t, out, font, tableX+90, hy, "Job")
	renderWord(t, out, font, tableX+170, hy, "Level")
	renderWord(t, out, font, tableX+215, hy, "Title")
	renderWord(t, out, font, tableX+262, hy-6, "Weekly")
	renderWord(t, out, font, tableX+260, hy+6, "Mission")
	renderWord(t, out, font, tableX+330, hy, "Culvert")
	renderWord(t, out, font, tableX+420, hy-6, "Flag")
	renderWord(t, out, font, tableX+418, hy+6, "Race")

	// Sidebar buttons and window title (junk that must be ignored).
	renderWord(t, out, font, 40, 30, "Guild")
	renderWord(t, out, font, 60, tableY+40, "Guild Info")
	renderWord(t, out, font, 60, tableY+90, "Members")
	renderWord(t, out, font, 60, tableY+140, "Guild Skills")
	renderWord(t, out, font, 60, tableY+190, "Bulletin Board")
	renderWord(t, out, font, 60, tableY+240, "Alliance")
	renderWord(t, out, font, 60, tableY+290, "Guild Contents")

	// TIP box below the table.
	ty := tableY + tb.Dy() + 30
	renderWord(t, out, font, tableX+20, ty, "TIP")
	renderWord(t, out, font, tableX+20, ty+18, "You can change a members rank by right clicking")
	renderWord(t, out, font, tableX+20, ty+36, "Use the Change Members Ranks button to change all members ranks")
	return out
}

// upscale2x nearest-neighbour doubles the image (Retina-style screenshot).
func upscale2x(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*2, b.Dy()*2))
	for y := 0; y < b.Dy()*2; y++ {
		for x := 0; x < b.Dx()*2; x++ {
			out.Set(x, y, img.At(b.Min.X+x/2, b.Min.Y+y/2))
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

// TestParticipationFullWindow validates parsing a synthetic full Guild window
// (header + sidebar + TIP around a real fixture table), at 1x and 2x scale.
func TestParticipationFullWindow(t *testing.T) {
	expected := loadExpected(t, 1)[1]
	jsonData, err := os.ReadFile(filepath.Join(gpqTestsDir, "1.json"))
	if err != nil {
		t.Fatalf("read 1.json: %v", err)
	}
	keys := orderedKeys(t, jsonData)
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

	entries, err := ParseParticipationImage(encodePNG(t, window), members, font)
	if err != nil {
		t.Fatalf("parse full window: %v", err)
	}
	checkEntries(t, entries, expected, keys, "full-window-1x")

	entries, err = ParseParticipationImage(encodePNG(t, upscale2x(window)), members, font)
	if err != nil {
		t.Fatalf("parse full window 2x: %v", err)
	}
	checkEntries(t, entries, expected, keys, "full-window-2x")
}
