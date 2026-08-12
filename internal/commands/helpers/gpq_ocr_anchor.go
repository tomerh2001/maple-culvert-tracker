package helpers

import (
	"embed"
	"image"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// This file implements the ANCHOR stage of the screenshot parser: every
// culvert screenshot of the Guild window renders the fixed "Member
// Participation Status" title above the participation table, in the same
// place relative to the table, at the window's UI scale. Finding that title
// via normalized cross-correlation of a grayscale reference patch therefore
// yields the screenshot's (scale, origin) pair DETERMINISTICALLY - no pitch
// guessing, no scale ladders. The reference patch is cropped from
// provided/real-sample.png (a crisp smooth-2x capture, title origin (50,42))
// at its native 2x, so a match of width w against the reference width W
// means UI scale s = gpqAnchorRefScale * w / W.
//
// The sweep is coarse-to-fine: candidate scales 0.9-5.2 in 0.1 steps are
// matched on box-downsampled pyramids (stride pruning - box averaging keeps
// the correlation peak broad, so a stride-1 search of the downsampled plane
// cannot step over it), the best coarse hits are refined at +-0.05 scale in
// 0.01 steps on a half-resolution plane, and the winner's position is
// polished at full resolution. A hit is validated twice: the title
// correlation itself must clear gpqAnchorMinNCC, and the column-header strip
// ("Name .. Job .. Level .. Title .. Weekly Mission") must correlate at its
// expected offset below the title - a second, unrelated patch that a spoofed
// or accidental title-like blob will not carry.

//go:embed anchor
var gpqAnchorFS embed.FS

// gpqAnchorRefScale is the UI scale the reference patches are rendered at.
const gpqAnchorRefScale = 2.0

// Anchor acceptance floors, applied to the full-resolution correlations.
// Calibrated on the real fixtures (title NCC ~0.99 on the source sample,
// >=0.9 on the three ~1.45x full-window captures) and on noise/solid/
// fake-font negatives (best spurious NCC well under 0.5).
const (
	gpqAnchorMinNCC       = 0.72
	gpqAnchorHeaderMinNCC = 0.60
)

// Coarse sweep bounds: 0.9x (slightly shrunken repost) up to 5.2x (a smooth
// 2x capture blown up 2.5x more). The coarse floor is deliberately loose -
// box downsampling and the 0.1 scale grid cost correlation that refinement
// recovers.
const (
	gpqAnchorScaleMin  = 0.90
	gpqAnchorScaleMax  = 5.20
	gpqAnchorTopFrac   = 0.40 // the title lives in the top ~40% of the image
	gpqAnchorCoarseTol = 0.26 // coarse floor = gpqAnchorMinNCC - this
	gpqAnchorRefineTop = 10   // coarse scale candidates refined
)

// anchorRefs holds the decoded grayscale reference patches.
type anchorRefs struct {
	title  *grayPatchRef
	header *grayPatchRef
	// headerDX/DY: offset of the header patch origin from the title patch
	// origin in reference (2x) pixels, from the crops' origins on
	// real-sample.png: title (50,42), header strip slice (140,104).
	headerDX, headerDY float64
}

// grayPatchRef is a reference patch at its native resolution.
type grayPatchRef struct {
	w, h int
	vals []float32 // row-major luminance 0..255
}

var (
	gpqAnchorOnce sync.Once
	gpqAnchorRef  *anchorRefs
	gpqAnchorErr  error
)

func loadAnchorRefs() (*anchorRefs, error) {
	gpqAnchorOnce.Do(func() {
		title, err := loadGrayPatch("anchor/title-2x.png")
		if err != nil {
			gpqAnchorErr = err
			return
		}
		header, err := loadGrayPatch("anchor/header-2x.png")
		if err != nil {
			gpqAnchorErr = err
			return
		}
		gpqAnchorRef = &anchorRefs{title: title, header: header, headerDX: 90, headerDY: 62}
	})
	return gpqAnchorRef, gpqAnchorErr
}

func loadGrayPatch(path string) (*grayPatchRef, error) {
	data, err := gpqAnchorFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	p := &grayPatchRef{w: b.Dx(), h: b.Dy(), vals: make([]float32, b.Dx()*b.Dy())}
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			p.vals[y*p.w+x] = float32((int(r>>8) + int(g>>8) + int(bl>>8)) / 3)
		}
	}
	return p, nil
}

