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
	r := deferReply(s, i, false)

	dest, err := helpers.LoadPersonalBestRankMetrics(db.DB, apiredis.RedisDB)
	if err != nil {
		log.Println("personalBests: load metrics failed", err)
		r.Edit("personal-bests command failed getting data from db. See server logs.")
		return
	}

	if len(dest) == 0 {
		r.Edit("No personal best scores found yet.\nSubmitters: post a screenshot and right click it -> Apps -> **Submit Scores**.")
		return
	}

	r.Edit("Here are the up-to-date rankings!", &discordgo.File{Name: "message.txt", Reader: strings.NewReader(helpers.FormatPersonalBestsTable(dest))})
}
