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

func registerReply(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// registerCharacter links a MapleStory character to a Discord account:
// your own by default, or someone else's via user:@member (submitters only).
func registerCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	characterName := ""
	skipNameCheck := false
	callerID := i.Member.User.ID
	targetID := callerID
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "character-name":
			characterName = strings.TrimSpace(v.StringValue())
		case "user":
			if u := v.UserValue(nil); u != nil {
				targetID = u.ID
			}
		case "skip-name-check":
			skipNameCheck = v.BoolValue()
		}
	}
	if characterName == "" {
		registerReply(s, i, "Please provide the character name: `/register character-name:YourCharacter`")
		return
	}

	forOther := targetID != callerID
	if forOther && !canSubmitScores(i) {
		registerReply(s, i, "Registering a character for someone else needs submitter permissions. They can register themselves with `/register character-name:TheirCharacter`.")
		return
	}

	// Normalise capitalisation against the official rankings when possible.
	if !skipNameCheck {
		charData, err := helpers.FetchCharacterData(characterName, apiredis.OPTIONAL_CONF_MAPLE_REGION.GetWithDefault(apiredis.RedisDB, "na"))
		if err != nil || charData == nil {
			registerReply(s, i, "I couldn't find `"+characterName+"` on the official MapleStory rankings. Double-check the spelling and try again. (If the rankings are just outdated, add `skip-name-check:True`.)")
			return
		}
		characterName = charData.CharacterName
	}

	who := "you"
	if forOther {
		who = "<@" + targetID + ">"
	}

	var existingID int64
	var existingOwner string
	err := db.DB.QueryRow(
		`SELECT id, discord_user_id FROM characters WHERE LOWER(maple_character_name) = LOWER($1)`,
		characterName).Scan(&existingID, &existingOwner)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.DB.Exec(
			`INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, $2)`,
			characterName, targetID); err != nil {
			log.Println("register: insert failed:", err)
			registerReply(s, i, "Something went wrong saving the character. Please try again later.")
			return
		}
	case err != nil:
		log.Println("register: lookup failed:", err)
		registerReply(s, i, "Something went wrong saving the character. Please try again later.")
		return
	case existingOwner == targetID:
		registerReply(s, i, "`"+characterName+"` is already registered to "+who+". Nothing to do!")
		return
	case existingOwner != "1" && existingOwner != "2" && existingOwner != "" && !canSubmitScores(i):
		registerReply(s, i, "`"+characterName+"` is already registered to <@"+existingOwner+">. If that's wrong, ask an admin or submitter to move it with `/register character-name:"+characterName+" user:@the-right-person`.")
		return
	default:
		// Unlinked, or a submitter/admin relinking it to the target.
		if _, err := db.DB.Exec(
			`UPDATE characters SET discord_user_id = $1 WHERE id = $2`,
			targetID, existingID); err != nil {
			log.Println("register: update failed:", err)
			registerReply(s, i, "Something went wrong saving the character. Please try again later.")
			return
		}
	}

	if forOther {
		registerReply(s, i, "Done! `"+characterName+"` is now registered to "+who+". :tada:")
		return
	}
	registerReply(s, i, "Done! `"+characterName+"` is now registered to you. :tada:\nTry `/culvert` to see your progression once your scores are in.")
}
