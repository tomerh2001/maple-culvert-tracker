package helpers

import (
	"database/sql"
	"fmt"
	"time"
)

// ScoreChange is one character's culvert score to write for a week.
type ScoreChange struct {
	CharacterID int64
	Score       int
}

// UpsertCulvertScores writes all changes for the given culvert week in a
// single transaction, inserting new rows and overwriting existing ones
// (relying on the character_culvert_scores_culvert_date_character_id unique
// constraint from db_migrations/1_createdb.up.sql). Every written row is
// stamped with updated_at = NOW(), which is what /culvert reports as "scores
// last updated" - callers only pass rows whose value actually changed (see
// planSubmission), so the stamp means "last changed", not "last seen".
// An empty changes slice is a no-op returning nil.
func UpsertCulvertScores(dbc *sql.DB, week time.Time, changes []ScoreChange) error {
	if len(changes) == 0 {
		return nil
	}
	weekStr := week.Format(time.DateOnly)
	tx, err := dbc.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	for _, c := range changes {
		if _, err := tx.Exec(
			`INSERT INTO character_culvert_scores (character_id, culvert_date, score, updated_at) VALUES ($1, $2, $3, NOW())
			 ON CONFLICT (culvert_date, character_id) DO UPDATE SET score = EXCLUDED.score, updated_at = NOW()`,
			c.CharacterID, weekStr, c.Score); err != nil {
			tx.Rollback()
			return fmt.Errorf("upsert character %d: %w", c.CharacterID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
