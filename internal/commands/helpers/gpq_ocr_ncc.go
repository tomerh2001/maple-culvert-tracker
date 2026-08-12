package helpers

import (
	"image"
	"math"
	"strings"
)

// This file implements the smooth-scaled matching path via normalized
// cross-correlation (NCC).
//
// Real-world screenshots of smooth-upscaled clients rarely reach the parser
// losslessly: image hosts re-encode them (palette quantization, chroma
// subsampling) and the upscale kernel varies. Binary template matching relies
// on binarization cuts calibrated to exact text colours, which such inputs
// break. The smooth path therefore matches the scaled glyph templates as
// continuous intensity fields against the raw luminance plane with NCC:
// correlation is invariant to affine intensity changes, which simultaneously
// absorbs the unknown text colour (white vs gray rows), the background level,
// colour quantization and the smoothing kernel. Binarized planes are used
// only to find rows and words and to bound the search - never in the match
// itself. The matcher still accepts nothing but the game's bitmap font: every
// candidate is an embedded glyph template upscaled to the detected UI scale,
// a candidate needs a high correlation against that exact geometry, and a
// word decode only survives when those templates explain the row's core ink
// almost entirely (the same whole-word accounting that keeps bare-bar glyphs
// from matching inside wider glyphs' stems).

const (
	// gpqNCCMinText / gpqNCCMinDigits are the minimum normalized
	// cross-correlations for a glyph candidate. Genuine glyphs correlate
	// >= ~0.7 even on quantized input while different shapes fall well below;
	// narrow letters ride closer to the line than digits, and a missed letter
	// only weakens name reconciliation whereas a hallucinated digit corrupts
	// a score, so text is admitted a little sooner than digits.
	gpqNCCMinText   = 0.62
	gpqNCCMinDigits = 0.70
	// gpqNCCCostText / gpqNCCCostDigits convert a candidate's (1 - ncc) into
	// DP cost units comparable to skipped core-ink pixels. The text scale is
	// gentler for the same reason as above: with a steep scale, a narrow
	// letter's match cost exceeds its tiny skip cost and the DP silently
	// drops it.
	gpqNCCCostText   = 2.0
	gpqNCCCostDigits = 3.0
	// gpqNCCMaxRun1x: vertical ink runs longer than this many 1x pixels are
	// window chrome (frame lines, scrollbar tracks), not glyphs; they are
	// removed from the segmentation planes so row bands stay separable.
	gpqNCCMaxRun1x = 14
	// gpqNCCMaxRunH1x: the horizontal counterpart (frame lines, separators,
	// button borders). Much larger than the vertical cap because halo
	// blending can bridge a whole word into one horizontal run - the longest
	// legitimate word run (a 13-glyph name) stays well under this at any
	// scale, while window-wide chrome lines exceed it.
	gpqNCCMaxRunH1x = 110
	// gpqNCCDigitSkipInk1x: a digits-only decode that leaves more than this
	// much core ink (in 1x-equivalent pixels) unexplained probably dropped a
	// digit - a wrong score is worse than a missed row, so it is discarded.
	gpqNCCDigitSkipInk1x = 8
)

// gpqGrayPhases are the sub-pixel phase offsets gray templates are generated
// at. NCC on continuous intensities degrades slowly with phase error, so
// half-pixel steps suffice.
var gpqGrayPhases = []float64{0, 0.125, 0.25, 0.375, 0.5, 0.625, 0.75, 0.875}

// gpqGrayPhasesChained is the coarser grid the CHAINED template sets use
// (screenshots magnified from below reference scale): the chain blur widens
// the correlation peak in phase, so quarter-pixel steps lose nothing while
// quartering the fine-tier scan and build cost.
var gpqGrayPhasesChained = []float64{0, 0.25, 0.5, 0.75}

// gpqGrayHalos are the glyph-antialiasing halo strengths templates are
// generated at. The game's own AA fringe varies with text colour and capture
// chain (and integer-scale smoothing preserves it crisply), so a single model
// value leaves one colour's glyphs just under the acceptance floor; the
// matcher keeps whichever variant correlates best. The upper value stays
// below the 0.55 ink cut so halo pixels never count as core ink.
var gpqGrayHalos = []float64{gpqSmoothHalo, 0.5}

// ── Luminance plane ─────────────────────────────────────────────────────────

// nccPlane holds the luminance field and its integral images the NCC matcher
// scores against, plus the binary plane used for segmentation only.
type nccPlane struct {
	w, h   int
	lum    []uint8   // row-major luminance
	lumF   []float32 // the same luminance as float32 (dot-product hot loop)
	integ  []float64 // (w+1)*(h+1) summed-area table of lum
	integ2 []float64 // summed-area table of lum²
	loose  [][]bool  // halo-inclusive neutral ink (row/word segmentation)
}

// buildNCCPlane computes the luminance plane, its integral images and the
// segmentation planes for one image at the given UI scale.
func buildNCCPlane(img image.Image, scale float64) *nccPlane {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	p := &nccPlane{w: w, h: h, lum: make([]uint8, w*h), lumF: make([]float32, w*h)}
	loose := make([][]bool, h)
	for y := 0; y < h; y++ {
		looseRow := make([]bool, w)
		for x := 0; x < w; x++ {
			r32, g32, b32, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			r, g, bl := int(r32>>8), int(g32>>8), int(b32>>8)
			p.lum[y*w+x] = uint8((r + g + bl) / 3)
			p.lumF[y*w+x] = float32(p.lum[y*w+x])
			maxc, minc := r, r
			if g > maxc {
				maxc = g
			}
			if bl > maxc {
				maxc = bl
			}
			if g < minc {
				minc = g
			}
			if bl < minc {
				minc = bl
			}
			looseRow[x] = maxc-minc <= gpqSmoothSpreadMax && minc >= gpqSmoothGrayLooseC
		}
		loose[y] = looseRow
	}
	clearLongVerticalRuns(loose, scalePx(gpqNCCMaxRun1x, scale))
	clearLongHorizontalRuns(loose, scalePx(gpqNCCMaxRunH1x, scale))
	p.loose = loose

	p.integ = make([]float64, (w+1)*(h+1))
	p.integ2 = make([]float64, (w+1)*(h+1))
	for y := 1; y <= h; y++ {
		var rs, rs2 float64
		row := p.lum[(y-1)*w : y*w]
		for x := 1; x <= w; x++ {
			v := float64(row[x-1])
			rs += v
			rs2 += v * v
			p.integ[y*(w+1)+x] = p.integ[(y-1)*(w+1)+x] + rs
			p.integ2[y*(w+1)+x] = p.integ2[(y-1)*(w+1)+x] + rs2
		}
	}
	return p
}

