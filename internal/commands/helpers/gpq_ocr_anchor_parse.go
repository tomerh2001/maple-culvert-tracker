package helpers

import (
	"image"
	"math"
	"strconv"
	"sync"
)

// This file parses the participation table once the anchor fixed (scale,
// origin): the window is a fixed-size game dialog, so every column and row
// sits at a KNOWN offset from the title in reference units - no header
// decoding, no column inference. The anchored table region is resampled ONCE
// to the reference 2x resolution (area averaging when shrinking, bilinear
// when growing), so every row is decoded at the same fixed 2x geometry with
// the native 2x glyph templates regardless of the screenshot's scale: decode
// cost is constant, and downsampling a blurrily upscaled screenshot recovers
// the template-sharp 2x rendering it was blown up from. Rows are decoded with
// the two glyph-template engines (crisp nearest-neighbour binary matching and
// smooth NCC matching), and each row keeps whichever decode explains more
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

// gpqROIRef* bound the resampled table region in reference (2x) pixels
// relative to the title origin: the name and culvert columns plus the 17 row
// bands, with margins generous enough for band-tolerance drift, template
// padding and the truncation-boundary check, yet excluding the sidebar,
// buttons and the TIP box.
const (
	gpqROIRefX0 = 48
	gpqROIRefX1 = 816
	gpqROIRefY0 = 124
	gpqROIRefY1 = 990
)

// anchorRowDecode is one engine's decode of a row: raw name text, score
// digits, and the ink accounting used to arbitrate between engines.
type anchorRowDecode struct {
	name      string
	score     string
	explained int
	total     int
}

