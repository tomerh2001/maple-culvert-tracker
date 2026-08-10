package commands

//lint:file-ignore ST1001 Dot imports by jet

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// messageLinkRe matches https://discord.com/channels/<guild>/<channel>/<message>
// (any subdomain) or a bare "<channel>/<message>" pair.
var messageLinkRe = regexp.MustCompile(`(?:https?://[a-z.]*discord(?:app)?\.com/channels/\d+/)?(\d+)/(\d+)$`)

func parseMessageLink(s string) (channelID, messageID string, ok bool) {
	m := messageLinkRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// submitScores submits weekly scores from either an attached scores file or a
// linked message (scores file or screenshots via OCR).
func submitScores(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	var err error

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	content := new(string)
	editContent := func(msg string) {
		*content = msg
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: content})
	}

	culvertDate := helpers.GetCulvertResetDate(time.Now())
	culvertDateStr := culvertDate.Format(time.DateOnly)
	overwriteExisting := false
	messageLink := ""
	var attachment *discordgo.MessageAttachment

	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "scores-attachment":
			if a, ok := i.ApplicationCommandData().Resolved.Attachments[v.Value.(string)]; ok {
				attachment = a
			}
		case "message-link":
			messageLink = v.StringValue()
		case "date":
			culvertDateStr = strings.Trim(v.StringValue(), " ")
			culvertDate, err = time.Parse(time.DateOnly, culvertDateStr)
			if err != nil {
				editContent("Invalid date format provided! Please use YYYY-MM-DD.")
				return
			}
			if culvertDate.Weekday() != helpers.GetCulvertResetDay(time.Now()) {
				editContent("The provided date is not a Wednesday! Culvert resets occur on Wednesdays.")
				return
			}
		case "overwrite-existing":
			overwriteExisting = v.BoolValue()
		}
	}

	scores := map[string]int{}
	switch {
	case attachment != nil:
		errMsg := scoresFromJSONAttachment(attachment, scores)
		if errMsg != "" {
			editContent(errMsg)
			return
		}
	case messageLink != "":
		channelID, messageID, ok := parseMessageLink(messageLink)
		if !ok {
			editContent("That doesn't look like a message link. Right click a message -> Copy Message Link and paste it here.")
			return
		}
		msg, err := s.ChannelMessage(channelID, messageID)
		if err != nil {
			editContent("Failed to fetch that message. Make sure the link is from this server and I can see the channel.")
			return
		}
		if !scoresFromMessage(s, i, msg, content, scores) {
			return
		}
	default:
		editContent("Provide either a `scores-attachment` file or a `message-link` to a message with screenshots.\nTip: you can also just right click that message -> Apps -> **Submit Scores**.")
		return
	}

	finalizeSubmitScores(s, i, content, scores, culvertDate, culvertDateStr, overwriteExisting)
}

// scoresFromJSONAttachment downloads and parses a .txt/.json scores file into
// the given map, returning a user-displayable error message on failure.
func scoresFromJSONAttachment(attachment *discordgo.MessageAttachment, scores map[string]int) string {
	if attachment.Size > 2048*1024 {
		return "Attachment size exceeds 2MB limit! Please upload a smaller file."
	}
	if !strings.HasSuffix(attachment.Filename, ".txt") && !strings.HasSuffix(attachment.Filename, ".json") {
		return "Invalid attachment format! Please upload a .txt or .json file."
	}
	resp, err := http.Get(attachment.URL)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "Failed to download your attachment! Please try again."
	}
	defer resp.Body.Close()
	bodyContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return "Error reading attachment content! Please try again."
	}
	if err := json.Unmarshal(bodyContent, &scores); err != nil {
		return "Failed to parse attachment content! Please ensure it's valid JSON format of { \"character-name\": 123, \"character-name-2\": 456 }."
	}
	return ""
}

