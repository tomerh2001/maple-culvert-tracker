package commands

import (
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// emptyWeekMessage is the /culvert-all empty state.
func emptyWeekMessage(weekLabel string) string {
	return "No scores recorded for " + weekLabel + " yet.\nSubmitters: post a screenshot and right click it -> Apps -> **Submit Scores**."
}

// noTrackedCharactersMessage is the shared empty-roster state.
const noTrackedCharactersMessage = "No characters are tracked yet. Members can `/register`, or a submitter can just right click a screenshot message -> Apps -> **Submit Scores**."

// culvertBoard renders the guild's ranked board for one culvert week
// (default: the current one) as the shared week embed - the same look as the
// weekly thread table. Public reply.
func culvertBoard(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)
	tenant := tenantOf(i)

	week := cmdhelpers.CurrentCulvertWeek(time.Now())
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "date" {
			d, err := parseFlexibleDate(v.StringValue())
			if err != nil {
				r.Edit(badDateMessage)
				return
			}
			// An explicit date names a week LABEL.
			week = cmdhelpers.GetCulvertResetDate(d)
		}
	}

	if week.After(time.Now()) {
		r.Edit("Invalid date, cannot be in the future")
		return
	}
	weekLabel := cmdhelpers.FormatWeekLabel(week, time.Now())

	embed, rowCount, err := apihelpers.WeekTableEmbed(db.DB, apiredis.RedisDB, tenant, "Culvert - "+weekLabel, week)
	if err != nil {
		log.Println("culvert-all:", err)
		r.Edit("Failed to retrieve characters' data from database. See server logs.")
		return
	}
	if rowCount == 0 {
		chars, cerr := cmdhelpers.GetActiveCharacters(apiredis.RedisDB, db.DB, tenant)
		if cerr == nil && len(*chars) == 0 {
			r.Edit(noTrackedCharactersMessage)
			return
		}
		r.Edit(emptyWeekMessage(weekLabel))
		return
	}
	r.EditData(&discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}
