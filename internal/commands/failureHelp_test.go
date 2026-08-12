package commands

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// The embedded example screenshot must be a real, decodable PNG - it is
// attached to every OCR-stage failure reply.
func TestExampleCulvertPNGEmbedded(t *testing.T) {
	if len(exampleCulvertPNG) == 0 {
		t.Fatal("exampleCulvertPNG is empty")
	}
	pngMagic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if !bytes.HasPrefix(exampleCulvertPNG, pngMagic) {
		t.Fatal("exampleCulvertPNG does not start with the PNG signature")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(exampleCulvertPNG))
	if err != nil {
		t.Fatalf("exampleCulvertPNG does not decode as PNG: %v", err)
	}
	if cfg.Width == 0 || cfg.Height == 0 {
		t.Fatalf("exampleCulvertPNG has degenerate dimensions %dx%d", cfg.Width, cfg.Height)
	}
}

// The failure help keeps the specific error first, explains every
// requirement, labels the attachment, and fits a single Discord message.
func TestScreenshotFailureContent(t *testing.T) {
	specific := "Image 1: could not locate the culvert window in this image"
	got := screenshotFailureContent(specific)

	if !strings.HasPrefix(got, specific+"\n") {
		t.Errorf("content does not lead with the specific error:\n%s", got)
	}
	for _, want := range []string{
		"Member Participation Status",
		"title must be fully visible",
		"**Name**",
		"**Culvert**",
		"unobstructed",
		"window size or UI scale",
		"not a re-compressed copy",
		"ONE message",
		"example of a good screenshot",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("content missing %q:\n%s", want, got)
		}
	}
	if len(got) > 1900 {
		t.Errorf("content is %d chars, exceeding the single-message budget", len(got))
	}
}

// errScreenshotUnusable is the failure-kind marker: errors.As must find it on
// wrapped failures (-> attach example) and must not fire on plain errors.
func TestErrScreenshotUnusableMarker(t *testing.T) {
	base := errors.New("boom")
	wrapped := error(errScreenshotUnusable{base})

	var unusable errScreenshotUnusable
	if !errors.As(wrapped, &unusable) {
		t.Fatal("errors.As failed to detect errScreenshotUnusable")
	}
	if wrapped.Error() != "boom" {
		t.Errorf("wrapped message = %q, want %q", wrapped.Error(), "boom")
	}
	if !errors.Is(wrapped, base) {
		t.Error("errScreenshotUnusable does not unwrap to its cause")
	}

	var notThere errScreenshotUnusable
	if errors.As(errors.New("plain"), &notThere) {
		t.Error("errors.As matched a plain error")
	}

	// The window-not-found wrapping used by ocrImagesToScores stays
	// detectable both as the marker and as the sentinel.
	windowErr := error(errScreenshotUnusable{fmt.Errorf("Image 1: %s", helpers.ErrCulvertWindowNotFound.Error())})
	var u2 errScreenshotUnusable
	if !errors.As(windowErr, &u2) {
		t.Error("window-not-found wrapping lost the errScreenshotUnusable marker")
	}
}
