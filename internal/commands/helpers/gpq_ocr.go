package helpers

import (
	"embed"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"sync/atomic"
	"time"

	"golang.org/x/text/unicode/norm"
)

// parseDeadlineNs is a cooperative wall-clock deadline (unix nanos) the hot
// match loops poll so a single expensive pass cannot run unbounded. 0 disables
// it. Set by ParseParticipationImage; package-global because threading it
// through every matcher signature would be invasive and the OCR is never run
// concurrently for two different images in one goroutine.
var parseDeadlineNs atomic.Int64

func setParseDeadline(d time.Time) { parseDeadlineNs.Store(d.UnixNano()) }
func clearParseDeadline()          { parseDeadlineNs.Store(0) }

// deadlineExceeded reports whether the cooperative parse deadline has passed.
func deadlineExceeded() bool {
	ns := parseDeadlineNs.Load()
	return ns != 0 && time.Now().UnixNano() > ns
}

// This file ports the lossless pixel-template-matching path of the Python
// gpq-image-ocr project (gpq.py + font_match.py) for the "small" image style
// (pre-cropped GPQ score table). No OCR/Tesseract is used: the game renders
// text in a fixed bitmap font with exactly two colours, so each glyph is
// matched against known templates.

//go:embed font
var gpqFontFS embed.FS

// binarize thresholds mirroring gpq.py: only the game's two text colours
// (#FFFFFF and #B3B3B3) survive as ink.
const (
	gpqTextTol       = 20
	gpqWhiteMin      = 255 - gpqTextTol
	gpqGrayTarget    = 179
	gpqGrayLo        = gpqGrayTarget - gpqTextTol
	gpqGrayHi        = gpqGrayTarget + gpqTextTol
	gpqGraySpreadMax = 25
)

// Matching tunables (font_match.py defaults).
const (
	gpqMatchTol = 0.12
	gpqMaxSkip  = 4
	gpqGapStop  = 8
)

// nameMatchThreshold: below this confidence a decoded name is treated as a
// literal (new/unknown member) rather than reconciled to a known member.
const nameMatchThreshold = 0.7

type gpqGlyph struct {
	r    rune
	bits [][]bool // [h][w], true = ink (black text pixel)
	w    int
	h    int
	ink  int   // number of true bits (matching prefilter)
	cols []int // per-column ink counts (matching prefilter)
}

// glyphBucket groups the glyph list by template width so the matcher can
// reject a whole width class per position with one ink-budget check.
type glyphBucket struct {
	w, minInk, maxInk, maxH int
	idx                     []int
}

// buildBuckets indexes glyphs by width, ascending.
func buildBuckets(glyphs []gpqGlyph) []glyphBucket {
	byW := map[int]*glyphBucket{}
	widths := []int{}
	for i := range glyphs {
		g := &glyphs[i]
		b := byW[g.w]
		if b == nil {
			b = &glyphBucket{w: g.w, minInk: g.ink, maxInk: g.ink, maxH: g.h}
			byW[g.w] = b
			widths = append(widths, g.w)
		}
		if g.ink < b.minInk {
			b.minInk = g.ink
		}
		if g.ink > b.maxInk {
			b.maxInk = g.ink
		}
		if g.h > b.maxH {
			b.maxH = g.h
		}
		b.idx = append(b.idx, i)
	}
	sort.Ints(widths)
	out := make([]glyphBucket, 0, len(widths))
	for _, w := range widths {
		out = append(out, *byW[w])
	}
	return out
}

// GPQFont holds the flattened glyph templates used for pixel matching.
type GPQFont struct {
	glyphs  []gpqGlyph
	buckets []glyphBucket
	// tol overrides gpqMatchTol when > 0 (used for rescaled screenshots,
	// where glyphs are slightly distorted).
	tol float64
	// scale is the UI scale the templates are rendered at (0 or 1 = native).
	// Pixel-metric tunables (gap and skip distances) scale along with it.
	scale float64
	// gapAdvance: after a glyph match, re-sync on the next inter-glyph gap
	// instead of advancing by the template width. Used by scaled fonts, whose
	// template widths can be off by a pixel from the on-screen instance
	// (advancing a rounded width into the next glyph truncates the word).
	gapAdvance bool

	scaledMu    sync.Mutex
	scaledCache map[int]*GPQFont

	// grayMu/grayCache cache the gray (intensity-field) template sets used by
	// the smooth NCC matcher, per scale percent (see gpq_ocr_ncc.go).
	grayMu    sync.Mutex
	grayCache map[int]*grayFont
}