// clearLongVerticalRuns clears ink pixels belonging to vertical runs longer
// than maxRun: no glyph is that tall, so such runs are window chrome.
func clearLongVerticalRuns(grid [][]bool, maxRun int) {
	h := len(grid)
	if h == 0 {
		return
	}
	w := len(grid[0])
	for x := 0; x < w; x++ {
		for y := 0; y < h; {
			if !grid[y][x] {
				y++
				continue
			}
			y0 := y
			for y < h && grid[y][x] {
				y++
			}
			if y-y0 > maxRun {
				for yy := y0; yy < y; yy++ {
					grid[yy][x] = false
				}
			}
		}
	}
}

// clearLongHorizontalRuns clears ink pixels belonging to horizontal runs
// longer than maxRun: no word is that wide, so such runs are window chrome
// (frame lines, separators) that would otherwise merge row bands or inject
// phantom bands into the pitch model.
func clearLongHorizontalRuns(grid [][]bool, maxRun int) {
	for _, row := range grid {
		w := len(row)
		for x := 0; x < w; {
			if !row[x] {
				x++
				continue
			}
			x0 := x
			for x < w && row[x] {
				x++
			}
			if x-x0 > maxRun {
				for xx := x0; xx < x; xx++ {
					row[xx] = false
				}
			}
		}
	}
}

// rectSums returns the sum and sum-of-squares of the luminance over the
// rectangle [x0, x0+w) x [y0, y0+h).
func (p *nccPlane) rectSums(x0, y0, w, h int) (sp, sp2 float64) {
	stride := p.w + 1
	a := y0*stride + x0
	b := a + w
	c := (y0+h)*stride + x0
	d := c + w
	return p.integ[d] - p.integ[b] - p.integ[c] + p.integ[a],
		p.integ2[d] - p.integ2[b] - p.integ2[c] + p.integ2[a]
}

// bandLoosePrefix returns per-column ink prefix sums of the loose plane
// restricted to rows [y0, y1): pre[x+1]-pre[x] = ink in column x.
func (p *nccPlane) bandLoosePrefix(y0, y1 int) []int {
	pre := make([]int, p.w+1)
	for x := 0; x < p.w; x++ {
		c := 0
		for y := y0; y < y1; y++ {
			if p.loose[y][x] {
				c++
			}
		}
		pre[x+1] = pre[x] + c
	}
	return pre
}

// spanCoreThreshold derives the "certainly ink" luminance cuts for one word
// span adaptively: background is the span's low percentile, the text core its
// high one; the core cut sits at 55% of the way up, the strong cut (used only
// to anchor glyph-onset candidate columns) at 75% and the mid cut (faint but
// real ink, the faint-miss penalty's floor) at 35%. Fixed cuts cannot work
// here - the text colour is unknown (white vs gray rows) and smooth scaling
// pulls stroke intensities toward the background, so a cut calibrated for
// one combination leaves another with an empty core plane (which would make
// skipping ink free and empty every decode). ok=false means the span has no
// text-like contrast at all.
func (p *nccPlane) spanCoreThreshold(x0, x1, y0, y1 int) (int, int, int, bool) {
	var hist [256]int
	nPix := 0
	for y := y0; y < y1; y++ {
		row := p.lum[y*p.w : (y+1)*p.w]
		for x := x0; x < x1; x++ {
			hist[row[x]]++
			nPix++
		}
	}
	if nPix == 0 {
		return 0, 0, 0, false
	}
	q := func(f float64) int {
		target := int(f * float64(nPix))
		if target < 1 {
			target = 1
		}
		acc := 0
		for v := 0; v < 256; v++ {
			acc += hist[v]
			if acc >= target {
				return v
			}
		}
		return 255
	}
	bg, peak := q(0.10), q(0.98)
	if peak-bg < 40 {
		return 0, 0, 0, false
	}
	return bg + (peak-bg)*11/20, bg + (peak-bg)*3/4, bg + (peak-bg)*7/20, true
}

// spanCorePrefix builds the span-local per-column prefix sums of core-ink
// pixels (luminance >= thr) over rows [y0, y1), columns [x0, x1).
func (p *nccPlane) spanCorePrefix(x0, x1, y0, y1, thr int) []int {
	n := x1 - x0
	pre := make([]int, n+1)
	for i := 0; i < n; i++ {
		c := 0
		for y := y0; y < y1; y++ {
			if int(p.lum[y*p.w+x0+i]) >= thr {
				c++
			}
		}
		pre[i+1] = pre[i] + c
	}
	return pre
}

// spanFaintPrefix builds span-local per-column prefix sums of FAINT-INK
// WEIGHT: for every pixel, how far its luminance climbs above the mid cut,
// capped at the core cut (real-but-sub-core evidence like blurred ascenders
// and i-dots). Weighted sums beat counts here: a genuine ascender at 40-50%
// intensity outweighs background texture just above the mid cut.
func (p *nccPlane) spanFaintPrefix(x0, x1, y0, y1, thrMid, thrCore int) []int {
	n := x1 - x0
	pre := make([]int, n+1)
	for i := 0; i < n; i++ {
		c := 0
		for y := y0; y < y1; y++ {
			v := int(p.lum[y*p.w+x0+i])
			if v > thrCore {
				v = thrCore
			}
			if v > thrMid {
				c += v - thrMid
			}
		}
		pre[i+1] = pre[i] + c
	}
	return pre
}

// bandSpans returns word x-spans [x0, x1) of the loose ink within one band,
// restricted to columns [xLo, xHi), split on gaps >= gapStop blank columns.
func bandSpans(loosePre []int, xLo, xHi, gapStop int) [][2]int {
	colInk := func(x int) bool { return loosePre[x+1] > loosePre[x] }
	spans := [][2]int{}
	x := xLo
	for x < xHi {
		if !colInk(x) {
			x++
			continue
		}
		x1 := x
		blank := 0
		for cur := x; cur < xHi; cur++ {
			if colInk(cur) {
				x1 = cur + 1
				blank = 0
			} else {
				blank++
				if blank >= gapStop {
					break
				}
			}
		}
		spans = append(spans, [2]int{x, x1})
		x = x1
		for x < xHi && !colInk(x) {
			x++
		}
	}
	return spans
}