// resampleTableROI crops the anchored table region and resamples it to the
// reference 2x resolution in one pass: area averaging when the screenshot is
// larger than 2x (each destination pixel integrates its source box - which
// also RECOVERS sharpness on post-capture upscales), bilinear interpolation
// when smaller, and a plain integer-offset crop at exactly 2x (lossless: the
// crisp binary engine sees the original pixels). Source reads outside the
// image are black - absent table rows simply yield no ink bands. The
// returned anchorHit places the title origin for the resampled image, so all
// reference offsets are direct pixel coordinates.
func resampleTableROI(img image.Image, hit anchorHit) (image.Image, anchorHit) {
	outHit := anchorHit{
		scale: gpqAnchorRefScale,
		ox:    -gpqROIRefX0, oy: -gpqROIRefY0,
		ncc: hit.ncc, headerNCC: hit.headerNCC,
	}
	f := hit.scale / gpqAnchorRefScale
	const outW = gpqROIRefX1 - gpqROIRefX0
	const outH = gpqROIRefY1 - gpqROIRefY0
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	sx0 := float64(hit.ox) + gpqROIRefX0*f
	sy0 := float64(hit.oy) + gpqROIRefY0*f

	// Flatten the needed source region into an RGB slab once (black-padded
	// one pixel beyond the sampling reach), so the resample loops read raw
	// bytes instead of going through the image interface per sample.
	ix0 := int(math.Floor(sx0)) - 1
	iy0 := int(math.Floor(sy0)) - 1
	ix1 := int(math.Ceil(sx0+float64(outW)*f)) + 2
	iy1 := int(math.Ceil(sy0+float64(outH)*f)) + 2
	sw, sh := ix1-ix0, iy1-iy0
	slab := make([]uint8, sw*sh*3)
	yLo, yHi := clampInt(-iy0, 0, sh), clampInt(h-iy0, 0, sh)
	xLo, xHi := clampInt(-ix0, 0, sw), clampInt(w-ix0, 0, sw)
	for y := yLo; y < yHi; y++ {
		sy := b.Min.Y + iy0 + y
		for x := xLo; x < xHi; x++ {
			r, g, bl, _ := img.At(b.Min.X+ix0+x, sy).RGBA()
			i := (y*sw + x) * 3
			slab[i] = uint8(r >> 8)
			slab[i+1] = uint8(g >> 8)
			slab[i+2] = uint8(bl >> 8)
		}
	}
	rsx0 := sx0 - float64(ix0)
	rsy0 := sy0 - float64(iy0)

	out := image.NewRGBA(image.Rect(0, 0, outW, outH))
	set := func(x, y int, r, g, bl uint8) {
		i := out.PixOffset(x, y)
		out.Pix[i] = r
		out.Pix[i+1] = g
		out.Pix[i+2] = bl
		out.Pix[i+3] = 0xFF
	}

	switch {
	case math.Abs(f-1) <= 0.0025:
		// Already at reference resolution: integer-offset crop, no filtering.
		ox := int(math.Round(rsx0))
		oy := int(math.Round(rsy0))
		for y := 0; y < outH; y++ {
			base := ((oy+y)*sw + ox) * 3
			for x := 0; x < outW; x++ {
				i := base + x*3
				set(x, y, slab[i], slab[i+1], slab[i+2])
			}
		}
	case f < 1:
		// Magnify: bilinear interpolation between the four surrounding
		// source pixels (destination pixel centers mapped into source space).
		for y := 0; y < outH; y++ {
			syf := rsy0 + (float64(y)+0.5)*f - 0.5
			yb := int(math.Floor(syf))
			fy := syf - float64(yb)
			for x := 0; x < outW; x++ {
				sxf := rsx0 + (float64(x)+0.5)*f - 0.5
				xb := int(math.Floor(sxf))
				fx := sxf - float64(xb)
				i00 := (yb*sw + xb) * 3
				i01 := i00 + sw*3
				w00 := (1 - fx) * (1 - fy)
				w10 := fx * (1 - fy)
				w01 := (1 - fx) * fy
				w11 := fx * fy
				var px [3]uint8
				for c := 0; c < 3; c++ {
					v := w00*float64(slab[i00+c]) + w10*float64(slab[i00+3+c]) +
						w01*float64(slab[i01+c]) + w11*float64(slab[i01+3+c])
					px[c] = uint8(v + 0.5)
				}
				set(x, y, px[0], px[1], px[2])
			}
		}
	default:
		// Minify: area averaging over each destination pixel's source box
		// (partial edge pixels weighted by coverage).
		for y := 0; y < outH; y++ {
			sy0f := rsy0 + float64(y)*f
			sy1f := sy0f + f
			iyA := clampInt(int(sy0f), 0, sh)
			iyB := clampInt(int(math.Ceil(sy1f)), 0, sh)
			for x := 0; x < outW; x++ {
				sx0f := rsx0 + float64(x)*f
				sx1f := sx0f + f
				ixA := clampInt(int(sx0f), 0, sw)
				ixB := clampInt(int(math.Ceil(sx1f)), 0, sw)
				var acc [3]float64
				var wsum float64
				for yy := iyA; yy < iyB; yy++ {
					wy := math.Min(sy1f, float64(yy+1)) - math.Max(sy0f, float64(yy))
					base := yy * sw * 3
					for xx := ixA; xx < ixB; xx++ {
						wx := math.Min(sx1f, float64(xx+1)) - math.Max(sx0f, float64(xx))
						wgt := wy * wx
						i := base + xx*3
						acc[0] += wgt * float64(slab[i])
						acc[1] += wgt * float64(slab[i+1])
						acc[2] += wgt * float64(slab[i+2])
						wsum += wgt
					}
				}
				if wsum > 0 {
					set(x, y, uint8(acc[0]/wsum+0.5), uint8(acc[1]/wsum+0.5), uint8(acc[2]/wsum+0.5))
				}
			}
		}
	}
	return out, outHit
}

// anchorSlotRow is one parsed row tagged with its pitch-grid slot.
type anchorSlotRow struct {
	k       int
	row     parsedRow
	nearest bool
}

// gpqAnchorCrispMaxDn is the mean per-glyph template distance below which a
// native nearest-engine table counts as genuinely crisp (measured: crisp
// upscales decode at ~0.01-0.03, blurred ones ride the 0.12/0.22
// tolerances).
const gpqAnchorCrispMaxDn = 0.05

// gpqAnchorCrispMaxScale caps the native crisp pass: above it even the
// heavily prefiltered binary matching costs real time on slow machines, and
// the resampled decode already meets the accuracy gates there.
const gpqAnchorCrispMaxScale = 3.6

// parseAnchored extracts the table rows of an anchored screenshot. The table
// region is resampled to the reference 2x resolution first (see
// resampleTableROI), so the decode always runs at the one fixed geometry the
// 2x templates are exact for - its cost does not depend on the screenshot's
// scale. Returns the rows in band order plus the winning engine name
// ("anchor-nearest" or "anchor-smooth", whichever explained more rows).
//
// One class of screenshot beats the resample: a CRISP nearest-neighbour
// post-capture upscale. Its pixels are exact duplicates, so the phase-swept
// binary templates decode it perfectly at the NATIVE scale regardless of the
// anchor's small scale-estimate error - while that same estimate error makes
// the area-averaging resample smear the duplication grid. Those screenshots
// get a strict-binary native pass first; it is accepted only when the table
// comes back essentially complete, score-monotone and at near-zero template
// distance (provable crispness), and it costs next to nothing when the
// content is smooth instead, because the strict two-colour binarization of
// blended pixels yields almost no ink to even attempt matching on.
func parseAnchored(img image.Image, hit anchorHit, font *GPQFont, clock *parseClock) ([]parsedRow, string) {
	if hit.scale > gpqAnchorRefScale+0.01 && hit.scale <= gpqAnchorCrispMaxScale {
		if rows, ok := parseAnchoredCrispNative(img, hit, font, clock); ok {
			return slotRows(rows), "anchor-nearest"
		}
	}
	roi, roiHit := resampleTableROI(img, hit)
	slots, engine := parseAnchoredEngines(roi, roiHit, hit.scale, font, clock)
	return slotRows(slots), engine
}

