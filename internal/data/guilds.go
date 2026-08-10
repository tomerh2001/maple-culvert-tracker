package data

import (
	"os"
	"strings"
)

// EnvVarDiscordExtraGuildIDs optionally lists additional Discord server ids
// (comma separated) served by this deployment over the same database. This is
// intentionally env-only: it cannot be changed from Discord.
const EnvVarDiscordExtraGuildIDs = "DISCORD_EXTRA_GUILD_IDS"

// AllGuildIDs returns the primary guild id followed by any extra guild ids,
// deduplicated, preserving order.
func AllGuildIDs() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(os.Getenv(EnvVarDiscordGuildID))
	for _, id := range strings.Split(os.Getenv(EnvVarDiscordExtraGuildIDs), ",") {
		add(id)
	}
	return out
}

// IsAllowedGuild reports whether the given guild id is served by this bot.
func IsAllowedGuild(guildID string) bool {
	for _, id := range AllGuildIDs() {
		if id == guildID {
			return true
		}
	}
	return false
}
