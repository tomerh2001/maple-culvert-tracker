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

// IsAllowedGuild reports whether the given guild id is one of the env-listed
// guilds (the default tenant's guilds). It no longer gates interactions - the
// bot serves every guild - but still defines the home deployment for tenant
// mapping and the /health env-guild checks.
func IsAllowedGuild(guildID string) bool {
	for _, id := range AllGuildIDs() {
		if id == guildID {
			return true
		}
	}
	return false
}

// PrimaryGuildID returns the DISCORD_GUILD_ID env guild id: the id of the
// DEFAULT tenant. It doubles as the redis key prefix that pre-tenant versions
// of this bot used for everything, which is why the default tenant keeps it.
func PrimaryGuildID() string {
	return strings.TrimSpace(os.Getenv(EnvVarDiscordGuildID))
}

// TenantID maps a Discord guild id to its data tenant (the shared-data
// group):
//   - a guild listed in the env (DISCORD_GUILD_ID + DISCORD_EXTRA_GUILD_IDS)
//     belongs to the PRIMARY guild's tenant. This preserves the owner's
//     existing cross-server sharing AND all pre-tenant data with zero
//     migration: redis keys were always prefixed with the primary guild id,
//     which IS this tenant's prefix.
//   - any other guild is its own tenant: its data never mixes with anyone
//     else's.
func TenantID(guildID string) string {
	if IsAllowedGuild(guildID) {
		return PrimaryGuildID()
	}
	return guildID
}

// TenantGuildIDs returns the guilds whose Discord members make up a tenant's
// roster: every env-listed guild for the default tenant (members are merged
// across them, the pre-tenant behavior), or just the guild itself for any
// other tenant.
func TenantGuildIDs(tenantID string) []string {
	if tenantID == PrimaryGuildID() {
		return AllGuildIDs()
	}
	return []string{tenantID}
}
