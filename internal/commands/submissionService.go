package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// ImageSubmission is transport-neutral. Original images and nonfatal warnings
// survive every adapter; a fatal semantic gate produces no score map.
type ImageSubmission struct {
	Scores   map[string]int
	Pages    []apihelpers.ScreenshotPage
	Warnings string
}

func ParseSubmissionImages(tenant string, images [][]byte) (*ImageSubmission, error) {
	if len(images) == 0 || len(images) > 20 {
		return nil, errors.New("a submission requires 1 to 20 images")
	}
	total := 0
	for _, b := range images {
		total += len(b)
		if len(b) > 32<<20 || total > 64<<20 {
			return nil, errors.New("image batch exceeds limits")
		}
	}
	oc, err := ocrBytesToScores(tenant, images)
	if err != nil {
		return nil, err
	}
	result := &ImageSubmission{Scores: map[string]int{}}
	result.Pages, result.Warnings, err = submissionImagesResult(oc, result.Scores)
	if err != nil {
		return nil, err
	}
	if len(result.Scores) == 0 {
		return nil, errors.New("nothing was parsed; no scores were written")
	}
	return result, nil
}

// SubmissionRoster preserves the existing optional role/cache filtering.
func SubmissionRoster(dbc *sql.DB, tenant string) ([]int64, string, error) {
	chars, meta, err := helpers.GetActiveCharactersWithMeta(apiredis.RedisDB, dbc, tenant)
	if err != nil {
		return nil, "", err
	}
	ids := make([]int64, 0, len(*chars))
	for _, c := range *chars {
		ids = append(ids, c.ID)
	}
	return ids, meta.StalenessWarning(), nil
}

func SubmissionRankings(tenant string) func(string) (string, bool) { return rankingsLookup(tenant) }

// The same PostgreSQL transaction lock is used by manual and API submissions.
func LockSubmissionTenant(ctx context.Context, tx *sql.Tx, tenant string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 45139))`, tenant)
	return err
}

type ScoreSubmissionResult struct {
	Plan             submissionPlan `json:"-"`
	AutoTracked      []string       `json:"auto_tracked"`
	Unverified       []string       `json:"unverified_names"`
	NewNames         []string       `json:"new_names"`
	OverwrittenNames []string       `json:"overwritten_names"`
	ChangedIDs       []int64        `json:"changed_character_ids"`
	Recorded         int            `json:"recorded"`
	Unchanged        int            `json:"unchanged"`
	ScoredAfter      int            `json:"scored_after"`
	TotalTracked     int            `json:"total_tracked"`
}

// ApplyScoreSubmission requires LockSubmissionTenant and the caller's transaction.
// Auto-tracking and all changed scores therefore commit with an API receipt, or
// roll back together. It neither commits nor sends a Discord message.
func ApplyScoreSubmission(ctx context.Context, tx *sql.Tx, tenant string, week time.Time, submitted map[string]int, activeIDs []int64, lookup func(string) (string, bool)) (*ScoreSubmissionResult, error) {
	if tenant == "" || len(submitted) == 0 {
		return nil, errors.New("empty submission")
	}
	for name, score := range submitted {
		if name == "" || len(name) > 255 || score < 0 || int64(score) > 2147483647 {
			return nil, errors.New("invalid score entry")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.maple_character_name,s.score FROM characters c LEFT JOIN character_culvert_scores s ON s.character_id=c.id AND s.culvert_date=$1 WHERE c.guild_id=$2 AND c.id=ANY($3) ORDER BY c.maple_character_name`, week.Format(time.DateOnly), tenant, pq.Array(activeIDs))
	if err != nil {
		return nil, err
	}
	tracked := []trackedScore{}
	existingCount := 0
	for rows.Next() {
		var t trackedScore
		var score sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &score); err != nil {
			rows.Close()
			return nil, err
		}
		if score.Valid {
			n := int(score.Int64)
			t.Existing = &n
			existingCount++
		}
		tracked = append(tracked, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	result := &ScoreSubmissionResult{Unverified: canonicalizeSubmittedWith(submitted, tracked, lookup)}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan := planSubmission(tracked, submitted, true)
	names := make([]string, 0, len(plan.AutoTrack))
	for _, e := range plan.AutoTrack {
		names = append(names, e.Name)
	}
	created, ids, err := helpers.TrackCharacters(tx, tenant, names)
	if err != nil {
		return nil, err
	}
	result.AutoTracked = created
	for _, e := range plan.AutoTrack {
		id, ok := ids[e.Name]
		if !ok {
			return nil, fmt.Errorf("missing tracked character")
		}
		// A filtered-out existing character remains tenant-bound and preserves the
		// manual command's auto-track path. Equality is still a genuine no-op.
		var prior int
		err := tx.QueryRowContext(ctx, `SELECT score FROM character_culvert_scores WHERE character_id=$1 AND culvert_date=$2`, id, week.Format(time.DateOnly)).Scan(&prior)
		if err == nil && prior == e.Score {
			continue
		}
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		plan.Changes = append(plan.Changes, helpers.ScoreChange{CharacterID: id, Score: e.Score})
	}
	for _, c := range plan.Changes {
		_, err := tx.ExecContext(ctx, `INSERT INTO character_culvert_scores(character_id,culvert_date,score,updated_at) VALUES($1,$2,$3,NOW()) ON CONFLICT(culvert_date,character_id) DO UPDATE SET score=EXCLUDED.score,updated_at=NOW()`, c.CharacterID, week.Format(time.DateOnly), c.Score)
		if err != nil {
			return nil, err
		}
		result.ChangedIDs = append(result.ChangedIDs, c.CharacterID)
	}
	result.Plan = plan
	result.NewNames = plan.NewNames
	result.OverwrittenNames = plan.OverwrittenNames
	result.Recorded = len(plan.Changes)
	result.Unchanged = len(submitted) - len(plan.Changes)
	result.ScoredAfter = existingCount + len(plan.NewNames) + len(plan.AutoTrack)
	result.TotalTracked = len(tracked) + len(created)
	return result, nil
}
