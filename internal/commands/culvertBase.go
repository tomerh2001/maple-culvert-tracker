package commands

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func culvertBase(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)

	// Parse discord param character-name
	charName := ""
	date := ""
	weeks := int64(8)
	yAxisStartAt0 := false

	cmdData := i.ApplicationCommandData()
	targetUserID := i.Member.User.ID
	options := cmdData.Options
	for _, v := range options {
		if v.Name == "character-name" {
			charName = strings.ToLower(v.StringValue())
		}
		if v.Name == "user" {
			if u := v.UserValue(nil); u != nil {
				targetUserID = u.ID
			}
		}
		if v.Name == "date" {
			date = v.StringValue()
		}
		if v.Name == "weeks" {
			weeks = v.IntValue()
		}
		if v.Name == "y-axis-start-at-0" {
			yAxisStartAt0 = v.BoolValue()
		}
	}
	if cmdData.Name == "Culvert" && cmdData.TargetID != "" {
		// User context menu: right click a member -> Apps -> Culvert.
		targetUserID = cmdData.TargetID
	}
	isSelf := targetUserID == i.Member.User.ID

	// Validate date format
	if date != "" {
		d, err := time.Parse(time.DateOnly, date) // YYYY-MM-DD
		if err != nil {
			r.Edit("Invalid date format, should be YYYY-MM-DD")
			return
		}
		date = cmdhelpers.GetCulvertResetDate(d).Format(time.DateOnly)
	}

	// Default: the caller's (or targeted user's) own characters. When a
	// character name is given, search every tracked character instead.
	sql := `SELECT id, maple_character_name FROM characters WHERE characters.discord_user_id = $1 ORDER BY id`
	byName := charName != "" && targetUserID == i.Member.User.ID && cmdData.Name != "Culvert"
	if byName {
		sql = `SELECT id, maple_character_name FROM characters WHERE characters.discord_user_id != '1' ORDER BY maple_character_name`
	}

	// Count # of chars
	stmt, err := db.DB.Prepare(sql)
	if err != nil {
		log.Println("Failed prepare find characters", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	args := []any{}
	if strings.Contains(sql, "$1") {
		args = append(args, targetUserID)
	}
	rows, err := stmt.Query(args...)
	if err != nil {
		log.Println("Query at find characters", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	count := 0
	characters := map[string]struct {
		name string
		id   int64
	}{}
	choices := ""
	lastSeenCharName := ""
	var lastSeenCharID int64 = 0
	choicesNumOfCharInLine := 0
	for rows.Next() {
		count++
		var c string
		var i int64
		rows.Scan(&i, &c)
		if choicesNumOfCharInLine < 3 {
			choices += c + ","
		} else {
			choices += c + "\n"
		}
		choicesNumOfCharInLine++
		if choicesNumOfCharInLine > 3 {
			choicesNumOfCharInLine = 0
		}
		characters[strings.ToLower(c)] = struct {
			name string
			id   int64
		}{name: c, id: i}
		lastSeenCharID = i
		lastSeenCharName = c
	}
	rows.Close()
	stmt.Close()

	if count == 0 && !byName {
		// No registered character: explain what to do in plain words.
		msg := "**<@" + targetUserID + ">** hasn't registered a MapleStory character yet.\n" +
			"They can link one by typing `/register` and entering their character name - after that this command will work."
		if isSelf {
			msg = "You haven't registered a MapleStory character yet!\n" +
				"Type `/register` and enter your character name (for example `/register character-name:HTomer`), then try again."
		}
		r.Edit(msg)
		return
	}

	choicesMsg := "Available characters:"
	if !isSelf && !byName {
		choicesMsg = "<@" + targetUserID + "> has multiple characters. Pick one with `/culvert user:@them character-name:<name>`. Available characters:"
	}

	if _, ok := characters[charName]; count == 0 || (count > 1 && charName == "") || (!ok && charName != "") {
		r.Edit(choicesMsg, &discordgo.File{Name: "message.csv", Reader: strings.NewReader(choices)})
		return
	} else if ok {
		lastSeenCharID = characters[charName].id
		lastSeenCharName = characters[charName].name
	}
	// There is only 1 character, and at this point charID is correct too.

	additionalWhere := ""
	if date != "" {
		additionalWhere += " AND character_culvert_scores.culvert_date <= $2"
	}
	// query score
	sql = `SELECT character_culvert_scores.culvert_date, character_culvert_scores.score FROM characters INNER JOIN character_culvert_scores ON character_culvert_scores.character_id = characters.id WHERE characters.id = $1` + additionalWhere + ` ORDER BY character_culvert_scores.culvert_date DESC LIMIT ` + strconv.FormatInt(weeks, 10)
	// Concat here is not an sql injection because I trust discord sanitizing the `weeks` variable
	stmt, err = db.DB.Prepare(sql)
	if err != nil {
		log.Println("Failed 1st prepare at culvert command", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	defer stmt.Close()
	args = []any{lastSeenCharID}
	if date != "" {
		args = append(args, date)
	}
	rows, err = stmt.Query(args...)
	if err != nil {
		log.Println("Query at culvert command", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	defer rows.Close()
	chartData := []data.ChartMakerPoints{}
	for rows.Next() {
		pt := data.ChartMakerPoints{}
		rows.Scan(&pt.Label, &pt.Score)
		pt.RawDate = pt.Label[:10]
		pt.Label = pt.Label[5:10]
		chartData = append(chartData, pt)
	}

	if len(chartData) == 0 {
		r.Edit("No data on " + lastSeenCharName + "...")
		return
	}
	slices.Reverse(chartData)

	jsonData, err := json.Marshal(chartData)
	if err != nil {
		log.Println("json at culvert command failed?", err)
		r.Edit("Something and something broko...")
		return
	}

	statistics, _ := cmdhelpers.GetCharacterStatistics(db.DB, apiredis.RedisDB, lastSeenCharName, date, chartData)
	// Code below handles statistics nil value
	// Error here does not break execution

	// Sample below
	// jsonData := []byte(`[{"label":"2/26","score":0},{"label":"3/5","score":1233},{"label":"3/12","score":8000},{"label":"3/19","score":8100},{"label":"3/26","score":5600},{"label":"4/2","score":5500},{"label":"4/9","score":25000}]`)
	resp, err := http.Post("http://"+os.Getenv(data.EnvVarChartMakerHost)+"/chartmaker?y-axis-start-at-0="+strconv.FormatBool(yAxisStartAt0), "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != http.StatusOK {
		r.Edit("Looks like my `chartmaker` component is broken... ")
		return
	}
	defer resp.Body.Close()
	r.EditData(helpers.GenerateDiscordCulvertOutput(resp.Body, lastSeenCharName, date, statistics))
}
