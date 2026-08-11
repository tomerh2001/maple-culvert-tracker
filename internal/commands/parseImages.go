package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func parseImages(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)

	messageLink := ""
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "message-link" {
			messageLink = v.StringValue()
		}
	}
	channelID, messageID, ok := parseMessageLink(messageLink)
	if !ok {
		r.Edit("That doesn't look like a message link. Right click a message -> Copy Message Link and paste it here.\nTip: you can also just right click the message -> Apps -> **Parse Images**.")
		return
	}

	msg, err := s.ChannelMessage(channelID, messageID)
	if err != nil {
		log.Println("parseImages: failed to fetch message", messageID, "in channel", channelID, err)
		r.Edit("Failed to fetch that message. Make sure the link is from this server and I can see the channel.")
		return
	}
	imageURLs := collectImageURLs(msg)
	if len(imageURLs) == 0 {
		r.Edit("No image attachments found on the linked message.")
		return
	}

	runParseImages(r, imageURLs)
}

// ocrOutcome is the merged result of OCRing one message's images.
type ocrOutcome struct {
	// merged holds one entry per distinct parsed row across all images
	// (attachment order preserved, top-to-bottom rows). Rows are keyed by
	// their identity (reconciled member when matched, raw decode otherwise):
	// re-seeing the same key with the same score is an overlapping
	// screenshot and is deduplicated; the same key with a DIFFERENT score is
	// recorded as a conflict and never silently overwritten.
	merged    []helpers.ScoreEntry
	unmatched []helpers.ScoreEntry
	// truncated is set when any image's parse hit its time budget: the rows
	// may be incomplete.
	truncated bool
	conflicts []string
	defects   []string
}

// ocrImagesToScores downloads and OCRs GPQ score images. A non-nil error is
// safe to display to the user.
func ocrImagesToScores(imageURLs []string) (*ocrOutcome, error) {
	font, err := helpers.LoadGPQFont()
	if err != nil {
		log.Println("parseImages: failed to load font templates:", err)
		return nil, errors.New("Internal error loading font templates. Please try again later.")
	}

	characters, err := helpers.GetActiveCharacters(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("parseImages: failed to query active characters:", err)
		return nil, errors.New("Internal error querying active characters. Please try again later.")
	}
	memberNames := make([]string, 0, len(*characters))
	for _, c := range *characters {
		memberNames = append(memberNames, c.MapleCharacterName)
	}

	// Process each image in parallel: download into memory then parse.
	type imgResult struct {
		res *helpers.ParseResult
		err error
	}
	results := make([]imgResult, len(imageURLs))
	var wg sync.WaitGroup
	for idx, url := range imageURLs {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			data, err := downloadBytes(url)
			if err != nil {
				results[idx] = imgResult{err: err}
				return
			}
			if cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data)); cerr == nil {
				log.Printf("parseImages: image %dx%d (%d KB) %s", cfg.Width, cfg.Height, len(data)/1024, url)
			}
			start := time.Now()
			res, err := helpers.ParseParticipation(data, memberNames, font)
			if res != nil {
				log.Printf("parseImages: parsed %d rows in %s (engine %s, scale %.2f, truncated %v)",
					len(res.Rows), time.Since(start).Round(time.Millisecond), res.Engine, res.Scale, res.Truncated)
			}
			results[idx] = imgResult{res: res, err: err}
		}(idx, url)
	}
	wg.Wait()

	out := &ocrOutcome{merged: []helpers.ScoreEntry{}}
	mergedPos := map[string]int{}
	for idx, r := range results {
		if r.err != nil {
			log.Println("parseImages: failed to process image", imageURLs[idx], r.err)
			return nil, errors.New("Failed to process one of the images. Please ensure they are valid `small` style GPQ score images.")
		}
		out.truncated = out.truncated || r.res.Truncated
		for _, d := range r.res.Defects {
			if len(imageURLs) > 1 {
				d = fmt.Sprintf("image %d: %s", idx+1, d)
			}
			out.defects = append(out.defects, d)
		}
		for _, e := range r.res.Rows {
			key := e.Name // reconciled member when matched, raw decode otherwise
			pos, seen := mergedPos[key]
			switch {
			case !seen:
				mergedPos[key] = len(out.merged)
				out.merged = append(out.merged, e)
			case out.merged[pos].Score != e.Score:
				out.conflicts = append(out.conflicts, fmt.Sprintf("`%s`: %d vs %d (image %d)",
					key, out.merged[pos].Score, e.Score, idx+1))
			}
		}
	}

	out.unmatched = collectUnmatched(out.merged)
	return out, nil
}

