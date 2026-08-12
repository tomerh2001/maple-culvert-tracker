package helpers

import (
	"bytes"
	"image"
	"os"
	"testing"
	"time"
)

// TestAnchorGeometryDebug reports, for each real fixture, the anchor hit
// (scale, origin, NCC scores) and the per-row name/culvert span extents in
// reference (2x) units relative to the title origin. It is the measurement
// harness the geometry constants in gpq_ocr_anchor_parse.go were derived
// with; it never fails, it only logs.
func TestAnchorGeometryDebug(t *testing.T) {
	for _, name := range []string{"real-sample.png", "real-full-1.png", "real-full-2.png", "real-full-3.png"} {
		data, err := os.ReadFile("../../../provided/" + name)
		if err != nil {
			t.Skipf("no %s", name)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		clock := (&parseRun{}).until(start.Add(30 * time.Second))
		hit, ok := findAnchor(img, clock)
		t.Logf("%s: anchor ok=%v scale=%.3f origin=(%d,%d) ncc=%.3f headerNCC=%.3f in %s",
			name, ok, hit.scale, hit.ox, hit.oy, hit.ncc, hit.headerNCC, time.Since(start).Round(time.Millisecond))
		if !ok {
			continue
		}

		// Measure spans on the loose plane per pitch slot, converted to
		// reference units relative to the title origin.
		p := buildNCCPlane(img, hit.scale)
		toRef := func(v int) float64 { return float64(v) * gpqAnchorRefScale / hit.scale }
		rowsTop := hit.oy + hit.px(gpqRefFirstRowTop)
		pitch := gpqRefRowPitch * hit.scale / gpqAnchorRefScale
		for k := 0; k < gpqRefRowCount; k++ {
			y0 := rowsTop + int(pitch*float64(k)+0.5)
			y1 := y0 + int(pitch*0.6)
			if y1 > p.h {
				break
			}
			loosePre := p.bandLoosePrefix(y0, y1)
			spans := bandSpans(loosePre, 0, p.w, scalePx(gpqGapStop, hit.scale))
			refSpans := make([][2]int, 0, len(spans))
			for _, sp := range spans {
				refSpans = append(refSpans, [2]int{
					int(toRef(sp[0]-hit.ox) + 0.5),
					int(toRef(sp[1]-hit.ox) + 0.5),
				})
			}
			t.Logf("  row %2d yref=%3.0f spans(ref)=%v", k, toRef(y0-hit.oy), refSpans)
		}
	}
}
