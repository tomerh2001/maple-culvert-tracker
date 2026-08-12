package commands

import (
	"bytes"
	_ "embed"

	"github.com/bwmarrin/discordgo"
)

// exampleCulvertPNG is a copy of provided/real-sample.png (the committed
// real-guild test fixture): a full Guild window on the Member Participation
// Status page, exactly what the OCR wants. It is attached to OCR-stage
// failure replies as a known-good example. go:embed cannot reach outside the
// package, hence the in-package copy.
//
//go:embed example_culvert.png
var exampleCulvertPNG []byte

// errScreenshotUnusable marks an OCR-stage failure the submitter can fix by
// retaking or re-sending the screenshot (window not found, undecodable or
// undownloadable image). It is the failure-kind decision consulted by
// scoresFromMessage: wrapped errors get the requirements explainer plus the
// example screenshot; everything else (internal errors, semantic gates such
// as conflicts/truncation/ordering) stays text-only.
type errScreenshotUnusable struct{ err error }

func (e errScreenshotUnusable) Error() string { return e.err.Error() }
func (e errScreenshotUnusable) Unwrap() error { return e.err }

// screenshotFailureContent renders the shared OCR failure help: the specific
// error first, then what a successful parse needs.
func screenshotFailureContent(specific string) string {
	return specific + "\n\nFor a successful parse:\n" +
		"- Screenshot the in-game **Guild** window, **Member Participation Status** page - the window title must be fully visible (it is how the bot finds the table).\n" +
		"- Keep the **Name** and **Culvert** columns unobstructed - no windows, tooltips, or cursors covering them.\n" +
		"- Any window size or UI scale works. Send the game screenshot itself, not a re-compressed copy or a photo of the screen.\n" +
		"- Long roster? Scroll the list and attach ALL page screenshots to ONE message, then right-click that message.\n" +
		"Attached: an example of a good screenshot."
}

// editScreenshotFailure fills the deferred response with the OCR failure
// help and attaches the example screenshot. This is the product-owner-mandated
// exception (with the /culvert chart) to text-only command replies, and works
// on ephemeral responses.
func (r *reply) editScreenshotFailure(specific string) {
	r.EditData(&discordgo.InteractionResponseData{
		Content: screenshotFailureContent(specific),
		Files: []*discordgo.File{{
			Name:        "example_culvert.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader(exampleCulvertPNG),
		}},
	})
}
