package commands

//lint:file-ignore ST1001 Dot imports by jet
import (
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	"github.com/jedib0t/go-pretty/v6/table"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// emptyWeekMessage is the /culvert-board empty state.
func emptyWeekMessage(weekLabel string) string {
	return "No scores recorded for " + weekLabel + " yet.\nSubmitters: post a screenshot and right click it -> Apps -> **Submit Scores**."
}

// noTrackedCharactersMessage is the shared empty-roster state.
const noTrackedCharactersMessage = "No characters are tracked yet. Members can `/register`, or a submitter can just right click a screenshot message -> Apps -> **Submit Scores**."

// culvertBoard renders the guild's score-descending table for one culvert
// week (default: the current one). Public reply, text only.
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

	// Population aligned with the tracked roster (GetActiveCharacters).
	chars, _, err := cmdhelpers.GetActiveCharactersWithMeta(apiredis.RedisDB, db.DB, tenant)
	if err != nil {
		log.Println("culvert-board: get active characters:", err)
		r.Edit("Failed to retrieve characters' data from database. See server logs.")
		return
	}
	if len(*chars) == 0 {
		r.Edit(noTrackedCharactersMessage)
		return
	}
	charIDs := make([]Expression, 0, len(*chars))
	for _, c := range *chars {
		charIDs = append(charIDs, Int64(c.ID))
	}

	// Fixed ordering: score descending, names break ties.
	stmt := SELECT(Characters.MapleCharacterName.AS("maple_character_name"), CharacterCulvertScores.Score.AS("score")).
		FROM(CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).
		WHERE(CharacterCulvertScores.CulvertDate.EQ(DateT(week)).AND(CharacterCulvertScores.CharacterID.IN(charIDs...))).
		ORDER_BY(CharacterCulvertScores.Score.DESC(), Characters.MapleCharacterName.ASC())

	dest := []struct {
		Score              int32
		MapleCharacterName string
	}{}
	if err := stmt.Query(db.DB, &dest); err != nil {
		log.Println("culvert-board:", err)
		r.Edit("Failed to retrieve characters' data from database. See server logs.")
		return
	}

	if len(dest) == 0 {
		r.Edit(emptyWeekMessage(weekLabel))
		return
	}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Pos", "Character", "Score"})
	for idx, row := range dest {
		t.AppendRow(table.Row{idx + 1, row.MapleCharacterName, row.Score})
	}

	content := "Culvert board for " + weekLabel + "\n" +
		fmt.Sprintf("%d of %d tracked characters have a score this week.", len(dest), len(*chars))
	r.EditWithTable(content, t.Render())
}
