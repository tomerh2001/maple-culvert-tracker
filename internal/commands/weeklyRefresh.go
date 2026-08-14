package commands

import (
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
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

// AddStartupWeeklyRefresh registers a Ready handler that, once the bot
// connects, re-renders the current-week weekly announcement for every tenant.
// This keeps the posted summary + thread table in sync with the database after
// a redeploy or any offline data fix (e.g. a name correction made straight in
// the DB), without waiting for the next command or submission. The tenant set
// is derived from the Ready payload's guild list (authoritative, and free of
// any dependency on the async-populated active-guild registry), so foreign
// public-install tenants are covered too. Runs in a goroutine so it never
// blocks the gateway; tenants with no announcement for the week are a silent
// no-op. Safe to re-run on gateway reconnect - RefreshWeeklyAnnouncement only
// edits existing messages, never creates them.
func AddStartupWeeklyRefresh(s *discordgo.Session) {
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		guildIDs := make([]string, 0, len(r.Guilds))
		for _, g := range r.Guilds {
			guildIDs = append(guildIDs, g.ID)
		}
		go RefreshAllWeekly(s, guildIDs)
	})
}

// RefreshAllWeekly refreshes the current-week announcement for each distinct
// tenant among the given guilds (see AddStartupWeeklyRefresh).
func RefreshAllWeekly(s *discordgo.Session, guildIDs []string) {
	week := helpers.CurrentCulvertWeek(time.Now())
	seen := map[string]bool{}
	for _, gid := range guildIDs {
		tenant := data.TenantID(gid)
		if seen[tenant] {
			continue
		}
		seen[tenant] = true
		if err := apihelpers.RefreshWeeklyAnnouncement(s, db.DB, apiredis.RedisDB, tenant, week); err != nil {
			log.Printf("startup weekly refresh (tenant %s): %v", tenant, err)
		}
	}
}
