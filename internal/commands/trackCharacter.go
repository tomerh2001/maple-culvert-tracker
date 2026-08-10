package commands

//lint:file-ignore ST1001 Dot imports by jet

import (
	"errors"
	"strings"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	"github.com/go-jet/jet/v2/qrm"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func trackCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}

	// Parse discord param character-name
	discordUserID := "2"
	characterName := ""
	skipNameCheck := false
	options := i.ApplicationCommandData().Options
	for _, v := range options {
		if v.Name == "character-name" {
			characterName = strings.Trim(strings.ToLower(v.StringValue()), " ")
		}
		if v.Name == "discord-user-id" {
			// User-type option: the client resolves the @mention to a user.
			if u := v.UserValue(nil); u != nil {
				discordUserID = u.ID
			}
		}
		if v.Name == "skip-name-check" {
			skipNameCheck = v.BoolValue()
		}
	}

	//  Validate maple character name
	if !skipNameCheck {
		charData, err := helpers.FetchCharacterData(characterName, apiredis.OPTIONAL_CONF_MAPLE_REGION.GetWithDefault(apiredis.RedisDB, "na"))
		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Data: &discordgo.InteractionResponseData{
					Content: "Failed to find character name in maple  rankings",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		characterName = charData.CharacterName
	}

	// Validate discord user id
	result, err := helpers.FetchAllMembers(s)
	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed to fetch members",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var discordUser *data.WebGuildMember
	for _, v := range result {
		if v.DiscordUserID == discordUserID || v.DiscordUsername == discordUserID {
			// Do not compare with global name or nickname as those are not unique
			discordUser = &v
			break
		}
	}

	if discordUser == nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed to find discord user",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Link character

	// Check for existing character
	existingCharacter := struct {
		ID int64
	}{}
	stmt := SELECT(Characters.ID.AS("id")).FROM(Characters).WHERE(LOWER(Characters.MapleCharacterName).EQ(String(strings.ToLower(characterName))))

	err = stmt.Query(db.DB, &existingCharacter)
	if err != nil && !errors.Is(err, qrm.ErrNoRows) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Track character failed database search. This is not normal!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// INSERT or UPDATE character with discord user id
	if existingCharacter.ID == 0 {
		// INSERT
		stmt := Characters.INSERT(Characters.DiscordUserID, Characters.MapleCharacterName).
			VALUES(String(discordUser.DiscordUserID), String(characterName))
		_, err = stmt.Exec(db.DB)
		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Track character failed database INSERT. This is not normal!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Character `" + characterName + "` tracked for `" + discordUser.DiscordGlobalName + "`!",
			},
		})
	} else {
		// UPDATE
		stmt := Characters.UPDATE(Characters.DiscordUserID).
			SET(String(discordUser.DiscordUserID)).
			WHERE(Characters.ID.EQ(Int64(existingCharacter.ID)))
		_, err = stmt.Exec(db.DB)
		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Track character failed database UPDATE. This is not normal!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Character `" + characterName + "` tracked for `" + discordUser.DiscordUsername + "`!",
			},
		})
	}
}
