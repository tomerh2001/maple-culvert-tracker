package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// culvertResetLine renders the NEXT weekly reset as a Discord timestamp so every
// reader sees it in their own timezone - no hard-coded clock time.
func culvertResetLine() string {
	// CurrentCulvertWeek returns the current week's Wednesday key (00:00 UTC);
	// the week spans [key+1d, key+8d), so key+8d is the upcoming reset.
	next := helpers.CurrentCulvertWeek(time.Now()).AddDate(0, 0, 8)
	return fmt.Sprintf("<t:%d:F>", next.Unix())
}

// helpText is the /culvert-help embed description (rendered inside a colored
// embed, see helpCommand). %%RESET%% is replaced with the next-reset timestamp.
const helpText = `**1) Link your character(s)**
Run ` + "`/register`" + `. The bot reads your Discord server nickname and automatically links all matching IGNs to your account.

Other character commands:
- ` + "`/unregister name:YourCharacter`" + ` — remove a character from your account (its score history is kept).
- ` + "`/characters`" + ` — view all characters currently linked to your account.

**2) Track your progress**
Run ` + "`/culvert`" + ` to view your weekly Culvert scores and progress as a chart.

**3) How scores are added** — :lock: admins & submitters only
You never submit your own scores. An admin (or a member with the submitter role) posts a screenshot of the in-game Culvert rankings and the bot reads every score off it: right click the screenshot message → Apps → **Submit Scores**, or ` + "`/submit-scores`" + `.
If one of your scores is missing or wrong, let an admin know - you can't add it yourself.

**4) Weekly reset**
The Culvert week resets every week.
Next reset: %%RESET%%

Each Discord server has its own private dataset — characters, scores, and history are never shared or mixed between servers.

*Admins: run ` + "`/setup`" + ` for the server setup guide.*
*Created by [Tomerh2001](<https://github.com/tomerh2001/maple-culvert-tracker>)*`

const setupText = `## Culvert Tracker - admin setup guide

Anyone can add this bot to their server - each server's data (characters, scores, settings, announcements) is fully isolated. Setting it up for YOUR server takes two minutes:

**1. Roles**
- ` + "`/config setting:Discord Guild Role IDs value:@YourGuildRole`" + ` - members with this role are the tracked roster
- ` + "`/config setting:Submitter Role IDs value:@Staff`" + ` - who may submit scores / manage characters (by default only admins can, which is fine too)

**2. Weekly announcement channel** (optional but recommended)
` + "`/config setting:Discord Weekly Announcement Channel ID value:#culvert`" + `
The bot then keeps ONE summary message per week there (coverage, top scores, guild total - edited in place), with the FULL table as the first comment of its thread (also edited in place), followed by submission notes and personal-best shoutouts. Any change - submission, /register, /unregister, /set-culvert, /reset-week - refreshes both. Leave unset for no announcements at all.
Also optional: ` + "`/config setting:Discord Screenshot Archive Channel ID value:#culvert-screenshots`" + ` keeps ONE message per week there with the score screenshots each submission was parsed from - resubmitting a page replaces its older version, so the message always shows the newest shot of every page. A browsable history of the raw inputs.

**3. Track the roster**
Members self-serve with ` + "`/register`" + ` (submitters can link anyone: ` + "`/register name:X user:@member`" + `). Submitting scores auto-tracks unknown names automatically, so a first screenshot submission builds the roster for you. ` + "`/unregister`" + ` handles leavers (history is kept).

**4. Submit scores weekly**
Screenshot the in-game **Guild -> Guild Contents -> Member Participation Status** window (full window is fine). Submit it either way: right click the posted message -> Apps -> **Submit Scores**, or attach it to ` + "`/submit-scores`" + ` (up to 5 screenshots for a long roster).
- Scores land on the current culvert week (it rolls over weekly)
- If some scores already exist with different values, nothing is written: the bot shows the conflicts and asks you to submit the same screenshots again within 10 minutes to confirm the overwrite
- Single fixes (typos, missed rows, past weeks): ` + "`/set-culvert name:X score:123 date:YYYY-MM-DD`" + `

**5. Housekeeping**
` + "`/health`" + ` runs the full self-check for this server, ` + "`/config`" + ` reviews all settings, ` + "`/unregister`" + ` untracks characters or members, and ` + "`/reset-week`" + ` wipes this week's recorded scores (run it twice to confirm) if a submission went badly wrong.

*Created by [Tomerh2001](<https://github.com/tomerh2001/maple-culvert-tracker>)*`

func helpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	deferReply(s, i, true).EditData(&discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{{
			Title:       "Culvert Tracker — How to Use It",
			Color:       apihelpers.WeekEmbedColor,
			Description: strings.ReplaceAll(helpText, "%%RESET%%", culvertResetLine()),
		}},
	})
}

func setupCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	deferReply(s, i, true).EditChunked(setupText + "\n" + setupStatusBlock(s, tenantOf(i)))
}

// setupStatusMaxProblems caps the health issues shown in /setup's status block
// (fails first); /health has the full list.
const setupStatusMaxProblems = 8

// setupStatusBlock renders the live tail of /setup for the invoking tenant:
// every /config setting's set/unset state plus the current health
// warns/fails.
func setupStatusBlock(s *discordgo.Session, tenantID string) string {
	var b strings.Builder
	b.WriteString("\n## Current status\n**Settings** (`/config` to change)")
	for _, name := range editableSettingKeys() {
		k := apiredis.KeysMap[name]
		label := name
		if d := apiredis.GetHumanReadableDescriptions(k); d != nil {
			label = d.Name
		}
		state := "not set"
		if strings.TrimSpace(k.For(tenantID).GetWithDefault(apiredis.RedisDB, "")) != "" {
			state = "set"
		}
		b.WriteString("\n- " + label + ": " + state)
	}

	problems := helpers.FilterProblems(helpers.RunHealthChecks(s, tenantID))
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
