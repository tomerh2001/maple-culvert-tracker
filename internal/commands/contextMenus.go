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
func parseImagesFromMessage(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	content := new(string)
	editContent := func(msg string) {
		*content = msg
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: content})
	}

	msg := targetMessage(s, i)
	if msg == nil {
		editContent("Failed to fetch the selected message.")
		return
	}

	imageURLs := collectImageURLs(msg)
	if len(imageURLs) == 0 {
		editContent("No image attachments found on the selected message.")
		return
	}

	runParseImages(s, i, imageURLs)
}

// submitScoresFromMessage is the one-step message context menu submission:
// right click a message -> Apps -> Submit Scores. If the message carries a
// pre-parsed .txt/.json scores attachment it is submitted directly, otherwise
// the image attachments are OCR'd and the result is submitted. Scores go to
// the current culvert week and existing scores are never overwritten; use
// /submit-scores with overwrite-existing for corrections.
func submitScoresFromMessage(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	content := new(string)
	editContent := func(msg string) {
		*content = msg
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: content})
	}

	msg := targetMessage(s, i)
	if msg == nil {
		editContent("Failed to fetch the selected message.")
		return
	}

	culvertDate := helpers.GetCulvertResetDate(time.Now())
	culvertDateStr := culvertDate.Format(time.DateOnly)

	scores := map[string]int{}
	if !scoresFromMessage(s, i, msg, content, scores) {
		return
	}

	finalizeSubmitScores(s, i, content, scores, culvertDate, culvertDateStr, false)
}

// scoresFromMessage fills scores from a message: a pre-parsed .txt/.json
// scores file when present (e.g. the gpq_scores.json produced by Parse
// Images), otherwise OCR of its image attachments. On failure it edits the
// (deferred) interaction response itself and returns false.
func scoresFromMessage(s *discordgo.Session, i *discordgo.InteractionCreate, msg *discordgo.Message, content *string, scores map[string]int) bool {
	editContent := func(m string) {
		*content = m
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: content})
	}

	var scoresAttachment *discordgo.MessageAttachment
	for _, a := range msg.Attachments {
		if strings.HasSuffix(a.Filename, ".txt") || strings.HasSuffix(a.Filename, ".json") {
			scoresAttachment = a
			break
		}
	}

	if scoresAttachment != nil {
		if scoresAttachment.Size > 2048*1024 {
			editContent("Attachment size exceeds 2MB limit! Please upload a smaller file.")
			return false
		}
		body, err := downloadBytes(scoresAttachment.URL)
		if err != nil {
			log.Println("scoresFromMessage: failed to download attachment:", err)
			editContent("Failed to download the scores attachment! Please try again.")
			return false
		}
		if err := json.Unmarshal(body, &scores); err != nil {
			editContent("Failed to parse attachment content! Please ensure it's valid JSON format of { \"character-name\": 123, \"character-name-2\": 456 }.")
			return false
		}
		return true
	}

	imageURLs := collectImageURLs(msg)
	if len(imageURLs) == 0 {
		editContent("No image or scores attachments found on the selected message.")
		return false
	}

	merged, unmatched, err := ocrImagesToScores(imageURLs)
	if err != nil {
		editContent(err.Error())
		return false
	}
	if len(unmatched) > 0 {
		*content = "Nothing submitted: some parsed character names did not match any active character. Fix the images or track these characters first."
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: content,
			Files: []*discordgo.File{{
				Name:        "unmatched.txt",
				ContentType: "text/plain",
				Reader:      strings.NewReader(formatUnmatchedScoresTable(unmatched)),
			}},
		})
		return false
	}
	if idx := firstDescendingViolation(merged); idx >= 0 {
		editContent("Nothing submitted: parsed scores are not in descending order (`" +
			merged[idx-1].Name + "` -> `" + merged[idx].Name +
			"`), which usually means an OCR misread. Run Parse Images on the message, verify the JSON, then submit that instead.")
		return false
	}
	for _, e := range merged {
		scores[e.Name] = e.Score
	}
	return true
}
