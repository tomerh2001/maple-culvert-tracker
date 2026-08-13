package apiredis

import "testing"

// The weekly channel is the one setting scoped per invoking guild (each server
// in a shared tenant announces into its own channel); everything else stays
// tenant-shared.
func TestWeeklyChannelIsPerGuild(t *testing.T) {
	if !CONF_DISCORD_WEEKLY_CHANNEL_ID.IsPerGuild() {
		t.Error("weekly channel must be per-guild")
	}
	for _, k := range []redisInternalKey{
		CONF_DISCORD_SUBMIT_ROLE_IDS,
		CONF_DISCORD_GUILD_ROLE_IDS,
		CONF_DISCORD_ADMIN_CHANNEL_ID,
	} {
		if k.IsPerGuild() {
			t.Errorf("%s must stay tenant-shared, not per-guild", k.Name)
		}
	}
}

// A per-guild key scoped to two different guild ids yields two distinct redis
// keys - the mechanism that keeps each server's weekly channel separate.
func TestPerGuildScopingDistinctKeys(t *testing.T) {
	a := CONF_DISCORD_WEEKLY_CHANNEL_ID.For("111").RedisKey()
	b := CONF_DISCORD_WEEKLY_CHANNEL_ID.For("222").RedisKey()
	if a == b {
		t.Fatalf("expected distinct keys, both were %q", a)
	}
	if a != "111_CONF_DISCORD_WEEKLY_CHANNEL_ID" {
		t.Errorf("unexpected key %q", a)
	}
}
