package helpers

import (
	"image"
	"strconv"
	"sync"
)

// This file parses the participation table once the anchor fixed (scale,
// origin): the window is a fixed-size game dialog, so every column and row
// sits at a KNOWN offset from the title in reference units - no header
// decoding, no column inference. Rows are decoded with the two glyph-template
// engines at the one anchored scale (crisp nearest-neighbour binary matching
// and smooth NCC matching), and each row keeps whichever decode explains more
// ink. Only the game's bitmap-font geometry is ever accepted.

// Table geometry in reference (2x) pixels, relative to the title patch's
// top-left corner. Measured ONCE on the provided real fixtures:
// provided/real-sample.png (crisp smooth-2x; title patch origin (50,42),
// first row band tops 190+48k, name spans starting x=124, culvert digits
// x=[750,836] for 6-digit scores) and provided/real-full-{1,3}.png (~1.45x,
// game-side ellipsis-truncated names end at the same reference boundary,
// verified via TestAnchorGeometryDebug).
const (
	// gpqRefRowPitch: vertical distance between consecutive row-band tops
	// (real-sample band tops sit at 190+48k image px, title y0=42).
	gpqRefRowPitch = 48.0
	// gpqRefFirstRowTop: first data row band top, relative to title y0.
	gpqRefFirstRowTop = 148.0
	// gpqRefRowCount: the participation window always shows 17 table rows.
	gpqRefRowCount = 17
	// gpqRefNameX0/X1: the FULL rendered name column. Names are left-aligned
	// at ref x 74-75 on every fixture; the game's own ellipsis truncation
	// reaches ref x 211 (CárlosJunk.. on real-full-3), and the widest Job
	// text ("Angelic Buster") starts at ref x 209 - the columns genuinely
	// interleave, so the crop ends just past the truncation boundary and the
	// decode takes the FIRST ink span only, dropping job fragments that leak
	// in. A clipped crop here was the old parser's "StarryLeyv"/"IDKJag" bug.
	gpqRefNameX0 = 68.0
	gpqRefNameX1 = 216.0
	// gpqRefNameTrunc: the game-side name truncation boundary (the farthest
	// ref x ellipsis dots reach). A name span ending within a few px of it
	// is truncation evidence even when the dots fail to decode; fully
	// rendered names stop earlier (the longest, unrenquited, ends at 204).
	gpqRefNameTrunc = 211.0
	// gpqRefCulvertX0/X1: the culvert score column (right-aligned digits,
	// observed ref span [698, 784] for 7-glyph scores; X0 clears the Weekly
	// Mission column at <= 626, X1 stays short of Flag Race at >= 867).
	gpqRefCulvertX0 = 648.0
	gpqRefCulvertX1 = 796.0
	// gpqRefBandTol: row bands may start up to this many reference px off
	// their pitch slot (lowercase-only names have no ascender; the culvert
	// digits usually pin the top anyway).
	gpqRefBandTol = 9.0
	// gpqRefRowsPad: vertical margin around the rows region for band
	// detection.
	gpqRefRowsPad = 6.0
)

// anchorRowDecode is one engine's decode of a row: raw name text, score
// digits, and the ink accounting used to arbitrate between engines.
type anchorRowDecode struct {
	name      string
	score     string
	explained int
	total     int
	sumDn     float64 // binary engine: accepted glyphs' summed match distance
	glyphs    int     // binary engine: accepted glyph count
}

// gpqAnchorMaxDecodeScale bounds the scale the glyph engines run at: above
// it the screenshot is box-halved (and the anchor hit rescaled) until it
// fits. Screenshots only reach such sizes through post-capture upscaling, so
// halving destroys nothing the engines could use, while NCC decode cost
// grows with the fourth power of the scale.
const gpqAnchorMaxDecodeScale = 2.6

