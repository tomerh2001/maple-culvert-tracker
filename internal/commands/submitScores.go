package commands

//lint:file-ignore ST1001 Dot imports by jet

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
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

	r := deferReply(s, i, false)

	culvertDate := helpers.GetCulvertResetDate(time.Now())
	culvertDateStr := culvertDate.Format(time.DateOnly)
	overwriteExisting := false
	zeroMissing := false
	autoTrackNew := true
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
		case "auto-track-new":
			autoTrackNew = v.BoolValue()
		case "date":
			culvertDateStr = strings.Trim(v.StringValue(), " ")
			culvertDate, err = time.Parse(time.DateOnly, culvertDateStr)
			if err != nil {
				r.Edit("Invalid date format provided! Please use YYYY-MM-DD.")
				return
			}
			if culvertDate.Weekday() != helpers.GetCulvertResetDay(time.Now()) {
				r.Edit("The provided date is not a Wednesday! Culvert resets occur on Wednesdays.")
				return
			}
		case "overwrite-existing":
			overwriteExisting = v.BoolValue()
		case "zero-missing":
			zeroMissing = v.BoolValue()
		}
	}

	scores := map[string]int{}
	parseWarnings := ""
	switch {
	case attachment != nil:
		errMsg := scoresFromJSONAttachment(attachment, scores)
		if errMsg != "" {
			r.Edit(errMsg)
			return
		}
	case messageLink != "":
		channelID, messageID, ok := parseMessageLink(messageLink)
		if !ok {
			r.Edit("That doesn't look like a message link. Right click a message -> Copy Message Link and paste it here.")
			return
		}
		msg, err := s.ChannelMessage(channelID, messageID)
		if err != nil {
			r.Edit("Failed to fetch that message. Make sure the link is from this server and I can see the channel.")
			return
		}
		var ok2 bool
		parseWarnings, ok2 = scoresFromMessage(s, r, msg, scores)
		if !ok2 {
			return
		}
	default:
		r.Edit("Provide either a `scores-attachment` file or a `message-link` to a message with screenshots.\nTip: you can also just right click that message -> Apps -> **Submit Scores**.")
		return
	}

	finalizeSubmitScores(s, r, scores, culvertDate, culvertDateStr, overwriteExisting, zeroMissing, autoTrackNew, parseWarnings)
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
// (character name -> score) has been parsed from a source: it plans the
// submission against the tracked roster and existing week data, auto-tracks
// unknown names (when enabled), applies the changes in one transaction and
// replies with a receipt. parseWarnings ("" when clean) carries the source's
// non-fatal parse defects into the receipt.
func finalizeSubmitScores(s *discordgo.Session, r *reply, submitted map[string]int, culvertDate time.Time, culvertDateStr string, overwriteExisting, zeroMissing, autoTrackNew bool, parseWarnings string) {
	if len(submitted) == 0 {
		r.Edit("Nothing was parsed from that input - no changes were made.")
		return
	}

	characters, rosterMeta, err := helpers.GetActiveCharactersWithMeta(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("submitScores: Error querying active characters:", err)
		r.Edit("Internal server error while querying active characters. Please try again later.")
		return
	}

	tracked := []trackedScore{}
	if len(*characters) > 0 {
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
			r.Edit("Internal server error while querying existing scores. Please try again later.")
			return
		}

		for _, v := range trackedCharacterScores {
			t := trackedScore{ID: int64(v.ID), Name: v.MapleCharacterName}
			if v.Score.Valid {
				existing := int(v.Score.Int64)
				t.Existing = &existing
			}
			tracked = append(tracked, t)
		}
	}

	plan := planSubmission(tracked, submitted, overwriteExisting, zeroMissing, autoTrackNew)

	if len(plan.Conflicts) > 0 {
		msg := fmt.Sprintf("%d existing score(s) for %s differ from the submitted values (see conflicts.txt). Set the `overwrite-existing` option to `True` to overwrite them. No changes were made.",
			len(plan.Conflicts), culvertDateStr)
		r.Edit(msg, &discordgo.File{
			Name:        "conflicts.txt",
			ContentType: "text/plain",
			Reader:      strings.NewReader(formatConflictsTable(plan.Conflicts)),
		})
		return
	}

	autoTracked := []string{}
	if len(plan.AutoTrack) > 0 {
		names := make([]string, 0, len(plan.AutoTrack))
		for _, e := range plan.AutoTrack {
			names = append(names, e.Name)
		}
		created, ids, err := helpers.TrackCharacters(db.DB, names)
		if err != nil {
			log.Println("submitScores: auto-track failed:", err)
			r.Edit("Score submission failed while tracking new characters - no scores were written. See server logs for details.")
			return
		}
		autoTracked = created
		for _, e := range plan.AutoTrack {
			id, ok := ids[e.Name]
			if !ok {
				log.Println("submitScores: auto-tracked character has no id:", e.Name)
				continue
			}
			plan.Changes = append(plan.Changes, helpers.ScoreChange{CharacterID: id, Score: e.Score})
			plan.New++
		}
	}

	if err := helpers.UpsertCulvertScores(db.DB, culvertDate, plan.Changes); err != nil {
		log.Println("submitScores: upsert failed:", err)
		r.Edit("Score submission failed while writing to the database - no scores were written. See server logs for details.")
		return
	}

	receipt := fmt.Sprintf("Submitted %d scores (%d new, %d overwritten, %d zero-filled) for week %s.",
		len(plan.Changes), plan.New, plan.Overwritten, plan.ZeroFilled, culvertDateStr)
	if len(autoTracked) > 0 {
		receipt += fmt.Sprintf("\nAuto-tracked %d new character(s): %s (members can `/register` to claim them)",
			len(autoTracked), "`"+strings.Join(capList(autoTracked, 20), "`, `")+"`")
	}
	if len(plan.Skipped) > 0 {
		names := make([]string, 0, len(plan.Skipped))
		for _, e := range plan.Skipped {
			names = append(names, e.Name)
		}
		receipt += fmt.Sprintf("\nSkipped %d untracked name(s): %s\nTrack them with: `/track-characters names:%s`",
			len(names), "`"+strings.Join(capList(names, 20), "`, `")+"`", strings.Join(names, ","))
	}

	if len(plan.Changes) > 0 {
		changedIDs := make([]int64, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			changedIDs = append(changedIDs, c.CharacterID)
		}
		receipt += announcementStatusLine(apihelpers.AnnounceSubmission(s, db.DB, apiredis.RedisDB, culvertDate, changedIDs))
	}
	receipt += parseWarnings
	receipt += rosterMeta.StalenessWarning()

	r.Edit(receipt)
}

// announcementStatusLine renders the weekly-announcement outcome for a receipt.
func announcementStatusLine(err error) string {
	switch {
	case err == nil:
		return "\nWeekly announcement updated."
	case errors.Is(err, apihelpers.ErrNoWeeklyChannel):
		return "\n:warning: Weekly announcement skipped - no weekly channel configured (`/config`)."
	default:
		return "\n:warning: Weekly announcement failed: " + err.Error()
	}
}
