package helpers

import (
	"database/sql"
	"time"
)

// CountWeekScores returns how many culvert score rows the tenant has recorded
// for the week.
func CountWeekScores(dbc *sql.DB, tenantID string, week time.Time) (int, error) {
	var n int
	err := dbc.QueryRow(
		`SELECT COUNT(*) FROM character_culvert_scores s
		 JOIN characters c ON c.id = s.character_id
		 WHERE c.guild_id = $1 AND s.culvert_date = $2`,
		tenantID, week.Format(time.DateOnly)).Scan(&n)
	return n, err
}

// DeleteWeekScores deletes ALL of the tenant's culvert score rows for the
// week (the /reset-week action) and reports how many went. Characters stay
// tracked; other tenants' rows and other weeks are untouched by construction
// (the guild_id + culvert_date predicates).
func DeleteWeekScores(dbc *sql.DB, tenantID string, week time.Time) (int64, error) {
	res, err := dbc.Exec(
		`DELETE FROM character_culvert_scores s
		 USING characters c
		 WHERE s.character_id = c.id AND c.guild_id = $1 AND s.culvert_date = $2`,
		tenantID, week.Format(time.DateOnly))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
