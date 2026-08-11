package helpers

import (
	"image"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// This file generalises the fixed-crop "small" parser to arbitrary
// screenshots of the Guild "Member Participation Status" window: the table is
// located instead of assumed. Each text row is segmented into words by column
// gaps; a data row is recognised by its numeric columns (Level, Weekly
// Mission, Culvert, Flag Race), making the culvert score the second-to-last
// pure-digit word of the row. Works for the legacy pre-cropped table too,
// where the name is simply the leftmost word.
//
// Scaled screenshots (Retina 2x, fractional Windows/display scaling) are
// handled by scaled-template matching at the image's native resolution, so no
// screenshot pixels are destroyed by resampling: crisply-scaled text is
// matched with nearest-neighbour-upscaled binary templates under the strict
// tolerance, and smoothly-scaled or re-encoded text with the normalized
// cross-correlation matcher (gpq_ocr_ncc.go). Either way only the game's
// bitmap font geometry is accepted.

type ocrWord struct {
	text string
	x0   int // inclusive, relative to the parsed grid
	x1   int // exclusive
}

// segmentWords splits a row band into words separated by >= gapStop blank
// columns (scaled with the font) and decodes each one.
func segmentWords(band [][]bool, font *GPQFont) []ocrWord {
	return segmentWordsDual(band, band, font)
}

// segmentWordsDual is segmentWords over loose/tight ink planes (see
// matchRowDual): word boundaries come from the loose plane.
func segmentWordsDual(loose, tight [][]bool, font *GPQFont) []ocrWord {
	h := len(loose)
	if h == 0 {
		return nil
	}
	w := len(loose[0])
	colInk := make([]bool, w)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			if loose[y][x] {
				colInk[x] = true
				break
			}
		}
	}

	gapStop := font.gapStopPx()
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
				if blank >= gapStop {
					break
				}
			}
		}
		sliceL := make([][]bool, h)
		sliceT := make([][]bool, h)
		for y := 0; y < h; y++ {
			sliceL[y] = loose[y][x:x1]
			sliceT[y] = tight[y][x:x1]
		}
		if text := matchRowDual(sliceL, sliceT, font); text != "" {
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

// digitsScaled is pureDigits for rescaled screenshots. Smooth scaling can
// strip the serifs off a digit '1', leaving the bare vertical bar this font
// also uses for I and l - those read as '1'. A thousands separator blurred
// together with a neighbouring fragment can decode as a dotted 'i' - that
// reads as a separator. Diacritic marks picked up from edge noise are
// dropped before classifying.
func digitsScaled(s string) string {
	if s == "" {
		return ""
	}
	digits := ""
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		switch {
		case r >= '0' && r <= '9':
			digits += string(r)
		case r == ',' || r == '.' || r == 'i':
			// separators (real or misread) are fine
		case r == 'I' || r == 'l':
			digits += "1"
		default:
			return ""
		}
	}
	return digits
}

// binarizeFull binarizes the whole image into an ink grid with the strict
// two-colour rule.
func binarizeFull(img image.Image) [][]bool {
	b := img.Bounds()
	return binarizeCrop(img, 0, 0, b.Dx())
}

// Binarization floor for the halo-inclusive segmentation plane of smooth
// screenshots (see buildNCCPlane): any plausible ink of either text colour
// (#FFFFFF white, #B3B3B3 gray) blended toward the dark window background
// stays above it, while backgrounds and window chrome stay below.
var (
	gpqSmoothGrayLooseC = 88 // ~0.38 gray-over-background blend
	gpqSmoothSpreadMax  = 40 // text/background blends stay near-neutral
)

// fixtureRowPitch is the vertical distance between table text row band starts
// at 1x (measured on the provided fixtures: 24px between consecutive rows).
const fixtureRowPitch = 24.0

// estimateScaleFromPitch derives the UI scale from the spacing between text
// rows in the binarized image. Sidebar text and other window chrome inject
// off-pitch spacings (and band merging shifts with scale), so instead of a
// bare median the estimator votes: the candidate scale whose implied table
// pitch explains the most row spacings wins, and the estimate is refined
// from exactly the spacings that voted for it. Returns 0 when no plausible
// pitch dominates.
func estimateScaleFromPitch(grid [][]bool) float64 {
	est, _ := estimateScaleAndFirstRow(grid)
	return est
}