// ── Gray (intensity-field) glyph templates ──────────────────────────────────

// grayGlyph is one scaled glyph as a continuous intensity template: the
// binary 1x template with its antialiasing-halo model, resampled to the UI
// scale, padded by one 1x pixel of background on each side (the game's
// letter spacing) so every template has contrast for NCC and carries its own
// edge geometry.
type grayGlyph struct {
	r          rune      // base rune (',' baseline logic); pairs use 0
	text       string    // decoded text: one rune, or two for a pair template
	w, h       int       // padded template size, image pixels
	padX, padY int       // background padding on each side
	coreW      int       // advance width (unpadded glyph width)
	coreH      int       // unpadded glyph height
	vals       []float32 // w*h row-major intensities in [0,1]
	sumT       float64   // Σ vals
	varTN      float64   // n·variance = Σv² − (Σv)²/n
	ink        int       // core pixels >= 0.55 (budget prefilter, DP cost)
	floor      float64   // acceptance floor override when > font's (rescue templates)
	coarse     bool      // member of the coarse (half-pixel phase) scan tier
}

// grayGroup indexes every template variant of one decoded text (all phases,
// halos, ligature junction offsets), split into the coarse scan tier - the
// half-pixel phase variants scored first - and the fine remainder, which is
// only scored when the coarse tier shows the group is in contention (see
// nccCandidatesAt). minFloor is the lowest acceptance floor any variant of
// the group can be admitted at.
type grayGroup struct {
	coarse   []int
	fine     []int
	minFloor float64
}

// grayFont is a set of gray glyph templates at one UI scale, together with
// the acceptance floor and cost scale its decodes use (text vs digits).
type grayFont struct {
	glyphs    []grayGlyph
	groups    []grayGroup
	scale     float64
	minNCC    float64
	costScale float64
	// faintDiv > 0 charges each candidate 1/faintDiv per FAINT image pixel
	// (above the mid cut but below the core cut) where the template is
	// blank: the evidence that separates 'h' from 'n' or an i-dot from
	// nothing lives below the core threshold at fractional scales, and
	// without this charge a single wide glyph covering two letters wins on
	// raw correlation. Zero disables (digits: a faint separator fragment
	// must not shift a digit decode).
	faintDiv int
}

// glyphFloor is the acceptance floor variant g is admitted at in this font:
// the font's floor, raised by rescue-template overrides, and lowered to the
// text floor for the comma in a digits font (a thousands separator is
// stripped from the decoded value, so a false comma cannot corrupt a score -
// but a REJECTED genuine comma becomes skipped ink that trips the
// dropped-digit guard).
func (gf *grayFont) glyphFloor(g *grayGlyph) float64 {
	minNCC := gf.minNCC
	if g.floor > minNCC {
		minNCC = g.floor
	}
	if g.r == ',' && minNCC > gpqNCCMinText {
		minNCC = gpqNCCMinText
	}
	return minNCC
}

// buildGroups (re)derives the per-text variant groups from the glyph list.
func (gf *grayFont) buildGroups() {
	byText := map[string]int{}
	gf.groups = nil
	for i := range gf.glyphs {
		g := &gf.glyphs[i]
		gi, ok := byText[g.text]
		if !ok {
			gi = len(gf.groups)
			byText[g.text] = gi
			gf.groups = append(gf.groups, grayGroup{minFloor: gf.glyphFloor(g)})
		}
		grp := &gf.groups[gi]
		if fl := gf.glyphFloor(g); fl < grp.minFloor {
			grp.minFloor = fl
		}
		if g.coarse {
			grp.coarse = append(grp.coarse, i)
		} else {
			grp.fine = append(grp.fine, i)
		}
	}
	// Every group needs a coarse tier, or it could never come into
	// contention (defensive: all phase grids include 0).
	for gi := range gf.groups {
		if len(gf.groups[gi].coarse) == 0 {
			gf.groups[gi].coarse, gf.groups[gi].fine = gf.groups[gi].fine, nil
		}
	}
}

// digitsOnly returns a view restricted to digit and comma templates, with the
// stricter digit acceptance parameters.
func (gf *grayFont) digitsOnly() *grayFont {
	out := &grayFont{scale: gf.scale, minNCC: gpqNCCMinDigits, costScale: gpqNCCCostDigits}
	for i := range gf.glyphs {
		r := gf.glyphs[i].r
		if (r >= '0' && r <= '9') || r == ',' {
			out.glyphs = append(out.glyphs, gf.glyphs[i])
		}
	}
	out.buildGroups()
	return out
}

// isCoarsePhase reports whether a sub-pixel phase offset belongs to the
// coarse scan tier (the half-pixel grid).
func isCoarsePhase(d float64) bool { return d == 0 || d == 0.5 }

// smoothSampleAt is smoothSample with a configurable halo strength and floor
// semantics valid for negative coordinates (padding regions sample outside
// the template box).
func smoothSampleAt(g *gpqGlyph, sx, sy, halo float64) float64 {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	fx := sx - float64(x0)
	fy := sy - float64(y0)
	ink := func(x, y int) bool {
		if x < 0 || y < 0 || x >= g.w || y >= g.h {
			return false
		}
		return g.bits[y][x]
	}
	at := func(x, y int) float64 {
		if ink(x, y) {
			return 1
		}
		if ink(x-1, y) || ink(x+1, y) || ink(x, y-1) || ink(x, y+1) {
			return halo
		}
		return 0
	}
	return at(x0, y0)*(1-fx)*(1-fy) + at(x0+1, y0)*fx*(1-fy) +
		at(x0, y0+1)*(1-fx)*fy + at(x0+1, y0+1)*fx*fy
}

