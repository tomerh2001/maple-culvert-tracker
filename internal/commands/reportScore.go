package commands

import (
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// reportScore lets a member report their own culvert score for the current
// week - only when an admin enabled self reporting via /config.
func reportScore(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if apiredis.OPTIONAL_CONF_ALLOW_SELF_REPORT.GetWithDefault(apiredis.RedisDB, "false") != "true" {
		registerReply(s, i, "Self-reporting is disabled on this server. Scores are submitted by the designated submitters.")
		return
	}

	score := -1
	charName := ""
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "score" {
			score = int(v.IntValue())
		}
		if v.Name == "character-name" {
			charName = strings.TrimSpace(v.StringValue())
		}
	}
	if score < 0 {
		registerReply(s, i, "Please provide your culvert score, e.g. `/report-score score:123456`")
		return
	}

	callerID := i.Member.User.ID
	rows, err := db.DB.Query(
		`SELECT id, maple_character_name FROM characters WHERE discord_user_id = $1 ORDER BY id`, callerID)
	if err != nil {
		log.Println("report-score: character lookup failed:", err)
		registerReply(s, i, "Something went wrong. Please try again later.")
		return
	}
	defer rows.Close()
	type ch struct {
		id   int64
		name string
	}
	chars := []ch{}
	for rows.Next() {
		c := ch{}
		if err := rows.Scan(&c.id, &c.name); err == nil {
			chars = append(chars, c)
		}
	}

	if len(chars) == 0 {
		registerReply(s, i, "You haven't registered a MapleStory character yet!\nType `/register` and enter your character name first, then report your score.")
		return
	}
	target := chars[0]
	if len(chars) > 1 {
		if charName == "" {
			names := make([]string, 0, len(chars))
			for _, c := range chars {
				names = append(names, "`"+c.name+"`")
			}
			registerReply(s, i, "You have multiple characters ("+strings.Join(names, ", ")+"). Add `character-name:` to pick one.")
			return
		}
		found := false
		for _, c := range chars {
			if strings.EqualFold(c.name, charName) {
				target = c
				found = true
				break
			}
		}
		if !found {
			registerReply(s, i, "`"+charName+"` isn't one of your registered characters.")
			return
		}
	}

	week := helpers.GetCulvertResetDate(time.Now())
	weekStr := week.Format(time.DateOnly)
	if _, err := db.DB.Exec(
		`INSERT INTO character_culvert_scores (character_id, culvert_date, score) VALUES ($1, $2, $3)
		 ON CONFLICT (culvert_date, character_id) DO UPDATE SET score = $3`,
		target.id, weekStr, score); err != nil {
		log.Println("report-score: upsert failed:", err)
		registerReply(s, i, "Something went wrong saving your score. Please try again later.")
		return
	}

	go apihelpers.AnnounceSubmission(s, db.DB, apiredis.RedisDB, week, []int64{target.id})
	registerReply(s, i, "Recorded **"+apihelpers.FormatThousands(score)+"** for `"+target.name+"` (week of "+weekStr+"). :white_check_mark:")
}
