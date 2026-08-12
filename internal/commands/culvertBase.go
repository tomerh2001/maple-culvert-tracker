package commands

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
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

// maxCulvertChartWeeks is the sane cap on charted weeks when from/to leave
// the window open (the most recent weeks win).
const maxCulvertChartWeeks = 104

// userMentionRe matches a Discord user mention: <@123> or <@!123>.
var userMentionRe = regexp.MustCompile(`^<@!?(\d+)>$`)

// culvertBase serves /culvert (public) and the Culvert user context menu.
// name resolves to either a member (mention / empty = invoker / menu target:
// their registered characters, with a choice list when they have several) or
// a tracked character by name (case-insensitive). from/to bound the charted
// weeks; either side may be omitted.
func culvertBase(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, false)
	tenant := tenantOf(i)

	nameArg := ""
	fromStr := ""
	toStr := ""
	cmdData := i.ApplicationCommandData()
	for _, v := range cmdData.Options {
		switch v.Name {
		case "name":
			nameArg = strings.TrimSpace(v.StringValue())
		case "from":
			fromStr = v.StringValue()
		case "to":
			toStr = v.StringValue()
		}
	}

	targetUserID := i.Member.User.ID
	charName := ""
	switch {
	case cmdData.Name == "Culvert" && cmdData.TargetID != "":
		// User context menu: right click a member -> Apps -> Culvert.
		targetUserID = cmdData.TargetID
	case nameArg != "":
		if m := userMentionRe.FindStringSubmatch(nameArg); m != nil {
			targetUserID = m[1]
		} else {
			charName = strings.ToLower(nameArg)
		}
	}
	byName := charName != ""
	isSelf := !byName && targetUserID == i.Member.User.ID

	// Explicit dates name week LABELS: normalize both bounds to week keys so
	// e.g. a Saturday `from` includes its own week.
	fromKey := ""
	toKey := ""
	if fromStr != "" {
		d, err := parseFlexibleDate(fromStr)
		if err != nil {
			r.Edit(badDateMessage)
			return
		}
		fromKey = cmdhelpers.GetCulvertResetDate(d).Format(time.DateOnly)
	}
	if toStr != "" {
		d, err := parseFlexibleDate(toStr)
		if err != nil {
			r.Edit(badDateMessage)
			return
		}
		toKey = cmdhelpers.GetCulvertResetDate(d).Format(time.DateOnly)
	}
	if fromKey != "" && toKey != "" && fromKey > toKey {
		r.Edit("`from` is after `to` - nothing to chart.")
		return
	}

	// Resolve the character within the tenant: the member's registered
	// characters, or a tracked character by name.
	sql := `SELECT id, maple_character_name FROM characters WHERE characters.guild_id = $1 AND characters.discord_user_id = $2 ORDER BY id`
	if byName {
		sql = `SELECT id, maple_character_name FROM characters WHERE characters.guild_id = $1 AND characters.discord_user_id != '1' ORDER BY maple_character_name`
	}
	stmt, err := db.DB.Prepare(sql)
	if err != nil {
		log.Println("culvert: prepare find characters:", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	args := []any{tenant}
	if !byName {
		args = append(args, targetUserID)
	}
	rows, err := stmt.Query(args...)
	if err != nil {
		log.Println("culvert: query find characters:", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	count := 0
	names := []string{}
	characters := map[string]struct {
		name string
		id   int64
	}{}
	matchedName := ""
	var matchedID int64
	for rows.Next() {
		var c string
		var id int64
		rows.Scan(&id, &c)
		count++
		names = append(names, c)
		characters[strings.ToLower(c)] = struct {
			name string
			id   int64
		}{name: c, id: id}
		matchedID = id
		matchedName = c
	}
	rows.Close()
	stmt.Close()

	if byName {
		hit, ok := characters[charName]
		if !ok {
			r.Edit("No tracked character named `" + nameArg + "` found. Check the spelling, or mention the member instead (`/culvert name:@them`).")
			return
		}
		matchedID, matchedName = hit.id, hit.name
	} else {
		if count == 0 {
			msg := "**<@" + targetUserID + ">** hasn't registered a MapleStory character yet.\n" +
				"They can link one by typing `/register` and entering their character name - after that this command will work."
			if isSelf {
				msg = "You haven't registered a MapleStory character yet!\n" +
					"Type `/register` and enter your character name (for example `/register name:HTomer`), then try again."
			}
			r.Edit(msg)
			return
		}
		if count > 1 {
			// Several registered characters: list the choices inline (text
			// only) and let the caller pick one by name.
			who := "You have"
			if !isSelf {
				who = "<@" + targetUserID + "> has"
			}
			r.EditChunked(who + " multiple characters - pick one with `/culvert name:<character>`:\n`" +
				strings.Join(names, "`, `") + "`")
			return
		}
		// Exactly one: matchedID/matchedName already hold it.
	}

	where := ""
	args = []any{matchedID}
	if fromKey != "" {
		args = append(args, fromKey)
		where += " AND character_culvert_scores.culvert_date >= $" + strconv.Itoa(len(args))
	}
	if toKey != "" {
		args = append(args, toKey)
		where += " AND character_culvert_scores.culvert_date <= $" + strconv.Itoa(len(args))
	}
	sql = `SELECT character_culvert_scores.culvert_date, character_culvert_scores.score FROM characters INNER JOIN character_culvert_scores ON character_culvert_scores.character_id = characters.id WHERE characters.id = $1` + where + ` ORDER BY character_culvert_scores.culvert_date DESC LIMIT ` + strconv.Itoa(maxCulvertChartWeeks)
	stmt, err = db.DB.Prepare(sql)
	if err != nil {
		log.Println("culvert: prepare scores query:", err)
		r.Edit("Something went wrong querying the database.")
		return
	}
	defer stmt.Close()
	rows, err = stmt.Query(args...)
	if err != nil {
		log.Println("culvert: scores query:", err)
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
		r.Edit("No data on " + matchedName + " in that period...")
		return
	}
	slices.Reverse(chartData)

	jsonData, err := json.Marshal(chartData)
	if err != nil {
		log.Println("culvert: chart data marshal failed:", err)
		r.Edit("Something went wrong building the chart data.")
		return
	}

	statistics, _ := cmdhelpers.GetCharacterStatistics(db.DB, apiredis.RedisDB, tenant, matchedName, toKey, chartData)
	// Statistics are decorative: a nil value only hides the extra fields.

	resp, err := http.Post("http://"+os.Getenv(data.EnvVarChartMakerHost)+"/chartmaker?y-axis-start-at-0=false", "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != http.StatusOK {
		r.Edit("Looks like my `chartmaker` component is broken... ")
		return
	}
	defer resp.Body.Close()
	r.EditData(helpers.GenerateDiscordCulvertOutput(resp.Body, tenant, matchedName, toKey, statistics))
}