// buildGrayGlyphChained renders one template the way an anchored screenshot
// BELOW reference scale reaches the decoder: the glyph as the CLIENT shows
// it - its crisp 2x composition (pre = 2 doubles a 1x bitmap; pre = 1 takes
// a bitmap already on the composition grid) bilinear-minified to the
// anchored source scale srcFac with the halo model, at sub-pixel phase
// dx/dy in source pixels - then bilinear-magnified by up to the reference
// resolution, the same magnification resampleTableROI applies to the image.
// Template and content thus carry the same compound blur. Crisp
// reference-scale templates would not: their sharp strokes correlate the
// blurred instances of DIFFERENT thin glyphs (t/i/1, rt/i1) almost equally.
// And rasterizing from the 1x bitmaps would overshoot the blur the other
// way: the client's downscale keeps thin strokes at full intensity (they
// are two pixels wide on its 2x grid), which tent-sampling a 1x bitmap
// dilutes - the same confusions then flip direction (t reading as I).
func buildGrayGlyphChained(g *gpqGlyph, pre int, srcFac, up, dx, dy, halo float64) grayGlyph {
	base := g
	if pre == 2 {
		base = upscale2x(g)
	}
	eff := srcFac / float64(pre) // sampling rate from base's grid
	// Source-scale field, padded by one 1x pixel of background (the game's
	// letter spacing) like the direct build.
	sPadX := scalePx(1, srcFac)
	sPadY := scalePx(1, srcFac)
	sCoreW := int(math.Ceil(float64(base.w)*eff - dx))
	sCoreH := int(math.Ceil(float64(base.h)*eff - dy))
	if sCoreW < 1 {
		sCoreW = 1
	}
	if sCoreH < 1 {
		sCoreH = 1
	}
	sw := sCoreW + 2*sPadX
	sh := sCoreH + 2*sPadY
	src := make([]float64, sw*sh)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			src[y*sw+x] = smoothSampleAt(base, (float64(x-sPadX)+dx)/eff, (float64(y-sPadY)+dy)/eff, halo)
		}
	}
	srcAt := func(x, y int) float64 {
		if x < 0 || y < 0 || x >= sw || y >= sh {
			return 0
		}
		return src[y*sw+x]
	}

	// Reference-resolution geometry; the bilinear magnification aligns the
	// core origins and maps destination pixel centers into source space,
	// mirroring resampleTableROI.
	fac := srcFac * up
	padX := scalePx(1, fac)
	padY := scalePx(1, fac)
	coreW := int(float64(sCoreW)*up + 0.5)
	coreH := int(float64(sCoreH)*up + 0.5)
	if coreW < 1 {
		coreW = 1
	}
	if coreH < 1 {
		coreH = 1
	}
	w := coreW + 2*padX
	h := coreH + 2*padY
	vals := make([]float32, w*h)
	ink := 0
	var sum, sum2 float64
	for y := 0; y < h; y++ {
		syf := float64(sPadY) + (float64(y-padY)+0.5)/up - 0.5
		yb := int(math.Floor(syf))
		fy := syf - float64(yb)
		for x := 0; x < w; x++ {
			sxf := float64(sPadX) + (float64(x-padX)+0.5)/up - 0.5
			xb := int(math.Floor(sxf))
			fx := sxf - float64(xb)
			v := srcAt(xb, yb)*(1-fx)*(1-fy) + srcAt(xb+1, yb)*fx*(1-fy) +
				srcAt(xb, yb+1)*(1-fx)*fy + srcAt(xb+1, yb+1)*fx*fy
			vals[y*w+x] = float32(v)
			sum += v
			sum2 += v * v
			if v >= 0.55 && x >= padX && x < padX+coreW && y >= padY && y < padY+coreH {
				ink++
			}
		}
	}
	n := float64(w * h)
	return grayGlyph{
		r: g.r, text: string(g.r), w: w, h: h, padX: padX, padY: padY, coreW: coreW, coreH: coreH,
		vals: vals, sumT: sum, varTN: sum2 - sum*sum/n, ink: ink,
	}
}

// buildGrayGlyph resamples one binary template to scale fac at sub-pixel
// phase (dx, dy) as an intensity field with the given halo strength.
func buildGrayGlyph(g *gpqGlyph, fac, dx, dy, halo float64) grayGlyph {
	padX := scalePx(1, fac)
	padY := scalePx(1, fac)
	coreW := int(math.Ceil(float64(g.w)*fac - dx))
	coreH := int(math.Ceil(float64(g.h)*fac - dy))
	if coreW < 1 {
		coreW = 1
	}
	if coreH < 1 {
		coreH = 1
	}
	w := coreW + 2*padX
	h := coreH + 2*padY
	vals := make([]float32, w*h)
	ink := 0
	var sum, sum2 float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := smoothSampleAt(g, (float64(x-padX)+dx)/fac, (float64(y-padY)+dy)/fac, halo)
			vals[y*w+x] = float32(v)
			sum += v
			sum2 += v * v
			if v >= 0.55 && x >= padX && x < padX+coreW && y >= padY && y < padY+coreH {
				ink++
			}
		}
	}
	n := float64(w * h)
	return grayGlyph{
		r: g.r, text: string(g.r), w: w, h: h, padX: padX, padY: padY, coreW: coreW, coreH: coreH,
		vals: vals, sumT: sum, varTN: sum2 - sum*sum/n, ink: ink,
	}
}

// gpqLigatures are the glyph sequences whose horizontal strokes merge into
// one continuous bar under fractional-scale blur: the crossbars of f/t
// chains, optionally continued by a z whose top bar sits at the same height
// ("ftz" in Sw1ftz renders as a single 16px-wide bar). Single-glyph
// templates cannot reach the NCC floor against the merged forms, so these
// sequences get composite templates built from the same 1x bitmaps (bottom
// aligned, one blank spacing column - the font's letter spacing - filled
// wherever ink flanks it on both sides). "ly" is the crossbar-less member
// of the family: blur bridges the l's bar into the y's arm, and without a
// composite that models the bridge WITHOUT a left crossbar, a 't' template
// wins the merged span ("Gremlynz" decoding as "Gremtynz").
var gpqLigatures = []string{
	"ff", "ft", "tf", "tt", "fz", "tz", "fy", "ty", "ly",
	"ffz", "ftz", "tfz", "ttz",
}

