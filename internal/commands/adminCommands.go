package commands

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// editableSettingKeys returns the /config-editable settings in stable order.
func editableSettingKeys() []string {
	keys := []string{}
	for name, k := range apiredis.KeysMap {
		if k.EditableType != apiredis.EditableTypeNone {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	return keys
}

// ConfigSettingChoices builds the slash command choices for /config.
func ConfigSettingChoices() []*discordgo.ApplicationCommandOptionChoice {
	choices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, name := range editableSettingKeys() {
		label := name
		if d := apiredis.GetHumanReadableDescriptions(apiredis.KeysMap[name]); d != nil {
			label = d.Name
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: label, Value: name})
	}
	return choices
}

// configCommand shows and edits the bot settings (redis-backed), replacing the
// old web admin panel. Admin only.
func configCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !isAdmin(i) {
		registerReply(s, i, "Only server admins can use `/config`.")
		return
	}
	r := deferReply(s, i, true)

	settingName := ""
	value := ""
	valueSet := false
	channelVal := ""
	roleVal := ""
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "setting":
			settingName = v.StringValue()
		case "value":
			value = strings.TrimSpace(v.StringValue())
			valueSet = true
		case "channel":
			if c := v.ChannelValue(nil); c != nil {
				channelVal = c.ID
			}
		case "role":
			if r := v.RoleValue(nil, ""); r != nil {
				roleVal = r.ID
			}
		}
	}

	// No setting: list everything.
	if settingName == "" {
		out := "**Bot settings** (set with `/config setting:<name> value:<value>`; channel/role settings also accept the `channel:`/`role:` options)\n"
		for _, name := range editableSettingKeys() {
			k := apiredis.KeysMap[name]
			cur := k.GetWithDefault(apiredis.RedisDB, "")
			display := "_(not set)_"
			if cur != "" {
				display = "`" + cur + "`"
				switch k.EditableType {
				case apiredis.EditableTypeDiscordChannel:
					display = "<#" + cur + ">"
				case apiredis.EditableTypeDiscordRole:
					parts := []string{}
					for _, id := range strings.Split(cur, ",") {
						if id = strings.TrimSpace(id); id != "" {
							parts = append(parts, "<@&"+id+">")
						}
					}
					display = strings.Join(parts, ", ")
				}
			}
			label := name
			if d := apiredis.GetHumanReadableDescriptions(k); d != nil {
				label = d.Name
			}
			out += "\n**" + label + "** (`" + name + "`): " + display
		}
		r.EditChunked(out)
		return
	}

	k, ok := apiredis.KeysMap[settingName]
	if !ok || k.EditableType == apiredis.EditableTypeNone {
		r.Edit("Unknown setting.")
		return
	}
	desc := apiredis.GetHumanReadableDescriptions(k)
	label := settingName
	if desc != nil {
		label = desc.Name
	}

	// Channel/role options are a convenience for the corresponding types.
	if channelVal != "" && k.EditableType == apiredis.EditableTypeDiscordChannel {
		value = channelVal
		valueSet = true
	}
	if roleVal != "" && k.EditableType == apiredis.EditableTypeDiscordRole {
		if k.Multiple {
			if cur := k.GetWithDefault(apiredis.RedisDB, ""); cur != "" && value == "" {
				value = cur + "," + roleVal
			} else if value == "" {
				value = roleVal
			}
		} else {
			value = roleVal
		}
		valueSet = true
	}

	if !valueSet {
		cur := k.GetWithDefault(apiredis.RedisDB, "")
		hint := ""
		if desc != nil {
			hint = "\n" + desc.Description
		}
		r.Edit("**" + label + "** is currently: `" + cur + "`" + hint)
		return
	}

	// Validate by type.
	switch k.EditableType {
	case apiredis.EditableTypeBool:
		if value != "true" && value != "false" && value != "" {
			r.Edit("Value must be `true` or `false`.")
			return
		}
	case apiredis.EditableTypeFloat64:
		if value != "" {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				r.Edit("Value must be a number.")
				return
			}
		}
	case apiredis.EditableTypeSelection:
		valid := false
		for _, sel := range apiredis.EditableSelectionsMap[k] {
			if value == sel {
				valid = true
				break
			}
		}
		if !valid {
			r.Edit("Value must be one of: `" + strings.Join(apiredis.EditableSelectionsMap[k], "`, `") + "`")
			return
		}
	case apiredis.EditableTypeDiscordChannel, apiredis.EditableTypeDiscordRole:
		cleaned := []string{}
		for _, id := range strings.Split(value, ",") {
			id = strings.TrimSpace(id)
			id = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(id, "<#"), "<@&"), "<"), ">")
			if id == "" {
				continue
			}
			for _, ch := range id {
				if ch < '0' || ch > '9' {
					r.Edit("Value must be a channel/role id (or mention), comma separated when multiple.")
					return
				}
			}
			cleaned = append(cleaned, id)
		}
		if len(cleaned) > 1 && !k.Multiple {
			r.Edit("This setting takes a single id.")
			return
		}
		value = strings.Join(cleaned, ",")
	}

	if err := k.Set(apiredis.RedisDB, value); err != nil {
		log.Println("config: set failed:", err)
		r.Edit("Failed to save the setting. Please try again later.")
		return
	}
	if value == "" {
		r.Edit("**" + label + "** cleared.")
		return
	}
	r.Edit("**" + label + "** set to `" + value + "`. :white_check_mark:")
}