// halveImage box-downsamples the image by exactly 2 on both axes.
func halveImage(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					pr, pg, pb, _ := img.At(b.Min.X+x*2+dx, b.Min.Y+y*2+dy).RGBA()
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

// Engine masks for one anchored decode pass.
const (
	anchorEnginesBoth = iota
	anchorEnginesNearestOnly
	anchorEnginesSmoothOnly
	// anchorEnginesSmoothNames: smooth engine, names only - the caller
	// overlays scores from a higher-resolution digit pass.
	anchorEnginesSmoothNames
)

// anchorSlotRow is one parsed row tagged with its pitch-grid slot.
type anchorSlotRow struct {
	k       int
	row     parsedRow
	nearest bool
	sumDn   float64 // nearest engine's match-distance sum for the row
	glyphs  int
}

// parseAnchored extracts the table rows of an anchored screenshot. Returns
// the rows in band order plus the winning engine name ("anchor-nearest" or
// "anchor-smooth", whichever explained more rows).
//
// Above gpqAnchorMaxDecodeScale the engines specialise: the cheap nearest
// engine tries the NATIVE resolution first (post-capture upscales are often
// crisp, and exact binary matching is immune to the scale); only when it
// cannot carry the table is the screenshot box-halved for the smooth engine,
// whose cost grows with the fourth power of the scale while upscaled blur
// survives halving untouched.
func parseAnchored(img image.Image, hit anchorHit, font *GPQFont, clock *parseClock) ([]parsedRow, string) {
	if hit.scale <= gpqAnchorMaxDecodeScale {
		slots, engine := parseAnchoredEngines(img, hit, font, clock, anchorEnginesBoth)
		return slotRows(slots), engine
	}

	// Oversized screenshots (post-capture upscales). The cheap nearest
	// engine tries the NATIVE resolution first: a CRISP upscale matches the
	// phased binary templates near-exactly at any scale, and genuine
	// crispness is measurable - accepted glyphs sit at near-zero template
	// distance, while blurred content only scrapes past the tolerance. A
	// complete, monotone (the table is score-sorted), near-exact nearest
	// table is accepted outright.
	rowsN, _ := parseAnchoredEngines(img, hit, font, clock, anchorEnginesNearestOnly)
	full, monotone := 0, true
	prev := -1
	sumDn, glyphs := 0.0, 0
	for _, r := range rowsN {
		sumDn += r.sumDn
		glyphs += r.glyphs
		if r.row.score == "" {
			continue
		}
		full++
		v, err := strconv.Atoi(r.row.score)
		if err != nil || (prev >= 0 && v > prev) {
			monotone = false
			break
		}
		prev = v
	}
	if full >= gpqRefRowCount-3 && monotone && glyphs > 0 && sumDn/float64(glyphs) <= gpqAnchorCrispMaxDn {
		return slotRows(rowsN), "anchor-nearest"
	}

	// Smooth content: names from a box-halved copy (upscaled blur survives
	// halving untouched, the smooth engine's s^4 decode cost does not) and
	// scores from a digits-only pass at the highest resolution the budget
	// allows - box halving a doubly-blurred screenshot can merge adjacent
	// digits, and a wrong score is worse than anything.
	imgD, hitD := img, hit
	for hitD.scale > gpqAnchorMaxDigitScale {
		imgD = halveImage(imgD)
		hitD.scale /= 2
		hitD.ox /= 2
		hitD.oy /= 2
	}
	digits := parseAnchoredDigits(imgD, hitD, font, clock)

	imgH, hitH := imgD, hitD
	for hitH.scale > gpqAnchorMaxDecodeScale {
		imgH = halveImage(imgH)
		hitH.scale /= 2
		hitH.ox /= 2
		hitH.oy /= 2
	}
	rowsS, _ := parseAnchoredEngines(imgH, hitH, font, clock, anchorEnginesSmoothNames)
	for i := range rowsS {
		if d, ok := digits[rowsS[i].k]; ok {
			rowsS[i].row.score = d
		}
	}
	return slotRows(rowsS), "anchor-smooth"
}

// gpqAnchorCrispMaxDn is the mean per-glyph template distance below which a
// nearest-engine table counts as genuinely crisp (measured: crisp upscales
// decode at ~0.01-0.03, blurred ones ride the 0.12/0.22 tolerances).
const gpqAnchorCrispMaxDn = 0.05

// gpqAnchorMaxDigitScale caps the digits-only score pass, which costs far
// less than a full decode and therefore affords more resolution.
const gpqAnchorMaxDigitScale = 3.6

// slotRows flattens tagged slot rows (already in band order).
func slotRows(slots []anchorSlotRow) []parsedRow {
	rows := make([]parsedRow, 0, len(slots))
	for _, r := range slots {
		rows = append(rows, r.row)
	}
	return rows
}

// parseAnchoredDigits decodes ONLY the culvert cells, with the reduced digit
// template set, at the image's native resolution.
func parseAnchoredDigits(img image.Image, hit anchorHit, font *GPQFont, clock *parseClock) map[int]string {
	s := hit.scale
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	culX0 := clampInt(hit.ox+hit.px(gpqRefCulvertX0), 0, w)
	culX1 := clampInt(hit.ox+hit.px(gpqRefCulvertX1), 0, w)
	rowsTop := hit.oy + hit.px(gpqRefFirstRowTop)
	pitch := gpqRefRowPitch * s / gpqAnchorRefScale
	if culX0 >= culX1 {
		return nil
	}
	p := buildNCCPlane(img, s)
	gfD := font.grayScaledFontDigitsLite(s)
	ins := nccInsertPenalty(s)
	gapStop := scalePx(gpqGapStop, s)

	// Detect the digit row bands like the main pass does - a fixed y-window
	// accumulates pitch rounding drift over 17 rows.
	pad := hit.px(gpqRefRowsPad)
	yLo := clampInt(rowsTop-pad, 0, h)
	yHi := clampInt(rowsTop+int(pitch*gpqRefRowCount+0.5)+pad, 0, h)
	if yLo >= yHi {
		return nil
	}
	sub := make([][]bool, yHi-yLo)
	for y := yLo; y < yHi; y++ {
		sub[y-yLo] = p.loose[y][culX0:culX1]
	}
	tol := hit.px(gpqRefBandTol)
	out := map[int]string{}
	for _, band := range detectRowsGap(sub, scalePx(3, s)) {
		if clock.exceeded() {
			break
		}
		y0 := yLo + band[0]
		k := int((float64(y0-rowsTop) + pitch/2) / pitch)
		if k < 0 || k >= gpqRefRowCount {
			continue
		}
		if off := y0 - (rowsTop + int(pitch*float64(k)+0.5)); off < -tol || off > tol {
			continue
		}
		y1 := yLo + band[1]
		loosePre := p.bandLoosePrefix(y0, y1)
		spans := bandSpans(loosePre, culX0, culX1, gapStop)
		if len(spans) == 0 {
			continue
		}
		text, _, _ := nccDecodeSpanCov(p, gfD, spans[0][0], spans[len(spans)-1][1], y0, y1, loosePre, ins, true, clock)
		if d := keepDigits(text); d != "" {
			out[k] = d
		}
	}
	return out
}

func parseAnchoredEngines(img image.Image, hit anchorHit, font *GPQFont, clock *parseClock, engines int) ([]anchorSlotRow, string) {
	s := hit.scale
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	nameX0 := clampInt(hit.ox+hit.px(gpqRefNameX0), 0, w)
	nameX1 := clampInt(hit.ox+hit.px(gpqRefNameX1), 0, w)
	culX0 := clampInt(hit.ox+hit.px(gpqRefCulvertX0), 0, w)
	culX1 := clampInt(hit.ox+hit.px(gpqRefCulvertX1), 0, w)
	truncX := hit.ox + hit.px(gpqRefNameTrunc)
	rowsTop := hit.oy + hit.px(gpqRefFirstRowTop)
	pitch := gpqRefRowPitch * s / gpqAnchorRefScale
	pad := hit.px(gpqRefRowsPad)
	yLo := clampInt(rowsTop-pad, 0, h)
	yHi := clampInt(rowsTop+int(pitch*gpqRefRowCount+0.5)+pad, 0, h)
	if nameX0 >= nameX1 || culX0 >= culX1 || yLo >= yHi {
		return nil, "anchor-smooth"
	}

	// The NCC plane doubles as the segmentation plane for BOTH engines: its
	// loose ink survives smooth scaling and re-encoding, and on crisp
	// screenshots it is a superset of the strict binary ink.
	p := buildNCCPlane(img, s)
	var strict [][]bool
	if engines == anchorEnginesBoth || engines == anchorEnginesNearestOnly {
		strict = binarizeFull(img)
	}

	// Row bands from the ink of the name and culvert columns only (the fixed
	// crops exclude sidebar text, buttons and the TIP box by construction).
	sub := make([][]bool, yHi-yLo)
	for y := yLo; y < yHi; y++ {
		row := make([]bool, (nameX1-nameX0)+(culX1-culX0))
		copy(row, p.loose[y][nameX0:nameX1])
		copy(row[nameX1-nameX0:], p.loose[y][culX0:culX1])
		sub[y-yLo] = row
	}
	bands := detectRowsGap(sub, scalePx(3, s))

	// Validate band starts against the anchored pitch grid: each band must
	// sit on a row slot; off-grid bands are chrome or bleed, not table rows.
	type slotBand struct {
		y0, y1 int
		off    int
	}
	slots := make([]*slotBand, gpqRefRowCount)
	tol := hit.px(gpqRefBandTol)
	for _, band := range bands {
		y0 := yLo + band[0]
		k := int((float64(y0-rowsTop) + pitch/2) / pitch)
		if k < 0 || k >= gpqRefRowCount {
			continue
		}
		off := y0 - (rowsTop + int(pitch*float64(k)+0.5))
		if off < -tol || off > tol {
			continue
		}
		if off < 0 {
			off = -off
		}
		if slots[k] == nil || off < slots[k].off {
			slots[k] = &slotBand{y0: y0, y1: yLo + band[1], off: off}
		}
	}

	sf := font
	if s > 1.01 {
		sf = font.scaledFont(s, gpqScaledTolNearest)
	}
	sfDigits := sf.digitsOnly().withTolerance(gpqCulvertTol)
	gf := font.grayScaledFont(s)
	gfDigits := gf.digitsOnly()
	ins := nccInsertPenalty(s)
	gapStop := scalePx(gpqGapStop, s)

	type rowResult struct {
		ok      bool
		row     parsedRow
		nearest bool
		sumDn   float64
		glyphs  int
	}
	results := make([]rowResult, gpqRefRowCount)
	var wg sync.WaitGroup
	for k, slot := range slots {
		if slot == nil {
			continue
		}
		wg.Add(1)
		go func(k int, slot *slotBand) {
			defer wg.Done()
			if clock.exceeded() {
				return
			}
			y0, y1 := slot.y0, slot.y1
			loosePre := p.bandLoosePrefix(y0, y1)

			decode := func(x0, x1 int, digits bool) (anchorRowDecode, anchorRowDecode) {
				spans := bandSpans(loosePre, x0, x1, gapStop)
				var smooth, nearest anchorRowDecode
				if len(spans) == 0 {
					return nearest, smooth
				}
				sx0, sx1 := spans[0][0], spans[len(spans)-1][1]
				if !digits {
					// The name is the FIRST span only: the Job column's
					// widest text leaks a fragment past the crop's right
					// edge, always separated from the name by a full gap.
					sx1 = spans[0][1]
				}
				if engines != anchorEnginesNearestOnly && !(digits && engines == anchorEnginesSmoothNames) {
					// Smooth engine: NCC decode over the luminance plane.
					gfont := gf
					if digits {
						gfont = gfDigits
					}
					smooth.name, smooth.explained, smooth.total =
						nccDecodeSpanCov(p, gfont, sx0, sx1, y0, y1, loosePre, ins, digits, clock)
				}
				if engines == anchorEnginesBoth || engines == anchorEnginesNearestOnly {
					// Nearest engine: strict binary matching of the same span.
					sl := make([][]bool, y1-y0)
					for y := y0; y < y1; y++ {
						sl[y-y0] = strict[y][sx0:sx1]
					}
					bfont := sf
					if digits {
						bfont = sfDigits
					}
					nearest.name, nearest.explained, nearest.total, nearest.sumDn, nearest.glyphs = matchRowDualCovQ(sl, sl, bfont, clock)
				}
				return nearest, smooth
			}

			nearName, smoothName := decode(nameX0, nameX1, false)
			nearScore, smoothScore := decode(culX0, culX1, true)
			nearScore.score = keepDigits(nearScore.name)
			smoothScore.score = keepDigits(smoothScore.name)

			// Engine arbitration per row, per CELL: a mismatched engine
			// drops glyphs from its decode (binary templates lose whole
			// letters on a smooth screenshot; NCC has less to say on a
			// pixel-perfect one), so the longer decode wins, and equal
			// lengths fall back to the explained-ink fraction with ties
			// kept by the nearest engine (its match is exact where it
			// applies at all). Ink counts are NOT compared across engines -
			// each plane sees a different pixel population. The nearest
			// engine must also retain a credible share of the ink the NCC
			// plane sees, or its plane simply lost the text.
			nearCredible := (engines == anchorEnginesBoth || engines == anchorEnginesNearestOnly) &&
				nearName.total*5 >= (nearName.total+smoothName.total)*2
			pick := func(near, smooth anchorRowDecode, nearText, smoothText string) (string, bool, anchorRowDecode) {
				nl, sl := len([]rune(nearText)), len([]rune(smoothText))
				vNear := inkFraction(near.explained, near.total)
				vSmooth := inkFraction(smooth.explained, smooth.total)
				switch {
				case nl == 0 && sl == 0:
					return "", false, smooth
				case nearCredible && nl > sl:
					return nearText, true, near
				case sl > nl:
					return smoothText, false, smooth
				case nearCredible && vNear >= vSmooth:
					return nearText, true, near
				default:
					return smoothText, false, smooth
				}
			}
			nameT, viaNearest, nameSrc := pick(nearName, smoothName, nearName.name, smoothName.name)
			scoreT, _, scoreSrc := pick(nearScore, smoothScore, nearScore.score, smoothScore.score)
			if nameT == "" {
				return
			}
			expl := nameSrc.explained + scoreSrc.explained
			total := nameSrc.total + scoreSrc.total

			name, ellipsis := cleanDecodedName(nameT)
			if name == "" || digitsScaled(name) != "" {
				return
			}
			// Truncation evidence: decoded ellipsis dots, or the name span
			// running into the game's truncation boundary.
			spans := bandSpans(loosePre, nameX0, nameX1, gapStop)
			if len(spans) > 0 && spans[0][1] >= truncX-hit.px(4) {
				ellipsis = true
			}
			results[k] = rowResult{
				ok: true, nearest: viaNearest,
				sumDn:  nearName.sumDn + nearScore.sumDn,
				glyphs: nearName.glyphs + nearScore.glyphs,
				row: parsedRow{
					rawName:  name,
					ellipsis: ellipsis,
					score:    scoreT,
					v:        inkFraction(expl, total),
					bandY0:   y0,
				},
			}
		}(k, slot)
	}
	wg.Wait()

	rows := []anchorSlotRow{}
	nearestWins, smoothWins := 0, 0
	for k, r := range results {
		if !r.ok {
			continue
		}
		rows = append(rows, anchorSlotRow{k: k, row: r.row, nearest: r.nearest, sumDn: r.sumDn, glyphs: r.glyphs})
		if r.nearest {
			nearestWins++
		} else {
			smoothWins++
		}
	}
	engine := "anchor-smooth"
	if nearestWins > smoothWins {
		engine = "anchor-nearest"
	}
	return rows, engine
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
