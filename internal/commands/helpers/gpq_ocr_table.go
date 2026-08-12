package helpers

import (
	"image"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// This file holds the legacy 1x table parser (header-word location plus
// numeric-column row recognition) and the ParseParticipation entry point.
//
// Screenshots of the full Guild window are handled by the ANCHOR pipeline
// (gpq_ocr_anchor.go / gpq_ocr_anchor_parse.go): the window title fixes
// (scale, origin) deterministically and the fixed dialog geometry does the
// rest. The legacy parser remains as the fallback for anchorless images -
// the pre-cropped table format has no window title - and only ever runs at
// native 1x with strict binary matching.

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

// ParseResult is the full outcome of parsing one participation screenshot.
type ParseResult struct {
	// Rows are the parsed table rows in band order: one entry per parsed
	// row, never deduplicated. Name/Matched/Confidence reflect roster
	// reconciliation; RawName and Score are roster-independent.
	Rows []ScoreEntry
	// Truncated is set when the winning parse itself hit its time budget
	// (or nothing parsed and some pass expired): the rows may be incomplete
	// and MUST NOT be submitted automatically.
	Truncated bool
	// Defects are non-fatal parse anomalies (skipped rows, reconciliation
	// collisions) worth surfacing to the operator.
	Defects []string
	// Scale is the screenshot's UI scale (anchored, or 1 for the legacy
	// fallback), Engine the matching engine that produced the rows
	// ("anchor-nearest", "anchor-smooth" or "legacy-1x"), AnchorFound
	// whether the window-title anchor located the table and AnchorNCC the
	// title patch's correlation score (0 without an anchor).
	Scale       float64
	Engine      string
	AnchorFound bool
	AnchorNCC   float64
}

// Wall-clock budgets for one ParseParticipation call. The anchor removed the
// scale search, so the whole parse fits a tight budget; the anchor sweep gets
// its own fixed sub-deadline so a pathological image cannot starve the row
// decodes.
const (
	gpqParseBudget  = 8 * time.Second
	gpqAnchorBudget = 4 * time.Second
)

// gpqMaxPixels rejects absurdly large images before any work happens.
const gpqMaxPixels = 24_000_000

// ParseParticipation decodes a screenshot of the guild participation table -
// either the full Guild window or a pre-cropped table body. The full window
// is located via the "Member Participation Status" title anchor, which fixes
// (scale, origin) deterministically; the anchored parse then runs both glyph
// engines at that ONE scale. Anchorless images fall back to the legacy
// strict 1x parse (the pre-cropped table format); when that also finds
// nothing, the image is not a culvert window we can trust and an error says
// so. Parsing is roster-free: the rows (raw names, scores, order) are
// identical for any roster; names are reconciled against memberNames once,
// after the parse.
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
	anchorClock := run.until(start.Add(gpqAnchorBudget))
	decodeClock := run.until(start.Add(gpqParseBudget))

	if hit, ok := findAnchor(img, anchorClock); ok {
		rows, engine := parseAnchored(img, hit, font, decodeClock)
		res := &ParseResult{
			Scale:       hit.scale,
			Engine:      engine,
			AnchorFound: true,
			AnchorNCC:   hit.ncc,
		}
		res.Rows = resolveRows(rows, memberNames, &res.Defects)
		res.Truncated = decodeClock.expiredNow() || (len(rows) == 0 && run.truncated.Load())
		return res, nil
	}
	if run.truncated.Load() {
		// The anchor search itself ran out of budget: report an honest
		// truncation instead of "not a culvert window".
		return &ParseResult{Truncated: true, Engine: "legacy-1x", Scale: 1}, nil
	}

	// No anchor: pre-cropped table images carry no window title. The legacy
	// strict 1x parse handles exactly those. NO scale ladder runs here - an
	// anchorless image that the strict parse cannot read is not something to
	// guess at.
	grid := binarizeFull(img)
	rows, _ := parseParticipationGrid(grid, font, decodeClock)
	if len(rows) == 0 {
		if run.truncated.Load() {
			return &ParseResult{Truncated: true, Engine: "legacy-1x", Scale: 1}, nil
		}
		return nil, errCulvertWindowNotFound
	}
	res := &ParseResult{Scale: 1, Engine: "legacy-1x"}
	res.Rows = resolveRows(rows, memberNames, &res.Defects)
	res.Truncated = decodeClock.expiredNow()
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

// ErrCulvertWindowNotFound is returned when neither the window-title anchor
// nor the legacy pre-cropped-table parse recognises the image: honest
// failure beats guessing at scales.
var ErrCulvertWindowNotFound = errWindowNotFound{}

// errCulvertWindowNotFound is the internal alias the parser returns.
var errCulvertWindowNotFound error = ErrCulvertWindowNotFound

type errWindowNotFound struct{}

func (errWindowNotFound) Error() string {
	return "could not locate the culvert window in this image - please post a screenshot of the full Guild window (Member Participation Status page)"
}
