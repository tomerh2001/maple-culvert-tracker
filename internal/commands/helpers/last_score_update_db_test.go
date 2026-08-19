package helpers

// Integration tests for the score-freshness lookups against a real postgres,
// exercised only when TEST_DATABASE_URL is set (see internal/db/testdb).

import (
	"database/sql"
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

// insertUnstampedScore writes a score row the way rows existed before
// db_migrations/8 added updated_at: a value with no write time at all.
func insertUnstampedScore(t *testing.T, dbc *sql.DB, charID int64, week time.Time, score int) {
	t.Helper()
	if _, err := dbc.Exec(
		`INSERT INTO character_culvert_scores (character_id, culvert_date, score, updated_at) VALUES ($1, $2, $3, NULL)`,
		charID, week.Format(time.DateOnly), score); err != nil {
		t.Fatalf("inserting unstamped score: %v", err)
	}
}

// A written score is stamped, and rewriting it moves the stamp forward.
func TestCharactersLastScoreUpdateTracksWrites(t *testing.T) {
	dbc := testdb.TestDB(t)
	id := insertTestCharacter(t, dbc, "FreshA")

	before := time.Now().Add(-time.Minute)
	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: id, Score: 100}}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first, err := CharactersLastScoreUpdate(dbc, []int64{id}, "", "")
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if first.At.IsZero() {
		t.Fatal("write time not recorded for a freshly written score")
	}
	if first.At.Before(before) {
		t.Fatalf("write time %v predates the write", first.At)
	}
	if got := first.LastWeek.Format(time.DateOnly); got != testWeek.Format(time.DateOnly) {
		t.Fatalf("last week = %s, want %s", got, testWeek.Format(time.DateOnly))
	}

	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: id, Score: 150}}); err != nil {
		t.Fatalf("overwriting upsert: %v", err)
	}
	second, err := CharactersLastScoreUpdate(dbc, []int64{id}, "", "")
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if !second.At.After(first.At) {
		t.Fatalf("write time did not advance on overwrite: %v then %v", first.At, second.At)
	}
}

// The window bounds the answer to exactly the weeks a /culvert chart drew.
func TestCharactersLastScoreUpdateHonorsWindow(t *testing.T) {
	dbc := testdb.TestDB(t)
	id := insertTestCharacter(t, dbc, "FreshWindow")
	older := testWeek.AddDate(0, 0, -7)

	if err := UpsertCulvertScores(dbc, older, []ScoreChange{{CharacterID: id, Score: 10}}); err != nil {
		t.Fatalf("older upsert: %v", err)
	}
	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: id, Score: 20}}); err != nil {
		t.Fatalf("current upsert: %v", err)
	}

	got, err := CharactersLastScoreUpdate(dbc, []int64{id}, "", older.Format(time.DateOnly))
	if err != nil {
		t.Fatalf("windowed lookup: %v", err)
	}
	if want := older.Format(time.DateOnly); got.LastWeek.Format(time.DateOnly) != want {
		t.Fatalf("last week = %s, want %s - the window must exclude the newer score",
			got.LastWeek.Format(time.DateOnly), want)
	}
}

// Rows predating db_migrations/8 have no write time: the week they belong to
// is the only freshness signal, and it must not be reported as an instant.
func TestCharactersLastScoreUpdateUnstampedRows(t *testing.T) {
	dbc := testdb.TestDB(t)
	id := insertTestCharacter(t, dbc, "FreshLegacy")
	insertUnstampedScore(t, dbc, id, testWeek, 42)

	got, err := CharactersLastScoreUpdate(dbc, []int64{id}, "", "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !got.At.IsZero() {
		t.Fatalf("write time = %v, want zero for an unstamped row", got.At)
	}
	if want := testWeek.Format(time.DateOnly); got.LastWeek.Format(time.DateOnly) != want {
		t.Fatalf("last week = %s, want %s", got.LastWeek.Format(time.DateOnly), want)
	}
}

// No characters, no query: a member with nothing charted gets the zero value.
func TestCharactersLastScoreUpdateNoCharacters(t *testing.T) {
	dbc := testdb.TestDB(t)
	got, err := CharactersLastScoreUpdate(dbc, nil, "", "")
	if err != nil {
		t.Fatalf("empty lookup: %v", err)
	}
	if !got.At.IsZero() || !got.LastWeek.IsZero() {
		t.Fatalf("empty lookup returned %+v, want the zero value", got)
	}
}

// The server-wide lookup covers every character of that tenant - and only
// that tenant.
func TestTenantLastScoreUpdateIsTenantScoped(t *testing.T) {
	dbc := testdb.TestDB(t)
	mine := insertTestCharacterInTenant(t, dbc, testTenantA, "FreshMine")
	theirs := insertTestCharacterInTenant(t, dbc, testTenantB, "FreshTheirs")

	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: theirs, Score: 999}}); err != nil {
		t.Fatalf("other tenant upsert: %v", err)
	}
	empty, err := TenantLastScoreUpdate(dbc, testTenantA)
	if err != nil {
		t.Fatalf("lookup before own scores: %v", err)
	}
	if !empty.At.IsZero() || !empty.LastWeek.IsZero() {
		t.Fatalf("tenant A saw tenant B's scores: %+v", empty)
	}

	if err := UpsertCulvertScores(dbc, testWeek, []ScoreChange{{CharacterID: mine, Score: 5}}); err != nil {
		t.Fatalf("own upsert: %v", err)
	}
	got, err := TenantLastScoreUpdate(dbc, testTenantA)
	if err != nil {
		t.Fatalf("lookup after own scores: %v", err)
	}
	if got.At.IsZero() {
		t.Fatal("tenant write time not recorded")
	}
}
