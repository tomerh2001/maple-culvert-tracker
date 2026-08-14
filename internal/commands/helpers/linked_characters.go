package helpers

import (
	"database/sql"
)

// LinkedCharacterNames returns the MapleStory character names linked to one
// Discord member within a tenant, ordered by name. A "linked" character is a
// row whose discord_user_id is that member's real id; the untracked ('1') and
// unlinked ('2') sentinels are never a real member id, so passing a real id
// naturally excludes them. Powers /characters. The slice is non-nil (empty when
// the member has linked nothing).
func LinkedCharacterNames(dbc *sql.DB, tenantID, discordUserID string) ([]string, error) {
	rows, err := dbc.Query(
		`SELECT maple_character_name FROM characters WHERE guild_id = $1 AND discord_user_id = $2 ORDER BY maple_character_name`,
		tenantID, discordUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// LinkedUser is one member who has linked at least one character, with their
// character names ordered by name. Powers /registered.
type LinkedUser struct {
	DiscordUserID string
	Names         []string
}

// LinkedUsers returns every DISTINCT linked member in a tenant - those whose
// discord_user_id is a real id, excluding the untracked ('1') and unlinked
// ('2') sentinels - each with their linked character names. Members are ordered
// by discord_user_id and names by character name, so the output is
// deterministic. Powers /registered; it scales to hundreds of members (the
// caller chunks the render under Discord's message limit).
func LinkedUsers(dbc *sql.DB, tenantID string) ([]LinkedUser, error) {
	rows, err := dbc.Query(
		`SELECT discord_user_id, maple_character_name FROM characters WHERE guild_id = $1 AND discord_user_id NOT IN ('1', '2') ORDER BY discord_user_id, maple_character_name`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkedUser{}
	idx := map[string]int{}
	for rows.Next() {
		var uid, name string
		if err := rows.Scan(&uid, &name); err != nil {
			return nil, err
		}
		if p, ok := idx[uid]; ok {
			out[p].Names = append(out[p].Names, name)
			continue
		}
		idx[uid] = len(out)
		out = append(out, LinkedUser{DiscordUserID: uid, Names: []string{name}})
	}
	return out, rows.Err()
}