// compositeGlyph renders a sequence of 1x templates into one, aligned on the
// BASELINE (descs[i] is glyph i's descender depth below it - 'y' hangs below
// the crossbar glyphs). offs[i] is the horizontal offset of glyph i+1
// relative to the previous glyph's right edge: the game KERNS crossbar pairs
// (in "ft" the t overlaps the f by one column, continuing its crossbar), so
// junctions are generated at several offsets and the matcher keeps whichever
// variant correlates best. Spacing columns flanked by ink on both sides are
// filled - the merged stroke the blur produces.
func compositeGlyph(glyphs []*gpqGlyph, offs, descs []int) gpqGlyph {
	above, below, w := 0, 0, 0
	x := 0
	for gi, g := range glyphs {
		if gi > 0 {
			x += offs[gi-1]
		}
		if x < 0 {
			// A negative junction offset can outrun a narrow glyph (the l
			// bar): clamp rather than draw off-grid.
			x = 0
		}
		if a := g.h - descs[gi]; a > above {
			above = a
		}
		if descs[gi] > below {
			below = descs[gi]
		}
		if x+g.w > w {
			w = x + g.w
		}
		x += g.w
	}
	h := above + below
	bits := make([][]bool, h)
	for y := 0; y < h; y++ {
		bits[y] = make([]bool, w)
	}
	x = 0
	for gi, g := range glyphs {
		if gi > 0 {
			x += offs[gi-1]
		}
		if x < 0 {
			x = 0
		}
		top := above - (g.h - descs[gi])
		for y := 0; y < g.h; y++ {
			for xx := 0; xx < g.w; xx++ {
				if g.bits[y][xx] {
					bits[top+y][x+xx] = true
				}
			}
		}
		if gi > 0 && offs[gi-1] > 0 && x >= offs[gi-1]+1 {
			for y := 0; y < h; y++ {
				if bits[y][x-offs[gi-1]-1] && bits[y][x] {
					for c := x - offs[gi-1]; c < x; c++ {
						bits[y][c] = true
					}
				}
			}
		}
		x += g.w
	}
	return gpqGlyph{r: 0, bits: bits, w: w, h: h, ink: countInk(bits), cols: countCols(bits)}
}

// ligatureOffsets enumerates the junction-offset combinations composites are
// generated at (1x-pixel units): each junction abuts (0), overlaps (-2, -1;
// crossbar kerning) or keeps the regular one-column spacing (1).
func ligatureOffsets(n int, steps []int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	out := [][]int{}
	for _, rest := range ligatureOffsets(n-1, steps) {
		for _, off := range steps {
			out = append(out, append(append([]int{}, rest...), off))
		}
	}
	return out
}

// upscale2x doubles a 1x template with nearest-neighbour sampling (the grid
// ligature composites are laid out on).
func upscale2x(g *gpqGlyph) *gpqGlyph {
	bits := make([][]bool, g.h*2)
	for y := range bits {
		bits[y] = make([]bool, g.w*2)
		for x := range bits[y] {
			bits[y][x] = g.bits[y/2][x/2]
		}
	}
	return &gpqGlyph{r: g.r, bits: bits, w: g.w * 2, h: g.h * 2, ink: countInk(bits), cols: countCols(bits)}
}

// grayRefFontFor returns the reference-resolution gray template set matched
// to the ANCHORED source scale the table region was resampled from: the
// direct 2x set when the screenshot was at or above reference resolution
// (minification preserves - or restores - sharpness), the chain-blurred set
// (see buildGrayGlyphChained) when it was magnified from below.
func (f *GPQFont) grayRefFontFor(srcScale float64) *grayFont {
	if srcScale >= gpqAnchorRefScale-0.005 {
		return f.grayScaledFont(gpqAnchorRefScale)
	}
	return f.grayFontAt(gpqAnchorRefScale, srcScale)
}

// grayScaledFont returns (building and caching once per scale) the gray
// template set for the given scale factor. Must be called on the base font.
func (f *GPQFont) grayScaledFont(fac float64) *grayFont {
	return f.grayFontAt(fac, 0)
}

// grayFontAt builds and caches one gray template set: rendered directly at
// scale fac when srcScale is zero, otherwise rendered at srcScale and
// bilinear-magnified to fac (the resampleTableROI chain).
func (f *GPQFont) grayFontAt(fac, srcScale float64) *grayFont {
	pct := int(fac*100 + 0.5)
	key := pct
	srcExact := 0.0
	if srcScale > 0 {
		srcPct := int(srcScale*100 + 0.5)
		srcExact = float64(srcPct) / 100
		key = 2_000_000 + srcPct // separate cache namespace per source scale
	}
	f.grayMu.Lock()
	defer f.grayMu.Unlock()
	if f.grayCache == nil {
		f.grayCache = map[int]*grayFont{}
	}
	if cached := f.grayCache[key]; cached != nil {
		return cached
	}
	exact := float64(pct) / 100
	build := func(g *gpqGlyph, bfac, dx, dy, halo float64) grayGlyph {
		if srcExact <= 0 {
			return buildGrayGlyph(g, bfac, dx, dy, halo)
		}
		ratio := srcExact / exact
		// Templates requested at the full font scale come from 1x bitmaps
		// (pre-doubled onto the client's 2x composition grid); the rescue
		// composites at half scale are laid out on that grid already.
		pre := 1
		if bfac > exact-1e-9 {
			pre = 2
		}
		return buildGrayGlyphChained(g, pre, bfac*ratio, 1/ratio, dx, dy, halo)
	}
	phases := gpqGrayPhases
	if srcExact > 0 {
		phases = gpqGrayPhasesChained
	}
	gf := &grayFont{scale: exact, minNCC: gpqNCCMinText, costScale: gpqNCCCostText, faintDiv: 1}
	for i := range f.glyphs {
		for _, halo := range gpqGrayHalos {
			for _, dy := range phases {
				for _, dx := range phases {
					g := build(&f.glyphs[i], exact, dx, dy, halo)
					g.coarse = isCoarsePhase(dx) && isCoarsePhase(dy)
					gf.glyphs = append(gf.glyphs, g)
				}
			}
		}
	}
	glyphFor := func(r rune) *gpqGlyph {
		for i := range f.glyphs {
			if f.glyphs[i].r == r {
				return &f.glyphs[i]
			}
		}
		return nil
	}
	xHeight := 0
	if u := glyphFor('u'); u != nil {
		xHeight = u.h
	}
	for _, lig := range gpqLigatures {
		parts := make([]*gpqGlyph, 0, len(lig))
		parts2x := make([]*gpqGlyph, 0, len(lig))
		descs := make([]int, 0, len(lig))
		descs2x := make([]int, 0, len(lig))
		ok := true
		for _, r := range lig {
			g := glyphFor(r)
			if g == nil {
				ok = false
				break
			}
			parts = append(parts, g)
			parts2x = append(parts2x, upscale2x(g))
			desc := 0
			if strings.ContainsRune("gjpqy", r) && xHeight > 0 && g.h > xHeight {
				desc = g.h - xHeight
			}
			descs = append(descs, desc)
			descs2x = append(descs2x, desc*2)
		}
		if !ok {
			continue
		}
		// Primary composites on the 1x grid.
		for _, offs := range ligatureOffsets(len(parts)-1, []int{-2, -1, 0, 1}) {
			comp := compositeGlyph(parts, offs, descs)
			for _, halo := range gpqGrayHalos {
				for _, dy := range phases {
					for _, dx := range phases {
						g := build(&comp, exact, dx, dy, halo)
						g.text = lig
						g.coarse = isCoarsePhase(dx) && isCoarsePhase(dy)
						gf.glyphs = append(gf.glyphs, g)
					}
				}
			}
		}
		// Half-pixel junction variants (the client composes at 2x, so pair
		// kerning can land between 1x pixels: Suffer's "ff" sits at -1.5).
		// These are RESCUE templates behind a raised floor so they can only
		// win where they fit conspicuously well.
		if len(parts) == 2 {
			for _, off2 := range []int{-3, -1} {
				comp := compositeGlyph(parts2x, []int{off2}, descs2x)
				for _, halo := range gpqGrayHalos {
					for _, dy := range []float64{0, 0.25, 0.5, 0.75} {
						for _, dx := range []float64{0, 0.25, 0.5, 0.75} {
							g := build(&comp, exact/2, dx, dy, halo)
							g.text = lig
							g.floor = 0.74
							g.coarse = isCoarsePhase(dx) && isCoarsePhase(dy)
							gf.glyphs = append(gf.glyphs, g)
						}
					}
				}
			}
		}
	}
	gf.buildGroups()
	f.grayCache[key] = gf
	return gf
}