// grayPatch is a resampled patch template with precomputed NCC statistics.
type grayPatch struct {
	w, h  int
	vals  []float32
	sumT  float64
	varTN float64
}

func (t *grayPatch) finalize() *grayPatch {
	var sum, sum2 float64
	for _, v := range t.vals {
		sum += float64(v)
		sum2 += float64(v) * float64(v)
	}
	t.sumT = sum
	t.varTN = sum2 - sum*sum/float64(len(t.vals))
	return t
}

// resamplePatch resamples the reference patch by factor f: box (area)
// averaging when minifying, bilinear when magnifying - point-sampling a
// minified patch would alias its strokes away.
func resamplePatch(ref *grayPatchRef, f float64) *grayPatch {
	w := int(float64(ref.w)*f + 0.5)
	h := int(float64(ref.h)*f + 0.5)
	if w < 4 || h < 2 {
		return nil
	}
	p := &grayPatch{w: w, h: h, vals: make([]float32, w*h)}
	if f < 1 {
		inv := 1 / f
		for y := 0; y < h; y++ {
			sy0 := float64(y) * inv
			sy1 := sy0 + inv
			iy0, iy1 := int(sy0), int(math.Ceil(sy1))
			if iy1 > ref.h {
				iy1 = ref.h
			}
			for x := 0; x < w; x++ {
				sx0 := float64(x) * inv
				sx1 := sx0 + inv
				ix0, ix1 := int(sx0), int(math.Ceil(sx1))
				if ix1 > ref.w {
					ix1 = ref.w
				}
				var acc, wsum float64
				for yy := iy0; yy < iy1; yy++ {
					wy := math.Min(sy1, float64(yy+1)) - math.Max(sy0, float64(yy))
					for xx := ix0; xx < ix1; xx++ {
						wx := math.Min(sx1, float64(xx+1)) - math.Max(sx0, float64(xx))
						acc += wy * wx * float64(ref.vals[yy*ref.w+xx])
						wsum += wy * wx
					}
				}
				if wsum > 0 {
					p.vals[y*w+x] = float32(acc / wsum)
				}
			}
		}
		return p.finalize()
	}
	for y := 0; y < h; y++ {
		sy := (float64(y) + 0.5) / f
		y0 := int(sy - 0.5)
		fy := sy - 0.5 - float64(y0)
		if y0 < 0 {
			y0, fy = 0, 0
		}
		if y0 >= ref.h-1 {
			y0, fy = ref.h-2, 1
		}
		for x := 0; x < w; x++ {
			sx := (float64(x) + 0.5) / f
			x0 := int(sx - 0.5)
			fx := sx - 0.5 - float64(x0)
			if x0 < 0 {
				x0, fx = 0, 0
			}
			if x0 >= ref.w-1 {
				x0, fx = ref.w-2, 1
			}
			v00 := float64(ref.vals[y0*ref.w+x0])
			v10 := float64(ref.vals[y0*ref.w+x0+1])
			v01 := float64(ref.vals[(y0+1)*ref.w+x0])
			v11 := float64(ref.vals[(y0+1)*ref.w+x0+1])
			p.vals[y*w+x] = float32(v00*(1-fx)*(1-fy) + v10*fx*(1-fy) + v01*(1-fx)*fy + v11*fx*fy)
		}
	}
	return p.finalize()
}

// grayPlane is a luminance plane with summed-area tables (the anchor's
// search space; the full-image nccPlane is not built until a hit is found).
type grayPlane struct {
	w, h   int
	lum    []float32
	integ  []float64
	integ2 []float64
}

func buildGrayPlane(img image.Image, y0, y1 int) *grayPlane {
	b := img.Bounds()
	w := b.Dx()
	if y1 > b.Dy() {
		y1 = b.Dy()
	}
	h := y1 - y0
	p := &grayPlane{w: w, h: h, lum: make([]float32, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y0+y).RGBA()
			p.lum[y*w+x] = float32((int(r>>8) + int(g>>8) + int(bl>>8)) / 3)
		}
	}
	p.buildIntegrals()
	return p
}

