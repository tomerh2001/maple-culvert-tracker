package commands

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func listCharacters(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)

	characters := []string{}
	charactersFullData, meta, err := helpers.GetActiveCharactersWithMeta(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("Error fetching active characters:", err)
		r.Edit("Sorry, I encountered an error while fetching your characters. Please try again later.")
		return
	}
	for _, v := range *charactersFullData {
		characters = append(characters, v.MapleCharacterName)
	}

	if len(characters) == 0 {
		r.Edit("No characters are tracked yet. Members can `/register`, submitters can `/track-characters` or just submit a screenshot." + meta.StalenessWarning())
		return
	}

	jsonCharacters, _ := json.Marshal(characters)
	msg := fmt.Sprintf("Tracking %d character(s) - full list attached.", len(characters)) + meta.StalenessWarning()
	r.Edit(msg, &discordgo.File{Name: "characters.json", Reader: strings.NewReader(string(jsonCharacters))})
}
