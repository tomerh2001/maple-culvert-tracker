package helpers

import (
	"image"
	"math"
	"sort"
	"sync"
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
	gpqNCCMinText   = 0.65
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
var gpqGrayPhases = []float64{0, 0.5}

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
	integ  []float64 // (w+1)*(h+1) summed-area table of lum
	integ2 []float64 // summed-area table of lum²
	loose  [][]bool  // halo-inclusive neutral ink (row/word segmentation)
}

// buildNCCPlane computes the luminance plane, its integral images and the
// segmentation planes for one image at the given UI scale.
func buildNCCPlane(img image.Image, scale float64) *nccPlane {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	p := &nccPlane{w: w, h: h, lum: make([]uint8, w*h)}
	loose := make([][]bool, h)
	for y := 0; y < h; y++ {
		looseRow := make([]bool, w)
		for x := 0; x < w; x++ {
			r32, g32, b32, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			r, g, bl := int(r32>>8), int(g32>>8), int(b32>>8)
			p.lum[y*w+x] = uint8((r + g + bl) / 3)
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

// cloneGrid deep-copies a binary ink grid.
func cloneGrid(grid [][]bool) [][]bool {
	out := make([][]bool, len(grid))
	for y, row := range grid {
		out[y] = append([]bool(nil), row...)
	}
	return out
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
// to anchor glyph-onset candidate columns) at 75%. Fixed cuts cannot work
// here - the text colour is unknown (white vs gray rows) and smooth scaling
// pulls stroke intensities toward the background, so a cut calibrated for
// one combination leaves another with an empty core plane (which would make
// skipping ink free and empty every decode). ok=false means the span has no
// text-like contrast at all.
func (p *nccPlane) spanCoreThreshold(x0, x1, y0, y1 int) (int, int, bool) {
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
		return 0, 0, false
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
		return 0, 0, false
	}
	return bg + (peak-bg)*11/20, bg + (peak-bg)*3/4, true
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
	r          rune
	w, h       int       // padded template size, image pixels
	padX, padY int       // background padding on each side
	coreW      int       // advance width (unpadded glyph width)
	coreH      int       // unpadded glyph height
	vals       []float32 // w*h row-major intensities in [0,1]
	sumT       float64   // Σ vals
	varTN      float64   // n·variance = Σv² − (Σv)²/n
	ink        int       // core pixels >= 0.55 (budget prefilter, DP cost)
}

// grayFont is a set of gray glyph templates at one UI scale, together with
// the acceptance floor and cost scale its decodes use (text vs digits).
type grayFont struct {
	glyphs    []grayGlyph
	scale     float64
	minNCC    float64
	costScale float64
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
	return out
}

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
		r: g.r, w: w, h: h, padX: padX, padY: padY, coreW: coreW, coreH: coreH,
		vals: vals, sumT: sum, varTN: sum2 - sum*sum/n, ink: ink,
	}
}

// grayScaledFont returns (building and caching once per scale) the gray
// template set for the given scale factor. Must be called on the base font.
func (f *GPQFont) grayScaledFont(fac float64) *grayFont {
	pct := int(fac*100 + 0.5)
	f.grayMu.Lock()
	defer f.grayMu.Unlock()
	if f.grayCache == nil {
		f.grayCache = map[int]*grayFont{}
	}
	if cached := f.grayCache[pct]; cached != nil {
		return cached
	}
	exact := float64(pct) / 100
	gf := &grayFont{scale: exact, minNCC: gpqNCCMinText, costScale: gpqNCCCostText}
	for i := range f.glyphs {
		for _, halo := range gpqGrayHalos {
			for _, dy := range gpqGrayPhases {
				for _, dx := range gpqGrayPhases {
					gf.glyphs = append(gf.glyphs, buildGrayGlyph(&f.glyphs[i], exact, dx, dy, halo))
				}
			}
		}
	}
	f.grayCache[pct] = gf
	return gf
}

// ── NCC scoring and word decoding ───────────────────────────────────────────

// nccScore correlates template g with the luminance plane, core top-left at
// (x, y). Returns -1 when the placement is out of bounds or degenerate.
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
		var acc float32
		for i, tv := range trow {
			acc += tv * float32(p.lum[base+i])
		}
		stp += float64(acc)
	}
	return (stp - g.sumT*sp/n) / math.Sqrt(denP*g.varTN)
}

