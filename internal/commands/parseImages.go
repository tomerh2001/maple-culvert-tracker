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

// ocrImagesToScores downloads and OCRs GPQ score images, returning the merged
// scores (attachment order preserved, top-to-bottom rows; duplicate names keep
// their first-seen position with later scores overwriting) and any parsed
// names that do not match an active tracked character. A non-nil error is safe
// to display to the user.
func ocrImagesToScores(imageURLs []string) (merged []helpers.ScoreEntry, unmatched []helpers.ScoreEntry, err error) {
	font, err := helpers.LoadGPQFont()
	if err != nil {
		log.Println("parseImages: failed to load font templates:", err)
		return nil, nil, errors.New("Internal error loading font templates. Please try again later.")
	}

	characters, err := helpers.GetActiveCharacters(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("parseImages: failed to query active characters:", err)
		return nil, nil, errors.New("Internal error querying active characters. Please try again later.")
	}
	memberNames := make([]string, 0, len(*characters))
	activeSet := make(map[string]bool, len(*characters))
	for _, c := range *characters {
		memberNames = append(memberNames, c.MapleCharacterName)
		activeSet[c.MapleCharacterName] = true
	}

	// Process each image in parallel: download into memory then parse.
	type imgResult struct {
		scores []helpers.ScoreEntry
		err    error
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
			scores, err := helpers.ParseParticipationImage(data, memberNames, font)
			log.Printf("parseImages: parsed %d rows in %s", len(scores), time.Since(start).Round(time.Millisecond))
			results[idx] = imgResult{scores: scores, err: err}
		}(idx, url)
	}
	wg.Wait()

	merged = []helpers.ScoreEntry{}
	mergedPos := map[string]int{}
	for idx, r := range results {
		if r.err != nil {
			log.Println("parseImages: failed to process image", imageURLs[idx], r.err)
			return nil, nil, errors.New("Failed to process one of the images. Please ensure they are valid `small` style GPQ score images.")
		}
		for _, e := range r.scores {
			if pos, ok := mergedPos[e.Name]; ok {
				merged[pos].Score = e.Score
			} else {
				mergedPos[e.Name] = len(merged)
				merged = append(merged, e)
			}
		}
	}

	return merged, collectUnmatchedScores(merged, activeSet), nil
}

// runParseImages OCRs the given images and edits the deferred interaction
// response with the FULL annotated row set (every parsed row marked "tracked"
// or "NEW" - never an unmatched-only view) plus the gpq_scores.json file.
func runParseImages(r *reply, imageURLs []string) {
	merged, unmatched, err := ocrImagesToScores(imageURLs)
	if err != nil {
		r.Edit(err.Error())
		return
	}

	out, err := marshalOrderedScores(merged)
	if err != nil {
		log.Println("parseImages: failed to marshal result:", err)
		r.Edit("Internal error building the JSON result.")
		return
	}

	msg := fmt.Sprintf("Parsed %d row(s) from %d image(s): %d tracked, %d NEW.",
		len(merged), len(imageURLs), len(merged)-len(unmatched), len(unmatched))
	// Non-fatal validation: scores should be in descending order. If not, warn
	// but still attach the output so the user can inspect/correct it.
	if idx := firstDescendingViolation(merged); idx >= 0 {
		msg += "\n:warning: Scores are not in descending order (`" +
			merged[idx-1].Name + "`: " + strconv.Itoa(merged[idx-1].Score) + " -> `" +
			merged[idx].Name + "`: " + strconv.Itoa(merged[idx].Score) +
			"). The output may be incorrect; please verify."
	}
	r.EditWithTable(msg, formatAnnotatedScoresTable(merged, trackedSetOf(merged, unmatched)), "parsed_scores.txt",
		&discordgo.File{
			Name:        "gpq_scores.json",
			ContentType: "application/json",
			Reader:      strings.NewReader(string(out)),
		})
}

func collectUnmatchedScores(entries []helpers.ScoreEntry, activeSet map[string]bool) []helpers.ScoreEntry {
	unmatched := make([]helpers.ScoreEntry, 0)
	for _, entry := range entries {
		if !activeSet[entry.Name] {
			unmatched = append(unmatched, entry)
		}
	}
	return unmatched
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
