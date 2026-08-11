package helpers

import (
	"image"
	"math"
	"strconv"
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

// segmentWordsDual splits a row band into words separated by >= gapStop
// blank columns (scaled with the font) and decodes each one, over loose/tight
// ink planes (see matchRowDual): word boundaries come from the loose plane.
func segmentWordsDual(loose, tight [][]bool, font *GPQFont, clock *parseClock) []ocrWord {
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
		if text := matchRowDual(sliceL, sliceT, font, clock); text != "" {
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
func findTableHeader(loose, tight [][]bool, font *GPQFont, yLo, yHi int, clock *parseClock) (headerY1, nameX0, culvertX0, culvertX1, rightX int, ok bool) {
	s := font.scaleFactor()
	for _, band := range detectRowsGap(loose, scalePx(3, s)) {
		if band[1] <= yLo || (yHi >= 0 && band[0] >= yHi) {
			continue
		}
		if clock.exceeded() {
			break
		}
		words := segmentWordsDual(loose[band[0]:band[1]], tight[band[0]:band[1]], font, clock)
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
func locateTable(loose, tight [][]bool, font *GPQFont, firstRowY int, clock *parseClock) tableRegion {
	s := font.scaleFactor()
	width := 0
	if len(loose) > 0 {
		width = len(loose[0])
	}
	headerY1, nameX0, culvertX0, culvertX1, rightX, ok := 0, 0, 0, 0, 0, false
	if firstRowY >= 0 {
		pitch := scalePx(int(fixtureRowPitch), s)
		headerY1, nameX0, culvertX0, culvertX1, rightX, ok =
			findTableHeader(loose, tight, font, firstRowY-4*pitch, firstRowY+pitch, clock)
	}
	if !ok {
		headerY1, nameX0, culvertX0, culvertX1, rightX, ok = findTableHeader(loose, tight, font, -1, -1, clock)
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
// word gap. Rows carry the RAW decoded name - reconciliation against the
// roster happens once, after engine arbitration, so the parse itself never
// depends on the roster.
func parseTableRows(grid [][]bool, region tableRegion, font *GPQFont, clock *parseClock) []parsedRow {
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
		ok  bool
		row parsedRow
	}
	results := make([]rowResult, len(bands))
	var wg sync.WaitGroup
	for bandIdx, band := range bands {
		wg.Add(1)
		go func(bandIdx int, band [2]int) {
			defer wg.Done()
			if clock.exceeded() {
				return
			}
			rowsL := sub[band[0]:band[1]]
			rowsT := rowsL
			words := segmentWordsDual(rowsL, rowsT, font, clock)
			if len(words) < 2 {
				return
			}
			type digitWord struct {
				value string
				wi    int
			}
			digitWords := []digitWord{}
			for wi, w := range words {
				if d := digitFn(w.text); d != "" {
					digitWords = append(digitWords, digitWord{value: d, wi: wi})
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

			wordSlices := func(w ocrWord) ([][]bool, [][]bool) {
				sliceL := make([][]bool, len(rowsL))
				sliceT := make([][]bool, len(rowsL))
				for y := range rowsL {
					sliceL[y] = rowsL[y][w.x0:w.x1]
					sliceT[y] = rowsT[y][w.x0:w.x1]
				}
				return sliceL, sliceT
			}

			// The culvert score: with a located header, the word under the
			// Culvert column (robust even when another numeric cell
			// misdecodes), re-decoded with digit and comma templates only so
			// letter templates cannot poison an eroded digit. Otherwise the
			// second-to-last digit word (Flag Race is last).
			score := ""
			scoreExpl, scoreInk := 0, 0
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
					sliceL, sliceT := wordSlices(words[cw])
					var text string
					text, scoreExpl, scoreInk = matchRowDualCov(sliceL, sliceT, font.digitsOnly().withTolerance(gpqCulvertTol), clock)
					score = keepDigits(text)
				}
			}
			if score == "" {
				if len(digitWords) < 3 {
					return
				}
				dw := digitWords[len(digitWords)-2]
				score = dw.value
				sliceL, sliceT := wordSlices(words[dw.wi])
				_, scoreExpl, scoreInk = matchRowDualCov(sliceL, sliceT, font, clock)
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
			rawText, nameExpl, nameInk := matchRowDualCov(nameZoneL, nameZoneT, font, clock)
			name, ellipsis := cleanDecodedName(rawText)
			if name == "" || digitFn(name) != "" {
				return // row without a name column
			}
			ellipsis = ellipsis || inkNearRightEdge(nameZoneL, scalePx(nameEdgeEvidencePx, s))
			results[bandIdx] = rowResult{ok: true, row: parsedRow{
				rawName:  name,
				ellipsis: ellipsis,
				score:    score,
				v:        inkFraction(nameExpl+scoreExpl, nameInk+scoreInk),
				bandY0:   region.headerY1 + band[0],
			}}
		}(bandIdx, band)
	}
	wg.Wait()

	rows := []parsedRow{}
	for _, r := range results {
		if r.ok {
			rows = append(rows, r.row)
		}
	}
	return rows
}

// inkFraction is explained/total clamped to [0,1] (0 when there is no ink).
func inkFraction(explained, total int) float64 {
	if total <= 0 {
		return 0
	}
	f := float64(explained) / float64(total)
	if f > 1 {
		f = 1
	}
	return f
}

// parseParticipationGrid runs header detection + row extraction on one grid.
func parseParticipationGrid(grid [][]bool, font *GPQFont, clock *parseClock) ([]parsedRow, tableRegion) {
	region := locateTable(grid, grid, font, -1, clock)
	return parseTableRows(grid, region, font, clock), region
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

// ── Roster-free parse arbitration ───────────────────────────────────────────
//
// Competing parses (engines, candidate scales) are ranked by INTRINSIC
// quality only - explained-ink mass damped by pitch conformity and score
// monotonicity - never by how many rows reconcile against the roster. The
// winning parse's rows (raw names, scores, order) are therefore identical
// for any roster, including an empty one; reconciliation happens exactly
// once afterwards, on the winner.

// scaledOutcome is one candidate parse with its arbitration metadata.
type scaledOutcome struct {
	rows    []parsedRow
	engine  string
	scale   float64
	header  bool
	quality float64
}

// parseQuality scores a candidate parse: quality =
// (Σ v_i) * (0.5 + 0.5*P) * (0.75 + 0.25*M), where v_i is each row's
// explained-ink fraction, P the pitch conformity and M the score
// monotonicity. imgScale is the IMAGE's UI scale (row pitch model), not the
// candidate template scale.
func parseQuality(rows []parsedRow, imgScale float64) float64 {
	if len(rows) == 0 {
		return 0
	}
	sumV := 0.0
	for _, r := range rows {
		sumV += r.v
	}
	p := pitchConformity(rows, imgScale)
	m := scoreMonotonicity(rows)
	return sumV * (0.5 + 0.5*p) * (0.75 + 0.25*m)
}

// pitchConformity is the fraction of adjacent emitted rows whose band-start
// spacing lies on the modal pitch progression (a 1-4 pitch multiple, within
// +-3 scaled px). 1 when fewer than two rows.
func pitchConformity(rows []parsedRow, imgScale float64) float64 {
	if len(rows) < 2 {
		return 1
	}
	pitch := fixtureRowPitch * imgScale
	tol := 3 * imgScale
	conforming := 0
	for i := 1; i < len(rows); i++ {
		d := float64(rows[i].bandY0 - rows[i-1].bandY0)
		for k := 1.0; k <= 4; k++ {
			if math.Abs(d-k*pitch) <= tol {
				conforming++
				break
			}
		}
	}
	return float64(conforming) / float64(len(rows)-1)
}

// scoreMonotonicity is the fraction of adjacent score pairs that are
// non-increasing (the table is sorted by culvert score). 1 when fewer than
// two rows.
func scoreMonotonicity(rows []parsedRow) float64 {
	if len(rows) < 2 {
		return 1
	}
	nonIncreasing := 0
	prev, _ := strconv.Atoi(rows[0].score)
	for i := 1; i < len(rows); i++ {
		cur, _ := strconv.Atoi(rows[i].score)
		if cur <= prev {
			nonIncreasing++
		}
		prev = cur
	}
	return float64(nonIncreasing) / float64(len(rows)-1)
}

// expectedRowCount estimates from the pitch model how many table rows a
// complete parse of the region should emit: the length of the contiguous
// pitch progression of row bands below the header cut (region columns only).
// Returns 0 when fewer than 4 bands exist - N is then unknowable and every
// engine must run.
func expectedRowCount(plane [][]bool, region tableRegion, imgScale float64) int {
	if len(plane) == 0 || region.headerY1 >= len(plane) {
		return 0
	}
	x0, x1 := region.x0, region.x1
	if x1 > len(plane[0]) {
		x1 = len(plane[0])
	}
	if x0 < 0 || x0 >= x1 {
		return 0
	}
	sub := make([][]bool, 0, len(plane)-region.headerY1)
	for y := region.headerY1; y < len(plane); y++ {
		sub = append(sub, plane[y][x0:x1])
	}
	bands := detectRowsGap(sub, scalePx(3, imgScale))
	if len(bands) < 4 {
		return 0
	}
	pitch := fixtureRowPitch * imgScale
	tol := 3 * imgScale
	n := 1
	for i := 1; i < len(bands); i++ {
		if math.Abs(float64(bands[i][0]-bands[i-1][0])-pitch) > tol {
			break // first off-pitch band ends the table (TIP box, buttons)
		}
		n++
	}
	if n < 4 {
		return 0
	}
	return n
}

// passComplete reports whether a parse is complete enough that running
// further engines cannot beat it: it emitted (nearly) every expected row
// with high explained-ink coverage on a conforming pitch progression. Always
// false when N is unknowable - then every engine runs.
func passComplete(rows []parsedRow, n int, imgScale float64) bool {
	if n < 4 || len(rows) < n-1 {
		return false
	}
	sumV := 0.0
	for _, r := range rows {
		sumV += r.v
	}
	return sumV/float64(len(rows)) >= 0.85 && pitchConformity(rows, imgScale) >= 0.9
}

// snapRegionToPlane re-anchors a table region located on one ink plane for
// parsing on a different one: band boundaries shift between the strict
// binary plane and the fatter halo-inclusive NCC plane, and a header cut
// landing INSIDE a band of the target plane would slice its first row in
// half. The cut is snapped to the nearest inter-band gap of the target
// plane, and when the first band below the cut is not within one pitch of
// the pitch estimate's first table row, the cut is re-anchored just above
// that row instead.
func snapRegionToPlane(region tableRegion, plane [][]bool, imgScale float64, firstRowY int) tableRegion {
	if !region.found || len(plane) == 0 {
		return region
	}
	x0, x1 := region.x0, region.x1
	if x1 > len(plane[0]) {
		x1 = len(plane[0])
	}
	if x0 < 0 || x0 >= x1 {
		return region
	}
	sub := make([][]bool, len(plane))
	for y := range plane {
		sub[y] = plane[y][x0:x1]
	}
	bands := detectRowsGap(sub, scalePx(3, imgScale))
	if len(bands) == 0 {
		return region
	}
	h := region.headerY1
	for _, b := range bands {
		if h >= b[0] && h < b[1] {
			// Inside a band: snap to the nearer edge of the surrounding gaps.
			if h-b[0] <= b[1]-h {
				h = b[0] - 1
				if h < 0 {
					h = 0
				}
			} else {
				h = b[1]
			}
			break
		}
	}
	if firstRowY >= 0 {
		pitch := scalePx(int(fixtureRowPitch), imgScale)
		first := -1
		for _, b := range bands {
			if b[0] >= h {
				first = b[0]
				break
			}
		}
		if first < 0 || first-firstRowY > pitch || firstRowY-first > pitch {
			h = firstRowY - scalePx(4, imgScale)
			if h < 0 {
				h = 0
			}
		}
	}
	region.headerY1 = h
	return region
}

// parseScaledAround matches scaled glyph templates at the image's native
// resolution, searching a small grid of scales around the pitch estimate and
// both scaling modes: crisp nearest-neighbour (binary strict matching) and
// smooth (NCC against the luminance plane, see gpq_ocr_ncc.go). The table is
// located once (its position in image pixels does not depend on the candidate
// template scale). cleanGrid is the chrome-cleaned binary grid the pitch
// estimate was derived from. Engines are skipped ONLY via the deterministic
// completeness gate (passComplete) - never by leftover wall-clock - so the
// set of passes attempted is reproducible; an expired clock fails decodes
// closed and flags the run truncated instead.
func parseScaledAround(img image.Image, est float64, firstRowY int, cleanGrid [][]bool, font *GPQFont, sweepClock, nccClock *parseClock) scaledOutcome {
	nccP := buildNCCPlane(img, est)

	// Locate the header: crisp nearest-neighbour matching on the strict grid
	// first (fast), NCC over the luminance plane otherwise (real client
	// windows render the header in a different smooth font, which template
	// matching cannot read - the NCC locator derives the columns from the
	// data rows instead).
	region := locateTable(cleanGrid, cleanGrid, font.scaledFont(est, gpqScaledTolNearest), firstRowY, sweepClock)
	regionBin, regionNCC := region, region
	if region.found {
		regionNCC = snapRegionToPlane(region, nccP.loose, est, firstRowY)
	} else {
		region = locateTableNCC(nccP, font.grayScaledFont(est), firstRowY, nccClock)
		regionNCC = region
		regionBin = snapRegionToPlane(region, cleanGrid, est, firstRowY)
	}

	n := expectedRowCount(nccP.loose, regionNCC, est)
	best := scaledOutcome{engine: "scaled-binary", scale: est, header: region.found}
	update := func(rows []parsedRow, engine string, f float64) {
		q := parseQuality(rows, est)
		if q > best.quality || (q == best.quality && len(rows) > len(best.rows)) {
			best = scaledOutcome{rows: rows, engine: engine, scale: f, header: region.found, quality: q}
		}
	}
	for _, df := range []float64{0, -0.03, 0.03} {
		f := est + df
		if f < 1.01 {
			continue
		}
		sf := font.scaledFont(f, gpqScaledTolNearest)
		binRows := parseTableRows(cleanGrid, regionBin, sf, sweepClock)
		update(binRows, "scaled-binary", f)
		// The smooth NCC pass costs seconds; skip it only when the pitch
		// model proves the lossless nearest match already recovered
		// essentially every row (roster-independent gate).
		if !passComplete(binRows, n, est) && !nccClock.exceeded() {
			update(parseTableRowsNCC(nccP, regionNCC, font.grayScaledFont(f), nccClock), "ncc", f)
		}
		// The phase variants absorb small scale-estimate error, so the pitch
		// estimate alone almost always suffices; widen the search only when
		// it clearly under-delivers.
		if df == 0 && passComplete(best.rows, n, est) {
			break
		}
	}
	return best
}

// parseScaledLadder is the fallback when the strict parse found nothing and
// the image has too few rows to estimate a pitch: sweep a coarse scale
// ladder with both scaling modes. With so few rows the expected row count is
// usually unknowable, in which case every rung runs.
func parseScaledLadder(img image.Image, strict [][]bool, font *GPQFont, sweepClock, nccClock *parseClock) scaledOutcome {
	// Clean chrome runs sized for the largest ladder scale (larger runs are
	// chrome at every attempted scale).
	strictClean := cloneGrid(strict)
	clearLongVerticalRuns(strictClean, scalePx(gpqNCCMaxRun1x, 3.0))
	clearLongHorizontalRuns(strictClean, scalePx(gpqNCCMaxRunH1x, 3.0))
	best := scaledOutcome{engine: "ladder-binary", scale: 1}
	update := func(rows []parsedRow, engine string, f float64, header bool) {
		q := parseQuality(rows, f)
		if q > best.quality || (q == best.quality && len(rows) > len(best.rows)) {
			best = scaledOutcome{rows: rows, engine: engine, scale: f, header: header, quality: q}
		}
	}
	for _, f := range []float64{1.25, 1.5, 1.75, 2.0, 2.5, 3.0} {
		if nccClock.exceeded() {
			// Budget exhausted: the run is flagged truncated; stop instead of
			// burning cycles on decodes that now all fail closed.
			break
		}
		sf := font.scaledFont(f, gpqScaledTolNearest)
		region := locateTable(strictClean, strictClean, sf, -1, sweepClock)
		binRows := parseTableRows(strictClean, region, sf, sweepClock)
		update(binRows, "ladder-binary", f, region.found)
		if passComplete(binRows, expectedRowCount(strictClean, region, f), f) {
			return best
		}
		nccP := buildNCCPlane(img, f)
		gf := font.grayScaledFont(f)
		nccRegion := locateTableNCC(nccP, gf, -1, nccClock)
		nccRows := parseTableRowsNCC(nccP, nccRegion, gf, nccClock)
		update(nccRows, "ladder-ncc", f, nccRegion.found)
		if passComplete(nccRows, expectedRowCount(nccP.loose, nccRegion, f), f) {
			return best
		}
	}
	return best
}

// ParseResult is the full outcome of parsing one participation screenshot.
type ParseResult struct {
	// Rows are the parsed table rows in band order: one entry per parsed
	// row, never deduplicated. Name/Matched/Confidence reflect roster
	// reconciliation; RawName and Score are roster-independent.
	Rows []ScoreEntry
	// Truncated is set when any matching pass hit its time budget: the rows
	// may be incomplete and MUST NOT be submitted automatically.
	Truncated bool
	// Defects are non-fatal parse anomalies (skipped rows, reconciliation
	// collisions) worth surfacing to the operator.
	Defects []string
	// Scale is the template scale of the winning parse, Engine its matching
	// engine and HeaderFound whether a table header anchored the region.
	Scale       float64
	Engine      string
	HeaderFound bool
}

// Wall-clock budgets for one ParseParticipation call. Every pass gets a
// fixed sub-deadline derived from the single entry time - never from
// leftover time of earlier passes - so the set of passes attempted does not
// depend on scheduling luck: the strict 1x pass is generously bounded, the
// scaled-binary sweep must finish within its own window and the NCC engine
// gets the remainder of the total.
const (
	gpqParseBudget  = 12 * time.Second
	gpqStrictBudget = 4 * time.Second
	gpqSweepBudget  = 4 * time.Second // sweep deadline = strict + sweep
)

// gpqMaxPixels rejects absurdly large images before any work happens.
const gpqMaxPixels = 24_000_000

// ParseParticipation decodes a screenshot of the guild participation table -
// either the full Guild window or a pre-cropped table body. Native 1x is
// matched strictly; scaled screenshots (integer or fractional UI scales) are
// matched with glyph templates upscaled to the detected scale. Engine
// arbitration is roster-free: the winning parse's rows (raw names, scores,
// order) are identical for any roster; names are reconciled against
// memberNames once, on the winner.
func ParseParticipation(imgData []byte, memberNames []string, font *GPQFont) (*ParseResult, error) {
	img, _, err := image.Decode(strings.NewReader(string(imgData)))
	if err != nil {
		return nil, err
	}
	if b := img.Bounds(); b.Dx()*b.Dy() > gpqMaxPixels {
		return nil, errImageTooLarge
	}

	start := time.Now()
	run := &parseRun{}
	strictClock := run.until(start.Add(gpqStrictBudget))
	sweepClock := run.until(start.Add(gpqStrictBudget + gpqSweepBudget))
	nccClock := run.until(start.Add(gpqParseBudget))

	grid := binarizeFull(img)
	strictRows, strictRegion := parseParticipationGrid(grid, font, strictClock)
	best := scaledOutcome{
		rows:    strictRows,
		engine:  "strict-1x",
		scale:   1,
		header:  strictRegion.found,
		quality: parseQuality(strictRows, 1),
	}

	est0, _ := estimateScaleAndFirstRow(grid)
	if est0 < gpqMinScaledEst {
		if est0 == 0 && len(strictRows) == 0 {
			if out := parseScaledLadder(img, grid, font, sweepClock, nccClock); out.quality > best.quality {
				best = out
			}
		}
	} else {
		// Re-run the pitch estimate on the chrome-cleaned grid: parsing works
		// on the cleaned plane, and chrome runs shift band boundaries, so an
		// estimate taken from the raw grid can anchor the first row a band
		// off. The raw estimate only sizes the cleaning caps.
		cleanGrid := cloneGrid(grid)
		clearLongVerticalRuns(cleanGrid, scalePx(gpqNCCMaxRun1x, est0))
		clearLongHorizontalRuns(cleanGrid, scalePx(gpqNCCMaxRunH1x, est0))
		est, firstRowY := estimateScaleAndFirstRow(cleanGrid)
		if est < gpqMinScaledEst {
			est, firstRowY = est0, -1
		}
		if out := parseScaledAround(img, est, firstRowY, cleanGrid, font, sweepClock, nccClock); out.quality > best.quality {
			best = out
		}
	}

	res := &ParseResult{
		Scale:       best.scale,
		Engine:      best.engine,
		HeaderFound: best.header,
	}
	res.Rows = resolveRows(best.rows, memberNames, &res.Defects)
	res.Truncated = run.truncated.Load()
	return res, nil
}

// ParseParticipationImage is the legacy entry point: ParseParticipation
// reduced to its row entries.
func ParseParticipationImage(imgData []byte, memberNames []string, font *GPQFont) ([]ScoreEntry, error) {
	res, err := ParseParticipation(imgData, memberNames, font)
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// errImageTooLarge is returned for absurdly large screenshots.
var errImageTooLarge = errTooLarge{}

type errTooLarge struct{}

func (errTooLarge) Error() string {
	return "image is too large to parse - please post the screenshot at its original size"
}
