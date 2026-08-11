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
		newCount    int
		overwritten int
		zeroFilled  int
		conflicts   []scoreConflict
		unmatched   []string
	}
	cases := []struct {
		name        string
		submitted   map[string]int
		overwrite   bool
		zeroMissing bool
		want        want
	}{
		{
			name:      "empty input plans nothing",
			submitted: map[string]int{},
			want:      want{changes: map[int64]int{}},
		},
		{
			name:      "new score inserts",
			submitted: map[string]int{"Alice": 100},
			want:      want{changes: map[int64]int{1: 100}, newCount: 1},
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
			want: want{changes: map[int64]int{1: 50}, newCount: 1,
				conflicts: []scoreConflict{
					{Name: "Bob", Existing: 500, Incoming: 0},
					{Name: "Dave", Existing: 900, Incoming: 100},
				}},
		},
		{
			name:      "differing value with overwrite overwrites",
			submitted: map[string]int{"Bob": 600},
			overwrite: true,
			want:      want{changes: map[int64]int{2: 600}, overwritten: 1},
		},
		{
			name:      "0 downgrade with overwrite overwrites",
			submitted: map[string]int{"Bob": 0},
			overwrite: true,
			want:      want{changes: map[int64]int{2: 0}, overwritten: 1},
		},
		{
			name:      "absent tracked characters are never touched by default",
			submitted: map[string]int{"Bob": 500},
			want:      want{changes: map[int64]int{}},
		},
		{
			name:        "zero-missing fills absent characters without a score",
			submitted:   map[string]int{"Bob": 500},
			zeroMissing: true,
			// Alice (no score) is zero-filled; Carol already 0 is a no-op;
			// Dave (900, absent) needs overwrite -> conflict.
			want: want{changes: map[int64]int{1: 0}, zeroFilled: 1,
				conflicts: []scoreConflict{{Name: "Dave", Existing: 900, Incoming: 0}}},
		},
		{
			name:        "zero-missing with overwrite downgrades absent non-zero scores",
			submitted:   map[string]int{"Bob": 500},
			zeroMissing: true,
			overwrite:   true,
			want:        want{changes: map[int64]int{1: 0, 4: 0}, zeroFilled: 2},
		},
		{
			name:      "unmatched names are collected sorted",
			submitted: map[string]int{"Zed": 10, "Alice": 100, "Mallory": 20},
			want: want{changes: map[int64]int{1: 100}, newCount: 1,
				unmatched: []string{"Mallory", "Zed"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planSubmission(tracked, c.submitted, c.overwrite, c.zeroMissing)

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
			if p.New != c.want.newCount || p.Overwritten != c.want.overwritten || p.ZeroFilled != c.want.zeroFilled {
				t.Errorf("counts new/overwritten/zeroFilled = %d/%d/%d, want %d/%d/%d",
					p.New, p.Overwritten, p.ZeroFilled, c.want.newCount, c.want.overwritten, c.want.zeroFilled)
			}
			if len(p.Changes) != c.want.newCount+c.want.overwritten+c.want.zeroFilled {
				t.Errorf("len(Changes) = %d, want new+overwritten+zeroFilled = %d",
					len(p.Changes), c.want.newCount+c.want.overwritten+c.want.zeroFilled)
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
			gotUnmatched := make([]string, 0, len(p.Unmatched))
			for _, u := range p.Unmatched {
				gotUnmatched = append(gotUnmatched, u.Name)
			}
			if strings.Join(gotUnmatched, ",") != strings.Join(c.want.unmatched, ",") {
				t.Errorf("unmatched = %v, want %v", gotUnmatched, c.want.unmatched)
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

func TestTrackedSetOf(t *testing.T) {
	all := []helpers.ScoreEntry{{Name: "A"}, {Name: "B"}, {Name: "C"}}
	unmatched := []helpers.ScoreEntry{{Name: "B"}}
	set := trackedSetOf(all, unmatched)
	if !set["A"] || set["B"] || !set["C"] {
		t.Fatalf("trackedSetOf = %v, want A and C tracked, B not", set)
	}
}
