package commands

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func listCharacters(s *discordgo.Session, i *discordgo.InteractionCreate) {
	characters := []string{}
	charactersFullData, err := helpers.GetActiveCharacters(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("Error fetching active characters:", err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Sorry, I encountered an error while fetching your characters. Please try again later.",
			},
		})
		return
	}
	for _, v := range *charactersFullData {
		characters = append(characters, v.MapleCharacterName)
	}

	jsonCharacters, _ := json.Marshal(characters)
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Here you go! Copy the contents of the attached file and paste it to the OCR app.",
			Files:   []*discordgo.File{{Name: "characters.json", Reader: strings.NewReader(string(jsonCharacters))}},
		},
	})
}
