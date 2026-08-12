package commands

import (
	"strings"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

func intp(v int) *int { return &v }

func TestPlanSubmission(t *testing.T) {
	// Tracked roster used by most cases: Alice has no score this week, Bob has
	// 500, Carol has 0, Dave has 900.
	tracked := []trackedScore{
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Bob", Existing: intp(500)},
		{ID: 3, Name: "Carol", Existing: intp(0)},
		{ID: 4, Name: "Dave", Existing: intp(900)},
	}

	type want struct {
		changes     map[int64]int // characterID -> score upserted
		newNames    []string
		overwritten []string
		conflicts   []scoreConflict
		autoTrack   []string
	}
	cases := []struct {
		name      string
		submitted map[string]int
		overwrite bool
		want      want
	}{
		{
			name:      "empty input plans nothing",
			submitted: map[string]int{},
			want:      want{changes: map[int64]int{}},
		},
		{
			name:      "new score inserts",
			submitted: map[string]int{"Alice": 100},
			want:      want{changes: map[int64]int{1: 100}, newNames: []string{"Alice"}},
		},
		{
			name:      "identical resubmission is a no-op",
			submitted: map[string]int{"Bob": 500},
			want:      want{changes: map[int64]int{}},
		},
		{
			name:      "differing value without overwrite is a conflict",
			submitted: map[string]int{"Bob": 600},
			want: want{changes: map[int64]int{},
				conflicts: []scoreConflict{{Name: "Bob", Existing: 500, Incoming: 600}}},
		},
		{
			name:      "explicit 0 downgrade without overwrite is a conflict too",
			submitted: map[string]int{"Bob": 0},
			want: want{changes: map[int64]int{},
				conflicts: []scoreConflict{{Name: "Bob", Existing: 500, Incoming: 0}}},
		},
		{
			name:      "all conflicts are collected, not just the first",
			submitted: map[string]int{"Bob": 0, "Dave": 100, "Alice": 50},
			want: want{changes: map[int64]int{1: 50}, newNames: []string{"Alice"},
				conflicts: []scoreConflict{
					{Name: "Bob", Existing: 500, Incoming: 0},
					{Name: "Dave", Existing: 900, Incoming: 100},
				}},
		},
		{
			name:      "differing value with overwrite overwrites",
			submitted: map[string]int{"Bob": 600},
			overwrite: true,
			want:      want{changes: map[int64]int{2: 600}, overwritten: []string{"Bob"}},
		},
		{
			name:      "0 downgrade with overwrite overwrites",
			submitted: map[string]int{"Bob": 0},
			overwrite: true,
			want:      want{changes: map[int64]int{2: 0}, overwritten: []string{"Bob"}},
		},
		{
			name:      "absent tracked characters are never touched (no zero-filling exists)",
			submitted: map[string]int{"Bob": 500},
			want:      want{changes: map[int64]int{}},
		},
		{
			name:      "absent tracked characters are never touched even with overwrite",
			submitted: map[string]int{"Bob": 600},
			overwrite: true,
			want:      want{changes: map[int64]int{2: 600}, overwritten: []string{"Bob"}},
		},
		{
			name:      "unmatched names always land in AutoTrack (sorted)",
			submitted: map[string]int{"Zed": 10, "Alice": 100, "Mallory": 20},
			// Matched rows are still planned; the caller creates the AutoTrack
			// characters and appends their score changes itself.
			want: want{changes: map[int64]int{1: 100}, newNames: []string{"Alice"},
				autoTrack: []string{"Mallory", "Zed"}},
		},
		{
			name:      "an all-unmatched submission plans no direct changes",
			submitted: map[string]int{"Zed": 10, "Mallory": 20},
			want:      want{changes: map[int64]int{}, autoTrack: []string{"Mallory", "Zed"}},
		},
		{
			name:      "auto-track never hides conflicts on matched rows",
			submitted: map[string]int{"Bob": 600, "Zed": 10},
			want: want{changes: map[int64]int{},
				conflicts: []scoreConflict{{Name: "Bob", Existing: 500, Incoming: 600}},
				autoTrack: []string{"Zed"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planSubmission(tracked, c.submitted, c.overwrite)

			gotChanges := map[int64]int{}
			for _, ch := range p.Changes {
				gotChanges[ch.CharacterID] = ch.Score
			}
			if len(gotChanges) != len(c.want.changes) {
				t.Errorf("changes = %v, want %v", gotChanges, c.want.changes)
			}
			for id, score := range c.want.changes {
				if got, ok := gotChanges[id]; !ok || got != score {
					t.Errorf("change for character %d = %d (present %v), want %d", id, got, ok, score)
				}
			}
			if got := strings.Join(p.NewNames, ","); got != strings.Join(c.want.newNames, ",") {
				t.Errorf("NewNames = %v, want %v", p.NewNames, c.want.newNames)
			}
			if got := strings.Join(p.OverwrittenNames, ","); got != strings.Join(c.want.overwritten, ",") {
				t.Errorf("OverwrittenNames = %v, want %v", p.OverwrittenNames, c.want.overwritten)
			}
			if len(p.Changes) != len(c.want.newNames)+len(c.want.overwritten) {
				t.Errorf("len(Changes) = %d, want new+overwritten = %d",
					len(p.Changes), len(c.want.newNames)+len(c.want.overwritten))
			}
			if len(p.Conflicts) != len(c.want.conflicts) {
				t.Errorf("conflicts = %v, want %v", p.Conflicts, c.want.conflicts)
			} else {
				gotConf := map[string]scoreConflict{}
				for _, cf := range p.Conflicts {
					gotConf[cf.Name] = cf
				}
				for _, cf := range c.want.conflicts {
					if gotConf[cf.Name] != cf {
						t.Errorf("conflict for %s = %+v, want %+v", cf.Name, gotConf[cf.Name], cf)
					}
				}
			}
			names := func(entries []helpers.ScoreEntry) []string {
				out := make([]string, 0, len(entries))
				for _, e := range entries {
					out = append(out, e.Name)
				}
				return out
			}
			if got := names(p.AutoTrack); strings.Join(got, ",") != strings.Join(c.want.autoTrack, ",") {
				t.Errorf("autoTrack = %v, want %v", got, c.want.autoTrack)
			}
		})
	}
}

// TestOverwriteDecision pins the pure resubmit-to-confirm rule: a pending key
// matching this (submitter, message, week) turns conflicts into an overwrite;
// otherwise conflicts store a pending confirmation and nothing is applied.
func TestOverwriteDecision(t *testing.T) {
	cases := []struct {
		name             string
		hasConflicts     bool
		pendingMatch     bool
		wantOverwrite    bool
		wantStorePending bool
	}{
		{"no conflicts, nothing pending", false, false, false, false},
		{"no conflicts ignores a stale pending key", false, true, false, false},
		{"conflicts without pending key store a confirmation", true, false, false, true},
		{"conflicts with matching pending key overwrite", true, true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			overwrite, storePending := overwriteDecision(c.hasConflicts, c.pendingMatch)
			if overwrite != c.wantOverwrite || storePending != c.wantStorePending {
				t.Errorf("overwriteDecision(%v, %v) = (%v, %v), want (%v, %v)",
					c.hasConflicts, c.pendingMatch, overwrite, storePending, c.wantOverwrite, c.wantStorePending)
			}
		})
	}
}

func TestFormatConflictsTableListsAllColumns(t *testing.T) {
	got := formatConflictsTable([]scoreConflict{
		{Name: "Bob", Existing: 500, Incoming: 0},
		{Name: "Dave", Existing: 900, Incoming: 100},
	})
	for _, expected := range []string{"CHARACTER", "EXISTING", "INCOMING", "Bob", "500", "Dave", "900", "100"} {
		if !strings.Contains(got, expected) {
			t.Errorf("conflicts table does not contain %q:\n%s", expected, got)
		}
	}
}
