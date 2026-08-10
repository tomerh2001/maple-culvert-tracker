package helpers

import (
	"image"
	"strings"
)

// This file generalises the fixed-crop "small" parser to arbitrary
// screenshots of the Guild "Member Participation Status" window: the table is
// located instead of assumed. Each text row is segmented into words by column
// gaps; a data row is recognised by its numeric columns (Level, Weekly
// Mission, Culvert, Flag Race), making the culvert score the second-to-last
// pure-digit word of the row. Works for the legacy pre-cropped table too,
// where the name is simply the leftmost word. 2x (e.g. Retina) screenshots
// are handled by retrying on a box-downscaled copy.

type ocrWord struct {
	text string
	x0   int // inclusive, relative to the parsed grid
	x1   int // exclusive
}

// segmentWords splits a row band into words separated by >= gpqGapStop blank
// columns and decodes each one.
func segmentWords(band [][]bool, font *GPQFont) []ocrWord {
	h := len(band)
	if h == 0 {
		return nil
	}
	w := len(band[0])
	colInk := make([]bool, w)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			if band[y][x] {
				colInk[x] = true
				break
			}
		}
	}

	words := []ocrWord{}
	x := 0
	for x < w {
		if !colInk[x] {
			x++
			continue
		}
		x1 := x
		blank := 0
		for cur := x; cur < w; cur++ {
			if colInk[cur] {
				x1 = cur + 1
				blank = 0
			} else {
				blank++
				if blank >= gpqGapStop {
					break
				}
			}
		}
		slice := make([][]bool, h)
		for y := 0; y < h; y++ {
			slice[y] = band[y][x:x1]
		}
		if text := matchRow(slice, font); text != "" {
			words = append(words, ocrWord{text: text, x0: x, x1: x1})
		}
		x = x1
		for x < w && !colInk[x] {
			x++
		}
	}
	return words
}

// pureDigits returns the numeric content of a word if it consists only of
// digits and thousands separators, otherwise "".
func pureDigits(s string) string {
	if s == "" {
		return ""
	}
	digits := ""
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits += string(r)
		case r == ',' || r == '.':
			// separators are fine
		default:
			return ""
		}
	}
	return digits
}

// binarizeFull binarizes the whole image into an ink grid.
func binarizeFull(img image.Image) [][]bool {
	b := img.Bounds()
	return binarizeCrop(img, 0, 0, b.Dx())
}

