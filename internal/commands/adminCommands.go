package commands

import (
	"database/sql"
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
// old web admin panel. Admin only. The value parser cleans <#id>/<@&id>
// mentions, so pasting a channel or role mention as the value just works.
func configCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !isAdmin(i) {
		registerReply(s, i, "Only server admins can use `/config`.")
		return
	}
	r := deferReply(s, i, true)
	tenant := tenantOf(i)
	// settingScope picks a key's key-space: per-guild settings (the weekly
	// channel) are scoped to THIS server so each server in a shared tenant
	// keeps its own; everything else is tenant-shared.
	settingScope := func(perGuild bool) string {
		if perGuild {
			return i.GuildID
		}
		return tenant
	}

	settingName := ""
	value := ""
	valueSet := false
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "setting":
			settingName = v.StringValue()
		case "value":
			value = strings.TrimSpace(v.StringValue())
			valueSet = true
		}
	}

	// No setting: list everything.
	if settingName == "" {
		out := "**Bot settings** (set with `/config setting:<name> value:<value>` - `#channel`/`@role` mentions and comma separated id lists work as values)\n"
		for _, name := range editableSettingKeys() {
			k := apiredis.KeysMap[name]
			cur := k.For(settingScope(k.IsPerGuild())).GetWithDefault(apiredis.RedisDB, "")
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

	if !valueSet {
		cur := k.For(settingScope(k.IsPerGuild())).GetWithDefault(apiredis.RedisDB, "")
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

	if err := k.For(settingScope(k.IsPerGuild())).Set(apiredis.RedisDB, value); err != nil {
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

// setCulvert manually sets one character's culvert score for a week (the
// correction tool). Unknown names are ALWAYS auto-tracked (unlinked, '2') -
// canonicalized against the official rankings first - with a receipt note.
func setCulvert(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, true)
	tenant := tenantOf(i)
	name := ""
	score := -1
	dateStr := ""
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "name":
			name = strings.TrimSpace(v.StringValue())
		case "score":
			score = int(v.IntValue())
		case "date":
			dateStr = strings.TrimSpace(v.StringValue())
		}
	}

	week := helpers.CurrentCulvertWeek(time.Now())
	if dateStr != "" {
		d, err := parseFlexibleDate(dateStr)
		if err != nil {
			r.Edit(badDateMessage)
			return
		}
		// An explicit date names a week LABEL - normalize to its week key.
		week = helpers.GetCulvertResetDate(d)
	}
	weekStr := week.Format(time.DateOnly)
	weekLabel := helpers.FormatWeekLabel(week, time.Now())

	note := ""
	var charID int64
	var realName string
	err := db.DB.QueryRow(`SELECT id, maple_character_name FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND guild_id = $2`, name, tenant).Scan(&charID, &realName)
	if err == sql.ErrNoRows {
		// Unknown name: canonicalize against the rankings, then auto-track.
		canonical, verified := canonicalizeName(name, rankingsLookup(tenant))
		if canonical != name {
			// The canonical spelling may already be tracked (l/I mixups).
			err = db.DB.QueryRow(`SELECT id, maple_character_name FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND guild_id = $2`, canonical, tenant).Scan(&charID, &realName)
			if err == nil {
				note = "\nMatched `" + name + "` to the tracked character `" + realName + "` via the official rankings."
			}
		}
		if charID == 0 {
			_, ids, terr := helpers.TrackCharacters(db.DB, tenant, []string{canonical})
			if terr != nil {
				log.Println("set-culvert: auto-track failed:", terr)
				r.Edit("Something went wrong tracking the character. Please try again later.")
				return
			}
			charID, realName = ids[canonical], canonical
			note = "\nTracked `" + realName + "` as a new character (members can `/register` to claim it)."
			if !verified {
				note += "\n:warning: Spelling unverified - `" + realName + "` wasn't found on the official rankings."
			}
		}
	} else if err != nil {
		log.Println("set-culvert: lookup failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO character_culvert_scores (character_id, culvert_date, score, updated_at) VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (culvert_date, character_id) DO UPDATE SET score = $3, updated_at = NOW()`,
		charID, weekStr, score); err != nil {
		log.Println("set-culvert:", err)
		r.Edit("Something went wrong saving the score. Please try again later.")
		return
	}
	msg := "Set `" + realName + "` to **" + apihelpers.FormatThousands(score) + "** for " + weekLabel + ". :white_check_mark:" + note
	msg += announcementStatusLine(apihelpers.AnnounceSubmission(s, db.DB, apiredis.RedisDB, tenant, week, []int64{charID}))
	r.Edit(msg)
}
