package commands

import (
	"strings"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

func TestCollectUnmatchedScoresPreservesParsedOrderAndScores(t *testing.T) {
	entries := []helpers.ScoreEntry{
		{Name: "MissingFirst", Score: 123456},
		{Name: "ActiveCharacter", Score: 100000},
		{Name: "MissingSecond", Score: 98765},
	}
	activeSet := map[string]bool{"ActiveCharacter": true}

	got := collectUnmatchedScores(entries, activeSet)

	if len(got) != 2 {
		t.Fatalf("collectUnmatchedScores returned %d entries, want 2", len(got))
	}
	if got[0] != entries[0] || got[1] != entries[2] {
		t.Fatalf("collectUnmatchedScores returned %#v, want %#v then %#v", got, entries[0], entries[2])
	}
}

func TestFormatAnnotatedScoresTableShowsAllRowsWithStatusInOrder(t *testing.T) {
	entries := []helpers.ScoreEntry{
		{Name: "MissingFirst", Score: 123456},
		{Name: "ActiveCharacter", Score: 100000},
		{Name: "MissingSecond", Score: 98765},
	}
	tracked := map[string]bool{"ActiveCharacter": true}

	got := formatAnnotatedScoresTable(entries, tracked)

	for _, expected := range []string{"CHARACTER", "SCORE", "STATUS", "MissingFirst", "123456", "ActiveCharacter", "100000", "tracked", "NEW", "MissingSecond", "98765"} {
		if !strings.Contains(got, expected) {
			t.Errorf("table does not contain %q:\n%s", expected, got)
		}
	}
	if strings.Index(got, "MissingFirst") > strings.Index(got, "ActiveCharacter") ||
		strings.Index(got, "ActiveCharacter") > strings.Index(got, "MissingSecond") {
		t.Errorf("table changed parsed order:\n%s", got)
	}
	// The full row set is rendered - the tracked row must not be filtered out.
	if !strings.Contains(got, "ActiveCharacter") {
		t.Errorf("tracked row missing from annotated table:\n%s", got)
	}
}

func TestSortedScoreEntriesIncludesNamesAndScoresDeterministically(t *testing.T) {
	scores := map[string]int{
		"MissingSecond": 98765,
		"MissingFirst":  123456,
	}

	got := sortedScoreEntries(scores)

	if len(got) != 2 {
		t.Fatalf("sortedScoreEntries returned %d entries, want 2", len(got))
	}
	if got[0].Name != "MissingFirst" || got[0].Score != 123456 {
		t.Errorf("first entry = %#v, want MissingFirst with score 123456", got[0])
	}
	if got[1].Name != "MissingSecond" || got[1].Score != 98765 {
		t.Errorf("second entry = %#v, want MissingSecond with score 98765", got[1])
	}
}