// estimateScaleAndFirstRow additionally returns the y of the first band
// participating in the winning pitch (an upper bound on where the table
// starts; -1 when unknown).
func estimateScaleAndFirstRow(grid [][]bool) (float64, int) {
	bands := detectRows(grid)
	type delta struct{ d, y int }
	deltas := []delta{}
	for i := 1; i < len(bands); i++ {
		d := bands[i][0] - bands[i-1][0]
		if d >= 18 && d <= 90 {
			deltas = append(deltas, delta{d, bands[i-1][0]})
		}
	}
	if len(deltas) < 4 {
		return 0, -1
	}
	bestVotes, bestSum, bestCnt, bestY := 0, 0, 0, -1
	for c := 100; c <= 330; c += 5 {
		pitch := float64(c) / 100 * fixtureRowPitch
		votes, sum, firstY := 0, 0, -1
		for _, d := range deltas {
			if math.Abs(float64(d.d)-pitch) <= 2 {
				votes++
				sum += d.d
				if firstY < 0 {
					firstY = d.y
				}
			}
		}
		if votes > bestVotes {
			bestVotes, bestSum, bestCnt, bestY = votes, sum, votes, firstY
		}
	}
	if bestVotes < 3 {
		return 0, -1
	}
	return float64(bestSum) / float64(bestCnt) / fixtureRowPitch, bestY
}

// gpqHeaderWordConf is the fuzzy-match confidence needed to recognise the
// header's "Name"/"Culvert" words. Scaled decodes can drop a letter (e.g.
// "Name" reading as "Iame", similarity 0.75), and recognising the header
// needs both words in one band with the right order, so a bit of slack here
// is safe.
const gpqHeaderWordConf = 0.72

// findTableHeader scans the ink planes for a row band containing both a
// "Name" and a "Culvert" word (the participation table header). It returns
// the header's y end, the x start of the Name column, the x range of the
// Culvert header word and a right bound for the table, or ok=false when the
// image has no header (pre-cropped table).
func findTableHeader(loose, tight [][]bool, font *GPQFont, yLo, yHi int) (headerY1, nameX0, culvertX0, culvertX1, rightX int, ok bool) {
	s := font.scaleFactor()
	for _, band := range detectRowsGap(loose, scalePx(3, s)) {
		if band[1] <= yLo || (yHi >= 0 && band[0] >= yHi) {
			continue
		}
		words := segmentWordsDual(loose[band[0]:band[1]], tight[band[0]:band[1]], font)
		var nameW, culvertW, rightW *ocrWord
		for wi := range words {
			w := &words[wi]
			switch {
			case nameLikeliness(normName(w.text), normName("name")) >= gpqHeaderWordConf:
				if nameW == nil {
					nameW = w
				}
			case nameLikeliness(normName(w.text), normName("culvert")) >= gpqHeaderWordConf:
				culvertW = w
			}
			if rightW == nil || w.x1 > rightW.x1 {
				rightW = w
			}
		}
		if nameW != nil && culvertW != nil && culvertW.x0 > nameW.x1 {
			right := culvertW.x1 + scalePx(130, s)
			if rightW.x1+scalePx(60, s) > right {
				right = rightW.x1 + scalePx(60, s)
			}
			return band[1], nameW.x0, culvertW.x0, culvertW.x1, right, true
		}
	}
	return 0, 0, 0, 0, 0, false
}

// legacyNameColWidth is the name column width of the pre-cropped table format
// (and of the same table inside the full window, which it is cropped from).
const legacyNameColWidth = 68

// tableRegion describes where the participation table sits inside a grid, in
// image pixels (independent of the template scale used to parse the rows).
type tableRegion struct {
	headerY1  int
	x0, x1    int
	nameLimit int
	// Culvert header word x range, relative to x0 (0,0 when headerless).
	culvertX0, culvertX1 int
	found                bool
}

// locateTable finds the participation table via its header row. When
// firstRowY >= 0 (the pitch estimate's first table-ish band) the search
// focuses on the few bands just above it, falling back to a full sweep - a
// whole-image decode just to find two header words is expensive. Without a
// header (pre-cropped table) the region covers the whole grid.
func locateTable(loose, tight [][]bool, font *GPQFont, firstRowY int) tableRegion {
	s := font.scaleFactor()
	width := 0
	if len(loose) > 0 {
		width = len(loose[0])
	}
	headerY1, nameX0, culvertX0, culvertX1, rightX, ok := 0, 0, 0, 0, 0, false
	if firstRowY >= 0 {
		pitch := scalePx(int(fixtureRowPitch), s)
		headerY1, nameX0, culvertX0, culvertX1, rightX, ok =
			findTableHeader(loose, tight, font, firstRowY-4*pitch, firstRowY+pitch)
	}
	if !ok {
		headerY1, nameX0, culvertX0, culvertX1, rightX, ok = findTableHeader(loose, tight, font, -1, -1)
	}
	if ok {
		x0 := nameX0 - scalePx(70, s)
		if x0 < 0 {
			x0 = 0
		}
		x1 := rightX
		if x1 > width {
			x1 = width
		}
		// The name column left-aligns slightly left of its header word.
		return tableRegion{
			headerY1:  headerY1,
			x0:        x0,
			x1:        x1,
			nameLimit: (nameX0 - x0) + scalePx(legacyNameColWidth-10, s),
			culvertX0: culvertX0 - x0,
			culvertX1: culvertX1 - x0,
			found:     true,
		}
	}
	return tableRegion{x1: width, nameLimit: scalePx(legacyNameColWidth, s)}
}