// ── NCC scoring and word decoding ───────────────────────────────────────────

// nccScore correlates template g with the luminance plane, core top-left at
// (x, y): the pure dot-product correlation, no accounting - it runs for
// every (template, y offset) pair, so it must stay a tight loop over the
// float32 luminance. Returns -1 when the placement is out of bounds or
// degenerate.
func nccScore(p *nccPlane, g *grayGlyph, x, y int) float64 {
	x0 := x - g.padX
	y0 := y - g.padY
	if x0 < 0 || y0 < 0 || x0+g.w > p.w || y0+g.h > p.h {
		return -1
	}
	n := float64(g.w * g.h)
	sp, sp2 := p.rectSums(x0, y0, g.w, g.h)
	denP := sp2 - sp*sp/n
	if denP < 1e-6 || g.varTN < 1e-6 {
		return -1
	}
	var stp float64
	for yy := 0; yy < g.h; yy++ {
		base := (y0+yy)*p.w + x0
		trow := g.vals[yy*g.w : (yy+1)*g.w]
		lrow := p.lumF[base : base+g.w]
		lrow = lrow[:len(trow)]
		var acc0, acc1, acc2, acc3 float32
		i := 0
		for ; i+3 < len(trow); i += 4 {
			acc0 += trow[i] * lrow[i]
			acc1 += trow[i+1] * lrow[i+1]
			acc2 += trow[i+2] * lrow[i+2]
			acc3 += trow[i+3] * lrow[i+3]
		}
		for ; i < len(trow); i++ {
			acc0 += trow[i] * lrow[i]
		}
		stp += float64(acc0 + acc1 + acc2 + acc3)
	}
	return (stp - g.sumT*sp/n) / math.Sqrt(denP*g.varTN)
}

// nccExplained reports what template g placed at (x, y) EXPLAINS within its
// advance columns: coreExpl counts image core-ink pixels (>= thrCore)
// covered by template ink, faintExpl the covered faint-ink weight (luminance
// above thrMid, capped at thrCore). The caller subtracts these from the
// window totals: ink the placement leaves stranded - an 'h' ascender above
// an 'n', an i-dot above a bare stem - is charged like skipped ink, which no
// ink-count budget can hide. Runs once per accepted placement (nccScore
// already scanned the offsets), so the per-pixel branches stay off the hot
// path.
func nccExplained(p *nccPlane, g *grayGlyph, x, y, thrMid, thrCore int) (int, int) {
	x0 := x - g.padX
	y0 := y - g.padY
	coreExpl, faintExpl := 0, 0
	for yy := 0; yy < g.h; yy++ {
		base := (y0+yy)*p.w + x0
		trow := g.vals[yy*g.w : (yy+1)*g.w]
		for i := g.padX; i < g.padX+g.coreW; i++ {
			if trow[i] >= 0.35 {
				lum := int(p.lum[base+i])
				if lum >= thrCore {
					coreExpl++
					faintExpl += thrCore - thrMid
				} else if lum > thrMid {
					faintExpl += lum - thrMid
				}
			}
		}
	}
	return coreExpl, faintExpl
}

// nccCand is one acceptable glyph candidate at a column. cost is the DP cost
// of taking it (before the per-glyph insertion penalty): the correlation
// shortfall scaled to ink pixels, plus the window's core ink the template
// cannot account for - so a small glyph cannot "explain" a big one by
// matching a fragment of it (e.g. u inside 0), the unexplained remainder is
// charged just as skipping it would be.
type nccCand struct {
	text string // decoded text (one rune, or two for a pair template)
	n    int    // number of runes in text (insertion penalty multiplier)
	w    int    // advance (core) width
	cost int
}

// gpqNCCCoarseSlack is how far below a group's acceptance floor its best
// COARSE-tier correlation may fall while the group still gets the full
// fine-phase scan. The coarse tier's half-pixel phase grid misaligns a
// genuine glyph by at most a quarter pixel, which costs it far less
// correlation than this slack - so no group that could produce an accepted
// candidate is skipped - while templates for the wrong glyph sit far below
// the floor and die after the cheap coarse scan.
const gpqNCCCoarseSlack = 0.12

