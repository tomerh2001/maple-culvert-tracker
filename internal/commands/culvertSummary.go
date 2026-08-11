package commands

//lint:file-ignore ST1001 Dot imports by jet
import (
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	"github.com/jedib0t/go-pretty/v6/table"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// emptyWeekMessage is the shared /leaderboard empty state (table and chart).
func emptyWeekMessage(weekStr string) string {
	return "No scores recorded for the week of " + weekStr + " yet.\nSubmitters: post a screenshot and right click it -> Apps -> **Submit Scores**."
}

func culvertSummary(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)

	options := i.ApplicationCommandData().Options

	date := ""
	// this can only be `score` or `name`
	orderBy := "score"

	for _, v := range options {
		if v.Name == "date" {
			date = v.StringValue()
		}
		if v.Name == "order-by" {
			orderBy = v.StringValue()
		}
	}

	if date == "" {
		date = cmdhelpers.GetCulvertResetDate(time.Now()).Format(time.DateOnly)
	}

	d, err := time.Parse(time.DateOnly, date) // YYYY-MM-DD
	if err != nil {
		r.Edit("Invalid date format, should be YYYY-MM-DD")
		return
	}
	d = cmdhelpers.GetCulvertResetDate(d)

	if d.After(time.Now()) {
		r.Edit("Invalid date, cannot be in the future")
		return
	}
	weekStr := d.Format(time.DateOnly)

	// Population aligned with the tracked roster (GetActiveCharacters).
	chars, _, err := cmdhelpers.GetActiveCharactersWithMeta(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("leaderboard: get active characters:", err)
		r.Edit("Failed to retrieve characters' data from database. See server logs.")
		return
	}
	if len(*chars) == 0 {
		r.Edit("No characters are tracked yet. Members can `/register`, submitters can `/track-characters` or just submit a screenshot.")
		return
	}
	charIDs := make([]Expression, 0, len(*chars))
	for _, c := range *chars {
		charIDs = append(charIDs, Int64(c.ID))
	}

	var orderByClause []OrderByClause = []OrderByClause{CharacterCulvertScores.Score.DESC(), Characters.MapleCharacterName.ASC()}

	// get all rows for the specific date
	stmt := SELECT(Characters.MapleCharacterName.AS("maple_character_name"), CharacterCulvertScores.Score.AS("score")).FROM(CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).WHERE(CharacterCulvertScores.CulvertDate.EQ(DateT(d)).AND(CharacterCulvertScores.CharacterID.IN(charIDs...))).ORDER_BY(orderByClause...)

	dest := []struct {
		Score              int32
		MapleCharacterName string
		pos                int
	}{}

	err = stmt.Query(db.DB, &dest)
	if err != nil {
		log.Println(err)
		r.Edit("Failed to retrieve characters' data from database. See server logs.")
		return
	}

	if len(dest) == 0 {
		r.Edit(emptyWeekMessage(weekStr))
		return
	}

	// set pos
	for i := range dest {
		dest[i].pos = i + 1
	}

	if orderBy == "name" {
		slices.SortFunc(dest, func(a, b struct {
			Score              int32
			MapleCharacterName string
			pos                int
		}) int {
			return strings.Compare(a.MapleCharacterName, b.MapleCharacterName)
		})
	}

	columnCount := 1
	if len(dest) > 65 {
		columnCount = 2
	}

	if len(dest) > 130 {
		columnCount = 3
	}

	content := "Culvert summary for " + weekStr + "\n" +
		fmt.Sprintf("%d of %d tracked characters have a score this week.", len(dest), len(*chars))
	r.Edit(content, &discordgo.File{Name: "message.txt", Reader: strings.NewReader(cmdhelpers.FormatNthColumnList(columnCount, dest, table.Row{"Pos", "Character", "Score"}, func(data struct {
		Score              int32
		MapleCharacterName string
		pos                int
	}, idx int) table.Row {
		return table.Row{data.pos, data.MapleCharacterName, data.Score}
	}))})
}
