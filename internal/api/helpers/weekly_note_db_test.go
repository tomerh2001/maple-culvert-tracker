package helpers

// Integration test locking weekPersonalBests' line format against a real
// postgres (see internal/db/testdb): a linked PB mentions the owner before the
// backticked name ("<@id> `Name`: old -> new"); an unlinked PB shows just the
// backticked name. Exercised only when TEST_DATABASE_URL is set.

import (
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

func TestWeekPersonalBestsLineFormat(t *testing.T) {
	dbc := testdb.TestDB(t)
	week := annWeek                    // 2026-07-29, after Date2mPatch (2025-10-01)
	prevWeek := week.AddDate(0, 0, -7) // still after the patch cutoff

	const linkedOwner = "123456789012345678"
	var linkedID int64
	if err := dbc.QueryRow(
		`INSERT INTO characters (maple_character_name, discord_user_id, guild_id) VALUES ('LinkedChar', $1, $2) RETURNING id`,
		linkedOwner, annTenantA).Scan(&linkedID); err != nil {
		t.Fatalf("insert linked character: %v", err)
	}
	var unlinkedID int64
	if err := dbc.QueryRow(
		`INSERT INTO characters (maple_character_name, discord_user_id, guild_id) VALUES ('UnlinkedChar', '2', $1) RETURNING id`,
		annTenantA).Scan(&unlinkedID); err != nil {
		t.Fatalf("insert unlinked character: %v", err)
	}

	// Prior-week baseline + a higher current-week score = a personal best for both.
	for _, s := range []struct {
		id    int64
		week  time.Time
		score int
	}{
		{linkedID, prevWeek, 100}, {linkedID, week, 250},
		{unlinkedID, prevWeek, 80}, {unlinkedID, week, 90},
	} {
		if _, err := dbc.Exec(
			`INSERT INTO character_culvert_scores (character_id, culvert_date, score) VALUES ($1, $2, $3)`,
			s.id, s.week.Format(time.DateOnly), s.score); err != nil {
			t.Fatalf("insert score: %v", err)
		}
	}

	rows := []weekScore{
		{CharacterID: linkedID, Name: "LinkedChar", DiscordUserID: linkedOwner, Score: 250},
		{CharacterID: unlinkedID, Name: "UnlinkedChar", DiscordUserID: "2", Score: 90},
	}
	got := weekPersonalBests(dbc, week, rows)
	want := []string{
		"<@" + linkedOwner + "> `LinkedChar`: 100 -> 250",
		"`UnlinkedChar`: 80 -> 90",
	}
	if len(got) != len(want) {
		t.Fatalf("weekPersonalBests = %v, want %v", got, want)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Errorf("line %d = %q, want %q", idx, got[idx], want[idx])
		}
	}
}
