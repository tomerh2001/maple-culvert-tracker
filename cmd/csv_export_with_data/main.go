package main

import (
	"encoding/csv"
	"os"
	"strconv"
	"time"

	_ "github.com/joho/godotenv/autoload"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	tenantdata "github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func main() {
	helpers.EnvVarsTest()

	dateRaw := "2025-06-10"
	date, _ := time.Parse(time.DateOnly, dateRaw)

	/*
		This func call is broken, it does not handle the case when there is 1 or 2 of the 3 weeks without data
	*/
	data, err := apihelpers.ExportCharactersData(db.DB, apiredis.RedisDB, tenantdata.PrimaryGuildID(), 3, date)
	if err != nil {
		panic(err)
	}
	if len(data) < 1 {
		panic("no data")
	}
	csvRaw := [][]string{}

	dates := []string{}
	for _, v := range data[0].Scores {
		dates = append(dates, v.Date)
	}
	header := []string{"Name"}
	header = append(header, dates...)
	header = append(header, "Average", "Previous Best")
	csvRaw = append(csvRaw, header)
	for _, score := range data {
		csvRow := []string{score.Name}
		for _, v := range score.Scores {
			csvRow = append(csvRow, strconv.Itoa(v.Score))
		}
		csvRow = append(csvRow, strconv.Itoa(score.Average), strconv.Itoa(score.PreviousBest))
		csvRaw = append(csvRaw, csvRow)
	}
	file, err := os.Create("output.csv")
	if err != nil {
		panic(err)
	}
	csv.NewWriter(file).WriteAll(csvRaw)
}
