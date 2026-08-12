package helpers

import (
	"errors"
	"image"
	"image/color"
	"math/rand"
	"testing"
	"time"
)

// TestAnchorAntiSpoof: images without the genuine window-title anchor - noise,
// solid fills, and text rendered in the game font but WITHOUT the window -
// must yield anchor-not-found, produce zero rows and report the honest
// "could not locate the culvert window" error (the legacy 1x fallback only
// accepts the pre-cropped table shape: name column + 3 numeric columns).
func TestAnchorAntiSpoof(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(1))
	noise := image.NewRGBA(image.Rect(0, 0, 900, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			noise.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
		}
	}

	solid := image.NewRGBA(image.Rect(0, 0, 900, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			solid.Set(x, y, color.RGBA{34, 34, 34, 255})
		}
	}

	// Game-font words on a dark background, but no participation window: no
	// title anchor, and no name-plus-numeric-columns table row shape either.
	fake := image.NewRGBA(image.Rect(0, 0, 900, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			fake.Set(x, y, color.RGBA{34, 34, 34, 255})
		}
	}
	words := []string{"hello", "world", "guild", "members", "status", "click", "here"}
	for i, w := range words {
		renderWord(t, fake, font, 60+40*(i%3), 60+40*i, w)
	}

	for _, tc := range []struct {
		label string
		img   image.Image
	}{
		{"noise", noise},
		{"solid", solid},
		{"fake-font", fake},
	} {
		start := time.Now()
		res, err := ParseParticipation(encodePNG(t, tc.img), nil, font)
		elapsed := time.Since(start)
		switch {
		case err != nil:
			if !errors.Is(err, ErrCulvertWindowNotFound) {
				t.Errorf("%s: got error %v, want ErrCulvertWindowNotFound", tc.label, err)
			}
		case res.Truncated:
			// A clock expiry on absurd input is an acceptable honest outcome.
			if len(res.Rows) != 0 {
				t.Errorf("%s: truncated parse still returned %d rows", tc.label, len(res.Rows))
			}
		default:
			t.Errorf("%s: expected anchor-not-found error, got %d rows (engine %s)", tc.label, len(res.Rows), res.Engine)
		}
		if res != nil && res.AnchorFound {
			t.Errorf("%s: anchor falsely found", tc.label)
		}
		t.Logf("%s: rejected in %s", tc.label, elapsed.Round(time.Millisecond))
	}
}
