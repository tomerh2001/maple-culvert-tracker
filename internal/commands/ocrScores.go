package commands

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

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
func ocrImagesToScores(tenantID string, imageURLs []string) (*ocrOutcome, error) {
	font, err := helpers.LoadGPQFont()
	if err != nil {
		log.Println("ocrImagesToScores: failed to load font templates:", err)
		return nil, errors.New("Internal error loading font templates. Please try again later.")
	}

	characters, err := helpers.GetActiveCharacters(apiredis.RedisDB, db.DB, tenantID)
	if err != nil {
		log.Println("ocrImagesToScores: failed to query active characters:", err)
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
				log.Printf("ocrImagesToScores: image %dx%d (%d KB) %s", cfg.Width, cfg.Height, len(data)/1024, url)
			}
			start := time.Now()
			res, err := helpers.ParseParticipation(data, memberNames, font)
			if res != nil {
				log.Printf("ocrImagesToScores: parsed %d rows in %s (engine %s, scale %.2f, truncated %v)",
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
			log.Println("ocrImagesToScores: failed to process image", imageURLs[idx], r.err)
			// Screenshot-fixable failures are marked errScreenshotUnusable so
			// the caller replies with the requirements + example screenshot.
			if errors.Is(r.err, helpers.ErrCulvertWindowNotFound) {
				return nil, errScreenshotUnusable{fmt.Errorf("Image %d: %s", idx+1, r.err.Error())}
			}
			return nil, errScreenshotUnusable{errors.New("Failed to process one of the images.")}
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

// defectsWarning renders the non-fatal parse-defect list of an OCR outcome
// ("" when clean). Defects never block a parse or submission, but they are
// never dropped silently either.
func defectsWarning(defects []string) string {
	if len(defects) == 0 {
		return ""
	}
	return "\n:warning: Parse anomalies:\n- " + strings.Join(capList(defects, 5), "\n- ")
}

// orList renders a candidate list as "A", "A or B", or "A, B, or C".
func orList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " or " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
	}
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