// boxDownsample returns the plane reduced dec-fold on both axes by area
// averaging (NOT decimation: averaging keeps the correlation peak broad, so
// the downsampled search cannot step over it).
func (p *grayPlane) boxDownsample(dec int) *grayPlane {
	w := p.w / dec
	h := p.h / dec
	if w < 1 || h < 1 {
		return nil
	}
	d := &grayPlane{w: w, h: h, lum: make([]float32, w*h)}
	norm := float32(1) / float32(dec*dec)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float32
			for yy := 0; yy < dec; yy++ {
				row := p.lum[(y*dec+yy)*p.w+x*dec:]
				for xx := 0; xx < dec; xx++ {
					acc += row[xx]
				}
			}
			d.lum[y*w+x] = acc * norm
		}
	}
	d.buildIntegrals()
	return d
}

func (p *grayPlane) buildIntegrals() {
	w, h := p.w, p.h
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
}

func (p *grayPlane) rectSums(x0, y0, w, h int) (sp, sp2 float64) {
	stride := p.w + 1
	a := y0*stride + x0
	b := a + w
	c := (y0+h)*stride + x0
	d := c + w
	return p.integ[d] - p.integ[b] - p.integ[c] + p.integ[a],
		p.integ2[d] - p.integ2[b] - p.integ2[c] + p.integ2[a]
}

// patchNCC correlates the patch with the plane at (x0, y0). Returns -1 for
// out-of-bounds or degenerate windows.
func patchNCC(p *grayPlane, t *grayPatch, x0, y0 int) float64 {
	if t == nil || x0 < 0 || y0 < 0 || x0+t.w > p.w || y0+t.h > p.h {
		return -1
	}
	n := float64(t.w * t.h)
	sp, sp2 := p.rectSums(x0, y0, t.w, t.h)
	denP := sp2 - sp*sp/n
	if denP < 1e-6 || t.varTN < 1e-6 {
		return -1
	}
	var stp float64
	for yy := 0; yy < t.h; yy++ {
		base := (y0+yy)*p.w + x0
		trow := t.vals[yy*t.w : (yy+1)*t.w]
		var acc float32
		for i, tv := range trow {
			acc += tv * p.lum[base+i]
		}
		stp += float64(acc)
	}
	return (stp - t.sumT*sp/n) / math.Sqrt(denP*t.varTN)
}

// bestNCCAround scans positions (cx+-r, cy+-r) and returns the best
// correlation and its position.
func bestNCCAround(p *grayPlane, t *grayPatch, cx, cy, r int) (float64, int, int) {
	best, bx, by := -1.0, cx, cy
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if ncc := patchNCC(p, t, cx+dx, cy+dy); ncc > best {
				best, bx, by = ncc, cx+dx, cy+dy
			}
		}
	}
	return best, bx, by
}

// anchorHit is a validated anchor: the screenshot's UI scale, the image
// coordinates of the title patch's top-left corner, and the correlation
// scores of the two validation patches.
type anchorHit struct {
	scale     float64
	ox, oy    int
	ncc       float64
	headerNCC float64
}

// px converts a reference (2x) offset to image pixels at the hit's scale.
func (a *anchorHit) px(ref float64) int {
	v := ref * a.scale / gpqAnchorRefScale
	if v < 0 {
		return int(v - 0.5)
	}
	return int(v + 0.5)
}

