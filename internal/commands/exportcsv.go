package commands

//lint:file-ignore ST1001 Dot imports by jet

import (
	"bytes"
	"encoding/csv"
	"log"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func exportcsv(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)

	// Parse discord param character-name
	date := ""
	weeks := int64(4)
	options := i.ApplicationCommandData().Options
	for _, v := range options {
		if v.Name == "date" {
			date = v.StringValue()
		}
		if v.Name == "weeks" {
			weeks = v.IntValue()
		}
	}
	rawDate, err := helpers.GetLatestResetDate(db.DB)
	if err != nil {
		log.Println("exportcsv", err)
		r.Edit("Failed to retrieve latest culvert reset date. See server logs.")
		return
	}
	// Validate date format
	if date != "" {
		rawDate, err = time.Parse(time.DateOnly, date) // YYYY-MM-DD
		if err != nil {
			r.Edit("Invalid date format, should be YYYY-MM-DD")
			return
		}
		rawDate = helpers.GetCulvertResetDate(rawDate)
	}
	originalInputDate := rawDate.Format(time.DateOnly)

	allDates := []Expression{}
	allDatesRaw := []time.Time{}
	for i := 0; i < int(weeks); i++ {
		allDates = append(allDates, DateT(rawDate))
		allDatesRaw = append(allDatesRaw, rawDate)
		rawDate = helpers.GetCulvertPreviousDate(rawDate)
	}

	chars, err := helpers.GetActiveCharacters(apiredis.RedisDB, db.DB)
	if err != nil {
		log.Println("export csv get active chars failed", err)
		r.Edit("Export csv command failed, could not get active characters")
		return
	}

	charIDsInExpr := []Expression{}
	for _, v := range *chars {
		charIDsInExpr = append(charIDsInExpr, Int64(v.ID))
	}

	/*
		SELECT * data for x weeks and x date
		for each dataset, create map[string]map[string]int representing map[date]map[character_name]score
		create new csv from the map above
	*/

	stmt := SELECT(Characters.MapleCharacterName.AS("name"), CharacterCulvertScores.CulvertDate.AS("culvert_date"), CharacterCulvertScores.Score.AS("score")).FROM(CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).WHERE(CharacterCulvertScores.CulvertDate.IN(allDates...).AND(CharacterCulvertScores.CharacterID.IN(charIDsInExpr...))).ORDER_BY(Characters.MapleCharacterName.ASC())

	dest := []struct {
		CulvertDate time.Time
		Score       int32
		Name        string
	}{}
	err = stmt.Query(db.DB, &dest)
	if err != nil {
		log.Println("export csv select stmt failed", err)
		r.Edit("Export csv failed getting culvert data from db. See server logs.")
		return
	}

	m := map[string]map[string]int{}

	for _, v := range dest {
		d := v.CulvertDate.Format(time.DateOnly)
		if _, ok := m[d]; !ok {
			m[d] = map[string]int{}
		}
		m[d][v.Name] = int(v.Score)
	}

	bytesBuffer := bytes.NewBufferString("")
	w := csv.NewWriter(bytesBuffer)
	defer w.Flush()

	charNamesHeader := []string{"dates"}
	for _, v := range *chars {
		charNamesHeader = append(charNamesHeader, v.MapleCharacterName)
	}
	err = w.Write(charNamesHeader)
	w.Flush()
	if err != nil {
		log.Println("export csv write charNamesHeader", err)
		r.Edit("Export csv failed to write character names header. See server logs.")
		return
	}

	culvertData := [][]string{}
	for _, v := range allDatesRaw {
		d := v.Format(time.DateOnly)
		row := []string{d}
		for _, c := range *chars {
			score := -1
			if s, ok := m[d][c.MapleCharacterName]; ok {
				score = s
			}
			row = append(row, strconv.Itoa(score))
		}
		culvertData = append(culvertData, row)
	}

	for n, row := range culvertData {
		err = w.Write(row)
		w.Flush()
		if err != nil {
			log.Println("export csv write culvertData at "+strconv.Itoa(n), err)
			r.Edit("Export csv failed to write character names header. See server logs.")
			return
		}
	}

	r.Edit("Export for "+strconv.Itoa(int(weeks))+" weeks on "+originalInputDate, &discordgo.File{
		Name:        "export_" + originalInputDate + ".csv",
		ContentType: "text/csv",
		Reader:      bytesBuffer,
	})
}
