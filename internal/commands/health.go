package commands

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// healthCommand runs the bot's health checks and renders them as a compact
// pass/warn/fail list with fix hints. Admin only, ephemeral.
func healthCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !isAdmin(i) {
		registerReply(s, i, "Only server admins can use `/health`.")
		return
	}
	r := deferReply(s, i, true)

	results := helpers.RunHealthChecks(s)
	pass, warn, fail := 0, 0, 0
	for _, c := range results {
		switch c.Status {
		case helpers.CheckFail:
			fail++
		case helpers.CheckWarn:
			warn++
		default:
			pass++
		}
	}

	summary := ":white_check_mark: All checks passed."
	if warn+fail > 0 {
		summary = fmt.Sprintf(":stethoscope: %d passed, %d warning(s), %d failure(s).", pass, warn, fail)
	}
	r.EditChunked("## Bot health\n" + summary + helpers.FormatCheckResults(results))
}
