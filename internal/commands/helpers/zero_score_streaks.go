package helpers

import (
	"database/sql"
	"slices"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
)

// ZeroScoreStreak is a character's consecutive zero-score run ending on the
// requested culvert date.
type ZeroScoreStreak struct {
	MapleCharacterName string
	ZeroStreak         int64
	LastNonZeroDate    sql.NullTime
}

// GetCurrentZeroScoreStreaks returns characters that have a zero score on
// latestDate and counts their consecutive stored zero-score records backwards.
func GetCurrentZeroScoreStreaks(db *sql.DB, tenantID string, latestDate time.Time) ([]ZeroScoreStreak, error) {
	const query = `WITH ranked AS (
		SELECT
			c.id AS character_id,
			c.maple_character_name,
			sc.culvert_date,
			sc.score,
			ROW_NUMBER() OVER (
				PARTITION BY c.id
				ORDER BY sc.culvert_date DESC
			) AS rn
		FROM characters c
		INNER JOIN character_culvert_scores sc ON sc.character_id = c.id
		WHERE c.guild_id = $2
	),
	first_non_zero AS (
		SELECT
			character_id,
			MIN(rn) FILTER (WHERE score <> 0) AS first_non_zero_rn,
			MAX(culvert_date) FILTER (WHERE score <> 0) AS last_non_zero_date
		FROM ranked
		GROUP BY character_id
	),
	current_zero_streaks AS (
		SELECT
			r.character_id,
			r.maple_character_name,
			COUNT(*) AS zero_streak,
			f.last_non_zero_date
		FROM ranked r
		LEFT JOIN first_non_zero f ON f.character_id = r.character_id
		WHERE r.score = 0
			AND (f.first_non_zero_rn IS NULL OR r.rn < f.first_non_zero_rn)
		GROUP BY r.character_id, r.maple_character_name, f.last_non_zero_date
		HAVING COUNT(*) > 0
	),
	latest_characters AS (
		SELECT DISTINCT r.character_id
		FROM ranked r
		WHERE r.culvert_date = $1
	)
	SELECT
		cs.maple_character_name,
		cs.zero_streak,
		cs.last_non_zero_date
	FROM current_zero_streaks cs
	INNER JOIN latest_characters lc ON lc.character_id = cs.character_id
	ORDER BY cs.zero_streak DESC, cs.maple_character_name`

	rows, err := db.Query(query, latestDate, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	streaks := []ZeroScoreStreak{}
	for rows.Next() {
		var streak ZeroScoreStreak
		if err := rows.Scan(&streak.MapleCharacterName, &streak.ZeroStreak, &streak.LastNonZeroDate); err != nil {
			return nil, err
		}
		streaks = append(streaks, streak)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	SortZeroScoreStreaks(streaks)
	return streaks, nil
}

// SortZeroScoreStreaks applies the report's deterministic display ordering.
func SortZeroScoreStreaks(streaks []ZeroScoreStreak) {
	slices.SortStableFunc(streaks, func(a, b ZeroScoreStreak) int {
		if a.ZeroStreak > b.ZeroStreak {
			return -1
		}
		if a.ZeroStreak < b.ZeroStreak {
			return 1
		}
		if a.MapleCharacterName < b.MapleCharacterName {
			return -1
		}
		if a.MapleCharacterName > b.MapleCharacterName {
			return 1
		}
		return 0
	})
}

// FormatZeroScoreStreaks renders the shared inactive-player report table.
func FormatZeroScoreStreaks(streaks []ZeroScoreStreak) string {
	t := table.NewWriter()
	t.AppendHeader(table.Row{"Character", "Zero Streak", "Last Non-Zero Date"})
	for _, streak := range streaks {
		lastNonZeroDate := "Never"
		if streak.LastNonZeroDate.Valid {
			lastNonZeroDate = streak.LastNonZeroDate.Time.Format(time.DateOnly)
		}
		t.AppendRow(table.Row{streak.MapleCharacterName, streak.ZeroStreak, lastNonZeroDate})
	}
	return t.Render()
}