// withTolerance returns a view of the font using the given match tolerance.
func (f *GPQFont) withTolerance(tol float64) *GPQFont {
	return &GPQFont{
		glyphs:     f.glyphs,
		buckets:    f.buckets,
		tol:        tol,
		scale:      f.scale,
		gapAdvance: f.gapAdvance,
	}
}

// digitsOnly returns a view of the font restricted to digit and comma
// templates. Numeric cells re-decoded with it cannot be poisoned by
// letter templates matching eroded digit fragments.
func (f *GPQFont) digitsOnly() *GPQFont {
	v := f.withTolerance(f.tol)
	glyphs := make([]gpqGlyph, 0, 64)
	for gi := range f.glyphs {
		r := f.glyphs[gi].r
		if (r >= '0' && r <= '9') || r == ',' {
			glyphs = append(glyphs, f.glyphs[gi])
		}
	}
	v.glyphs = glyphs
	v.buckets = buildBuckets(glyphs)
	return v
}

func (f *GPQFont) matchTol() float64 {
	if f.tol > 0 {
		return f.tol
	}
	return gpqMatchTol
}

// scaleFactor returns the UI scale the templates are rendered at.
func (f *GPQFont) scaleFactor() float64 {
	if f.scale > 0 {
		return f.scale
	}
	return 1
}

// scalePx converts a 1x pixel metric to scale f, rounding to nearest.
func scalePx(v int, f float64) int {
	return int(float64(v)*f + 0.5)
}

func (f *GPQFont) gapStopPx() int { return scalePx(gpqGapStop, f.scaleFactor()) }
func (f *GPQFont) maxSkipPx() int { return scalePx(gpqMaxSkip, f.scaleFactor()) }

// effBandH is the band height used to normalise a template's match distance.
// The native path keeps the legacy full-band normalisation; scaled fonts cap
// it near the template height, so that tall (merged) bands do not loosen
// acceptance - the distance budget stays proportional to the glyph itself.
func (f *GPQFont) effBandH(h, glyphH int) int {
	if f.scale > 1.01 {
		if c := glyphH + scalePx(4, f.scaleFactor()); c < h {
			return c
		}
	}
	return h
}

// gpqScalePhases are the sub-pixel phase offsets scaled templates are
// generated at. A glyph rendered by the client's upscaler lands on the
// scaling grid at an arbitrary sub-pixel phase (per glyph!), which changes
// where columns/rows get duplicated; matching tries every phase variant and
// keeps the best. Eighth-pixel steps are exact for the common q<=8 scales
// (1.25x, 1.375x, 1.5x, 1.75x, 2x) and within 1/16 px for anything else,
// which keeps genuine glyphs inside the strict match tolerance.
var gpqScalePhases = []float64{0, 0.125, 0.25, 0.375, 0.5, 0.625, 0.75, 0.875}