// parseAnchoredCrispNative decodes the table with the strict binary engine
// at the image's native scale, and reports ok only when the result proves
// the content crisp (see parseAnchored). Bands and spans come from the
// strict ink plane alone: on a crisp upscale it captures every glyph (the
// two text colours survive duplication exactly), on smooth content it is
// nearly empty and the pass exits without cost.
func parseAnchoredCrispNative(img image.Image, hit anchorHit, font *GPQFont, clock *parseClock) ([]anchorSlotRow, bool) {
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
		return nil, false
	}

	strict := binarizeFull(img)
	sub := make([][]bool, yHi-yLo)
	for y := yLo; y < yHi; y++ {
		row := make([]bool, (nameX1-nameX0)+(culX1-culX0))
		copy(row, strict[y][nameX0:nameX1])
		copy(row[nameX1-nameX0:], strict[y][culX0:culX1])
		sub[y-yLo] = row
	}
	clearLongVerticalRuns(sub, scalePx(gpqNCCMaxRun1x, s))
	clearLongHorizontalRuns(sub, scalePx(gpqNCCMaxRunH1x, s))
	type slotBand struct {
		y0, y1 int
		off    int
	}
	slots := make([]*slotBand, gpqRefRowCount)
	tol := hit.px(gpqRefBandTol)
	for _, band := range detectRowsGap(sub, scalePx(3, s)) {
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

	sf := font.scaledFont(s, gpqScaledTolNearest)
	sfDigits := sf.digitsOnly().withTolerance(gpqCulvertTol)
	gapStop := scalePx(gpqGapStop, s)

	type rowResult struct {
		ok     bool
		row    parsedRow
		sumDn  float64
		glyphs int
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
			strictPre := colPrefixRows(strict, y0, y1)
			decode := func(x0, x1 int, bfont *GPQFont, digits bool) (string, int, int, float64, int, int) {
				spans := bandSpans(strictPre, x0, x1, gapStop)
				if len(spans) == 0 {
					return "", 0, 0, 0, 0, 0
				}
				sx0, sx1 := spans[0][0], spans[len(spans)-1][1]
				if !digits {
					sx1 = spans[0][1]
				}
				sl := make([][]bool, y1-y0)
				for y := y0; y < y1; y++ {
					sl[y-y0] = strict[y][sx0:sx1]
				}
				text, expl, total, dn, ng := matchRowDualCovQ(sl, sl, bfont, clock)
				return text, expl, total, dn, ng, sx1
			}
			nameT, nameExpl, nameTotal, dnN, ngN, nameEnd := decode(nameX0, nameX1, sf, false)
			scoreT, scoreExpl, scoreTotal, dnS, ngS, _ := decode(culX0, culX1, sfDigits, true)
			name, ellipsis := cleanDecodedName(nameT)
			if name == "" || digitsScaled(name) != "" {
				return
			}
			if nameEnd >= truncX-hit.px(4) {
				ellipsis = true
			}
			results[k] = rowResult{
				ok:     true,
				sumDn:  dnN + dnS,
				glyphs: ngN + ngS,
				row: parsedRow{
					rawName:  name,
					ellipsis: ellipsis,
					score:    keepDigits(scoreT),
					v:        inkFraction(nameExpl+scoreExpl, nameTotal+scoreTotal),
					bandY0:   y0,
				},
			}
		}(k, slot)
	}
	wg.Wait()

	// Crispness acceptance: essentially complete, score-sorted like the
	// window itself, and matched at near-zero template distance.
	rows := []anchorSlotRow{}
	full, monotone := 0, true
	prev := -1
	sumDn, glyphs := 0.0, 0
	for k, r := range results {
		if !r.ok {
			continue
		}
		rows = append(rows, anchorSlotRow{k: k, row: r.row, nearest: true})
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
		return rows, true
	}
	return nil, false
}

