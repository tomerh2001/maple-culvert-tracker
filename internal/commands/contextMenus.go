package commands

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// targetMessage returns the message a message context menu command (right
// click -> Apps) was invoked on.
func targetMessage(s *discordgo.Session, i *discordgo.InteractionCreate) *discordgo.Message {
	d := i.ApplicationCommandData()
	if d.Resolved != nil {
		if msg, ok := d.Resolved.Messages[d.TargetID]; ok && msg != nil {
			return msg
		}
	}
	msg, err := s.ChannelMessage(i.ChannelID, d.TargetID)
	if err != nil {
		log.Println("contextMenus: failed to fetch target message", d.TargetID, err)
		return nil
	}
	return msg
}

func collectImageURLs(msg *discordgo.Message) []string {
	imageURLs := []string{}
	for _, a := range msg.Attachments {
		if isImageAttachment(a) {
			imageURLs = append(imageURLs, a.URL)
		}
	}
	return imageURLs
}

// parseImagesFromMessage is the message context menu version of /parse-images:
// right click a message with GPQ score screenshots -> Apps -> Parse Images.
// Submitter-gated, matching the docs.
func parseImagesFromMessage(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, false)

	msg := targetMessage(s, i)
	if msg == nil {
		r.Edit("Failed to fetch the selected message.")
		return
	}

	imageURLs := collectImageURLs(msg)
	if len(imageURLs) == 0 {
		r.Edit("No image attachments found on the selected message.")
		return
	}

	runParseImages(r, imageURLs)
}

// submitScoresFromMessage is the one-step message context menu submission:
// right click a message -> Apps -> Submit Scores. If the message carries a
// pre-parsed .txt/.json scores attachment it is submitted directly, otherwise
// the image attachments are OCR'd and the result is submitted. Scores go to
// the current culvert week; existing scores are never overwritten and absent
// characters are never zero-filled - use /submit-scores for corrections.
// Unknown parsed names are auto-tracked (the /submit-scores default).
func submitScoresFromMessage(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, false)

	msg := targetMessage(s, i)
	if msg == nil {
		r.Edit("Failed to fetch the selected message.")
		return
	}

	culvertDate := helpers.GetCulvertResetDate(time.Now())
	culvertDateStr := culvertDate.Format(time.DateOnly)

	scores := map[string]int{}
	if !scoresFromMessage(s, r, msg, scores) {
		return
	}

	finalizeSubmitScores(s, r, scores, culvertDate, culvertDateStr, false, false, true)
}

// scoresFromMessage fills scores from a message: a pre-parsed .txt/.json
// scores file when present (e.g. the gpq_scores.json produced by Parse
// Images), otherwise OCR of its image attachments. On failure it edits the
// (deferred) interaction response itself and returns false.
func scoresFromMessage(s *discordgo.Session, r *reply, msg *discordgo.Message, scores map[string]int) bool {
	var scoresAttachment *discordgo.MessageAttachment
	for _, a := range msg.Attachments {
		if strings.HasSuffix(a.Filename, ".txt") || strings.HasSuffix(a.Filename, ".json") {
			scoresAttachment = a
			break
		}
	}

	if scoresAttachment != nil {
		if scoresAttachment.Size > 2048*1024 {
			r.Edit("Attachment size exceeds 2MB limit! Please upload a smaller file.")
			return false
		}
		body, err := downloadBytes(scoresAttachment.URL)
		if err != nil {
			log.Println("scoresFromMessage: failed to download attachment:", err)
			r.Edit("Failed to download the scores attachment! Please try again.")
			return false
		}
		if err := json.Unmarshal(body, &scores); err != nil {
			r.Edit("Failed to parse attachment content! Please ensure it's valid JSON format of { \"character-name\": 123, \"character-name-2\": 456 }.")
			return false
		}
		return true
	}

	imageURLs := collectImageURLs(msg)
	if len(imageURLs) == 0 {
		r.Edit("No image or scores attachments found on the selected message.")
		return false
	}

	oc, err := ocrImagesToScores(imageURLs)
	if err != nil {
		r.Edit(err.Error())
		return false
	}
	// Submission safety gates: an incomplete (time-limited) or internally
	// conflicting parse must never be written to the database.
	if oc.truncated {
		r.Edit("Nothing submitted: the parse hit its time limit - results may be incomplete. Try again or crop the screenshot, or run Parse Images and submit the verified JSON instead.")
		return false
	}
	if len(oc.conflicts) > 0 {
		r.Edit("Nothing submitted: the images disagree on some characters' scores:\n- " +
			strings.Join(capList(oc.conflicts, 10), "\n- ") +
			"\nRun Parse Images on the message, verify the JSON, then submit that instead.")
		return false
	}
	if idx := firstDescendingViolation(oc.merged); idx >= 0 {
		r.Edit("Nothing submitted: parsed scores are not in descending order (`" +
			oc.merged[idx-1].Name + "` -> `" + oc.merged[idx].Name +
			"`), which usually means an OCR misread. Run Parse Images on the message, verify the JSON, then submit that instead.")
		return false
	}
	for _, e := range oc.merged {
		scores[e.Name] = e.Score
	}
	return true
}