// parseTableRows extracts data rows from the region of the grid. A data row
// must have at least 3 numeric columns; the culvert score is the
// second-to-last one (Flag Race is last). The name is decoded from the name
// column zone only, since long names run into the Job column with less than a
// word gap.
func parseTableRows(grid [][]bool, region tableRegion, font *GPQFont, memberNames []string) []ScoreEntry {
	sub := make([][]bool, 0, len(grid)-region.headerY1)
	for y := region.headerY1; y < len(grid); y++ {
		sub = append(sub, grid[y][region.x0:region.x1])
	}

	s := font.scaleFactor()
	digitFn := pureDigits
	if s > 1.01 {
		digitFn = digitsScaled
	}
	bands := detectRowsGap(sub, scalePx(3, s))

	type rowResult struct {
		ok          bool
		name, score string
	}
	results := make([]rowResult, len(bands))
	var wg sync.WaitGroup
	for bandIdx, band := range bands {
		wg.Add(1)
		go func(bandIdx int, band [2]int) {
			defer wg.Done()
			rowsL := sub[band[0]:band[1]]
			rowsT := rowsL
			words := segmentWordsDual(rowsL, rowsT, font)
			if len(words) < 2 {
				return
			}
			digitWords := []string{}
			for _, w := range words {
				if d := digitFn(w.text); d != "" {
					digitWords = append(digitWords, d)
				}
			}
			// Data-row gate: without a header anchor a row must show at
			// least 3 numeric columns. With one, the culvert-column
			// re-decode below is the real gate, so a single surviving
			// numeric column suffices (some numeric cells merge into
			// neighbouring words at fractional scales).
			minDigitWords := 3
			if region.found {
				minDigitWords = 1
			}
			if len(digitWords) < minDigitWords {
				return // header, TIP text, buttons, ...
			}

			// The culvert score: with a located header, the word under the
			// Culvert column (robust even when another numeric cell
			// misdecodes), re-decoded with digit and comma templates only so
			// letter templates cannot poison an eroded digit. Otherwise the
			// second-to-last digit word (Flag Race is last).
			score := ""
			if region.found {
				lo := region.culvertX0 - scalePx(40, s)
				hi := region.culvertX1 + scalePx(70, s)
				bestOv := 0
				cw := -1
				for wi, w := range words {
					ov := min(w.x1, hi) - max(w.x0, lo)
					if ov > bestOv {
						bestOv = ov
						cw = wi
					}
				}
				if cw >= 0 {
					w := words[cw]
					sliceL := make([][]bool, len(rowsL))
					sliceT := make([][]bool, len(rowsL))
					for y := range rowsL {
						sliceL[y] = rowsL[y][w.x0:w.x1]
						sliceT[y] = rowsT[y][w.x0:w.x1]
					}
					score = keepDigits(matchRowDual(sliceL, sliceT, font.digitsOnly().withTolerance(gpqCulvertTol)))
				}
			}
			if score == "" {
				if len(digitWords) < 3 {
					return
				}
				score = digitWords[len(digitWords)-2]
			}

			limit := region.nameLimit
			if len(rowsL) > 0 && limit > len(rowsL[0]) {
				limit = len(rowsL[0])
			}
			nameZoneL := make([][]bool, len(rowsL))
			nameZoneT := make([][]bool, len(rowsL))
			for y := range rowsL {
				nameZoneL[y] = rowsL[y][:limit]
				nameZoneT[y] = rowsT[y][:limit]
			}
			name, ellipsis := cleanDecodedName(matchRowDual(nameZoneL, nameZoneT, font))
			if name == "" || digitFn(name) != "" {
				return // row without a name column
			}
			ellipsis = ellipsis || inkNearRightEdge(nameZoneL, scalePx(nameEdgeEvidencePx, s))
			results[bandIdx] = rowResult{ok: true, name: reconcileName(name, ellipsis, memberNames), score: score}
		}(bandIdx, band)
	}
	wg.Wait()

	names := []string{}
	scores := []string{}
	for _, r := range results {
		if r.ok {
			names = append(names, r.name)
			scores = append(scores, r.score)
		}
	}
	return mergeScoresWithNames(scores, names)
}

