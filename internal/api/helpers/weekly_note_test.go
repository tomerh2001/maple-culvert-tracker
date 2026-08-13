package helpers

import (
	"testing"
	"time"
)

// An empty week renders a stable "nothing yet" note without touching the DB -
// the path /reset-week relies on so the note clears instead of crashing.
func TestBuildWeekNoteEmpty(t *testing.T) {
	got := buildWeekNote(nil, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), "2026-08-12", nil)
	if got != "No scores recorded yet this week." {
		t.Errorf("empty-week note = %q", got)
	}
}
