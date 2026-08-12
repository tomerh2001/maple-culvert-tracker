package commands

import (
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// refreshWeeklyLine refreshes the invoking tenant's current-week artifacts
// (summary message + thread table) after a roster or score mutation, so the
// posted week never goes stale. It returns "" on success or when nothing was
// ever announced, and a receipt warning line on failure.
func refreshWeeklyLine(s *discordgo.Session, i *discordgo.InteractionCreate) string {
	err := apihelpers.RefreshWeeklyAnnouncement(
		s, db.DB, apiredis.RedisDB, tenantOf(i), helpers.CurrentCulvertWeek(time.Now()))
	if err != nil {
		return "\n:warning: The weekly message couldn't be refreshed: " + err.Error()
	}
	return ""
}