// nccCandidatesAt collects every template whose best NCC over the band's
// vertical offsets, with its core's left edge at column x, reaches the
// font's acceptance floor. Variants are scanned per text group in two
// tiers - coarse phases first, the rest only when the group is in
// contention (see gpqNCCCoarseSlack) - and the best passing variant per
// (text, width) becomes one candidate. tightPre is the span-local core-ink
// prefix for span columns [spanX0, spanX1).
func nccCandidatesAt(p *nccPlane, f *grayFont, x, y0, y1 int, loosePre, tightPre, faintPre []int, spanX0, spanX1 int, thrMid, thrCore int) []nccCand {
	bandH := y1 - y0
	n := spanX1 - spanX0
	win := func(pre []int, a, b int) int {
		a -= spanX0
		b -= spanX0
		if a < 0 {
			a = 0
		}
		if b > n {
			b = n
		}
		if a >= b {
			return 0
		}
		return pre[b] - pre[a]
	}
	tightWin := func(a, b int) int { return win(tightPre, a, b) }

	// cheapest passing placement per advance width within the current group
	// (phases shift coreW by a pixel, so one text can yield two widths).
	type placed struct {
		w, cost int
	}
	var bests []placed
	groupBest := -1.0
	scan := func(gi int) {
		g := &f.glyphs[gi]
		if x+g.coreW > p.w || g.coreH > bandH {
			return
		}
		// Ink-budget prefilter on the segmentation planes: the glyph's core
		// ink must be plausible for the window's loose (upper bound on real
		// ink incl. halos) and core-ink counts. Generous slack - NCC does the
		// real discrimination.
		lw := loosePre[x+g.coreW] - loosePre[x]
		tw := tightWin(x, x+g.coreW)
		if g.ink-lw > g.ink/3+6 || tw-g.ink > g.ink+6 {
			return
		}
		yStart := y0
		yEnd := y1 - g.coreH
		if g.r == ',' {
			// A comma hangs off the baseline: only offsets near the band's
			// bottom are geometrically valid.
			if m := y1 - g.coreH - scalePx(3, f.scale); m > yStart {
				yStart = m
			}
		}
		best := -1.0
		bestY := yStart
		if yEnd-yStart >= 4 {
			// The correlation peak spans several rows (glyph-height
			// templates), so a stride-2 sweep with a +-1 refinement finds
			// it at half the scoring cost.
			for y := yStart; y <= yEnd; y += 2 {
				if s := nccScore(p, g, x, y); s > best {
					best = s
					bestY = y
				}
			}
			for _, y := range [2]int{bestY - 1, bestY + 1} {
				if y < yStart || y > yEnd {
					continue
				}
				if s := nccScore(p, g, x, y); s > best {
					best = s
					bestY = y
				}
			}
		} else {
			for y := yStart; y <= yEnd; y++ {
				if s := nccScore(p, g, x, y); s > best {
					best = s
					bestY = y
				}
			}
		}
		if best > groupBest {
			groupBest = best
		}
		if best < f.glyphFloor(g) {
			return
		}
		// The variant passed its floor: account for what its placement
		// explains and derive its DP cost (this branchy pass runs only for
		// floor-passing variants - a small fraction of the scans).
		coreExpl, faintExpl := nccExplained(p, g, x, bestY, thrMid, thrCore)
		// Unexplained-ink residual, measured against the actual placement:
		// window core ink the placed template does not cover. This is what
		// keeps a small glyph from "explaining" a big one by matching a
		// fragment of it (u inside 0, n inside h) - the stranded remainder
		// is charged just as skipping it would be.
		residual := tw - coreExpl
		if residual < 0 {
			residual = 0
		}
		// The correlation shortfall is charged against ONE glyph's worth of
		// ink even for ligature composites: a pair template integrates twice
		// the area, so charging its full ink would make it cost double a
		// single at the same correlation - unbeatable by design.
		nRunes := len([]rune(g.text))
		cost := int((1-best)*f.costScale*float64(g.ink)/float64(nRunes)) + residual
		if f.faintDiv > 0 && thrCore > thrMid {
			// Faint-ink weight of the window (band rows, advance columns)
			// minus what the template explains: sub-core evidence (blurred
			// h ascenders, i-dots) a wrong glyph leaves stranded, converted
			// to core-ink-pixel units.
			if fm := (win(faintPre, x, x+g.coreW) - faintExpl) / (thrCore - thrMid); fm > 0 {
				cost += fm / f.faintDiv
			}
		}
		for bi := range bests {
			if bests[bi].w == g.coreW {
				if cost < bests[bi].cost {
					bests[bi].cost = cost
				}
				return
			}
		}
		bests = append(bests, placed{w: g.coreW, cost: cost})
	}

	cands := []nccCand{}
	for grpI := range f.groups {
		grp := &f.groups[grpI]
		bests = bests[:0]
		groupBest = -1.0
		for _, gi := range grp.coarse {
			scan(gi)
		}
		if groupBest >= grp.minFloor-gpqNCCCoarseSlack {
			for _, gi := range grp.fine {
				scan(gi)
			}
		}
		if len(bests) == 0 {
			continue
		}
		text := f.glyphs[grp.coarse[0]].text
		nRunes := len([]rune(text))
		for _, b := range bests {
			cands = append(cands, nccCand{text: text, n: nRunes, w: b.w, cost: b.cost})
		}
	}
	return cands
}

