package helpers

import (
	"testing"
	"time"
)

// An empty changes slice must be a pure no-op: nil error and no database
// access at all (a nil *sql.DB would panic if it were touched).
func TestUpsertCulvertScoresEmptySliceIsNoOp(t *testing.T) {
	if err := UpsertCulvertScores(nil, time.Now(), nil); err != nil {
		t.Fatalf("UpsertCulvertScores(nil week, empty) = %v, want nil", err)
	}
	if err := UpsertCulvertScores(nil, time.Now(), []ScoreChange{}); err != nil {
		t.Fatalf("UpsertCulvertScores(empty slice) = %v, want nil", err)
	}
}