// parseParticipationGrid runs header detection + row extraction on one grid.
func parseParticipationGrid(grid [][]bool, font *GPQFont, memberNames []string) ([]ScoreEntry, bool) {
	region := locateTable(grid, grid, font, -1)
	return parseTableRows(grid, region, font, memberNames), region.found
}

// Scaled-match tolerance for nearest-neighbour (crisp) scaling: it is
// lossless, and the phased template variants reproduce it exactly (or within
// a boundary pixel or two for odd scale ratios), so the native strict
// tolerance holds - which matters: the score column contains thousands
// separators with no font template, and a looser tolerance lets wide letters
// swallow "separator+digit" spans (e.g. ",7" decoding as T).
var (
	gpqScaledTolNearest = gpqMatchTol
	// gpqCulvertTol: the culvert-cell re-decode competes digits against
	// digits only, so a looser tolerance is safe there - the right digit
	// still wins on distance, and narrow glyphs ('1') survive the extra
	// quantisation noise fractional scales give them.
	gpqCulvertTol = 0.22
)

// gpqMinScaledEst: below this pitch-derived scale estimate the image is
// treated as native 1x and the strict parse is authoritative.
const gpqMinScaledEst = 1.05

// betterParse reports whether (e, header) beats (best, bestHeader).
func betterParse(e []ScoreEntry, header bool, best []ScoreEntry, bestHeader bool) bool {
	return len(e) > len(best) || (len(e) == len(best) && header && !bestHeader)
}

// parseScaledAround matches scaled glyph templates at the image's native
// resolution, searching a small grid of scales around the pitch estimate and
// both scaling modes: crisp nearest-neighbour (binary strict matching) and
// smooth (NCC against the luminance plane, see gpq_ocr_ncc.go). The table is
// located once (its position in image pixels does not depend on the candidate
// template scale).
func parseScaledAround(img image.Image, est float64, firstRowY int, strict [][]bool, font *GPQFont, memberNames []string, deadline time.Time) ([]ScoreEntry, bool) {
	nccP := buildNCCPlane(img, est)

	// Full-window screenshots carry chrome the pre-cropped tables never had:
	// scrollbar thumbs and frame lines binarize as bright neutral ink that
	// vertically welds row bands together. Work on a cleaned copy (the native
	// 1x path keeps the raw grid).
	strictClean := cloneGrid(strict)
	clearLongVerticalRuns(strictClean, scalePx(gpqNCCMaxRun1x, est))

	// Locate the header: crisp nearest-neighbour matching on the strict grid
	// first (fast), NCC over the luminance plane otherwise (real client
	// windows render the header in a different smooth font, which template
	// matching cannot read - the NCC locator derives the columns from the
	// data rows instead).
	region := locateTable(strictClean, strictClean, font.scaledFont(est, gpqScaledTolNearest), firstRowY)
	if !region.found {
		region = locateTableNCC(nccP, font.grayScaledFont(est), firstRowY)
	}

	// Rank competing parses by how many rows reconciled to known members
	// before row count: an engine mismatched to the input (binary matching on
	// a smoothly-scaled image) can fabricate many rows of junk that would
	// out-count a shorter, correct parse, but junk decodes do not reconcile.
	rec := reconciledCounter(memberNames)
	best := []ScoreEntry{}
	bestRec := 0
	update := func(e []ScoreEntry) {
		if r := rec(e); r > bestRec || (r == bestRec && len(e) > len(best)) {
			best, bestRec = e, r
		}
	}
	for _, df := range []float64{0, -0.03, 0.03} {
		f := est + df
		if f < 1.01 {
			continue
		}
		if time.Now().After(deadline) {
			return best, region.found
		}
		sf := font.scaledFont(f, gpqScaledTolNearest)
		update(parseTableRows(strictClean, region, sf, memberNames))
		// The smooth NCC pass costs seconds; skip it when the lossless
		// nearest match already recovered essentially every row.
		if bestRec < 15 && !time.Now().After(deadline) {
			update(parseTableRowsNCC(nccP, region, font.grayScaledFont(f), memberNames))
		}
		// The phase variants absorb small scale-estimate error, so the pitch
		// estimate alone almost always suffices; widen the search only when
		// it clearly under-delivers.
		if df == 0 && bestRec >= 10 {
			break
		}
	}
	return best, region.found
}