// nccCand is one acceptable glyph candidate at a column. cost is the DP cost
// of taking it (before the per-glyph insertion penalty): the correlation
// shortfall scaled to ink pixels, plus the window's core ink the template
// cannot account for - so a small glyph cannot "explain" a big one by
// matching a fragment of it (e.g. u inside 0), the unexplained remainder is
// charged just as skipping it would be.
type nccCand struct {
	r    rune
	w    int // advance (core) width
	cost int
}

// nccCandidatesAt collects every template whose best NCC over the band's
// vertical offsets, with its core's left edge at column x, reaches gpqNCCMin.
// Candidates are deduplicated per (rune, width), keeping the cheapest.
// tightPre is the span-local core-ink prefix for span columns [spanX0, spanX1).
func nccCandidatesAt(p *nccPlane, f *grayFont, x, y0, y1 int, loosePre, tightPre []int, spanX0, spanX1 int) []nccCand {
	bandH := y1 - y0
	n := spanX1 - spanX0
	tightWin := func(a, b int) int {
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
		return tightPre[b] - tightPre[a]
	}
	cands := []nccCand{}
	for gi := range f.glyphs {
		g := &f.glyphs[gi]
		if x+g.coreW > p.w || g.coreH > bandH {
			continue
		}
		// Ink-budget prefilter on the segmentation planes: the glyph's core
		// ink must be plausible for the window's loose (upper bound on real
		// ink incl. halos) and core-ink counts. Generous slack - NCC does the
		// real discrimination.
		lw := loosePre[x+g.coreW] - loosePre[x]
		tw := tightWin(x, x+g.coreW)
		if g.ink-lw > g.ink/3+6 || tw-g.ink > g.ink+6 {
			continue
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
		for y := yStart; y <= yEnd; y++ {
			if s := nccScore(p, g, x, y); s > best {
				best = s
			}
		}
		minNCC := f.minNCC
		if g.r == ',' && minNCC > gpqNCCMinText {
			// A thousands separator is stripped from the decoded value, so a
			// false comma cannot corrupt a score - but a REJECTED genuine
			// comma becomes skipped ink that trips the dropped-digit guard.
			// Admit it at the text floor even in the digits font.
			minNCC = gpqNCCMinText
		}
		if best < minNCC {
			continue
		}
		residual := tw - g.ink
		if residual < 0 {
			residual = 0
		}
		cost := int((1-best)*f.costScale*float64(g.ink)) + residual
		dup := false
		for ci := range cands {
			if cands[ci].r == g.r && cands[ci].w == g.coreW {
				dup = true
				if cost < cands[ci].cost {
					cands[ci].cost = cost
				}
				break
			}
		}
		if !dup {
			cands = append(cands, nccCand{r: g.r, w: g.coreW, cost: cost})
		}
	}
	return cands
}

// nccDecodeSpan decodes the word occupying image columns [x0, x1) of the band
// rows [y0, y1) with a dynamic program that globally minimises unexplained
// core ink: a glyph match costs (1-ncc)-scaled ink plus insertPenalty, a
// skipped column costs its core-ink pixels, and the cheapest full explanation
// wins. Core ink is cut adaptively per span (see spanCoreThreshold). Glyph
// candidates are only evaluated at core-ink onset columns (and their
// immediate left neighbours, where the true antialiased edge starts).
// When guardInk is true, a decode that leaves more than maxSkipInk core-ink
// pixels unexplained returns "" instead - used for score cells, where a
// dropped digit silently corrupts the value.
func nccDecodeSpan(p *nccPlane, f *grayFont, x0, x1, y0, y1 int, loosePre []int, insertPenalty int, guardInk bool, clock *parseClock) string {
	text, _, _ := nccDecodeSpanCov(p, f, x0, x1, y0, y1, loosePre, insertPenalty, guardInk, clock)
	return text
}

// nccDecodeSpanCov is nccDecodeSpan additionally reporting how much of the
// span's core ink the accepted glyphs explain (explained, total pixels).
// FAILS CLOSED on clock expiry: returns "" rather than a partial word.
func nccDecodeSpanCov(p *nccPlane, f *grayFont, x0, x1, y0, y1 int, loosePre []int, insertPenalty int, guardInk bool, clock *parseClock) (string, int, int) {
	n := x1 - x0
	if n <= 0 {
		return "", 0, 0
	}
	thr, thrStrong, ok := p.spanCoreThreshold(x0, x1, y0, y1)
	if !ok {
		return "", 0, 0
	}
	tightPre := p.spanCorePrefix(x0, x1, y0, y1, thr)
	strongPre := p.spanCorePrefix(x0, x1, y0, y1, thrStrong)
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

	const inf = int(^uint(0) >> 2)
	dp := make([]int, n+1)
	fromI := make([]int, n+1)
	fromCh := make([]rune, n+1)
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
			fromCh[i+1] = 0
		}
		if !candCol[i] || looseCol(x0+i) == 0 {
			continue
		}
		if clock.exceeded() {
			// Fail closed: an expired deadline must never yield a partial
			// word that could reconcile onto the wrong member.
			return "", 0, tightPre[n]
		}
		for _, cand := range nccCandidatesAt(p, f, x0+i, y0, y1, loosePre, tightPre, x0, x1) {
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
			if cost := dp[i] + cand.cost + insertPenalty; cost < dp[j] {
				dp[j] = cost
				fromI[j] = i
				fromCh[j] = cand.r
			}
		}
	}

	runes := []rune{}
	covered := make([]bool, n)
	for i := n; i > 0; i = fromI[i] {
		if fromCh[i] != 0 {
			runes = append(runes, fromCh[i])
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

// ── Table location and row parsing over the NCC plane ───────────────────────

// nccInsertPenalty is the per-glyph DP insertion cost: high enough that
// near-free tiny glyphs (commas, bare bars) cannot carpet a decode by
// "explaining" stray fragments, low enough that a genuine narrow letter's
// total match cost stays below its skip cost.
func nccInsertPenalty(scale float64) int { return int(scale*scale + 0.5) }

// digitsNCCClass classifies a decoded word as numeric for column
// identification ONLY (never for the score value, which always comes from a
// digits-only re-decode). On top of digitsScaled's confusions it maps u/o/O
// to 0: under smooth scaling a 0's bowl correlates with those shapes.
func digitsNCCClass(s string) string {
	mapped := []rune{}
	for _, r := range s {
		if r == 'u' || r == 'o' || r == 'O' {
			r = '0'
		}
		mapped = append(mapped, r)
	}
	return digitsScaled(string(mapped))
}

// nccRowShape is the span structure of one sampled data row: the x-ranges of
// its name span, culvert span and rightmost span.
type nccRowShape struct {
	bandY0               int
	nameX0               int
	culvertX0, culvertX1 int
	rightX1              int
}

// sampleRowShape decodes every span of one band and, if the band looks like a
// data row (>= 3 numeric columns), returns its column shape. The culvert span
// is the second-to-last numeric one (Flag Race is last); the name span sits
// two spans left of the first numeric one (Level), which skips sidebar text
// sharing the band without hardcoding window geometry.
func sampleRowShape(p *nccPlane, f *grayFont, y0, y1 int, loosePre []int, clock *parseClock) (nccRowShape, bool) {
	s := f.scale
	ins := nccInsertPenalty(s)
	spans := bandSpans(loosePre, 0, p.w, scalePx(gpqGapStop, s))
	if len(spans) < 4 {
		return nccRowShape{}, false
	}
	digitIdx := []int{}
	for si, sp := range spans {
		text := nccDecodeSpan(p, f, sp[0], sp[1], y0, y1, loosePre, ins, false, clock)
		if digitsNCCClass(text) != "" {
			digitIdx = append(digitIdx, si)
		}
	}
	if len(digitIdx) < 3 {
		return nccRowShape{}, false
	}
	nameIdx := digitIdx[0] - 2
	if nameIdx < 0 {
		return nccRowShape{}, false
	}
	culvert := spans[digitIdx[len(digitIdx)-2]]
	return nccRowShape{
		bandY0:    y0,
		nameX0:    spans[nameIdx][0],
		culvertX0: culvert[0],
		culvertX1: culvert[1],
		rightX1:   spans[len(spans)-1][1],
	}, true
}

// locateTableNCC derives the table region from the data rows themselves: the
// header of a real client window is rendered in a different (smooth UI) font
// the glyph templates can never decode, but the rows below it are the game's
// bitmap font, and their numeric-column structure identifies every column.
// Bands starting at the pitch estimate's first table row are sampled until
// enough agree on a shape; when that cut yields too few row shapes (a wrong
// firstRowY must be recoverable), the whole grid is rescanned from the top.
func locateTableNCC(p *nccPlane, f *grayFont, firstRowY int, clock *parseClock) tableRegion {
	s := f.scale
	bands := detectRowsGap(p.loose, scalePx(3, s))
	collect := func(fromY int) []nccRowShape {
		shapes := []nccRowShape{}
		tried := 0
		for _, band := range bands {
			if fromY >= 0 && band[1] <= fromY-4 {
				continue
			}
			if clock.exceeded() || tried >= 6 || len(shapes) >= 3 {
				break
			}
			tried++
			loosePre := p.bandLoosePrefix(band[0], band[1])
			if shape, ok := sampleRowShape(p, f, band[0], band[1], loosePre, clock); ok {
				shapes = append(shapes, shape)
			}
		}
		return shapes
	}
	shapes := collect(firstRowY)
	if len(shapes) < 2 && firstRowY >= 0 {
		// The firstRowY anchor may be wrong (estimated on a different plane):
		// rescan without it before giving up, and stop trusting it below.
		shapes = collect(-1)
		firstRowY = -1
	}
	if len(shapes) < 2 {
		return tableRegion{x1: p.w, nameLimit: scalePx(legacyNameColWidth, s)}
	}
	// Consensus: names are left-aligned (medians agree), culvert numbers are
	// right-aligned (union covers shorter values), the right edge is the
	// rightmost span seen.
	nameXs := make([]int, 0, len(shapes))
	culX0, culX1, rightX1 := shapes[0].culvertX0, shapes[0].culvertX1, 0
	for _, sh := range shapes {
		nameXs = append(nameXs, sh.nameX0)
		if sh.culvertX0 < culX0 {
			culX0 = sh.culvertX0
		}
		if sh.culvertX1 > culX1 {
			culX1 = sh.culvertX1
		}
		if sh.rightX1 > rightX1 {
			rightX1 = sh.rightX1
		}
	}
	sort.Ints(nameXs)
	nameX0 := nameXs[len(nameXs)/2]

	x0 := nameX0 - scalePx(8, s)
	if x0 < 0 {
		x0 = 0
	}
	// Keep the right edge tight: the numbers there are right-aligned, and a
	// generous margin would pull in scrollbar fragments that inflate row
	// bands (breaking baseline-anchored glyphs like the comma).
	x1 := rightX1 + scalePx(20, s)
	if x1 > p.w {
		x1 = p.w
	}
	headerY1 := shapes[0].bandY0 - scalePx(4, s)
	if firstRowY >= 0 {
		// Anchor at the estimator's first table band even when its own
		// sample failed to decode, so the first row is not cut off.
		for _, band := range bands {
			if band[1] > firstRowY-4 {
				if top := band[0] - scalePx(4, s); top < headerY1 {
					headerY1 = top
				}
				break
			}
		}
	}
	if headerY1 < 0 {
		headerY1 = 0
	}
	return tableRegion{
		headerY1:  headerY1,
		x0:        x0,
		x1:        x1,
		nameLimit: (nameX0 - x0) + scalePx(legacyNameColWidth-10, s),
		culvertX0: culX0 - x0,
		culvertX1: culX1 - x0,
		found:     true,
	}
}

// parseTableRowsNCC extracts data rows from the region using NCC decoding.
// With a located header only the name zone and the culvert cell are decoded
// (the culvert digits are the data-row gate); without one, every word is
// decoded and the usual 3-numeric-columns gate applies. Rows carry the RAW
// decoded name - reconciliation against the roster happens once, after
// engine arbitration, so the parse itself never depends on the roster.
func parseTableRowsNCC(p *nccPlane, region tableRegion, f *grayFont, clock *parseClock) []parsedRow {
	s := f.scale
	fDigits := f.digitsOnly()
	ins := nccInsertPenalty(s)
	insDig := nccInsertPenalty(s)
	gapStop := scalePx(gpqGapStop, s)

	sub := make([][]bool, 0, p.h-region.headerY1)
	for y := region.headerY1; y < p.h; y++ {
		sub = append(sub, p.loose[y][region.x0:region.x1])
	}
	bands := detectRowsGap(sub, scalePx(3, s))

	// Culvert-cell window: the region's culvert range covers the numbers seen
	// during location; the left margin admits a couple of extra digits (they
	// are right-aligned), the right margin stays tight so the Flag Race
	// column can never win the overlap.
	nameLimitAbs := region.x0 + region.nameLimit
	culLo := region.x0 + region.culvertX0 - scalePx(30, s)
	culHi := region.x0 + region.culvertX1 + scalePx(12, s)

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
			y0 := region.headerY1 + band[0]
			y1 := region.headerY1 + band[1]
			loosePre := p.bandLoosePrefix(y0, y1)
			spans := bandSpans(loosePre, region.x0, region.x1, gapStop)
			if len(spans) < 2 {
				return
			}

			score := ""
			scoreExpl, scoreInk := 0, 0
			if region.found {
				bestOv, cw := 0, -1
				for si, sp := range spans {
					ov := min(sp[1], culHi) - max(sp[0], culLo)
					if ov > bestOv {
						bestOv = ov
						cw = si
					}
				}
				if cw < 0 {
					return
				}
				var text string
				text, scoreExpl, scoreInk = nccDecodeSpanCov(p, fDigits, spans[cw][0], spans[cw][1], y0, y1, loosePre, insDig, true, clock)
				score = keepDigits(text)
				if score == "" {
					return
				}
			} else {
				// Headerless: classify the numeric columns, then re-decode
				// the culvert one (second-to-last) with digit templates only
				// so the value cannot carry a letter confusion.
				digitSpans := [][2]int{}
				for _, sp := range spans {
					text := nccDecodeSpan(p, f, sp[0], sp[1], y0, y1, loosePre, ins, false, clock)
					if digitsNCCClass(text) != "" {
						digitSpans = append(digitSpans, sp)
					}
				}
				if len(digitSpans) < 3 {
					return
				}
				cul := digitSpans[len(digitSpans)-2]
				var text string
				text, scoreExpl, scoreInk = nccDecodeSpanCov(p, fDigits, cul[0], cul[1], y0, y1, loosePre, insDig, true, clock)
				score = keepDigits(text)
				if score == "" {
					return
				}
			}

			sp0 := spans[0]
			if sp0[0] >= nameLimitAbs {
				return
			}
			nx1 := sp0[1]
			if nx1 > nameLimitAbs {
				nx1 = nameLimitAbs
			}
			rawText, nameExpl, nameInk := nccDecodeSpanCov(p, f, sp0[0], nx1, y0, y1, loosePre, ins, false, clock)
			name, ellipsis := cleanDecodedName(rawText)
			if name == "" || digitsScaled(name) != "" {
				return
			}
			// The name span running into the column limit is truncation
			// evidence even when the ellipsis dots failed to decode.
			ellipsis = ellipsis || sp0[1] >= nameLimitAbs-scalePx(nameEdgeEvidencePx, s)
			results[bandIdx] = rowResult{ok: true, row: parsedRow{
				rawName:  name,
				ellipsis: ellipsis,
				score:    score,
				v:        inkFraction(nameExpl+scoreExpl, nameInk+scoreInk),
				bandY0:   y0,
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
