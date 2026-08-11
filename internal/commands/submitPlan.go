package commands

import (
	"sort"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// trackedScore is one tracked character's state for the target culvert week.
type trackedScore struct {
	ID       int64
	Name     string
	Existing *int // nil = no score row for that week
}

// scoreConflict is a submitted value that differs from the already-stored one.
type scoreConflict struct {
	Name     string
	Existing int
	Incoming int
}

// submissionPlan is the outcome of planning a submission against current data.
type submissionPlan struct {
	Changes     []helpers.ScoreChange // rows to upsert
	New         int                   // characters with no prior score this week
	Overwritten int                   // existing scores replaced by a submitted value
	ZeroFilled  int                   // absent characters written as 0 (zero-missing)
	Conflicts   []scoreConflict       // differing values blocked by the overwrite guard
	Unmatched   []helpers.ScoreEntry  // submitted names that are not tracked (name-sorted)
}

// planSubmission decides which rows a submission writes. Rules:
//   - submitted names not in tracked land in Unmatched (the caller rejects)
//   - a value equal to the existing score is a no-op
//   - ANY differing value - including downgrading a score to 0 - is a conflict
//     unless overwrite is set (conflicts are all collected, never just the first)
//   - tracked characters absent from submitted are NEVER touched by default;
//     with zeroMissing, absent characters without a score are zero-filled, and
//     absent characters with a non-zero score are zero-filled only when
//     overwrite is also set (a conflict otherwise)
func planSubmission(tracked []trackedScore, submitted map[string]int, overwrite, zeroMissing bool) submissionPlan {
	p := submissionPlan{}
	seen := map[string]bool{}
	for _, t := range tracked {
		score, ok := submitted[t.Name]
		if ok {
			seen[t.Name] = true
		}
		switch {
		case ok && t.Existing == nil:
			p.Changes = append(p.Changes, helpers.ScoreChange{CharacterID: t.ID, Score: score})
			p.New++
		case ok && *t.Existing == score:
			// identical resubmission: no-op
		case ok && !overwrite:
			p.Conflicts = append(p.Conflicts, scoreConflict{Name: t.Name, Existing: *t.Existing, Incoming: score})
		case ok:
			p.Changes = append(p.Changes, helpers.ScoreChange{CharacterID: t.ID, Score: score})
			p.Overwritten++
		case !zeroMissing:
			// absent from the submission: never touched by default
		case t.Existing == nil:
			p.Changes = append(p.Changes, helpers.ScoreChange{CharacterID: t.ID, Score: 0})
			p.ZeroFilled++
		case *t.Existing == 0:
			// already zero: no-op
		case !overwrite:
			p.Conflicts = append(p.Conflicts, scoreConflict{Name: t.Name, Existing: *t.Existing, Incoming: 0})
		default:
			p.Changes = append(p.Changes, helpers.ScoreChange{CharacterID: t.ID, Score: 0})
			p.ZeroFilled++
		}
	}
	for name, score := range submitted {
		if !seen[name] {
			p.Unmatched = append(p.Unmatched, helpers.ScoreEntry{Name: name, Score: score})
		}
	}
	sort.Slice(p.Unmatched, func(a, b int) bool { return p.Unmatched[a].Name < p.Unmatched[b].Name })
	return p
}

// formatAnnotatedScoresTable renders the FULL parsed row set in the given
// order, marking each row "tracked" or "NEW" (never an unmatched-only view).
func formatAnnotatedScoresTable(entries []helpers.ScoreEntry, tracked map[string]bool) string {
	t := table.NewWriter()
	t.AppendHeader(table.Row{"Character", "Score", "Status"})
	for _, e := range entries {
		status := "tracked"
		if !tracked[e.Name] {
			status = "NEW"
		}
		t.AppendRow(table.Row{e.Name, e.Score, status})
	}
	return t.Render()
}

func formatConflictsTable(conflicts []scoreConflict) string {
	t := table.NewWriter()
	t.AppendHeader(table.Row{"Character", "Existing", "Incoming"})
	for _, c := range conflicts {
		t.AppendRow(table.Row{c.Name, c.Existing, c.Incoming})
	}
	return t.Render()
}

// countTracked returns how many entries are tracked characters.
func countTracked(entries []helpers.ScoreEntry, tracked map[string]bool) int {
	n := 0
	for _, e := range entries {
		if tracked[e.Name] {
			n++
		}
	}
	return n
}

// trackedSetOf derives the tracked-name set from a full parse and its
// unmatched subset.
func trackedSetOf(all, unmatched []helpers.ScoreEntry) map[string]bool {
	set := make(map[string]bool, len(all))
	for _, e := range all {
		set[e.Name] = true
	}
	for _, e := range unmatched {
		delete(set, e.Name)
	}
	return set
}
