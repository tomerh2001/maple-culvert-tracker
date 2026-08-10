package commands

//lint:file-ignore ST1001 Dot imports by jet
import (
	"log"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	"github.com/jedib0t/go-pretty/v6/table"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func culvertSummary(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Invalid date format, should be YYYY-MM-DD",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}
	d = cmdhelpers.GetCulvertResetDate(d)

	if d.After(time.Now()) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Invalid date, cannot be in the future",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var orderByClause []OrderByClause = []OrderByClause{CharacterCulvertScores.Score.DESC(), Characters.MapleCharacterName.ASC()}

	// get all rows for the specific date
	stmt := SELECT(Characters.MapleCharacterName.AS("maple_character_name"), CharacterCulvertScores.Score.AS("score")).FROM(CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).WHERE(CharacterCulvertScores.CulvertDate.EQ(DateT(d)).AND(Characters.DiscordUserID.NOT_EQ(String("1")))).ORDER_BY(orderByClause...)

	dest := []struct {
		Score              int32
		MapleCharacterName string
		pos                int
	}{}

	err = stmt.Query(db.DB, &dest)
	if err != nil {
		log.Println(err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed to retrieve characters' data from database. See server logs.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
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

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Culvert summary for " + d.Format(time.DateOnly),
			Files: []*discordgo.File{{Name: "message.txt", Reader: strings.NewReader(cmdhelpers.FormatNthColumnList(columnCount, dest, table.Row{"Pos", "Character", "Score"}, func(data struct {
				Score              int32
				MapleCharacterName string
				pos                int
			}, idx int) table.Row {
				return table.Row{data.pos, data.MapleCharacterName, data.Score}
			}))}},
		},
	})

}