// scaledFont returns a font whose glyph templates are upscaled by factor fac
// with crisp nearest-neighbour sampling (mirroring lossless UI scaling), at
// the given match tolerance. Every glyph is generated at each (x, y) phase
// pair of gpqScalePhases and the variants are flattened into the glyph list
// (the matcher's prefilter keeps this cheap). Scaled glyph sets are cached
// per factor on the receiver. Smoothly-scaled screenshots are not matched
// with binary templates at all - see the NCC matcher in gpq_ocr_ncc.go.
func (f *GPQFont) scaledFont(fac float64, tol float64) *GPQFont {
	pct := int(fac*100 + 0.5)
	if pct <= 100 {
		return f.withTolerance(tol)
	}
	f.scaledMu.Lock()
	cached := f.scaledCache[pct]
	f.scaledMu.Unlock()
	if cached == nil {
		exact := float64(pct) / 100
		glyphs := make([]gpqGlyph, 0, len(f.glyphs)*4)
		var sb strings.Builder
		for i := range f.glyphs {
			// Many phases collapse to the same bitmap (all of them for
			// integer scales, most for simple ratios): deduplicate per glyph.
			seen := map[string]bool{}
			for _, dy := range gpqScalePhases {
				for _, dx := range gpqScalePhases {
					g := scaleGlyph(&f.glyphs[i], exact, dx, dy)
					sb.Reset()
					sb.WriteByte(byte(g.w))
					sb.WriteByte(byte(g.h))
					for _, row := range g.bits {
						for _, v := range row {
							if v {
								sb.WriteByte(1)
							} else {
								sb.WriteByte(0)
							}
						}
					}
					key := sb.String()
					if !seen[key] {
						seen[key] = true
						glyphs = append(glyphs, g)
					}
				}
			}
		}
		cached = &GPQFont{glyphs: glyphs, buckets: buildBuckets(glyphs), scale: exact, gapAdvance: true}
		f.scaledMu.Lock()
		if f.scaledCache == nil {
			f.scaledCache = map[int]*GPQFont{}
		}
		f.scaledCache[pct] = cached
		f.scaledMu.Unlock()
	}
	return &GPQFont{
		glyphs:     cached.glyphs,
		buckets:    cached.buckets,
		tol:        tol,
		scale:      cached.scale,
		gapAdvance: true,
	}
}

// scaleGlyph upscales one binary template by fac at sub-pixel phase (dx, dy)
// with nearest-neighbour sampling. An instance whose first source column
// starts at image column X0 satisfies image col x -> source col
// floor((x-X0+d)/fac) with d = ceil(c0*fac)-c0*fac; generating templates for
// a grid of d values lets the matcher cover every on-screen phase.
func scaleGlyph(g *gpqGlyph, fac, dx, dy float64) gpqGlyph {
	w := int(math.Ceil(float64(g.w)*fac - dx))
	h := int(math.Ceil(float64(g.h)*fac - dy))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	bits := make([][]bool, h)
	for y := 0; y < h; y++ {
		row := make([]bool, w)
		for x := 0; x < w; x++ {
			sy := int((float64(y) + dy) / fac)
			sx := int((float64(x) + dx) / fac)
			if sy > g.h-1 {
				sy = g.h - 1
			}
			if sx > g.w-1 {
				sx = g.w - 1
			}
			row[x] = g.bits[sy][sx]
		}
		bits[y] = row
	}
	return gpqGlyph{r: g.r, bits: bits, w: w, h: h, ink: countInk(bits), cols: countCols(bits)}
}

// countInk returns the number of set bits in a template.
func countInk(bits [][]bool) int {
	n := 0
	for _, row := range bits {
		for _, v := range row {
			if v {
				n++
			}
		}
	}
	return n
}

// countCols returns per-column set-bit counts of a template.
func countCols(bits [][]bool) []int {
	if len(bits) == 0 {
		return nil
	}
	cols := make([]int, len(bits[0]))
	for _, row := range bits {
		for x, v := range row {
			if v {
				cols[x]++
			}
		}
	}
	return cols
}

// gpqSmoothHalo models the source screenshot's own glyph antialiasing: the
// game renders a partial-intensity halo around every ink pixel, which the
// strict 1x binarization discards but a smooth upscale blends back in. The
// NCC gray templates (gpq_ocr_ncc.go) give non-ink pixels adjacent to ink
// this fractional value so the resampled template fattens the same way the
// real image does.
const gpqSmoothHalo = 0.35

var (
	gpqFontOnce sync.Once
	gpqFont     *GPQFont
	gpqFontErr  error
)

// LoadGPQFont loads (once) and returns the embedded glyph templates.
func LoadGPQFont() (*GPQFont, error) {
	gpqFontOnce.Do(func() {
		gpqFont, gpqFontErr = loadGPQFont()
	})
	return gpqFont, gpqFontErr
}

