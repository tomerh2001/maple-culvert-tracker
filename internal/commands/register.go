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

// registerCharacter is the self-service version of track-character: it links
// the caller's own MapleStory character to their Discord account.
func registerCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	characterName := ""
	skipNameCheck := false
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "character-name" {
			characterName = strings.TrimSpace(v.StringValue())
		}
		if v.Name == "skip-name-check" {
			skipNameCheck = v.BoolValue()
		}
	}
	if characterName == "" {
		registerReply(s, i, "Please provide your character name: `/register character-name:YourCharacter`")
		return
	}
	callerID := i.Member.User.ID

	// Normalise capitalisation against the official rankings when possible.
	if !skipNameCheck {
		charData, err := helpers.FetchCharacterData(characterName, apiredis.OPTIONAL_CONF_MAPLE_REGION.GetWithDefault(apiredis.RedisDB, "na"))
		if err != nil || charData == nil {
			registerReply(s, i, "I couldn't find `"+characterName+"` on the official MapleStory rankings. Double-check the spelling and try again. (If the rankings are just outdated, add `skip-name-check:True`.)")
			return
		}
		characterName = charData.CharacterName
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
			characterName, callerID); err != nil {
			log.Println("register: insert failed:", err)
			registerReply(s, i, "Something went wrong saving your character. Please try again later.")
			return
		}
	case err != nil:
		log.Println("register: lookup failed:", err)
		registerReply(s, i, "Something went wrong saving your character. Please try again later.")
		return
	case existingOwner == callerID:
		registerReply(s, i, "`"+characterName+"` is already registered to you. You're all set - try `/culvert`!")
		return
	case existingOwner != "1" && existingOwner != "2" && existingOwner != "":
		registerReply(s, i, "`"+characterName+"` is already registered to <@"+existingOwner+">. If that's wrong, ask an admin to fix it with `/track-character`.")
		return
	default:
		if _, err := db.DB.Exec(
			`UPDATE characters SET discord_user_id = $1 WHERE id = $2`,
			callerID, existingID); err != nil {
			log.Println("register: update failed:", err)
			registerReply(s, i, "Something went wrong saving your character. Please try again later.")
			return
		}
	}

	registerReply(s, i, "Done! `"+characterName+"` is now registered to you. :tada:\nTry `/culvert` to see your progression once your scores are in.")
}