func chunkText(content string, limit int) []string {
	if len(content) <= limit {
		return []string{content}
	}
	chunks := []string{}
	cur := ""
	for _, line := range strings.Split(content, "\n") {
		if len(cur)+len(line)+1 > limit && cur != "" {
			chunks = append(chunks, cur)
			cur = ""
		}
		if cur != "" {
			cur += "\n"
		}
		cur += line
	}
	if cur != "" {
		chunks = append(chunks, cur)
	}
	return chunks
}

// untrackCharacter unlinks a character and hides it from active tracking.
func untrackCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, true)
	name := ""
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "character-name" {
			name = strings.TrimSpace(v.StringValue())
		}
	}
	res, err := db.DB.Exec(`UPDATE characters SET discord_user_id = '1' WHERE LOWER(maple_character_name) = LOWER($1)`, name)
	if err != nil {
		log.Println("untrack-character:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		r.Edit("No tracked character named `" + name + "` found.")
		return
	}
	r.Edit("`" + name + "` is no longer tracked. Its historical scores are kept and it can be re-tracked anytime.")
}

// renameCharacter renames a tracked character (post name-change).
func renameCharacterCmd(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, true)
	oldName := ""
	newName := ""
	skipNameCheck := false
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "character-name":
			oldName = strings.TrimSpace(v.StringValue())
		case "new-name":
			newName = strings.TrimSpace(v.StringValue())
		case "skip-name-check":
			skipNameCheck = v.BoolValue()
		}
	}

	if !skipNameCheck {
		charData, err := apihelpers.FetchCharacterData(newName, apiredis.OPTIONAL_CONF_MAPLE_REGION.GetWithDefault(apiredis.RedisDB, "na"))
		if err != nil || charData == nil {
			r.Edit("I couldn't find `" + newName + "` on the official MapleStory rankings. Double-check the spelling, or add `skip-name-check:True`.")
			return
		}
		newName = charData.CharacterName
	}

	var clash int64
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM characters WHERE LOWER(maple_character_name) = LOWER($1)`, newName).Scan(&clash); err == nil && clash > 0 {
		r.Edit("A character named `" + newName + "` already exists.")
		return
	}
	res, err := db.DB.Exec(`UPDATE characters SET maple_character_name = $1 WHERE LOWER(maple_character_name) = LOWER($2)`, newName, oldName)
	if err != nil {
		log.Println("rename-character:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		r.Edit("No tracked character named `" + oldName + "` found.")
		return
	}
	r.Edit("Renamed `" + oldName + "` to `" + newName + "`. All history moved with it. :white_check_mark:")
}

// setScore manually sets one character's score for a week (admin correction).
func setScore(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, true)
	name := ""
	score := -1
	dateStr := ""
	createIfMissing := false
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "character-name":
			name = strings.TrimSpace(v.StringValue())
		case "score":
			score = int(v.IntValue())
		case "date":
			dateStr = strings.TrimSpace(v.StringValue())
		case "create-if-missing":
			createIfMissing = v.BoolValue()
		}
	}

	week := helpers.GetCulvertResetDate(time.Now())
	if dateStr != "" {
		d, err := time.Parse(time.DateOnly, dateStr)
		if err != nil {
			r.Edit("Invalid date format, should be YYYY-MM-DD")
			return
		}
		if d.Weekday() != helpers.GetCulvertResetDay(d) {
			r.Edit("The provided date is not a culvert reset day (Wednesday).")
			return
		}
		week = d
	}
	weekStr := week.Format(time.DateOnly)

	created := false
	var charID int64
	var realName string
	err := db.DB.QueryRow(`SELECT id, maple_character_name FROM characters WHERE LOWER(maple_character_name) = LOWER($1)`, name).Scan(&charID, &realName)
	if err == sql.ErrNoRows && createIfMissing {
		createdNames, ids, terr := helpers.TrackCharacters(db.DB, []string{name})
		if terr != nil {
			log.Println("set-score: create-if-missing failed:", terr)
			r.Edit("Something went wrong tracking the character. Please try again later.")
			return
		}
		charID, realName = ids[name], name
		created = len(createdNames) > 0
	} else if err != nil {
		r.Edit("No tracked character named `" + name + "` found. Add `create-if-missing:True` to track it now, or bulk-track with `/track-characters names:" + name + "`.")
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO character_culvert_scores (character_id, culvert_date, score) VALUES ($1, $2, $3)
		 ON CONFLICT (culvert_date, character_id) DO UPDATE SET score = $3`,
		charID, weekStr, score); err != nil {
		log.Println("set-score:", err)
		r.Edit("Something went wrong saving the score. Please try again later.")
		return
	}
	msg := "Set `" + realName + "` to **" + apihelpers.FormatThousands(score) + "** for week " + weekStr + ". :white_check_mark:"
	if created {
		msg += "\nTracked `" + realName + "` as a new character (members can `/register` to claim it)."
	}
	msg += announcementStatusLine(apihelpers.AnnounceSubmission(s, db.DB, apiredis.RedisDB, week, []int64{charID}))
	r.Edit(msg)
}