func loadGPQFont() (*GPQFont, error) {
	entries, err := gpqFontFS.ReadDir("font")
	if err != nil {
		return nil, err
	}
	f := &GPQFont{}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "U") {
			continue
		}
		cp, err := strconv.ParseInt(e.Name()[1:], 16, 32)
		if err != nil {
			continue
		}
		r := rune(cp)
		dir := path.Join("font", e.Name())
		files, err := gpqFontFS.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, tf := range files {
			if !strings.HasSuffix(tf.Name(), ".png") {
				continue
			}
			data, err := gpqFontFS.ReadFile(path.Join(dir, tf.Name()))
			if err != nil {
				continue
			}
			bits, w, h, err := decodeTemplate(data)
			if err != nil || w == 0 || h == 0 {
				continue
			}
			f.glyphs = append(f.glyphs, gpqGlyph{r: r, bits: bits, w: w, h: h, ink: countInk(bits), cols: countCols(bits)})
		}
	}
	f.buckets = buildBuckets(f.glyphs)
	return f, nil
}

// decodeTemplate reads a grayscale glyph PNG and binarizes it (gray < 128 = ink).
func decodeTemplate(data []byte) ([][]bool, int, int, error) {
	img, _, err := image.Decode(strings.NewReader(string(data)))
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	bits := make([][]bool, h)
	for y := 0; y < h; y++ {
		row := make([]bool, w)
		for x := 0; x < w; x++ {
			r, _, _, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			row[x] = uint8(r>>8) < 128
		}
		bits[y] = row
	}
	return bits, w, h, nil
}

// ScoreEntry is a single parsed character name and score, preserving the
// top-to-bottom row order they appear in the image.
type ScoreEntry struct {
	Name  string
	Score int
}

// ParseSmallImage decodes a "small" GPQ score table image and returns the
// parsed character name/score entries in row order (top to bottom). Names are
// reconciled against memberNames.
func ParseSmallImage(imgData []byte, memberNames []string, font *GPQFont) ([]ScoreEntry, error) {
	img, _, err := image.Decode(strings.NewReader(string(imgData)))
	if err != nil {
		return nil, err
	}

	namesBin := binarizeCrop(img, 0, 0, 68)
	scoresBin := binarizeCrop(img, 305, 0, 415)

	decodedNames := decodeColumn(namesBin, font)
	decodedScores := decodeColumn(scoresBin, font)

	names := make([]string, len(decodedNames))
	for i, d := range decodedNames {
		names[i] = reconcileName(cleanDecodedName(d), memberNames)
	}
	scores := make([]string, len(decodedScores))
	for i, d := range decodedScores {
		scores[i] = keepDigits(d)
	}

	// Pad/truncate to equal length (gpq.py main behaviour).
	for len(names) < len(scores) {
		names = append(names, "__unknown_"+strconv.Itoa(len(names))+"__")
	}
	if len(scores) > len(names) {
		scores = scores[:len(names)]
	}

	return mergeScoresWithNames(scores, names), nil
}

// binarizeCrop crops the image to columns [x0, x1) (clamped to width) over the
// full height, returning a bool grid where true = ink.
func binarizeCrop(img image.Image, x0, y0, x1 int) [][]bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if x1 > w {
		x1 = w
	}
	if x0 < 0 {
		x0 = 0
	}
	if x0 > x1 {
		x0 = x1
	}
	cw := x1 - x0
	out := make([][]bool, h-y0)
	for y := y0; y < h; y++ {
		row := make([]bool, cw)
		for x := 0; x < cw; x++ {
			r32, g32, b32, _ := img.At(b.Min.X+x0+x, b.Min.Y+y).RGBA()
			r, g, bl := int(r32>>8), int(g32>>8), int(b32>>8)
			row[x] = isInk(r, g, bl)
		}
		out[y-y0] = row
	}
	return out
}

func isInk(r, g, b int) bool {
	isWhite := r >= gpqWhiteMin && g >= gpqWhiteMin && b >= gpqWhiteMin
	maxc := r
	if g > maxc {
		maxc = g
	}
	if b > maxc {
		maxc = b
	}
	minc := r
	if g < minc {
		minc = g
	}
	if b < minc {
		minc = b
	}
	spread := maxc - minc
	isGray := r >= gpqGrayLo && r <= gpqGrayHi &&
		g >= gpqGrayLo && g <= gpqGrayHi &&
		b >= gpqGrayLo && b <= gpqGrayHi &&
		spread <= gpqGraySpreadMax
	return isWhite || isGray
}

