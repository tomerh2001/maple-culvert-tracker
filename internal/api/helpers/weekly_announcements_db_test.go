package helpers

import (
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

// TestWeekScoresExcludesUntracked pins that characters untracked via
// /unregister (discord_user_id '1') keep their history rows but never appear
// in the rendered weekly table.
func TestWeekScoresExcludesUntracked(t *testing.T) {
	dbc := testdb.TestDB(t)

	week := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	var trackedID, untrackedID int64
	if err := dbc.QueryRow(
		`INSERT INTO characters (maple_character_name, discord_user_id) VALUES ('WeeklyTracked', '123') RETURNING id`,
	).Scan(&trackedID); err != nil {
		t.Fatal(err)
	}
	if err := dbc.QueryRow(
		`INSERT INTO characters (maple_character_name, discord_user_id) VALUES ('WeeklyUntracked', '1') RETURNING id`,
	).Scan(&untrackedID); err != nil {
		t.Fatal(err)
	}
	for id, score := range map[int64]int{trackedID: 100, untrackedID: 200} {
		if _, err := dbc.Exec(
			`INSERT INTO character_culvert_scores (culvert_date, character_id, score) VALUES ($1, $2, $3)`,
			week.Format(time.DateOnly), id, score); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := weekScores(dbc, week)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (untracked row must be excluded)", len(rows))
	}
	if rows[0].Name != "WeeklyTracked" || rows[0].Score != 100 {
		t.Fatalf("got %s:%d, want WeeklyTracked:100", rows[0].Name, rows[0].Score)
	}
}