// resetData wipes all tracked characters, scores and weekly announcement
// records so the guild can start from scratch. Admin only, with a typed
// confirmation. Settings (/config) are kept.
func resetData(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !isAdmin(i) {
		registerReply(s, i, "Only server admins can use `/reset`.")
		return
	}
	r := deferReply(s, i, true)
	confirm := ""
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "confirm" {
			confirm = strings.TrimSpace(v.StringValue())
		}
	}
	if confirm != "DELETE EVERYTHING" {
		r.Edit("This permanently deletes **all** tracked characters, **all** culvert scores, and the bot's weekly announcement records. Settings are kept.\nTo proceed, run `/reset confirm:DELETE EVERYTHING` (typed exactly).")
		return
	}

	var chars, scores int64
	db.DB.QueryRow(`SELECT COUNT(*) FROM characters`).Scan(&chars)
	db.DB.QueryRow(`SELECT COUNT(*) FROM character_culvert_scores`).Scan(&scores)

	if _, err := db.DB.Exec(`TRUNCATE character_culvert_scores, weekly_announcements, characters RESTART IDENTITY CASCADE`); err != nil {
		log.Println("reset:", err)
		r.Edit("Something went wrong wiping the data. Nothing may have been deleted; check the server logs.")
		return
	}

	r.Edit(fmt.Sprintf("Wiped **%d characters** and **%d scores**. The tracker is now a blank slate. :broom:\nOld weekly announcement messages in Discord are not deleted automatically - remove them manually if you want.\nStart fresh with `/register` and your next screenshot submission.", chars, scores))
}
