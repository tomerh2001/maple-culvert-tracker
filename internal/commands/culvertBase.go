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

// culvertCharRef is a member's registered character (id + display name), used
// to fan out per-character score queries for the multi-character chart.
type culvertCharRef struct {
	id   int64
	name string
}

// userMentionRe matches a Discord user mention: <@123> or <@!123>.
var userMentionRe = regexp.MustCompile(`^<@!?(\d+)>$`)

// culvertFreshnessLine tells a member how current this server's culvert data
// is, so an empty answer reads as "your score is missing" rather than "the bot
// is broken". It returns "" when the lookup fails - freshness is a nicety, it
// never replaces the answer itself.
func culvertFreshnessLine(tenant string) string {
	fresh, err := cmdhelpers.TenantLastScoreUpdate(db.DB, tenant)
	if err != nil {
		log.Println("culvert: last score update lookup:", err)
		return ""
	}
	switch {
	case !fresh.At.IsZero():
		return "\nThis server's scores were last updated " + cmdhelpers.DiscordTimestamp(fresh.At, "f") +
			" (" + cmdhelpers.DiscordTimestamp(fresh.At, "R") + ")."
	case !fresh.LastWeek.IsZero():
		// Scores recorded before db_migrations/8 carry no write stamp; the
		// newest scored week is all the freshness that was ever recorded.
		return "\nThe latest recorded week here is " + fresh.LastWeek.Format(time.DateOnly) +
			" (its exact update time wasn't recorded)."
	default:
		return "\nNo culvert scores have been submitted on this server yet."
	}
}

// culvertLastUpdate reports how current the given characters' charted scores
// are, as the (write time, newest scored week) pair the chart embed stamps
// itself with (see helpers.StampLastUpdated). A failed lookup degrades to an
// unstamped embed rather than to no chart.
func culvertLastUpdate(ids []int64, fromKey, toKey string) (time.Time, string) {
	fresh, err := cmdhelpers.CharactersLastScoreUpdate(db.DB, ids, fromKey, toKey)
	if err != nil {
		log.Println("culvert: character last score update lookup:", err)
		return time.Time{}, ""
	}
	week := ""
	if !fresh.LastWeek.IsZero() {
		week = fresh.LastWeek.Format(time.DateOnly)
	}
	return fresh.At, week
}

// culvertBase serves /culvert and the Culvert user context menu. Only an
// actual chart is posted publicly: every other outcome - no data, bad input,
// a backend failure - goes back through reply.EditPrivate so the channel never
// carries a non-answer.
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
			r.EditPrivate(badDateMessage)
			return
		}
		fromKey = cmdhelpers.GetCulvertResetDate(d).Format(time.DateOnly)
	}
	if toStr != "" {
		d, err := parseFlexibleDate(toStr)
		if err != nil {
			r.EditPrivate(badDateMessage)
			return
		}
		toKey = cmdhelpers.GetCulvertResetDate(d).Format(time.DateOnly)
	}
	if fromKey != "" && toKey != "" && fromKey > toKey {
		r.EditPrivate("`from` is after `to` - nothing to chart.")
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
		r.EditPrivate("Something went wrong querying the database.")
		return
	}
	args := []any{tenant}
	if !byName {
		args = append(args, targetUserID)
	}
	rows, err := stmt.Query(args...)
	if err != nil {
		log.Println("culvert: query find characters:", err)
		r.EditPrivate("Something went wrong querying the database.")
		return
	}
	count := 0
	refs := []culvertCharRef{}
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
		refs = append(refs, culvertCharRef{id: id, name: c})
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
			r.EditPrivate("No tracked character named `" + nameArg + "` found. Check the spelling, or mention the member instead (`/culvert name:@them`).")
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
			r.EditPrivate(msg)
			return
		}
		if count > 1 {
			// Several registered characters: chart all of them at once, one
			// line per character, over the same date window. (`/culvert
			// name:<character>` still isolates a single character.)
			renderMultiCulvertChart(r, tenant, targetUserID, refs, fromKey, toKey)
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
		r.EditPrivate("Something went wrong querying the database.")
		return
	}
	defer stmt.Close()
	rows, err = stmt.Query(args...)
	if err != nil {
		log.Println("culvert: scores query:", err)
		r.EditPrivate("Something went wrong querying the database.")
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
		r.EditPrivate("No data on " + matchedName + " in that period..." + culvertFreshnessLine(tenant))
		return
	}
	slices.Reverse(chartData)

	jsonData, err := json.Marshal(chartData)
	if err != nil {
		log.Println("culvert: chart data marshal failed:", err)
		r.EditPrivate("Something went wrong building the chart data.")
		return
	}

	statistics, _ := cmdhelpers.GetCharacterStatistics(db.DB, apiredis.RedisDB, tenant, matchedName, toKey, chartData)
	// Statistics are decorative: a nil value only hides the extra fields.

	resp, err := http.Post("http://"+os.Getenv(data.EnvVarChartMakerHost)+"/chartmaker?y-axis-start-at-0=false", "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != http.StatusOK {
		r.EditPrivate("Looks like my `chartmaker` component is broken... ")
		return
	}
	defer resp.Body.Close()
	updatedAt, latestWeek := culvertLastUpdate([]int64{matchedID}, fromKey, toKey)
	r.EditData(helpers.GenerateDiscordCulvertOutput(resp.Body, tenant, matchedName, toKey, statistics, updatedAt, latestWeek))
}