// finalizeSubmitScores runs the shared score submission flow once a scores map
// (character name -> score) has been parsed from a source.
func finalizeSubmitScores(s *discordgo.Session, i *discordgo.InteractionCreate, content *string, attachmentMap map[string]int, culvertDate time.Time, culvertDateStr string, overwriteExisting bool) {
	// query all scores for culvert date to see if any exist
	// get all active tracked characters
	characters, err := helpers.GetActiveCharacters(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("submitScores: Error querying active characters:", err)
		*content = "Internal server error while querying active characters. Please try again later."
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: content,
		})
		return
	}

	characterIDs := []Expression{}
	for _, v := range *characters {
		characterIDs = append(characterIDs, Int64(v.ID))
	}

	stmt := SELECT(Characters.ID.AS("id"), Characters.MapleCharacterName.AS("maple_character_name"), CharacterCulvertScores.Score.AS("score")).FROM(Characters.LEFT_JOIN(CharacterCulvertScores, Characters.ID.EQ(CharacterCulvertScores.CharacterID).AND(CharacterCulvertScores.CulvertDate.EQ(DateT(culvertDate))))).WHERE(Characters.ID.IN(characterIDs...))

	trackedCharacterScores := []struct {
		ID                 int
		MapleCharacterName string
		Score              sql.NullInt64
	}{}

	err = stmt.Query(db.DB, &trackedCharacterScores)
	if err != nil {
		log.Println("submitScores: Error querying tracked character scores:", err)
		*content = "Internal server error while querying existing scores. Please try again later."
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: content,
		})
		return
	}

	// validate overwriteExisting
	// check if there are any scores

	newMapIsNew := data.POSTCulvertBody{
		IsNew: true,
		Week:  culvertDateStr,
		Payload: []struct {
			CharacterID int64 `json:"character_id"`
			Score       int   `json:"score"`
		}{},
	}
	newMapIsNotNew := data.POSTCulvertBody{
		IsNew: false,
		Week:  culvertDateStr,
		Payload: []struct {
			CharacterID int64 `json:"character_id"`
			Score       int   `json:"score"`
		}{},
	}
	for _, v := range trackedCharacterScores {
		if _, ok := attachmentMap[v.MapleCharacterName]; ok {
			// break if overwriteExisting is not allowed and score exists
			if v.Score.Valid && attachmentMap[v.MapleCharacterName] > 0 && !overwriteExisting {
				*content = "Existing scores found, Set the `overwrite-existing` option to `True` to overwrite them. No changes were made for " + culvertDateStr + "."
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: content,
				})
				return
			}

			// character found in attachment map, score is nil = isNew: true, add to newMapIsNew
			if !v.Score.Valid { // score is null, must insert
				newMapIsNew.Payload = append(newMapIsNew.Payload, struct {
					CharacterID int64 `json:"character_id"`
					Score       int   `json:"score"`
				}{
					CharacterID: int64(v.ID),
					Score:       attachmentMap[v.MapleCharacterName],
				})
			} else {
				if v.Score.Valid && attachmentMap[v.MapleCharacterName] != int(v.Score.Int64) { // score exists, must update if different
					newMapIsNotNew.Payload = append(newMapIsNotNew.Payload, struct {
						CharacterID int64 `json:"character_id"`
						Score       int   `json:"score"`
					}{
						CharacterID: int64(v.ID),
						Score:       attachmentMap[v.MapleCharacterName],
					})
				}
			}

			// done processing this character, delete from attachmentMap
			delete(attachmentMap, v.MapleCharacterName)
			// the remaining entries in attachmentMap are untracked characters, which are missing in database, we need to send s.InteractionResponseEdit and return early later
		} else {
			// character exists in database, character not in attachment, meaning score this week must edit
			if !v.Score.Valid {
				// score is null, must insert
				newMapIsNew.Payload = append(newMapIsNew.Payload, struct {
					CharacterID int64 `json:"character_id"`
					Score       int   `json:"score"`
				}{
					CharacterID: int64(v.ID),
					Score:       0,
				})
			}
			if v.Score.Valid && v.Score.Int64 > 0 {
				// score exists, and not 0, must update to 0
				newMapIsNotNew.Payload = append(newMapIsNotNew.Payload, struct {
					CharacterID int64 `json:"character_id"`
					Score       int   `json:"score"`
				}{
					CharacterID: int64(v.ID),
					Score:       0,
				})
			}
		}
	}
	// done processing all tracked characters

	// check if there are untracked characters in attachmentMap
	if len(attachmentMap) > 0 {
		*content = "Failed to submit scores. Correct their name or track these new characters before submitting their scores."
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: content,
			Files: []*discordgo.File{{
				Name:        "names.txt",
				ContentType: "text/plain",
				Reader:      strings.NewReader(formatUnmatchedScoresTable(sortedScoreEntries(attachmentMap))),
			}},
		})
		return
	}

	// done sorting isNew and isNotNew maps

	// generate api auth token for internal api call
	apiAuth := apihelpers.GenerateAPIAuthToken(i.Member.User.Username, i.Member.User.ID, time.Now().Add(5*time.Minute))
	newMapIsNewSuccess := true
	newMapIsNotNewSuccess := true
	port := os.Getenv("BACKEND_HTTP_PORT")
	if port == "" {
		port = "8080"
	}

	client := &http.Client{}

	if len(newMapIsNew.Payload) > 0 {
		body, _ := json.Marshal(newMapIsNew)
		req, _ := http.NewRequest("POST", "http://localhost:"+port+"/api/maple/characters/culvert", strings.NewReader(string(body)))
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Authorization", "Bearer "+apiAuth)

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			newMapIsNewSuccess = false
			log.Println("submitScores: Error submitting new scores:", resp.StatusCode, err)
		}
	}

	if len(newMapIsNotNew.Payload) > 0 {
		body, _ := json.Marshal(newMapIsNotNew)
		req, _ := http.NewRequest("POST", "http://localhost:"+port+"/api/maple/characters/culvert", strings.NewReader(string(body)))
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Authorization", "Bearer "+apiAuth)

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			newMapIsNotNewSuccess = false
			log.Println("submitScores: Error updating existing scores:", resp.StatusCode, err)
		}
	}

	log.Println("submitScores: len(newMapIsNew.Payload)", len(newMapIsNew.Payload), "len(newMapIsNotNew.Payload)", len(newMapIsNotNew.Payload))
	if newMapIsNewSuccess && newMapIsNotNewSuccess {
		time.Sleep(2 * time.Second) // ensures we do not run in the discord throttling
		*content = fmt.Sprintf("%d new scores have been submitted successfully for culvert week of %s.", len(newMapIsNew.Payload)+len(newMapIsNotNew.Payload), culvertDateStr)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: content,
		})
		return
	}

	// not normal past this point
	log.Println("submitScores: One or both of the score submissions failed. submit-new-scores", newMapIsNewSuccess, "update-existing-scores", newMapIsNotNewSuccess)

	*content = "Scores submission failed. See server logs for details."
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: content,
	})
}

func sortedScoreEntries(scores map[string]int) []helpers.ScoreEntry {
	names := make([]string, 0, len(scores))
	for name := range scores {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]helpers.ScoreEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, helpers.ScoreEntry{Name: name, Score: scores[name]})
	}
	return entries
}