// findAnchor locates the window title as described in the file comment.
// Returns ok=false when no placement clears the floors or the clock expired
// (the run's truncated flag tells the caller which).
func findAnchor(img image.Image, clock *parseClock) (anchorHit, bool) {
	refs, err := loadAnchorRefs()
	if err != nil {
		return anchorHit{}, false
	}
	b := img.Bounds()
	top := int(float64(b.Dy())*gpqAnchorTopFrac + 0.5)
	if minTop := int(float64(refs.title.h) * gpqAnchorScaleMax / gpqAnchorRefScale); top < minTop {
		top = minTop
	}
	if top > b.Dy() {
		top = b.Dy()
	}
	plane := buildGrayPlane(img, 0, top)
	half := plane.boxDownsample(2)
	if half == nil {
		return anchorHit{}, false
	}
	// Coarse pyramid: small scales search a 4x-down plane, large scales an
	// 8x-down one (their patches are proportionally bigger, so the deeper
	// level keeps per-scale cost flat).
	quarter := plane.boxDownsample(4)
	eighth := plane.boxDownsample(8)

	type cand struct {
		scale float64
		x, y  int // full-resolution coords
		ncc   float64
	}
	nScales := int(math.Round((gpqAnchorScaleMax-gpqAnchorScaleMin)/0.1)) + 1
	cands := make([]cand, nScales)
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for si := 0; si < nScales; si++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(si int) {
			defer func() { <-sem; wg.Done() }()
			s := gpqAnchorScaleMin + float64(si)*0.1
			dec, lvl := 4, quarter
			if s >= 2.6 {
				dec, lvl = 8, eighth
			}
			cands[si] = cand{scale: s, ncc: -1}
			if lvl == nil {
				return
			}
			t := resamplePatch(refs.title, s/gpqAnchorRefScale/float64(dec))
			if t == nil || t.w > lvl.w || t.h > lvl.h {
				return
			}
			best := cand{scale: s, ncc: -1}
			for y := 0; y+t.h <= lvl.h; y++ {
				if clock.exceeded() {
					break
				}
				for x := 0; x+t.w <= lvl.w; x++ {
					if ncc := patchNCC(lvl, t, x, y); ncc > best.ncc {
						best = cand{scale: s, x: x * dec, y: y * dec, ncc: ncc}
					}
				}
			}
			cands[si] = best
		}(si)
	}
	wg.Wait()
	if clock.exceeded() {
		return anchorHit{}, false
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].ncc > cands[j].ncc })
	hit := anchorHit{ncc: -1}
	for ci := 0; ci < gpqAnchorRefineTop && ci < len(cands); ci++ {
		c := cands[ci]
		if c.ncc < gpqAnchorMinNCC-gpqAnchorCoarseTol {
			break
		}
		// Refine on the half-resolution plane: +-0.05 scale in 0.01 steps,
		// +-6 half-res px (covers the coarse grid's quantization).
		for ds := -5; ds <= 5; ds++ {
			if clock.exceeded() {
				return anchorHit{}, false
			}
			s := c.scale + float64(ds)*0.01
			if s < gpqAnchorScaleMin-0.05 || s > gpqAnchorScaleMax+0.05 {
				continue
			}
			t := resamplePatch(refs.title, s/gpqAnchorRefScale/2)
			ncc, hx, hy := bestNCCAround(half, t, c.x/2, c.y/2, 6)
			if ncc > hit.ncc {
				hit = anchorHit{scale: s, ox: hx * 2, oy: hy * 2, ncc: ncc}
			}
		}
	}
	if hit.ncc < gpqAnchorMinNCC-0.1 {
		return anchorHit{}, false
	}

	// Polish the winner's position at full resolution; this NCC is the one
	// the acceptance floor applies to.
	tFull := resamplePatch(refs.title, hit.scale/gpqAnchorRefScale)
	ncc, ox, oy := bestNCCAround(plane, tFull, hit.ox, hit.oy, 3)
	hit.ncc, hit.ox, hit.oy = ncc, ox, oy
	if hit.ncc < gpqAnchorMinNCC {
		return anchorHit{}, false
	}

	// Secondary validation: the column-header strip at its expected offset
	// below the title (+-4px slack for accumulated rounding).
	ht := resamplePatch(refs.header, hit.scale/gpqAnchorRefScale)
	ex := hit.ox + hit.px(refs.headerDX)
	ey := hit.oy + hit.px(refs.headerDY)
	hit.headerNCC, _, _ = bestNCCAround(plane, ht, ex, ey, 4)
	if hit.headerNCC < gpqAnchorHeaderMinNCC {
		return anchorHit{}, false
	}
	return hit, true
}
