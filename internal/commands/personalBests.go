package commands

import (
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func personalBests(s *discordgo.Session, i *discordgo.InteractionCreate) {
	dest, err := helpers.LoadPersonalBestRankMetrics(db.DB, apiredis.RedisDB)
	if err != nil {
		log.Println("personalBests: load metrics failed", err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "personal-bests command failed getting data from db. See server logs.",
			},
		})
		return
	}

	if len(dest) == 0 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "No personal best scores found.",
			},
		})
		return
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Here are the up-to-date rankings!",
			Files:   []*discordgo.File{{Name: "message.txt", Reader: strings.NewReader(helpers.FormatPersonalBestsTable(dest))}},
		},
	})
}
