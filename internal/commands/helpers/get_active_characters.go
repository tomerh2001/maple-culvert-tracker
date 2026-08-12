package helpers

//lint:file-ignore ST1001 Dot imports by jet
import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"

	"github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/model"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// RosterMeta describes how the active roster was computed, so callers can
// surface member-cache staleness warnings.
type RosterMeta struct {
	FilterConfigured bool          // CONF_DISCORD_GUILD_ROLE_IDS is non-empty
	FilterApplied    bool          // the member-cache narrowing was actually applied
	CacheAvailable   bool          // DATA_DISCORD_MEMBERS exists and parsed
	CacheAgeKnown    bool          // DATA_DISCORD_MEMBERS_UPDATED_AT parsed
	CacheAge         time.Duration // age of the cache when CacheAgeKnown
}

// rosterStaleAfter is how old the member cache may get before role-filtered
// outputs carry a staleness warning.
const rosterStaleAfter = 2 * time.Hour

// StalenessWarning returns a one-line warning (with leading newline) when role
// filtering is configured but the member cache is missing or stale, else "".
func (m RosterMeta) StalenessWarning() string {
	if !m.FilterConfigured {
		return ""
	}
	if !m.CacheAvailable || !m.CacheAgeKnown || m.CacheAge > rosterStaleAfter {
		return "\n:warning: Member cache is stale/unavailable - role filtering may be inaccurate."
	}
	return ""
}

// FilterCharactersByMembers narrows characters to those whose owner is still a
// cached guild member. Characters owned by the internal ids '1' (untracked,
// defensive - the base query already excludes them), '2' (unlinked but
// tracked) or an empty owner are kept regardless: only characters linked to a
// REAL Discord id absent from the cache are excluded.
func FilterCharactersByMembers(chars []model.Characters, memberIDs map[string]bool) []model.Characters {
	out := make([]model.Characters, 0, len(chars))
	for _, c := range chars {
		switch {
		case c.DiscordUserID == "1":
			continue
		case c.DiscordUserID == "" || c.DiscordUserID == "2" || memberIDs[c.DiscordUserID]:
			out = append(out, c)
		}
	}
	return out
}

// GetActiveCharacters returns the tenant's tracked roster; see
// GetActiveCharactersWithMeta for the rules.
func GetActiveCharacters(r *redis.Client, db *sql.DB, tenantID string) (*[]model.Characters, error) {
	chars, _, err := GetActiveCharactersWithMeta(r, db, tenantID)
	return chars, err
}

// GetActiveCharactersWithMeta returns the tenant's tracked roster and how it
// was computed. The database is canonical: the base set is every character of
// the tenant not explicitly untracked (discord_user_id != '1'; '2' =
// unlinked-but-tracked is included). Role filtering is an OPTIONAL narrowing
// applied only when the tenant has guild role ids configured AND its Discord
// member cache exists and parses; an empty role config or a
// missing/unparseable cache means NO filtering.
func GetActiveCharactersWithMeta(r *redis.Client, db *sql.DB, tenantID string) (*[]model.Characters, RosterMeta, error) {
	// Keep the member cache reasonably fresh for the filtering below (no-op
	// without a bot session; failures fall back to the existing cache state).
	maybeRefreshMemberCache(tenantID)

	meta := RosterMeta{}
	stmt := SELECT(Characters.AllColumns).FROM(
		Characters,
	).WHERE(Characters.DiscordUserID.NOT_EQ(String("1")).AND(Characters.GuildID.EQ(String(tenantID)))).ORDER_BY(Characters.MapleCharacterName)
	chars := []model.Characters{}
	if err := stmt.Query(db, &chars); err != nil {
		return nil, meta, err
	}

	meta.FilterConfigured = strings.TrimSpace(apiredis.CONF_DISCORD_GUILD_ROLE_IDS.For(tenantID).GetWithDefault(r, "")) != ""
	if ts := apiredis.DATA_DISCORD_MEMBERS_UPDATED_AT.For(tenantID).GetWithDefault(r, ""); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			meta.CacheAgeKnown = true
			meta.CacheAge = time.Since(t)
		}
	}

	members := []data.WebGuildMember{}
	if raw, err := apiredis.DATA_DISCORD_MEMBERS.For(tenantID).Get(r); err == nil {
		if jerr := json.Unmarshal([]byte(raw), &members); jerr == nil && len(members) > 0 {
			meta.CacheAvailable = true
		}
	}

	if meta.FilterConfigured && meta.CacheAvailable {
		memberIDs := make(map[string]bool, len(members))
		for _, m := range members {
			memberIDs[m.DiscordUserID] = true
		}
		chars = FilterCharactersByMembers(chars, memberIDs)
		meta.FilterApplied = true
	}

	return &chars, meta, nil
}
