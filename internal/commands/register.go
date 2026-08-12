package commands

import (
	"database/sql"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// registerCharacter links a MapleStory character to a Discord account:
// your own by default, or someone else's via user:@member (submitters only).
func registerCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	characterName := ""
	callerID := i.Member.User.ID
	targetID := callerID
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "name":
			characterName = strings.TrimSpace(v.StringValue())
		case "user":
			if u := v.UserValue(nil); u != nil {
				targetID = u.ID
			}
		}
	}
	if characterName == "" {
		r.Edit("Please provide the character name: `/register name:YourCharacter`")
		return
	}

	forOther := targetID != callerID
	if forOther && !canSubmitScores(i) {
		r.Edit("Registering a character for someone else needs submitter permissions. They can register themselves with `/register name:TheirCharacter`.")
		return
	}

	// Normalise capitalisation against the official rankings when possible.
	// The check is ADVISORY: a lookup failure or miss never blocks
	// registration, it only adds a warning to the receipt.
	warning := ""
	charData, err := helpers.FetchCharacterData(characterName, apiredis.OPTIONAL_CONF_MAPLE_REGION.For(tenant).GetWithDefault(apiredis.RedisDB, "na"))
	if err != nil || charData == nil {
		warning = "\n:warning: I couldn't verify `" + characterName + "` against the official rankings - double-check the spelling (`/unregister` + `/register` fixes typos)."
	} else {
		characterName = charData.CharacterName
	}

	who := "you"
	if forOther {
		who = "<@" + targetID + ">"
	}

	var existingID int64
	var existingOwner string
	err = db.DB.QueryRow(
		`SELECT id, discord_user_id FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND guild_id = $2`,
		characterName, tenant).Scan(&existingID, &existingOwner)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.DB.Exec(
			`INSERT INTO characters (maple_character_name, discord_user_id, guild_id) VALUES ($1, $2, $3)`,
			characterName, targetID, tenant); err != nil {
			log.Println("register: insert failed:", err)
			r.Edit("Something went wrong saving the character. Please try again later.")
			return
		}
	case err != nil:
		log.Println("register: lookup failed:", err)
		r.Edit("Something went wrong saving the character. Please try again later.")
		return
	case existingOwner == targetID:
		r.Edit("`" + characterName + "` is already registered to " + who + ". Nothing to do!" + warning)
		return
	case existingOwner != "1" && existingOwner != "2" && existingOwner != "" && !canSubmitScores(i):
		r.Edit("`" + characterName + "` is already registered to <@" + existingOwner + ">. If that's wrong, ask an admin or submitter to move it with `/register name:" + characterName + " user:@the-right-person`.")
		return
	default:
		// Unlinked, or a submitter/admin relinking it to the target.
		if _, err := db.DB.Exec(
			`UPDATE characters SET discord_user_id = $1 WHERE id = $2`,
			targetID, existingID); err != nil {
			log.Println("register: update failed:", err)
			r.Edit("Something went wrong saving the character. Please try again later.")
			return
		}
	}

	if forOther {
		r.Edit("Done! `" + characterName + "` is now registered to " + who + ". :tada:" + warning)
		return
	}
	r.Edit("Done! `" + characterName + "` is now registered to you. :tada:" + warning + "\nTry `/culvert` to see your progression once your scores are in.")
}

// unregisterCharacter untracks a character by name, or ALL characters
// registered to a member (user option / the invoker by default). History is
// always kept (rows keep their scores; discord_user_id becomes '1').
func unregisterCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	name := ""
	callerID := i.Member.User.ID
	targetID := callerID
	userGiven := false
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "name":
			name = strings.TrimSpace(v.StringValue())
		case "user":
			if u := v.UserValue(nil); u != nil {
				targetID = u.ID
				userGiven = true
			}
		}
	}

	// name given -> untrack that one character.
	if name != "" {
		var charID int64
		var owner, realName string
		err := db.DB.QueryRow(
			`SELECT id, discord_user_id, maple_character_name FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND discord_user_id != '1' AND guild_id = $2`,
			name, tenant).Scan(&charID, &owner, &realName)
		if err == sql.ErrNoRows {
			r.Edit("No tracked character named `" + name + "` found - nothing to do.")
			return
		}
		if err != nil {
			log.Println("unregister: lookup failed:", err)
			r.Edit("Something went wrong. Please try again later.")
			return
		}
		if owner != callerID && !canSubmitScores(i) {
			r.Edit("`" + realName + "` isn't registered to you - untracking someone else's character needs submitter permissions.")
			return
		}
		if _, err := db.DB.Exec(`UPDATE characters SET discord_user_id = '1' WHERE id = $1`, charID); err != nil {
			log.Println("unregister: update failed:", err)
			r.Edit("Something went wrong. Please try again later.")
			return
		}
		r.Edit("`" + realName + "` is no longer tracked. Its history is kept and it can be re-registered anytime.")
		return
	}

	// No name -> untrack ALL characters registered to the target member.
	if userGiven && targetID != callerID && !canSubmitScores(i) {
		r.Edit("Unregistering someone else's characters needs submitter permissions.")
		return
	}
	rows, err := db.DB.Query(
		`SELECT maple_character_name FROM characters WHERE discord_user_id = $1 AND guild_id = $2 ORDER BY maple_character_name`, targetID, tenant)
	if err != nil {
		log.Println("unregister: list failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	rows.Close()

	who := "you"
	if targetID != callerID {
		who = "<@" + targetID + ">"
	}
	if len(names) == 0 {
		r.Edit("No characters are registered to " + who + " - nothing to do.")
		return
	}
	if _, err := db.DB.Exec(`UPDATE characters SET discord_user_id = '1' WHERE discord_user_id = $1 AND guild_id = $2`, targetID, tenant); err != nil {
		log.Println("unregister: bulk update failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	r.Edit("Untracked `" + strings.Join(capList(names, 25), "`, `") + "` (registered to " + who + "). History is kept; `/register` re-links anytime.")
}
