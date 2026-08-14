package commands

import (
	"sort"

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
	Changes          []helpers.ScoreChange // rows to upsert
	NewNames         []string              // characters with no prior score this week
	OverwrittenNames []string              // existing scores replaced by a submitted value
	Conflicts        []scoreConflict       // differing values blocked by the overwrite guard
	AutoTrack        []helpers.ScoreEntry  // untracked names to create ('2') and score (name-sorted)
}

// planSubmission decides which rows a submission writes. Rules:
//   - submitted names not in tracked land in AutoTrack (auto-track is always
//     on: the caller creates them as unlinked-but-tracked and scores them)
//   - a value equal to the existing score is a no-op
//   - ANY differing value - including downgrading a score to 0 - is a conflict
//     unless overwrite is set (conflicts are all collected, never just the
//     first); live submissions always pass overwrite=true, so conflicts are a
//     planning-only signal retained for tests
//   - tracked characters absent from submitted are NEVER touched (no
//     zero-filling exists anymore)
func planSubmission(tracked []trackedScore, submitted map[string]int, overwrite bool) submissionPlan {
	p := submissionPlan{}
	seen := map[string]bool{}
	for _, t := range tracked {
		score, ok := submitted[t.Name]
		if ok {
			seen[t.Name] = true
		}
		switch {
		case !ok:
			// absent from the submission: never touched
		case t.Existing == nil:
			p.Changes = append(p.Changes, helpers.ScoreChange{CharacterID: t.ID, Score: score})
			p.NewNames = append(p.NewNames, t.Name)
		case *t.Existing == score:
			// identical resubmission: no-op
		case !overwrite:
			p.Conflicts = append(p.Conflicts, scoreConflict{Name: t.Name, Existing: *t.Existing, Incoming: score})
		default:
			p.Changes = append(p.Changes, helpers.ScoreChange{CharacterID: t.ID, Score: score})
			p.OverwrittenNames = append(p.OverwrittenNames, t.Name)
		}
	}
	unmatched := []helpers.ScoreEntry{}
	for name, score := range submitted {
		if !seen[name] {
			unmatched = append(unmatched, helpers.ScoreEntry{Name: name, Score: score})
		}
	}
	sort.Slice(unmatched, func(a, b int) bool { return unmatched[a].Name < unmatched[b].Name })
	p.AutoTrack = unmatched
	return p
}
