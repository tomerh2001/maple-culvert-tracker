package helpers

import (
	"time"

	"github.com/bwmarrin/discordgo"
)

// StampLastUpdated marks a culvert embed with how current its scores are, so
// readers can tell fresh data from a chart nobody has submitted for in weeks.
//
// updatedAt is when a charted score was last WRITTEN. It rides the embed's
// native footer + timestamp pair (rendered as "Scores last updated • Today at
// 19:05" in each reader's own timezone) - Discord does not render <t:...>
// mentions inside footers, and the same pair already carries the weekly
// summary's timestamp.
//
// Scores recorded before db_migrations/8 started stamping writes have no
// updatedAt. Rather than claim a time that was never recorded, the footer then
// falls back to latestWeek ("YYYY-MM-DD", the newest charted week with a
// score). With neither, the embed is left unstamped.
func StampLastUpdated(e *discordgo.MessageEmbed, updatedAt time.Time, latestWeek string) {
	if e == nil {
		return
	}
	switch {
	case !updatedAt.IsZero():
		e.Footer = &discordgo.MessageEmbedFooter{Text: "Scores last updated"}
		e.Timestamp = updatedAt.UTC().Format(time.RFC3339)
	case latestWeek != "":
		e.Footer = &discordgo.MessageEmbedFooter{Text: "Latest scored week: " + latestWeek}
	}
}
