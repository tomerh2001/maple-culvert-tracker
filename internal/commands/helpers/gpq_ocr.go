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

// parseRun tracks one ParseParticipation invocation. It exists so every
// matching pass of that invocation shares a single "did anything hit its
// deadline" flag, which the caller surfaces as ParseResult.Truncated.
type parseRun struct {
	truncated atomic.Bool
}

// until returns a cooperative clock for one pass of this run, expiring at t.
func (r *parseRun) until(t time.Time) *parseClock {
	return &parseClock{deadline: t, run: r}
}

// parseClock is the cooperative wall-clock deadline the hot match loops poll
// so a single expensive pass cannot run unbounded. It is threaded explicitly
// through the matching call chain (never package-global: images are parsed in
// parallel goroutines and must not share deadlines). A nil clock never
// expires (legacy paths and direct test calls).
type parseClock struct {
	deadline time.Time
	run      *parseRun
}

// runTruncated reports whether the owning run has recorded any deadline
// expiry so far (used to snapshot per-candidate truncation).
func (c *parseClock) runTruncated() bool {
	return c != nil && c.run != nil && c.run.truncated.Load()
}

// exceeded reports whether the deadline has passed, recording the expiry on
// the owning run so the parse result can be flagged as truncated.
func (c *parseClock) exceeded() bool {
	if c == nil || c.deadline.IsZero() {
		return false
	}
	if time.Now().After(c.deadline) {
		if c.run != nil {
			c.run.truncated.Store(true)
		}
		return true
	}
	return false
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
const nameMatchThreshold = 0.85

// nameMatchMargin: the best member match must beat the runner-up by at least
// this much, or the decode stays literal (ambiguous between two members).
const nameMatchMargin = 0.10

// nameEdgeEvidencePx (at 1x): ink within this many pixels of the name
// column's right boundary counts as truncation evidence - the game truncates
// long names exactly there, and the ellipsis dots often fail to decode.
const nameEdgeEvidencePx = 4

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

// ScoreEntry is a single parsed table row, preserving the top-to-bottom row
// order they appear in the image. Name is the reconciled member name when
// Matched, otherwise the raw decode (so legacy callers keep working); RawName
// always carries the raw decode, Row the parsed-row index in band order and
// Confidence the reconciliation confidence of the best roster candidate.
type ScoreEntry struct {
	Name       string
	Score      int
	RawName    string
	Matched    bool
	Confidence float64
	Row        int
}

// parsedRow is one raw parsed table row before any roster reconciliation:
// the cleaned raw name decode with its truncation evidence, the decoded score
// digits, the fraction of the row's core ink the accepted glyph templates
// explain (v in [0,1]) and the band's absolute y start (pitch conformity).
type parsedRow struct {
	rawName  string
	ellipsis bool
	score    string
	v        float64
	bandY0   int
}

// resolveRows converts parsed rows to entries: scores are parsed with
// unreadable ones skipped as recorded defects (never aborting the remaining
// rows) and non-positive ones skipped silently; every surviving row becomes
// one entry in band order (never deduplicated); then each raw name is
// reconciled once against the roster. When several rows reconcile to the same
// member, the highest-confidence row keeps the match and the others revert to
// their raw decode, recorded as a defect.
func resolveRows(rows []parsedRow, members []string, defects *[]string) []ScoreEntry {
	entries := []ScoreEntry{}
	ellipsis := []bool{}
	for i, r := range rows {
		score, err := strconv.Atoi(r.score)
		if err != nil {
			*defects = append(*defects, "row "+strconv.Itoa(i+1)+" ("+r.rawName+"): unreadable score - row skipped")
			continue
		}
		if score <= 0 {
			continue
		}
		entries = append(entries, ScoreEntry{Name: r.rawName, RawName: r.rawName, Score: score, Row: i})
		ellipsis = append(ellipsis, r.ellipsis)
	}

	for idx := range entries {
		member, conf, matched := reconcileNameConfidence(entries[idx].RawName, ellipsis[idx], members)
		entries[idx].Confidence = conf
		if matched {
			entries[idx].Name = member
			entries[idx].Matched = true
		}
	}

	// Collision resolution: two rows claiming the same member cannot both be
	// right. The higher-confidence row keeps the match (ties keep the earlier
	// row); the rest revert to their raw decode.
	claims := map[string][]int{}
	claimed := []string{}
	for idx := range entries {
		if entries[idx].Matched {
			if len(claims[entries[idx].Name]) == 0 {
				claimed = append(claimed, entries[idx].Name)
			}
			claims[entries[idx].Name] = append(claims[entries[idx].Name], idx)
		}
	}
	for _, m := range claimed {
		idxs := claims[m]
		if len(idxs) < 2 {
			continue
		}
		best := idxs[0]
		for _, id := range idxs[1:] {
			if entries[id].Confidence > entries[best].Confidence {
				best = id
			}
		}
		for _, id := range idxs {
			if id == best {
				continue
			}
			entries[id].Matched = false
			entries[id].Name = entries[id].RawName
			*defects = append(*defects, "rows "+strconv.Itoa(entries[best].Row+1)+" and "+strconv.Itoa(entries[id].Row+1)+
				" both resemble member "+m+" - kept the closer row, left "+entries[id].RawName+" raw")
		}
	}
	return entries
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

	decodedNames, nameEdges := decodeColumnWithEdges(namesBin, font, nameEdgeEvidencePx)
	decodedScores := decodeColumn(scoresBin, font)

	// Zip score rows with name rows (extra name rows are dropped, missing ones
	// padded - gpq.py main behaviour), then resolve like any other parse.
	rows := make([]parsedRow, 0, len(decodedScores))
	for i, d := range decodedScores {
		name, ellipsis := "", false
		if i < len(decodedNames) {
			name, ellipsis = cleanDecodedName(decodedNames[i])
			ellipsis = ellipsis || nameEdges[i]
		}
		if name == "" {
			name = "__unknown_" + strconv.Itoa(i) + "__"
		}
		rows = append(rows, parsedRow{rawName: name, ellipsis: ellipsis, score: keepDigits(d)})
	}
	var defects []string
	return resolveRows(rows, memberNames, &defects), nil
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
	res, _ := decodeColumnWithEdges(col, font, 0)
	return res
}

// decodeColumnWithEdges decodes each text row of the column and also reports
// whether the row's ink reaches within edgePx of the crop's right boundary.
// The game truncates names at that boundary, so edge contact is truncation
// evidence even when the ellipsis dots fail to decode.
func decodeColumnWithEdges(col [][]bool, font *GPQFont, edgePx int) ([]string, []bool) {
	res := []string{}
	edges := []bool{}
	for _, band := range detectRows(col) {
		rows := col[band[0]:band[1]]
		res = append(res, matchRow(rows, font))
		edges = append(edges, inkNearRightEdge(rows, edgePx))
	}
	return res, edges
}

// inkNearRightEdge reports whether any ink sits within edgePx of the right
// boundary of the given rows.
func inkNearRightEdge(rows [][]bool, edgePx int) bool {
	for _, r := range rows {
		lo := len(r) - edgePx
		if lo < 0 {
			lo = 0
		}
		for x := lo; x < len(r); x++ {
			if r[x] {
				return true
			}
		}
	}
	return false
}

// matchRow decodes the first "word" of a text row band (true = ink).
func matchRow(row [][]bool, font *GPQFont) string {
	return matchRowDual(row, row, font, nil)
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
// reduces exactly to strict binary matching. FAILS CLOSED on clock expiry:
// returns "" rather than a partial word.
func matchRowDual(loose, tight [][]bool, font *GPQFont, clock *parseClock) string {
	text, _, _ := matchRowDualCov(loose, tight, font, clock)
	return text
}

// matchRowDualCov is matchRowDual reporting ink coverage too: how many tight
// (certain) ink pixels of the band the accepted glyph templates explain, and
// the band's total tight ink. A matched glyph explains its advance span plus
// one column each side (mirroring the NCC decoder's accounting).
func matchRowDualCov(loose, tight [][]bool, font *GPQFont, clock *parseClock) (string, int, int) {
	h := len(loose)
	if h == 0 {
		return "", 0, 0
	}
	w := len(loose[0])
	loosePre := colPrefix(loose)
	tightPre := loosePre
	if &tight[0] != &loose[0] {
		tightPre = colPrefix(tight)
	}
	totalInk := tightPre[w]
	colInk := func(c int) bool { return loosePre[c+1] > loosePre[c] }

	gapStop := font.gapStopPx()
	maxSkip := font.maxSkipPx()
	out := []rune{}
	covered := make([]bool, w)
	cover := func(lo, hi int) {
		if lo < 0 {
			lo = 0
		}
		if hi > w {
			hi = w
		}
		for c := lo; c < hi; c++ {
			covered[c] = true
		}
	}
	x := 0
	skipped := 0
	blankRun := 0
	for x < w {
		if clock.exceeded() {
			// Fail closed: an expired deadline must never yield a partial
			// word that could reconcile onto the wrong member.
			return "", 0, totalInk
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
			cover(x-1, x+wT+1)
			if font.gapAdvance {
				// Scaled template widths can differ by a pixel from the
				// on-screen instance; advancing to just before the template
				// end and re-syncing on the next gap never overshoots into
				// the following glyph. The consumed trailing ink belongs to
				// this glyph's instance.
				x += wT - 1
				for x < w && colInk(x) {
					covered[x] = true
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
	explained := 0
	for c := 0; c < w; c++ {
		if covered[c] {
			explained += tightPre[c+1] - tightPre[c]
		}
	}
	return string(out), explained, totalInk
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
// none) and the trailing ellipsis dots themselves. ellipsis reports whether
// any such truncation evidence was present; reconcileName needs it to tell an
// in-game-truncated name apart from a fully rendered one.
func cleanDecodedName(s string) (cleaned string, ellipsis bool) {
	cleaned = strings.TrimRight(strings.ReplaceAll(s, ",", ""), ".")
	return cleaned, cleaned != s
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
// Both inputs are already normalised. This is the deliberately lax scorer
// used for table-header word recognition ("Name"/"Culvert"); roster
// reconciliation goes through the stricter memberMatchConfidence instead.
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

// memberMatchConfidence returns confidence in [0,1] that decoded name a is
// roster member b. Both inputs are already normalised. ellipsis reports
// whether the decode carried truncation evidence (the trailing "..." the game
// renders on long names - whose dots may decode as commas - or ink running
// into the name column's edge). Only with that evidence may a strict-prefix
// decode snap to a longer member: a full render that merely prefixes a longer
// roster name (e.g. "StellaMari" against "StellaMaris") is a different name,
// not a truncation.
func memberMatchConfidence(a, b string, ellipsis bool) float64 {
	if a == b {
		return 1.0
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return 0
	}
	conf := alignConfidence(ar, br, ellipsis)
	// Damaged scales sometimes cost the decode the member's leading glyph
	// ("aItex" for "Xaltrex"): retry against the beheaded member at a small
	// penalty, but only when the decode is short enough to have lost a rune.
	if len(br) >= 3 && len(ar) <= len(br)-1 {
		if c := alignConfidence(ar, br[1:], ellipsis) * 0.95; c > conf {
			conf = c
		}
	}
	if conf > 0.99 {
		// Only a == b may return 1.0: reconcileName exempts exact decodes
		// from the runner-up margin, fuzzy matches must still face it.
		conf = 0.99
	}
	return conf
}

// alignConfidence scores one alignment of decode ar against member br.
func alignConfidence(ar, br []rune, ellipsis bool) float64 {
	if string(ar) == string(br) {
		return 0.99
	}
	if len(br) > len(ar) && string(br[:len(ar)]) == string(ar) {
		// br strictly extends ar: only a display truncation justifies it.
		if ellipsis && len(ar) >= 3 {
			return 0.97
		}
		return 0
	}
	lcp := 0
	for lcp < len(ar) && lcp < len(br) && ar[lcp] == br[lcp] {
		lcp++
	}
	prefixScore := 0.0
	if lcp >= 4 {
		prefixScore = float64(lcp) / float64(len(ar))
	}
	conf := sequenceRatio(ar, br)
	if prefixScore > conf {
		conf = prefixScore
	}
	if ellipsis {
		// The display truncated the name at the column edge (and the decode
		// may have misread the boundary glyph): score against length-matched
		// prefixes too, so the member's hidden tail does not count against
		// the match. Only sensible when the member is not shorter than the
		// visible decode (give or take a boundary misread).
		if len(br) >= len(ar)-2 {
			n := len(ar)
			if len(br) < n {
				n = len(br)
			}
			if tr := sequenceRatio(ar[:n], br[:n]); tr > conf {
				conf = tr
			}
		}
		// Everything decoded except the last glyph or two (garbage from the
		// glyph the edge cut in half) is the member's prefix: a strong
		// truncation match, e.g. "CurseOfrc" for "CurseOfYoshi".
		if lcp >= 5 && lcp >= len(ar)-2 && conf < 0.9 {
			conf = 0.9
		}
	} else {
		// A decode without truncation evidence is a full name render: only
		// near-identical members qualify (bounded edit distance and length).
		d := len(ar) - len(br)
		if d < 0 {
			d = -d
		}
		if d > 2 || levenshtein(ar, br) > 2 {
			return 0
		}
	}
	if s := weightedEditSimilarity(ar, br); s > conf {
		conf = s
	}
	// Confusion-folded ratio: catches decodes whose damage is thin-stroke
	// confusions plus a small indel ("XaIìex" for "Xaltrex").
	if fr := sequenceRatio(foldThinRunes(ar), foldThinRunes(br)); fr > conf {
		conf = fr
	}
	if ar[0] != br[0] && !confusableRunes(ar[0], br[0]) {
		// A wrong first glyph is a strong "different name" signal: it demotes
		// e.g. "Xenpapi" vs "Senpapi" (ratio 0.857) below the threshold.
		conf *= 0.9
	}
	return conf
}

// confusableRunes reports whether two normalised runes render as
// near-identical thin strokes in this bitmap font: bilinear scaling erodes
// t's crossbar into an i/1, and l plus dotted variants already fold to i in
// normName.
func confusableRunes(a, b rune) bool {
	return isThinRune(a) && isThinRune(b)
}

func isThinRune(r rune) bool { return r == 'i' || r == '1' || r == 't' }

// foldThinRunes maps every thin-stroke rune to 'i' so confusions between
// them cost nothing in a sequence ratio.
func foldThinRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		if isThinRune(r) {
			out[i] = 'i'
		} else {
			out[i] = r
		}
	}
	return out
}

// weightedEditSimilarity scores ar against br with substitutions between
// confusable glyphs nearly free - damaged decodes read 't' as 'i'/'1'
// constantly ("koIsIare" for "kotstare") - and everything else costing 1.
func weightedEditSimilarity(ar, br []rune) float64 {
	n := len(br)
	prev := make([]float64, n+1)
	cur := make([]float64, n+1)
	for j := range prev {
		prev[j] = float64(j)
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = float64(i)
		for j := 1; j <= n; j++ {
			sub := 1.0
			if ar[i-1] == br[j-1] {
				sub = 0
			} else if confusableRunes(ar[i-1], br[j-1]) {
				sub = 0.25
			}
			m := prev[j] + 1
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1
			}
			if prev[j-1]+sub < m {
				m = prev[j-1] + sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	maxLen := len(ar)
	if n > maxLen {
		maxLen = n
	}
	sim := 1 - prev[n]/float64(maxLen)
	if sim < 0 {
		sim = 0
	}
	return sim
}

// reconcileName maps a decoded name to a roster member, or returns the decode
// unchanged when no member matches confidently enough. ellipsis is the
// truncation evidence reported by cleanDecodedName. Besides clearing
// nameMatchThreshold, the best match must beat the runner-up by
// nameMatchMargin so a decode resembling several members stays literal
// instead of guessing.
func reconcileName(dec string, ellipsis bool, members []string) string {
	name, _, _ := reconcileNameConfidence(dec, ellipsis, members)
	return name
}

// reconcileNameConfidence is reconcileName reporting the best candidate's
// confidence and whether the decode was actually reconciled onto a member.
func reconcileNameConfidence(dec string, ellipsis bool, members []string) (string, float64, bool) {
	if len([]rune(dec)) < 2 {
		return dec, 0, false
	}
	df := normName(dec)
	bestMember := ""
	best, second := 0.0, 0.0
	for _, m := range members {
		conf := memberMatchConfidence(df, normName(m), ellipsis)
		if conf > best {
			second = best
			best = conf
			bestMember = m
		} else if conf > second {
			second = conf
		}
	}
	if best == 1.0 {
		return bestMember, best, true // exact decode of a roster name
	}
	if best >= nameMatchThreshold && best-second >= nameMatchMargin {
		return bestMember, best, true
	}
	return dec, best, false
}

// levenshtein returns the edit distance between a and b (names are short, so
// the plain two-row DP is plenty).
func levenshtein(a, b []rune) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1
			}
			if prev[j-1]+cost < m {
				m = prev[j-1] + cost
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
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