// reconciledCounter returns a function counting how many parsed entries carry
// a known member name.
func reconciledCounter(memberNames []string) func([]ScoreEntry) int {
	set := make(map[string]bool, len(memberNames))
	for _, m := range memberNames {
		set[m] = true
	}
	return func(entries []ScoreEntry) int {
		n := 0
		for _, e := range entries {
			if set[e.Name] {
				n++
			}
		}
		return n
	}
}

// parseScaledLadder is the fallback when the strict parse found nothing and
// the image has too few rows to estimate a pitch: sweep a coarse scale
// ladder with both scaling modes.
func parseScaledLadder(img image.Image, strict [][]bool, font *GPQFont, memberNames []string, deadline time.Time) ([]ScoreEntry, bool) {
	// Clean chrome runs sized for the largest ladder scale (larger runs are
	// chrome at every attempted scale).
	strictClean := cloneGrid(strict)
	clearLongVerticalRuns(strictClean, scalePx(gpqNCCMaxRun1x, 3.0))
	rec := reconciledCounter(memberNames)
	best := []ScoreEntry{}
	bestHeader := false
	bestRec := 0
	update := func(e []ScoreEntry, header bool) {
		if r := rec(e); r > bestRec || (r == bestRec && betterParse(e, header, best, bestHeader)) {
			best, bestHeader, bestRec = e, header, r
		}
	}
	for _, f := range []float64{1.25, 1.5, 1.75, 2.0, 2.5, 3.0} {
		if time.Now().After(deadline) {
			return best, bestHeader
		}
		sf := font.scaledFont(f, gpqScaledTolNearest)
		region := locateTable(strictClean, strictClean, sf, -1)
		update(parseTableRows(strictClean, region, sf, memberNames), region.found)
		if bestHeader && bestRec >= 10 {
			return best, bestHeader
		}
		if time.Now().After(deadline) {
			return best, bestHeader
		}
		nccP := buildNCCPlane(img, f)
		gf := font.grayScaledFont(f)
		nccRegion := locateTableNCC(nccP, gf, -1)
		update(parseTableRowsNCC(nccP, nccRegion, gf, memberNames), nccRegion.found)
		if bestHeader && bestRec >= 10 {
			return best, bestHeader
		}
	}
	return best, bestHeader
}

// ParseParticipationImage decodes a screenshot of the guild participation
// table - either the full Guild window or a pre-cropped table body - and
// returns the parsed character name/score entries in row order. Names are
// reconciled against memberNames. Native 1x is matched strictly; scaled
// screenshots (integer or fractional UI scales) are matched with glyph
// templates upscaled to the detected scale.
// gpqParseBudget caps the wall-clock spent on one image: when exceeded, the
// best result found so far is returned instead of searching further scales.
const gpqParseBudget = 12 * time.Second

// gpqMaxPixels rejects absurdly large images before any work happens.
const gpqMaxPixels = 24_000_000

func ParseParticipationImage(imgData []byte, memberNames []string, font *GPQFont) ([]ScoreEntry, error) {
	img, _, err := image.Decode(strings.NewReader(string(imgData)))
	if err != nil {
		return nil, err
	}
	if b := img.Bounds(); b.Dx()*b.Dy() > gpqMaxPixels {
		return nil, errImageTooLarge
	}

	setParseDeadline(time.Now().Add(gpqParseBudget))
	defer clearParseDeadline()

	grid := binarizeFull(img)
	best, bestHeader := parseParticipationGrid(grid, font, memberNames)

	deadline := time.Now().Add(gpqParseBudget)
	est, firstRowY := estimateScaleAndFirstRow(grid)
	if est < gpqMinScaledEst {
		if est == 0 && len(best) == 0 {
			if e, h := parseScaledLadder(img, grid, font, memberNames, deadline); betterParse(e, h, best, bestHeader) {
				best = e
			}
		}
		return best, nil
	}

	if e, h := parseScaledAround(img, est, firstRowY, grid, font, memberNames, deadline); betterParse(e, h, best, bestHeader) {
		best = e
	}
	return best, nil
}

// errImageTooLarge is returned for absurdly large screenshots.
var errImageTooLarge = errTooLarge{}

type errTooLarge struct{}

func (errTooLarge) Error() string {
	return "image is too large to parse - please post the screenshot at its original size"
}
