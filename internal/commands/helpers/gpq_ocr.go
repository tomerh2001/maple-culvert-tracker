package helpers

import (
	"embed"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"path"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

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
}

// GPQFont holds the flattened glyph templates used for pixel matching.
type GPQFont struct {
	glyphs []gpqGlyph
	// tol overrides gpqMatchTol when > 0 (used for rescaled screenshots,
	// where glyphs are slightly distorted).
	tol float64
}

// withTolerance returns a view of the font using the given match tolerance.
func (f *GPQFont) withTolerance(tol float64) *GPQFont {
	return &GPQFont{glyphs: f.glyphs, tol: tol}
}

func (f *GPQFont) matchTol() float64 {
	if f.tol > 0 {
		return f.tol
	}
	return gpqMatchTol
}

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
			f.glyphs = append(f.glyphs, gpqGlyph{r: r, bits: bits, w: w, h: h})
		}
	}
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
		names[i] = reconcileName(strings.TrimRight(d, "."), memberNames)
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
// Y-projection, merging bands separated by <= 3px.
func detectRows(col [][]bool) [][2]int {
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
		if len(merged) > 0 && b.y0-merged[len(merged)-1].y1 <= 3 {
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
	h := len(row)
	if h == 0 {
		return ""
	}
	w := len(row[0])
	colInk := func(c int) bool {
		for y := 0; y < h; y++ {
			if row[y][c] {
				return true
			}
		}
		return false
	}

	out := []rune{}
	x := 0
	skipped := 0
	blankRun := 0
	for x < w {
		if !colInk(x) {
			x++
			blankRun++
			skipped = 0
			if len(out) > 0 && blankRun >= gpqGapStop {
				break
			}
			continue
		}
		blankRun = 0
		ch, wT, dn, ok := bestGlyphAt(row, x, font)
		if ok && dn <= font.matchTol() {
			out = append(out, ch)
			x += wT
			skipped = 0
		} else {
			x++
			skipped++
			if skipped > gpqMaxSkip {
				break
			}
		}
	}
	return string(out)
}

// bestGlyphAt finds the best-matching template starting at column x. Ties on
// normalised distance are broken toward the widest glyph, then lowest rune.
func bestGlyphAt(row [][]bool, x int, font *GPQFont) (rune, int, float64, bool) {
	h := len(row)
	w := len(row[0])
	found := false
	var bestDn float64
	var bestW int
	var bestCh rune

	for gi := range font.glyphs {
		g := &font.glyphs[gi]
		if x+g.w > w || g.h > h {
			continue
		}
		inkTotal := 0
		for yy := 0; yy < h; yy++ {
			for xx := 0; xx < g.w; xx++ {
				if row[yy][x+xx] {
					inkTotal++
				}
			}
		}
		bestD := -1
		for y := 0; y <= h-g.h; y++ {
			dIn := 0
			inkInBand := 0
			for iy := 0; iy < g.h; iy++ {
				trow := g.bits[iy]
				srow := row[y+iy]
				for ix := 0; ix < g.w; ix++ {
					pix := srow[x+ix]
					if pix != trow[ix] {
						dIn++
					}
					if pix {
						inkInBand++
					}
				}
			}
			d := dIn + (inkTotal - inkInBand)
			if bestD < 0 || d < bestD {
				bestD = d
				if bestD == 0 {
					break
				}
			}
		}
		if bestD < 0 {
			continue
		}
		dn := float64(bestD) / float64(h*g.w)
		if !found || glyphKeyLess(dn, g.w, g.r, bestDn, bestW, bestCh) {
			found = true
			bestDn = dn
			bestW = g.w
			bestCh = g.r
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