// nccDecodeSpanCov decodes the word occupying image columns [x0, x1) of the
// band rows [y0, y1) with a dynamic program that globally minimises
// unexplained core ink: a glyph match costs (1-ncc)-scaled ink plus
// insertPenalty, a skipped column costs its core-ink pixels, and the cheapest
// full explanation wins. Core ink is cut adaptively per span (see
// spanCoreThreshold). Glyph candidates are only evaluated at core-ink onset
// columns (and their immediate left neighbours, where the true antialiased
// edge starts). When guardInk is true, a decode that leaves more than
// maxSkipInk core-ink pixels unexplained returns "" instead - used for score
// cells, where a dropped digit silently corrupts the value. It reports how
// much of the span's core ink the accepted glyphs explain (explained, total
// pixels). FAILS CLOSED on clock expiry: returns "" rather than a partial
// word.
func nccDecodeSpanCov(p *nccPlane, f *grayFont, x0, x1, y0, y1 int, loosePre []int, insertPenalty int, guardInk bool, clock *parseClock) (string, int, int) {
	n := x1 - x0
	if n <= 0 {
		return "", 0, 0
	}
	thr, thrStrong, thrMid, ok := p.spanCoreThreshold(x0, x1, y0, y1)
	if !ok {
		return "", 0, 0
	}
	tightPre := p.spanCorePrefix(x0, x1, y0, y1, thr)
	strongPre := p.spanCorePrefix(x0, x1, y0, y1, thrStrong)
	faintPre := p.spanFaintPrefix(x0, x1, y0, y1, thrMid, thr)
	tightCol := func(x int) int { return tightPre[x-x0+1] - tightPre[x-x0] }
	strongCol := func(x int) int { return strongPre[x-x0+1] - strongPre[x-x0] }
	looseCol := func(x int) int { return loosePre[x+1] - loosePre[x] }

	// Candidate columns: onsets of core ink and of strong ink, plus the two
	// columns to their left (the antialiased glyph edge precedes the first
	// core column; a strong-ink onset conversely lands on the glyph's first
	// full-intensity column, whose faint edge neighbour may already have
	// crossed the core cut), plus the span start. Halo blending can bridge
	// the core-ink gap between adjacent glyphs (no onset), so whenever a
	// glyph match ends at a column, the columns just after it are marked as
	// candidates too - decoding chains through merged glyph runs.
	candCol := make([]bool, n)
	mark := func(x int) {
		if x >= x0 && x < x1 {
			candCol[x-x0] = true
		}
	}
	mark(x0)
	mark(x0 + 1)
	for x := x0; x < x1; x++ {
		if tightCol(x) > 0 && (x == x0 || tightCol(x-1) == 0) {
			mark(x - 2)
			mark(x - 1)
			mark(x)
		}
		if strongCol(x) > 0 && (x == x0 || strongCol(x-1) == 0) {
			mark(x - 2)
			mark(x - 1)
			mark(x)
		}
	}

	bridgePx := scalePx(1, f.scale) + 1
	const inf = int(^uint(0) >> 2)
	dp := make([]int, n+1)
	fromI := make([]int, n+1)
	fromCh := make([]string, n+1)
	for i := 1; i <= n; i++ {
		dp[i] = inf
	}
	for i := 0; i < n; i++ {
		if dp[i] == inf {
			continue
		}
		if c := dp[i] + tightCol(x0+i); c < dp[i+1] {
			dp[i+1] = c
			fromI[i+1] = i
			fromCh[i+1] = ""
		}
		if !candCol[i] || looseCol(x0+i) == 0 {
			continue
		}
		if clock.exceeded() {
			// Fail closed: an expired deadline must never yield a partial
			// word that could reconcile onto the wrong member.
			return "", 0, tightPre[n]
		}
		// Bridge base: the inter-glyph gap fills with blur from BOTH
		// neighbours' edges - real ink that the templates model in their
		// PADDING, which the DP's column-cover accounting cannot credit.
		// Charged at full skip cost that boundary makes a letter pair lose
		// to a single wide glyph covering both ("ri" decoding as "n"), so a
		// candidate may start from a state up to bridgePx columns back,
		// paying only for the STRONG ink it crosses: a blurred glyph edge
		// is mid-intensity and crosses nearly free, while a genuine stem is
		// full-intensity and stays expensive - so "n" cannot be eaten as
		// "r" plus a bridged stem. Only states where a glyph match ended
		// may bridge (digits keep the harsher half-of-core charge: their
		// guard counts skipped ink and a digit must never swallow a
		// neighbour's columns cheaply).
		bridgeBase, bridgeFrom := dp[i], i
		{
			binkc := 0
			for b := 1; b <= bridgePx && i-b >= 0; b++ {
				if f.faintDiv == 0 {
					binkc += tightCol(x0+i-b) / 2
				} else {
					binkc += strongCol(x0 + i - b)
				}
				if fromCh[i-b] != "" && dp[i-b] != inf && dp[i-b]+binkc < bridgeBase {
					bridgeBase, bridgeFrom = dp[i-b]+binkc, i-b
				}
			}
		}
		for _, cand := range nccCandidatesAt(p, f, x0+i, y0, y1, loosePre, tightPre, faintPre, x0, x1, thrMid, thr) {
			j := i + cand.w
			if j > n {
				j = n
			}
			// The next glyph starts within a few columns of this one's end
			// (template widths can be off by a pixel from the instance).
			mark(x0 + j - 1)
			mark(x0 + j)
			mark(x0 + j + 1)
			mark(x0 + j + 2)
			if cost := dp[i] + cand.cost + cand.n*insertPenalty; cost < dp[j] {
				dp[j] = cost
				fromI[j] = i
				fromCh[j] = cand.text
			}
			if cost := bridgeBase + cand.cost + cand.n*insertPenalty; cost < dp[j] {
				dp[j] = cost
				fromI[j] = bridgeFrom
				fromCh[j] = cand.text
			}
		}
	}

	runes := []rune{}
	covered := make([]bool, n)
	for i := n; i > 0; i = fromI[i] {
		if fromCh[i] != "" {
			rs := []rune(fromCh[i])
			for k := len(rs) - 1; k >= 0; k-- {
				runes = append(runes, rs[k])
			}
			// A matched glyph explains its advance span plus one column on
			// each side: the antialiased glyph edge is part of the template's
			// padding model but never inside the advance span.
			for c := fromI[i] - 1; c <= i && c < n; c++ {
				if c >= 0 {
					covered[c] = true
				}
			}
		}
	}
	totalInk := tightPre[n]
	skippedInk := 0
	for c := 0; c < n; c++ {
		if !covered[c] {
			skippedInk += tightPre[c+1] - tightPre[c]
		}
	}
	if guardInk && skippedInk > int(float64(gpqNCCDigitSkipInk1x)*f.scale*f.scale) {
		return "", totalInk - skippedInk, totalInk
	}
	for l, r := 0, len(runes)-1; l < r; l, r = l+1, r-1 {
		runes[l], runes[r] = runes[r], runes[l]
	}
	return string(runes), totalInk - skippedInk, totalInk
}

// ── DP insertion cost ───────────────────────────────────────────────────────

// nccInsertPenalty is the per-glyph DP insertion cost: high enough that
// near-free tiny glyphs (commas, bare bars) cannot carpet a decode by
// "explaining" stray fragments, low enough that a genuine narrow letter's
// total match cost stays below its skip cost.
func nccInsertPenalty(scale float64) int { return int(scale*scale + 0.5) }