// downscaleBy shrinks the image by factor f (>1) so screenshots taken at
// higher UI scales match the 1x glyph templates. Two sampling modes: area
// averaging (smooth/bilinear-scaled sources) and center-point sampling, which
// exactly recovers nearest-neighbour-scaled sources.
func downscaleBy(img image.Image, f float64, area bool) image.Image {
	b := img.Bounds()
	w := int(float64(b.Dx()) / f)
	h := int(float64(b.Dy()) / f)
	if w < 1 || h < 1 {
		return img
	}
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
			if !area {
				cy := int((float64(y) + 0.5) * f)
				cx := int((float64(x) + 0.5) * f)
				if cy >= b.Dy() {
					cy = b.Dy() - 1
				}
				if cx >= b.Dx() {
					cx = b.Dx() - 1
				}
				pr, pg, pb, _ := img.At(b.Min.X+cx, b.Min.Y+cy).RGBA()
				i := out.PixOffset(x, y)
				out.Pix[i] = uint8(pr >> 8)
				out.Pix[i+1] = uint8(pg >> 8)
				out.Pix[i+2] = uint8(pb >> 8)
				out.Pix[i+3] = 0xFF
				continue
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

// fixtureRowPitch is the vertical distance between table text rows at 1x.
const fixtureRowPitch = 26.2

// estimateScaleFromPitch measures the median spacing between text rows in the
// binarized image and derives the UI scale from it. Returns 0 when no
// plausible pitch is found.
func estimateScaleFromPitch(grid [][]bool) float64 {
	bands := detectRows(grid)
	pitches := []int{}
	for i := 1; i < len(bands); i++ {
		d := bands[i][0] - bands[i-1][0]
		if d >= 18 && d <= 90 {
			pitches = append(pitches, d)
		}
	}
	if len(pitches) < 4 {
		return 0
	}
	sortInts(pitches)
	return float64(pitches[len(pitches)/2]) / fixtureRowPitch
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// candidateScales returns the UI scales to attempt, cheapest-first: native,
// the pitch-derived estimate (with neighbours), then a coarse ladder.
func candidateScales(img image.Image) []float64 {
	scales := []float64{1.0}
	if est := estimateScaleFromPitch(binarizeFull(img)); est > 1.04 && est < 4.0 {
		for _, d := range []float64{0, -0.04, 0.04, -0.08, 0.08} {
			scales = append(scales, est+d)
		}
	}
	for i := 1; i <= 10; i++ {
		scales = append(scales, 1.0+float64(i)*0.2)
	}
	seen := map[int]bool{}
	out := []float64{}
	for _, f := range scales {
		key := int(f*100 + 0.5)
		f = float64(key) / 100 // snap to 2 decimals so integer scales stay exact
		if f >= 0.99 && !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}

// findTableHeader scans the grid for a row band containing both a "Name" and
// a "Culvert" word (the participation table header). It returns the header's
// y end, the x start of the Name column and a right bound for the table, or
// ok=false when the image has no header (pre-cropped table).
func findTableHeader(grid [][]bool, font *GPQFont) (headerY1, nameX0, rightX int, ok bool) {
	for _, band := range detectRows(grid) {
		words := segmentWords(grid[band[0]:band[1]], font)
		var nameW, culvertW, rightW *ocrWord
		for wi := range words {
			w := &words[wi]
			switch {
			case nameLikeliness(normName(w.text), "name") >= 0.8:
				if nameW == nil {
					nameW = w
				}
			case nameLikeliness(normName(w.text), "culvert") >= 0.8:
				culvertW = w
			}
			if rightW == nil || w.x1 > rightW.x1 {
				rightW = w
			}
		}
		if nameW != nil && culvertW != nil && culvertW.x0 > nameW.x1 {
			right := culvertW.x1 + 130
			if rightW.x1+60 > right {
				right = rightW.x1 + 60
			}
			return band[1], nameW.x0, right, true
		}
	}
	return 0, 0, 0, false
}

// legacyNameColWidth is the name column width of the pre-cropped table format
// (and of the same table inside the full window, which it is cropped from).
const legacyNameColWidth = 68

// parseParticipationGrid runs header detection + row extraction on one grid.
// A data row must have at least 3 numeric columns; the culvert score is the
// second-to-last one (Flag Race is last). The name is decoded from the name
// column zone only, since long names run into the Job column with less than a
// word gap.
func parseParticipationGrid(grid [][]bool, font *GPQFont, memberNames []string) ([]ScoreEntry, bool) {
	sub := grid
	headerFound := false
	nameLimit := legacyNameColWidth
	if headerY1, nameX0, rightX, ok := findTableHeader(grid, font); ok {
		headerFound = true
		x0 := nameX0 - 70
		if x0 < 0 {
			x0 = 0
		}
		x1 := rightX
		if len(grid) > 0 && x1 > len(grid[0]) {
			x1 = len(grid[0])
		}
		// The name column left-aligns slightly left of its header word.
		nameLimit = (nameX0 - x0) + legacyNameColWidth - 10
		sub = make([][]bool, 0, len(grid)-headerY1)
		for y := headerY1; y < len(grid); y++ {
			sub = append(sub, grid[y][x0:x1])
		}
	}

	names := []string{}
	scores := []string{}
	for _, band := range detectRows(sub) {
		rows := sub[band[0]:band[1]]
		words := segmentWords(rows, font)
		if len(words) < 2 {
			continue
		}
		digitWords := []string{}
		for _, w := range words {
			if d := pureDigits(w.text); d != "" {
				digitWords = append(digitWords, d)
			}
		}
		if len(digitWords) < 3 {
			continue // header, TIP text, buttons, ...
		}

		limit := nameLimit
		if len(rows) > 0 && limit > len(rows[0]) {
			limit = len(rows[0])
		}
		nameZone := make([][]bool, len(rows))
		for y := range rows {
			nameZone[y] = rows[y][:limit]
		}
		name := strings.TrimRight(matchRow(nameZone, font), ".")
		if name == "" || pureDigits(name) != "" {
			continue // row without a name column
		}
		names = append(names, reconcileName(name, memberNames))
		scores = append(scores, digitWords[len(digitWords)-2])
	}
	return mergeScoresWithNames(scores, names), headerFound
}

// ParseParticipationImage decodes a screenshot of the guild participation
// table - either the full Guild window or a pre-cropped table body - and
// returns the parsed character name/score entries in row order. Names are
// reconciled against memberNames. 1x and 2x scales are attempted.
func ParseParticipationImage(imgData []byte, memberNames []string, font *GPQFont) ([]ScoreEntry, error) {
	img, _, err := image.Decode(strings.NewReader(string(imgData)))
	if err != nil {
		return nil, err
	}

	type attempt struct {
		area    bool
		lenient bool
	}
	best := []ScoreEntry{}
	bestHeader := false
	for _, f := range candidateScales(img) {
		attempts := []attempt{{false, false}}
		if f > 1.01 {
			// Strict matching first (exact recoveries), lenient as fallback
			// for slightly distorted rescales.
			attempts = []attempt{{false, false}, {true, false}, {false, true}, {true, true}}
		}
		for _, a := range attempts {
			scaled := img
			parseFont := font
			if f > 1.01 {
				scaled = downscaleBy(img, f, a.area)
				if a.lenient {
					parseFont = font.withTolerance(0.22)
				}
			}
			entries, headerFound := parseParticipationGrid(binarizeFull(scaled), parseFont, memberNames)
			better := len(entries) > len(best) ||
				(len(entries) == len(best) && headerFound && !bestHeader)
			if better {
				best = entries
				bestHeader = headerFound
			}
			// A full page of rows under a located header from a strict parse
			// is confident; the pitch-derived scales come first, stop early.
			if !a.lenient && headerFound && len(entries) >= 15 {
				return best, nil
			}
		}
	}
	return best, nil
}