// detectRows returns [y0, y1) bands for each text row via dark-pixel
// Y-projection, merging bands separated by <= 3px (at 1x).
func detectRows(col [][]bool) [][2]int {
	return detectRowsGap(col, 3)
}

// detectRowsGap is detectRows with an explicit merge gap (scaled screenshots
// need it scaled along with the glyphs).
func detectRowsGap(col [][]bool, mergeGap int) [][2]int {
	type band struct{ y0, y1 int }
	bands := []band{}
	inB := false
	y0 := 0
	for y, row := range col {
		dark := 0
		for _, v := range row {
			if v {
				dark++
			}
		}
		if !inB && dark > 0 {
			inB, y0 = true, y
		} else if inB && dark == 0 {
			inB = false
			bands = append(bands, band{y0, y})
		}
	}
	if inB {
		bands = append(bands, band{y0, len(col)})
	}
	merged := []band{}
	for _, b := range bands {
		if len(merged) > 0 && b.y0-merged[len(merged)-1].y1 <= mergeGap {
			merged[len(merged)-1].y1 = b.y1
		} else {
			merged = append(merged, b)
		}
	}
	res := make([][2]int, len(merged))
	for i, b := range merged {
		res[i] = [2]int{b.y0, b.y1}
	}
	return res
}

func decodeColumn(col [][]bool, font *GPQFont) []string {
	res := []string{}
	for _, band := range detectRows(col) {
		res = append(res, matchRow(col[band[0]:band[1]], font))
	}
	return res
}

// matchRow decodes the first "word" of a text row band (true = ink).
func matchRow(row [][]bool, font *GPQFont) string {
	return matchRowDual(row, row, font)
}

// colPrefix returns per-column ink prefix sums: pre[x] = ink in columns
// [0, x), so window totals cost O(1).
func colPrefix(rows [][]bool) []int {
	h := len(rows)
	w := 0
	if h > 0 {
		w = len(rows[0])
	}
	pre := make([]int, w+1)
	for x := 0; x < w; x++ {
		cnt := 0
		for y := 0; y < h; y++ {
			if rows[y][x] {
				cnt++
			}
		}
		pre[x+1] = pre[x] + cnt
	}
	return pre
}

// matchRowDual decodes the first "word" of a text row band given two ink
// planes: loose (possibly ink) and tight (certainly ink). Pixels set in
// loose but not tight are antialiased edges and cost nothing whichever way a
// template covers them. Lossless sources pass the same grid twice, which
// reduces exactly to strict binary matching.
func matchRowDual(loose, tight [][]bool, font *GPQFont) string {
	h := len(loose)
	if h == 0 {
		return ""
	}
	w := len(loose[0])
	loosePre := colPrefix(loose)
	tightPre := loosePre
	if &tight[0] != &loose[0] {
		tightPre = colPrefix(tight)
	}
	colInk := func(c int) bool { return loosePre[c+1] > loosePre[c] }

	gapStop := font.gapStopPx()
	maxSkip := font.maxSkipPx()
	out := []rune{}
	x := 0
	skipped := 0
	blankRun := 0
	for x < w {
		if deadlineExceeded() {
			break
		}
		if !colInk(x) {
			x++
			blankRun++
			skipped = 0
			if len(out) > 0 && blankRun >= gapStop {
				break
			}
			continue
		}
		blankRun = 0
		ch, wT, dn, ok := bestGlyphAt(loose, tight, loosePre, tightPre, x, font)
		if ok && dn <= font.matchTol() {
			out = append(out, ch)
			if font.gapAdvance {
				// Scaled template widths can differ by a pixel from the
				// on-screen instance; advancing to just before the template
				// end and re-syncing on the next gap never overshoots into
				// the following glyph.
				x += wT - 1
				for x < w && colInk(x) {
					x++
				}
			} else {
				x += wT
			}
			skipped = 0
		} else {
			x++
			skipped++
			if skipped > maxSkip {
				break
			}
		}
	}
	return string(out)
}

