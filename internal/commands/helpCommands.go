package commands

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

const helpText = `## Culvert Tracker - how to use it

**1. Link your character(s) (once)**
Type ` + "`/register`" + ` and enter your in-game character name.
That connects your MapleStory character to your Discord account.
Got mules? Run ` + "`/register`" + ` once per character - link as many as you like.
- ` + "`/characters`" + ` - list a member's linked characters (yours by default)
- ` + "`/registered`" + ` - list everyone who has linked a character
- ` + "`/unregister name:YourCharacter`" + ` - remove one by name (history is kept)

**2. See your progress**
- ` + "`/culvert`" + ` - your weekly culvert scores as a chart
- ` + "`/culvert name:@someone`" + ` - a member's chart (or right click them -> Apps -> **Culvert**)
- ` + "`/culvert name:SomeChar`" + ` - any tracked character by name
- Add ` + "`from:`" + `/` + "`to:`" + ` (YYYY-MM-DD or a Discord timestamp) to bound the chart

**3. Guild-wide stats**
` + "`/culvert-all`" + ` - the guild's scores for a week (add ` + "`date:`" + ` for past weeks)

**4. How scores get in**
Submitters submit a screenshot of the in-game ranking two ways: right click the message -> Apps -> **Submit Scores**, or attach it to ` + "`/submit-scores`" + `. Single-score corrections go through ` + "`/set-culvert`" + `. If your score is missing, poke a submitter.

The culvert week rolls over at **Thursday 00:00 UTC** (03:00 Israel summer time).
Every server has its own private data - characters and scores never mix between servers.

*Admins: type ` + "`/setup`" + ` for the setup guide.*

*Created by [Tomerh2001](<https://github.com/tomerh2001/maple-culvert-tracker>)*`

const setupText = `## Culvert Tracker - admin setup guide

Anyone can add this bot to their server - each server's data (characters, scores, settings, announcements) is fully isolated. Setting it up for YOUR server takes two minutes:

**1. Roles**
- ` + "`/config setting:Discord Guild Role IDs value:@YourGuildRole`" + ` - members with this role are the tracked roster
- ` + "`/config setting:Submitter Role IDs value:@Staff`" + ` - who may submit scores / manage characters (by default only admins can, which is fine too)

**2. Weekly announcement channel** (optional but recommended)
` + "`/config setting:Discord Weekly Announcement Channel ID value:#culvert`" + `
The bot then keeps ONE summary message per week there (coverage, top scores, guild total - edited in place), with the FULL table as the first comment of its thread (also edited in place), followed by submission notes and personal-best shoutouts. Any change - submission, /register, /unregister, /set-culvert, /reset-week - refreshes both. Leave unset for no announcements at all.

**3. Track the roster**
Members self-serve with ` + "`/register`" + ` (submitters can link anyone: ` + "`/register name:X user:@member`" + `). Submitting scores auto-tracks unknown names automatically, so a first screenshot submission builds the roster for you. ` + "`/unregister`" + ` handles leavers (history is kept).

**4. Submit scores weekly**
Screenshot the in-game **Guild -> Guild Contents -> Member Participation Status** window (full window is fine). Submit it either way: right click the posted message -> Apps -> **Submit Scores**, or attach it to ` + "`/submit-scores`" + ` (up to 5 screenshots for a long roster).
- Scores land on the current culvert week; the week rolls over at **Thursday 00:00 UTC** (03:00 Israel summer time)
- If some scores already exist with different values, nothing is written: the bot shows the conflicts and asks you to submit the same screenshots again within 10 minutes to confirm the overwrite
- Single fixes (typos, missed rows, past weeks): ` + "`/set-culvert name:X score:123 date:YYYY-MM-DD`" + `

**5. Housekeeping**
` + "`/health`" + ` runs the full self-check for this server, ` + "`/config`" + ` reviews all settings, ` + "`/unregister`" + ` untracks characters or members, and ` + "`/reset-week`" + ` wipes this week's recorded scores (run it twice to confirm) if a submission went badly wrong.

*Created by [Tomerh2001](<https://github.com/tomerh2001/maple-culvert-tracker>)*`

func helpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	deferReply(s, i, true).EditChunked(helpText)
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