// renderMultiCulvertChart charts every one of a member's registered characters
// as a separate line on one chart (via the chartmaker multi-series endpoint),
// respecting the same from/to window. It replaces the old "pick one" list for
// members with two or more characters. fromKey/toKey are normalized week keys
// (possibly empty); toKey doubles as the embed's date label.
func renderMultiCulvertChart(r *reply, tenant, targetUserID string, refs []culvertCharRef, fromKey, toKey string) {
	// Build the per-character score window once ($1 is the character id;
	// bounds, if any, are $2/$3).
	where := ""
	bounds := []any{}
	if fromKey != "" {
		bounds = append(bounds, fromKey)
		where += " AND character_culvert_scores.culvert_date >= $" + strconv.Itoa(len(bounds)+1)
	}
	if toKey != "" {
		bounds = append(bounds, toKey)
		where += " AND character_culvert_scores.culvert_date <= $" + strconv.Itoa(len(bounds)+1)
	}
	sql := `SELECT character_culvert_scores.culvert_date, character_culvert_scores.score FROM characters INNER JOIN character_culvert_scores ON character_culvert_scores.character_id = characters.id WHERE characters.id = $1` + where + ` ORDER BY character_culvert_scores.culvert_date DESC LIMIT ` + strconv.Itoa(maxCulvertChartWeeks)
	stmt, err := db.DB.Prepare(sql)
	if err != nil {
		log.Println("culvert: prepare multi scores query:", err)
		r.EditPrivate("Something went wrong querying the database.")
		return
	}
	defer stmt.Close()

	series := make([]culvertCharSeries, 0, len(refs))
	for _, ref := range refs {
		args := append([]any{ref.id}, bounds...)
		rows, err := stmt.Query(args...)
		if err != nil {
			log.Println("culvert: multi scores query:", err)
			r.EditPrivate("Something went wrong querying the database.")
			return
		}
		pts := []data.ChartMakerPoints{}
		for rows.Next() {
			pt := data.ChartMakerPoints{}
			rows.Scan(&pt.Label, &pt.Score)
			pt.RawDate = pt.Label[:10]
			pt.Label = pt.Label[5:10]
			pts = append(pts, pt)
		}
		rows.Close()
		series = append(series, culvertCharSeries{name: ref.name, points: pts})
	}

	payload, more, ok := buildCulvertSeries(series, maxCulvertChartSeries)
	if !ok {
		r.EditPrivate("No culvert data for <@" + targetUserID + ">'s characters in that period..." + culvertFreshnessLine(tenant))
		return
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Println("culvert: multi chart data marshal failed:", err)
		r.EditPrivate("Something went wrong building the chart data.")
		return
	}

	resp, err := http.Post("http://"+os.Getenv(data.EnvVarChartMakerHost)+"/chartmaker-multiple", "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != http.StatusOK {
		r.EditPrivate("Looks like my `chartmaker` component is broken... ")
		return
	}
	defer resp.Body.Close()

	// "Scores last updated" covers exactly the characters the chart drew, not
	// the ones the series cap dropped.
	idsByName := make(map[string]int64, len(refs))
	for _, ref := range refs {
		idsByName[ref.name] = ref.id
	}
	names := make([]string, len(payload.DataPlots))
	charted := make([]int64, 0, len(payload.DataPlots))
	for i, p := range payload.DataPlots {
		names[i] = p.CharacterName
		if id, ok := idsByName[p.CharacterName]; ok {
			charted = append(charted, id)
		}
	}
	updatedAt, latestWeek := culvertLastUpdate(charted, fromKey, toKey)
	r.EditData(helpers.GenerateDiscordCulvertMultiOutput(resp.Body, toKey, names, more, updatedAt, latestWeek))
}