// bestGlyphAt finds the best-matching template starting at column x. Ties on
// normalised distance are broken toward the widest glyph, then lowest rune.
// loose/tight are the ink planes (see matchRowDual), loosePre/tightPre their
// column prefix sums. A pixel costs 1 when the template has ink where the
// image certainly has none (!loose), or no ink where the image certainly has
// some (tight); certain image ink outside the template's vertical span also
// costs 1.
func bestGlyphAt(loose, tight [][]bool, loosePre, tightPre []int, x int, font *GPQFont) (rune, int, float64, bool) {
	h := len(loose)
	w := len(loose[0])
	found := false
	var bestDn float64
	var bestW int
	var bestCh rune

	tol := font.matchTol()
	for bi := range font.buckets {
		b := &font.buckets[bi]
		if x+b.w > w {
			break // buckets are width-ascending: the rest cannot fit either
		}
		bLoose := loosePre[x+b.w] - loosePre[x]
		bTight := tightPre[x+b.w] - tightPre[x]
		bCut := int(tol * float64(font.effBandH(h, b.maxH)*b.w))
		// Whole-width-class prefilter on the shared ink budget.
		if bTight-b.maxInk > bCut || b.minInk-bLoose > bCut {
			continue
		}
		for _, gi := range b.idx {
			g := &font.glyphs[gi]
			if g.h > h {
				continue
			}
			looseTotal := bLoose
			tightTotal := bTight
			// cutoff: distances above the acceptance tolerance can never win
			// a usable match (callers reject dn > tol), so work stops early.
			hEff := font.effBandH(h, g.h)
			cutoff := int(tol * float64(hEff*g.w))
			// Prefilter: the distance is provably >= tightTotal - g.ink
			// (every certain ink pixel the template cannot cover costs 1)
			// and >= g.ink - looseTotal (every template ink pixel with no
			// possible image ink costs 1), so hopeless templates are skipped
			// without a pixel loop.
			if tightTotal-g.ink > cutoff || g.ink-looseTotal > cutoff {
				continue
			}
			// Per-column lower bound (valid at every vertical offset): each
			// column costs at least the certain ink the template cannot cover,
			// or the template ink the image cannot provide (tight is a subset of
			// loose, so at most one of the two is positive per column).
			lb := 0
			for c := 0; c < g.w; c++ {
				lCol := loosePre[x+c+1] - loosePre[x+c]
				tCol := tightPre[x+c+1] - tightPre[x+c]
				if d := g.cols[c] - lCol; d > 0 {
					lb += d
				} else if d := tCol - g.cols[c]; d > 0 {
					lb += d
				}
				if lb > cutoff {
					break
				}
			}
			if lb > cutoff {
				continue
			}
			bestD := -1
			yStart := 0
			if g.r == ',' {
				// A comma hangs off the baseline: only offsets that keep it in
				// the band's bottom rows are geometrically valid. Without this,
				// the tiny template floats mid-band and "explains" fragments of
				// eroded digits.
				if m := h - g.h - scalePx(3, font.scaleFactor()); m > 0 {
					yStart = m
				}
			}
			for y := yStart; y <= h-g.h; y++ {
				abortD := cutoff + 1
				if bestD >= 0 && bestD < cutoff {
					abortD = bestD
				}
				dIn := 0
				tightInBand := 0
				for iy := 0; iy < g.h; iy++ {
					trow := g.bits[iy]
					lrow := loose[y+iy]
					srow := tight[y+iy]
					for ix := 0; ix < g.w; ix++ {
						if trow[ix] {
							if !lrow[x+ix] {
								dIn++
							}
						} else if srow[x+ix] {
							dIn++
						}
						if srow[x+ix] {
							tightInBand++
						}
					}
					if dIn > abortD {
						break
					}
				}
				if dIn > abortD {
					continue
				}
				d := dIn + (tightTotal - tightInBand)
				if bestD < 0 || d < bestD {
					bestD = d
					if bestD == 0 {
						break
					}
				}
			}
			if bestD < 0 || bestD > cutoff {
				continue
			}
			dn := float64(bestD) / float64(font.effBandH(h, g.h)*g.w)
			if !found || glyphKeyLess(dn, g.w, g.r, bestDn, bestW, bestCh) {
				found = true
				bestDn = dn
				bestW = g.w
				bestCh = g.r
			}
		}
	}
	return bestCh, bestW, bestDn, found
}

