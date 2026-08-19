package helpers

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// LastScoreUpdate answers "how fresh is this data?" for a set of culvert
// scores, so a member reading /culvert can tell a stale bot apart from a
// missing score of their own.
//
// At is when a score row was last WRITTEN (character_culvert_scores.updated_at,
// added in db_migrations/8_score_updated_at.up.sql). Rows recorded before that
// migration carry no stamp, so At can be zero while scores exist - LastWeek,
// the newest culvert week holding any score, is the fallback signal and is
// itself zero only when nothing was ever recorded.
type LastScoreUpdate struct {
	At       time.Time
	LastWeek time.Time
}

// DiscordTimestamp renders an instant as a Discord timestamp mention, which
// every reader sees rendered in their own timezone. style is a Discord
// timestamp style letter: "f" (short date+time), "F" (long), "R" (relative,
// "5 days ago"). Discord renders these in message content, embed descriptions
// and field values - NOT in embed titles or footers (embeds carry their own
// Timestamp field for that).
func DiscordTimestamp(t time.Time, style string) string {
	return fmt.Sprintf("<t:%d:%s>", t.Unix(), style)
}

// TenantLastScoreUpdate reports the freshness of a whole server's culvert
// scores.
func TenantLastScoreUpdate(dbc *sql.DB, tenantID string) (LastScoreUpdate, error) {
	return scanLastScoreUpdate(dbc.QueryRow(
		`SELECT MAX(s.updated_at), MAX(s.culvert_date)
		 FROM character_culvert_scores s
		 INNER JOIN characters c ON c.id = s.character_id
		 WHERE c.guild_id = $1`, tenantID))
}

// CharactersLastScoreUpdate reports the freshness of the given characters'
// scores, optionally restricted to a week window (fromKey/toKey are
// "YYYY-MM-DD" week keys; either may be empty for an open side) so the answer
// matches exactly the rows a /culvert chart drew. An empty id list is not a
// query: it returns the zero value.
func CharactersLastScoreUpdate(dbc *sql.DB, ids []int64, fromKey, toKey string) (LastScoreUpdate, error) {
	if len(ids) == 0 {
		return LastScoreUpdate{}, nil
	}
	args := []any{pq.Array(ids)}
	where := ""
	if fromKey != "" {
		args = append(args, fromKey)
		where += fmt.Sprintf(" AND s.culvert_date >= $%d", len(args))
	}
	if toKey != "" {
		args = append(args, toKey)
		where += fmt.Sprintf(" AND s.culvert_date <= $%d", len(args))
	}
	return scanLastScoreUpdate(dbc.QueryRow(
		`SELECT MAX(s.updated_at), MAX(s.culvert_date)
		 FROM character_culvert_scores s
		 WHERE s.character_id = ANY($1)`+where, args...))
}

// scanLastScoreUpdate reads the (MAX(updated_at), MAX(culvert_date)) pair both
// queries select; SQL NULLs (no matching rows, or only unstamped ones) become
// zero times.
func scanLastScoreUpdate(row *sql.Row) (LastScoreUpdate, error) {
	var at, week sql.NullTime
	if err := row.Scan(&at, &week); err != nil {
		return LastScoreUpdate{}, err
	}
	out := LastScoreUpdate{}
	if at.Valid {
		out.At = at.Time
	}
	if week.Valid {
		out.LastWeek = week.Time
	}
	return out, nil
}
