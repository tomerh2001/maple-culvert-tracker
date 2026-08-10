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

// downscale2x box-averages the image 2:1 so 2x screenshots match the 1x
// glyph templates.
func downscale2x(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					pr, pg, pb, _ := img.At(b.Min.X+2*x+dx, b.Min.Y+2*y+dy).RGBA()
					r += pr >> 8
					g += pg >> 8
					bl += pb >> 8
				}
			}
			i := out.PixOffset(x, y)
			out.Pix[i] = uint8(r / 4)
			out.Pix[i+1] = uint8(g / 4)
			out.Pix[i+2] = uint8(bl / 4)
			out.Pix[i+3] = 0xFF
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

	best := []ScoreEntry{}
	bestHeader := false
	for _, scaled := range []image.Image{img, downscale2x(img)} {
		entries, headerFound := parseParticipationGrid(binarizeFull(scaled), font, memberNames)
		better := len(entries) > len(best) ||
			(len(entries) == len(best) && headerFound && !bestHeader)
		if better {
			best = entries
			bestHeader = headerFound
		}
	}
	return best, nil
}