// colPrefixRows returns per-column ink prefix sums of grid restricted to
// rows [y0, y1): pre[x+1]-pre[x] = ink in column x.
func colPrefixRows(grid [][]bool, y0, y1 int) []int {
	w := 0
	if len(grid) > 0 {
		w = len(grid[0])
	}
	pre := make([]int, w+1)
	for x := 0; x < w; x++ {
		c := 0
		for y := y0; y < y1; y++ {
			if grid[y][x] {
				c++
			}
		}
		pre[x+1] = pre[x] + c
	}
	return pre
}

// slotRows flattens tagged slot rows (already in band order).
func slotRows(slots []anchorSlotRow) []parsedRow {
	rows := make([]parsedRow, 0, len(slots))
	for _, r := range slots {
		rows = append(rows, r.row)
	}
	return rows
}

// parseAnchoredEngines decodes the resampled table region. srcScale is the
// anchored scale of the ORIGINAL screenshot: the smooth engine's templates
// are matched to the resample chain the content went through (see
// grayRefFontFor).
func parseAnchoredEngines(img image.Image, hit anchorHit, srcScale float64, font *GPQFont, clock *parseClock) ([]anchorSlotRow, string) {
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
	strict := binarizeFull(img)

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

	sf := font.scaledFont(s, gpqScaledTolNearest)
	sfDigits := sf.digitsOnly().withTolerance(gpqCulvertTol)
	gf := font.grayRefFontFor(srcScale)
	gfDigits := gf.digitsOnly()
	ins := nccInsertPenalty(s)
	gapStop := scalePx(gpqGapStop, s)

	type rowResult struct {
		ok      bool
		row     parsedRow
		nearest bool
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
				// Smooth engine: NCC decode over the luminance plane.
				gfont := gf
				if digits {
					gfont = gfDigits
				}
				smooth.name, smooth.explained, smooth.total =
					nccDecodeSpanCov(p, gfont, sx0, sx1, y0, y1, loosePre, ins, digits, clock)
				// Nearest engine: strict binary matching of the same span.
				sl := make([][]bool, y1-y0)
				for y := y0; y < y1; y++ {
					sl[y-y0] = strict[y][sx0:sx1]
				}
				bfont := sf
				if digits {
					bfont = sfDigits
				}
				nearest.name, nearest.explained, nearest.total = matchRowDualCov(sl, sl, bfont, clock)
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
			// lengths fall back to the explained-ink fraction. Ink counts are
			// NOT compared across engines - each plane sees a different pixel
			// population. The nearest engine must also retain a credible share
			// of the ink the NCC plane sees, or its plane simply lost the text.
			//
			// On an EXACT fraction tie (both engines fully explain their own
			// plane) with equal length but DIFFERING text, the two disagree
			// only because the capture is blended - a crisp one would decode
			// identically on both planes. There the binary engine's apparent
			// "exactness" is illusory: it thresholded antialiased pixels, so it
			// can read a confident but wrong glyph (real-full-6 reads the white
			// "Nunez" as "Iunez", folding the N's diagonal into an I stem),
			// while the NCC engine matched the continuous intensities. So a
			// NAME tie resolves to the smooth decode. A SCORE tie stays with
			// the nearest engine (preserveNearTie): a hallucinated digit
			// corrupts a value, and the binary plane's exact digit match is the
			// conservative choice where the two disagree on a number.
			nearCredible := nearName.total*5 >= (nearName.total+smoothName.total)*2
			pick := func(near, smooth anchorRowDecode, nearText, smoothText string, preserveNearTie bool) (string, bool, anchorRowDecode) {
				nl, sl := len([]rune(nearText)), len([]rune(smoothText))
				vNear := inkFraction(near.explained, near.total)
				vSmooth := inkFraction(smooth.explained, smooth.total)
				nearOnEqualFrac := vNear > vSmooth || (preserveNearTie && vNear == vSmooth)
				switch {
				case nl == 0 && sl == 0:
					return "", false, smooth
				case nearCredible && nl > sl:
					return nearText, true, near
				case sl > nl:
					return smoothText, false, smooth
				case nearCredible && nearOnEqualFrac:
					return nearText, true, near
				default:
					return smoothText, false, smooth
				}
			}
			nameT, viaNearest, nameSrc := pick(nearName, smoothName, nearName.name, smoothName.name, false)
			scoreT, _, scoreSrc := pick(nearScore, smoothScore, nearScore.score, smoothScore.score, true)
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
		rows = append(rows, anchorSlotRow{k: k, row: r.row, nearest: r.nearest})
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
