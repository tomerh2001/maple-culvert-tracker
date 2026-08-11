package commands

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

const helpText = `## Culvert Tracker - how to use it

**1. Link your character (once)**
Type ` + "`/register`" + ` and enter your in-game character name.
That connects your MapleStory character to your Discord account.

**2. See your progress**
- ` + "`/culvert`" + ` - your weekly culvert scores as a chart
- ` + "`/culvert user:@someone`" + ` - someone else's chart (or right click them -> Apps -> **Culvert**)
- ` + "`/culvert character-name:SomeChar`" + ` - any tracked character by name

**3. Guild-wide stats**
- ` + "`/leaderboard`" + ` - the guild's scores for a week (add ` + "`chart:True`" + ` for a progression chart)
- ` + "`/personal-bests`" + ` - everyone's best scores
- ` + "`/list-characters`" + ` - who is being tracked

**4. Report your own score** (only if enabled here)
` + "`/report-score score:123456`" + ` right after your culvert run.

Scores are usually entered weekly by the guild's submitters from a screenshot of the in-game ranking - if your score is missing, poke them.

*Admins: type ` + "`/setup`" + ` for the setup guide.*

*Created by [Tomerh2001](<https://github.com/tomerh2001/maple-culvert-tracker>)*`

const setupText = `## Culvert Tracker - admin setup guide

**1. Roles**
- ` + "`/config setting:Discord Guild Role IDs role:@YourGuildRole`" + ` - members with this role are the tracked roster
- ` + "`/config setting:Submitter Role IDs role:@Staff`" + ` - who may submit scores / manage characters (admins always can)

**2. Weekly announcement channel** (optional but recommended)
` + "`/config setting:Discord Weekly Announcement Channel ID channel:#culvert`" + `
The bot then keeps ONE message per week there with the full score table (edited in place), plus a thread with submission notes and personal-best shoutouts. Leave unset for no announcements at all.

**3. Track the roster**
Members self-serve with ` + "`/register`" + `; submitters can also link anyone with ` + "`/register character-name:X user:@member`" + ` or bulk-track names with ` + "`/track-characters`" + `. Submitting scores auto-tracks unknown names by default.

**4. Submit scores weekly**
Screenshot the in-game **Guild -> Guild Contents -> Member Participation Status** window (full window is fine), post it in Discord, then right click the message -> Apps -> **Submit Scores**. Done.
- Preview without saving: right click -> Apps -> **Parse Images** (or ` + "`/parse-images message-link:...`" + `)
- Corrections: ` + "`/submit-scores`" + ` with ` + "`overwrite-existing:True`" + ` and/or ` + "`date:`" + ` for past weeks, or ` + "`/set-score`" + ` for a single fix
- Optional: ` + "`/config setting:Allow self-reported scores value:true`" + ` lets members ` + "`/report-score`" + ` their own runs

**5. Housekeeping**
` + "`/rename-character`" + ` after name changes, ` + "`/untrack-character`" + ` for leavers (history is kept), ` + "`/export-csv`" + ` for spreadsheets, ` + "`/config`" + ` to review all settings.

*Created by [Tomerh2001](<https://github.com/tomerh2001/maple-culvert-tracker>)*`

func helpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	deferReply(s, i, true).Edit(helpText)
}

func setupCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	deferReply(s, i, true).EditChunked(setupText + "\n" + setupStatusBlock(s))
}

// setupStatusMaxProblems caps the health issues shown in /setup's status block
// (fails first); /health has the full list.
const setupStatusMaxProblems = 8

// setupStatusBlock renders the live tail of /setup: every /config setting's
// set/unset state plus the current health warns/fails.
func setupStatusBlock(s *discordgo.Session) string {
	var b strings.Builder
	b.WriteString("\n## Current status\n**Settings** (`/config` to change)")
	for _, name := range editableSettingKeys() {
		k := apiredis.KeysMap[name]
		label := name
		if d := apiredis.GetHumanReadableDescriptions(k); d != nil {
			label = d.Name
		}
		state := "not set"
		if strings.TrimSpace(k.GetWithDefault(apiredis.RedisDB, "")) != "" {
			state = "set"
		}
		b.WriteString("\n- " + label + ": " + state)
	}

	problems := helpers.FilterProblems(helpers.RunHealthChecks(s))
	if len(problems) == 0 {
		b.WriteString("\n\n:white_check_mark: All health checks pass.")
		return b.String()
	}
	b.WriteString("\n\n**Health** (full report: `/health`)")
	shown := problems
	if len(shown) > setupStatusMaxProblems {
		shown = shown[:setupStatusMaxProblems]
	}
	b.WriteString(helpers.FormatCheckResults(shown))
	if extra := len(problems) - len(shown); extra > 0 {
		b.WriteString(fmt.Sprintf("\n... and %d more (see `/health`)", extra))
	}
	return b.String()
}

// leaderboard consolidates the old culvert-summary (table) and
// culvert-mega-chart (chart:True) commands.
func leaderboard(s *discordgo.Session, i *discordgo.InteractionCreate) {
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "chart" && v.BoolValue() {
			culvertMegaChart(s, i)
			return
		}
	}
	culvertSummary(s, i)
}
