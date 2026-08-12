package commands

import (
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

func TestCollectUnmatchedPreservesParsedOrderAndScores(t *testing.T) {
	entries := []helpers.ScoreEntry{
		{Name: "MissingFirst", RawName: "MissingFirst", Score: 123456},
		{Name: "ActiveCharacter", RawName: "ActiveCharacter", Score: 100000, Matched: true},
		{Name: "MissingSecond", RawName: "MissingSecond", Score: 98765},
	}

	got := collectUnmatched(entries)

	if len(got) != 2 {
		t.Fatalf("collectUnmatched returned %d entries, want 2", len(got))
	}
	if got[0] != entries[0] || got[1] != entries[2] {
		t.Fatalf("collectUnmatched returned %#v, want %#v then %#v", got, entries[0], entries[2])
	}
}