// collectUnmatched returns the entries that did not reconcile onto a tracked
// character, preserving parsed order.
func collectUnmatched(entries []helpers.ScoreEntry) []helpers.ScoreEntry {
	unmatched := make([]helpers.ScoreEntry, 0)
	for _, e := range entries {
		if !e.Matched {
			unmatched = append(unmatched, e)
		}
	}
	return unmatched
}

// runParseImages OCRs the given images and edits the deferred interaction
// response with the FULL annotated row set (every parsed row marked "tracked"
// or "NEW" - never an unmatched-only view) plus the gpq_scores.json file.
func runParseImages(r *reply, imageURLs []string) {
	oc, err := ocrImagesToScores(imageURLs)
	if err != nil {
		r.Edit(err.Error())
		return
	}

	out, err := marshalOrderedScores(oc.merged)
	if err != nil {
		log.Println("parseImages: failed to marshal result:", err)
		r.Edit("Internal error building the JSON result.")
		return
	}

	msg := fmt.Sprintf("Parsed %d row(s) from %d image(s): %d tracked, %d NEW.",
		len(oc.merged), len(imageURLs), len(oc.merged)-len(oc.unmatched), len(oc.unmatched))
	msg += ocrWarnings(oc)
	// Non-fatal validation: scores should be in descending order. If not, warn
	// but still attach the output so the user can inspect/correct it.
	if idx := firstDescendingViolation(oc.merged); idx >= 0 {
		msg += "\n:warning: Scores are not in descending order (`" +
			oc.merged[idx-1].Name + "`: " + strconv.Itoa(oc.merged[idx-1].Score) + " -> `" +
			oc.merged[idx].Name + "`: " + strconv.Itoa(oc.merged[idx].Score) +
			"). The output may be incorrect; please verify."
	}
	r.EditWithTable(msg, formatAnnotatedScoresTable(oc.merged, trackedSetOf(oc.merged, oc.unmatched)), "parsed_scores.txt",
		&discordgo.File{
			Name:        "gpq_scores.json",
			ContentType: "application/json",
			Reader:      strings.NewReader(string(out)),
		})
}

// ocrWarnings renders the truncation/conflict/defect warnings of an OCR
// outcome (empty string when clean). Lists are capped so the reply stays
// within Discord's content limits.
func ocrWarnings(oc *ocrOutcome) string {
	msg := ""
	if oc.truncated {
		msg += "\n:warning: The parse hit its time limit - results may be incomplete. Try again or crop the screenshot."
	}
	if len(oc.conflicts) > 0 {
		msg += "\n:warning: Conflicting scores for the same character across images (kept the first, please verify):"
		msg += "\n- " + strings.Join(capList(oc.conflicts, 5), "\n- ")
	}
	if len(oc.defects) > 0 {
		msg += "\n:warning: Parse anomalies:"
		msg += "\n- " + strings.Join(capList(oc.defects, 5), "\n- ")
	}
	return msg
}

// capList truncates a list to n items, appending a "... and X more" marker.
func capList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	out := append([]string{}, items[:n]...)
	return append(out, fmt.Sprintf("... and %d more", len(items)-n))
}

// firstDescendingViolation returns the index of the first entry whose score is
// greater than the previous entry's score (i.e. breaks descending order), or -1
// if the entries are in non-increasing order.
func firstDescendingViolation(entries []helpers.ScoreEntry) int {
	for i := 1; i < len(entries); i++ {
		if entries[i].Score > entries[i-1].Score {
			return i
		}
	}
	return -1
}

// marshalOrderedScores emits a JSON object of name -> score with 4-space
// indentation, preserving the given entry order (encoding/json sorts map keys,
// so a map cannot be used here).
func marshalOrderedScores(entries []helpers.ScoreEntry) ([]byte, error) {
	if len(entries) == 0 {
		return []byte("{}"), nil
	}
	var b strings.Builder
	b.WriteString("{\n")
	for idx, e := range entries {
		key, err := json.Marshal(e.Name)
		if err != nil {
			return nil, err
		}
		b.WriteString("    ")
		b.Write(key)
		b.WriteString(": ")
		b.WriteString(strconv.Itoa(e.Score))
		if idx < len(entries)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func isImageAttachment(a *discordgo.MessageAttachment) bool {
	if strings.HasPrefix(a.ContentType, "image/") {
		return true
	}
	fn := strings.ToLower(a.Filename)
	return strings.HasSuffix(fn, ".png") ||
		strings.HasSuffix(fn, ".jpg") ||
		strings.HasSuffix(fn, ".jpeg") ||
		strings.HasSuffix(fn, ".gif") ||
		strings.HasSuffix(fn, ".webp")
}

var downloadClient = &http.Client{Timeout: 20 * time.Second}

func downloadBytes(url string) ([]byte, error) {
	resp, err := downloadClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