// glyphKeyLess compares match keys (dn, -width, rune) ascending.
func glyphKeyLess(dn1 float64, w1 int, ch1 rune, dn2 float64, w2 int, ch2 rune) bool {
	if dn1 != dn2 {
		return dn1 < dn2
	}
	if w1 != w2 {
		return w1 > w2
	}
	return ch1 < ch2
}

// cleanDecodedName strips punctuation noise from a decoded name: commas
// (matched off the dots of a truncation ellipsis - character names contain
// none) and the trailing ellipsis dots themselves.
func cleanDecodedName(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, ",", ""), ".")
}

func keepDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mergeScoresWithNames zips scores and names, stopping at the first non-integer
// score (mirrors the Python int()/break behaviour).
func mergeScoresWithNames(scores, names []string) []ScoreEntry {
	entries := []ScoreEntry{}
	pos := map[string]int{}
	for i := range scores {
		score, err := strconv.Atoi(scores[i])
		if err != nil {
			break
		}
		if score > 0 && i < len(names) {
			name := names[i]
			if idx, ok := pos[name]; ok {
				entries[idx].Score = score
			} else {
				pos[name] = len(entries)
				entries = append(entries, ScoreEntry{Name: name, Score: score})
			}
		}
	}
	return entries
}

// ── Name reconciliation (font_match.py) ─────────────────────────────────────

// fold lowercases and strips diacritical marks (NFD, drop combining marks).
func fold(s string) string {
	lower := strings.ToLower(s)
	decomposed := norm.NFD.String(lower)
	var b strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// normName folds and unifies l/I (identical glyphs in this font).
func normName(s string) string {
	return strings.ReplaceAll(fold(s), "l", "i")
}

// nameLikeliness returns confidence in [0,1] that decoded name a refers to b.
// Both inputs are already normalised.
func nameLikeliness(a, b string) float64 {
	if a == b {
		return 1.0
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar) >= 3 && strings.HasPrefix(b, a) {
		return 0.97
	}
	lcp := 0
	for lcp < len(ar) && lcp < len(br) && ar[lcp] == br[lcp] {
		lcp++
	}
	prefixScore := 0.0
	if lcp >= 4 {
		prefixScore = float64(lcp) / float64(len(ar))
	}
	ratio := sequenceRatio(ar, br)
	if ratio > prefixScore {
		return ratio
	}
	return prefixScore
}

func reconcileName(dec string, members []string) string {
	if len([]rune(dec)) < 2 {
		return dec
	}
	df := normName(dec)
	bestMember := ""
	bestConf := 0.0
	for _, m := range members {
		conf := nameLikeliness(df, normName(m))
		if conf > bestConf {
			bestConf = conf
			bestMember = m
		}
	}
	if bestConf >= nameMatchThreshold {
		return bestMember
	}
	return dec
}

// sequenceRatio replicates Python difflib.SequenceMatcher.ratio()
// (Ratcliff/Obershelp, no junk heuristics) = 2*M / (len(a)+len(b)).
func sequenceRatio(a, b []rune) float64 {
	total := len(a) + len(b)
	if total == 0 {
		return 1.0
	}
	m := matchingBlocksTotal(a, b)
	return 2.0 * float64(m) / float64(total)
}

func matchingBlocksTotal(a, b []rune) int {
	// b2j: rune -> sorted indices in b.
	b2j := map[rune][]int{}
	for j, r := range b {
		b2j[r] = append(b2j[r], j)
	}

	type task struct{ alo, ahi, blo, bhi int }
	queue := []task{{0, len(a), 0, len(b)}}
	total := 0
	for len(queue) > 0 {
		t := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		bi, bj, k := findLongestMatch(a, b2j, t.alo, t.ahi, t.blo, t.bhi)
		if k > 0 {
			total += k
			if t.alo < bi && t.blo < bj {
				queue = append(queue, task{t.alo, bi, t.blo, bj})
			}
			if bi+k < t.ahi && bj+k < t.bhi {
				queue = append(queue, task{bi + k, t.ahi, bj + k, t.bhi})
			}
		}
	}
	return total
}

func findLongestMatch(a []rune, b2j map[rune][]int, alo, ahi, blo, bhi int) (int, int, int) {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	return besti, bestj, bestsize
}
