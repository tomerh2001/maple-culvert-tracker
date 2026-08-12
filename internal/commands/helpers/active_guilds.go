package helpers

import (
	"log"
	"sort"
	"strings"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// The DATA_ACTIVE_GUILDS registry (process-global, comma separated guild ids)
// tracks every guild the bot is in. The bot maintains it on Ready and on
// GuildCreate/GuildDelete; periodicredis and cron jobs iterate it instead of
// the env guild list so freshly-added servers are covered without a redeploy.
// Joining a guild NEVER sends any message - /setup is the entry point.

// parseGuildList splits a comma separated id list, dropping empties and
// duplicates while preserving order.
func parseGuildList(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ActiveGuildIDs returns the registry's guild ids. When the registry is empty
// or unreadable (bot not booted yet, redis fresh) it falls back to the env
// guild list, which keeps the home deployment covered.
func ActiveGuildIDs(rdb *redis.Client) []string {
	raw := apiredis.DATA_ACTIVE_GUILDS.Global().GetWithDefault(rdb, "")
	if ids := parseGuildList(raw); len(ids) > 0 {
		return ids
	}
	return data.AllGuildIDs()
}

// ActiveTenants maps ActiveGuildIDs through data.TenantID and deduplicates:
// the set of tenants background jobs must serve. The default tenant sorts
// first (it is the env fallback), the rest alphabetically for determinism.
func ActiveTenants(rdb *redis.Client) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, gid := range ActiveGuildIDs(rdb) {
		t := data.TenantID(gid)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a] == data.PrimaryGuildID() {
			return true
		}
		if out[b] == data.PrimaryGuildID() {
			return false
		}
		return out[a] < out[b]
	})
	return out
}

// StoreActiveGuilds overwrites the registry with the given guild ids.
func StoreActiveGuilds(rdb *redis.Client, guildIDs []string) {
	if err := apiredis.DATA_ACTIVE_GUILDS.Global().Set(rdb, strings.Join(parseGuildList(strings.Join(guildIDs, ",")), ",")); err != nil {
		log.Println("StoreActiveGuilds: storing registry failed:", err)
	}
}

// AddActiveGuild registers one guild id (idempotent).
func AddActiveGuild(rdb *redis.Client, guildID string) {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return
	}
	raw := apiredis.DATA_ACTIVE_GUILDS.Global().GetWithDefault(rdb, "")
	ids := parseGuildList(raw)
	for _, id := range ids {
		if id == guildID {
			return
		}
	}
	StoreActiveGuilds(rdb, append(ids, guildID))
}

// RemoveActiveGuild unregisters one guild id (no-op when absent). The
// tenant's data is deliberately left untouched: rejoining the server finds
// everything as it was.
func RemoveActiveGuild(rdb *redis.Client, guildID string) {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return
	}
	raw := apiredis.DATA_ACTIVE_GUILDS.Global().GetWithDefault(rdb, "")
	ids := parseGuildList(raw)
	out := make([]string, 0, len(ids))
	changed := false
	for _, id := range ids {
		if id == guildID {
			changed = true
			continue
		}
		out = append(out, id)
	}
	if changed {
		StoreActiveGuilds(rdb, out)
	}
}
