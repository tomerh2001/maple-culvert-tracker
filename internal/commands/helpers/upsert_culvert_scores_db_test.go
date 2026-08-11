package helpers

// Integration tests for UpsertCulvertScores against a real postgres, exercised
// only when TEST_DATABASE_URL is set (see internal/db/testdb).

import (
	"database/sql"
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

// testWeek is any fixed culvert reset day (a Wednesday).
var testWeek = time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)

// insertTestCharacter creates an unlinked-but-tracked character and returns
// its id.
func insertTestCharacter(t *testing.T, dbc *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := dbc.QueryRow(
		`INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, '2') RETURNING id`,
		name).Scan(&id); err != nil {
		t.Fatalf("inserting test character %q: %v", name, err)
	}
	return id
}

// weekScoresByID reads all scores stored for a week as characterID -> score.
func weekScoresByID(t *testing.T, dbc *sql.DB, week time.Time) map[int64]int {
	t.Helper()
	rows, err := dbc.Query(
		`SELECT character_id, score FROM character_culvert_scores WHERE culvert_date = $1`,
		week.Format(time.DateOnly))
	if err != nil {
		t.Fatalf("querying week scores: %v", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var score int
		if err := rows.Scan(&id, &score); err != nil {
			t.Fatalf("scanning week scores: %v", err)
		}
		out[id] = score
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating week scores: %v", err)
	}
	return out
}

func TestUpsertCulvertScoresInsertAndIdempotency(t *testing.T) {
	dbc := testdb.TestDB(t)
	idA := insertTestCharacter(t, dbc, "UpsertA")
	idB := insertTestCharacter(t, dbc, "UpsertB")

	changes := []ScoreChange{{CharacterID: idA, Score: 100}, {CharacterID: idB, Score: 200}}
	if err := UpsertCulvertScores(dbc, testWeek, changes); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Resubmitting the identical changes must succeed and change nothing.
	if err := UpsertCulvertScores(dbc, testWeek, changes); err != nil {
		t.Fatalf("idempotent resubmission: %v", err)
	}

	got := weekScoresByID(t, dbc, testWeek)
	want := map[int64]int{idA: 100, idB: 200}
	if len(got) != len(want) || got[idA] != want[idA] || got[idB] != want[idB] {
		t.Fatalf("stored scores = %v, want %v", got, want)
	}
}

func TestUpsertCulvertScoresConflictUpdates(t *testing.T) {
	dbc := testdb.TestDB(t)
	idA := insertTestCharacter(t, dbc, "UpsertConflict")

	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: idA, Score: 100}}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: idA, Score: 150}}); err != nil {
		t.Fatalf("conflicting upsert: %v", err)
	}

	got := weekScoresByID(t, dbc, testWeek)
	if len(got) != 1 || got[idA] != 150 {
		t.Fatalf("stored scores = %v, want exactly {%d: 150}", got, idA)
	}
}

func TestUpsertCulvertScoresRollsBackAtomically(t *testing.T) {
	dbc := testdb.TestDB(t)
	idA := insertTestCharacter(t, dbc, "UpsertAtomic")

	// The second row violates the character_id foreign key; the whole
	// transaction must roll back, including the valid first row.
	changes := []ScoreChange{
		{CharacterID: idA, Score: 999},
		{CharacterID: 99999999, Score: 5},
	}
	if err := UpsertCulvertScores(dbc, testWeek, changes); err == nil {
		t.Fatal("upsert with FK-violating row succeeded, want error")
	}

	if got := weekScoresByID(t, dbc, testWeek); len(got) != 0 {
		t.Fatalf("scores written despite rollback: %v, want none", got)
	}
}

func TestUpsertCulvertScoresEmptyIsNoOpOnRealDB(t *testing.T) {
	dbc := testdb.TestDB(t)
	if err := UpsertCulvertScores(dbc, testWeek, nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
	if got := weekScoresByID(t, dbc, testWeek); len(got) != 0 {
		t.Fatalf("empty upsert wrote rows: %v", got)
	}
}
