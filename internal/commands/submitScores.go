package commands

//lint:file-ignore ST1001 Dot imports by jet

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// pendingOverwriteTTL is how long a resubmit-to-confirm window stays open.
const pendingOverwriteTTL = 10 * time.Minute

// finalizeSubmitScores runs the score submission flow once a scores map
// (character name -> score) has been parsed from the target message: it
// canonicalizes unknown names against the official rankings, plans the
// submission against the tracked roster and existing week data, applies the
// resubmit-to-confirm overwrite rule, auto-tracks unknown names, applies the
// changes and replies with a text-only receipt. targetMessageID scopes the
// pending overwrite confirmation to the exact message being resubmitted.
func finalizeSubmitScores(s *discordgo.Session, r *reply, i *discordgo.InteractionCreate, targetMessageID, resubmitHint string, submitted map[string]int, week time.Time, parseWarnings string) {
	if len(submitted) == 0 {
		r.Edit("Nothing was parsed from that input - no changes were made.")
		return
	}
	tenant := tenantOf(i)
	weekStr := week.Format(time.DateOnly)
	weekLabel := helpers.FormatWeekLabel(week, time.Now())

	characters, rosterMeta, err := helpers.GetActiveCharactersWithMeta(apiredis.RedisDB, db.DB, tenant)
	if err != nil {
		log.Println("submitScores: Error querying active characters:", err)
		r.Edit("Internal server error while querying active characters. Please try again later.")
		return
	}

	tracked := []trackedScore{}
	if len(*characters) > 0 {
		characterIDs := []Expression{}
		for _, v := range *characters {
			characterIDs = append(characterIDs, Int64(v.ID))
		}

		stmt := SELECT(Characters.ID.AS("id"), Characters.MapleCharacterName.AS("maple_character_name"), CharacterCulvertScores.Score.AS("score")).FROM(Characters.LEFT_JOIN(CharacterCulvertScores, Characters.ID.EQ(CharacterCulvertScores.CharacterID).AND(CharacterCulvertScores.CulvertDate.EQ(DateT(week))))).WHERE(Characters.ID.IN(characterIDs...))

		trackedCharacterScores := []struct {
			ID                 int
			MapleCharacterName string
			Score              sql.NullInt64
		}{}

		err = stmt.Query(db.DB, &trackedCharacterScores)
		if err != nil {
			log.Println("submitScores: Error querying tracked character scores:", err)
			r.Edit("Internal server error while querying existing scores. Please try again later.")
			return
		}

		for _, v := range trackedCharacterScores {
			t := trackedScore{ID: int64(v.ID), Name: v.MapleCharacterName}
			if v.Score.Valid {
				existing := int(v.Score.Int64)
				t.Existing = &existing
			}
			tracked = append(tracked, t)
		}
	}

	// Canonicalize would-be-new names against the official rankings before
	// planning: exact hits adopt the rankings spelling, l/I bar mixups may
	// resolve onto an already-tracked character, and unresolved names keep
	// their decode with a "spelling unverified" receipt note.
	unverified := canonicalizeSubmitted(tenant, submitted, tracked)

	plan := planSubmission(tracked, submitted, false)

	pendingKey := apiredis.PendingOverwriteKey(i.Member.User.ID, targetMessageID, weekStr).For(tenant)
	pendingMatch := pendingKey.GetWithDefault(apiredis.RedisDB, "") != ""
	overwrite, storePending := overwriteDecision(len(plan.Conflicts) > 0, pendingMatch)

	if storePending {
		if err := pendingKey.SetEx(apiredis.RedisDB, "1", pendingOverwriteTTL); err != nil {
			log.Println("submitScores: storing pending overwrite confirmation failed:", err)
		}
		msg := fmt.Sprintf("%d existing score(s) for %s differ from the submitted values - no changes were made.",
			len(plan.Conflicts), weekLabel)
		msg += "\n" + resubmitHint
		r.EditWithTable(msg, formatConflictsTable(plan.Conflicts))
		return
	}
	if overwrite {
		plan = planSubmission(tracked, submitted, true)
	}

	existingCount := 0
	for _, t := range tracked {
		if t.Existing != nil {
			existingCount++
		}
	}

	autoTracked := []string{}
	autoScored := 0
	if len(plan.AutoTrack) > 0 {
		names := make([]string, 0, len(plan.AutoTrack))
		for _, e := range plan.AutoTrack {
			names = append(names, e.Name)
		}
		created, ids, err := helpers.TrackCharacters(db.DB, tenant, names)
		if err != nil {
			log.Println("submitScores: auto-track failed:", err)
			r.Edit("Score submission failed while tracking new characters - no scores were written. See server logs for details.")
			return
		}
		autoTracked = created
		for _, e := range plan.AutoTrack {
			id, ok := ids[e.Name]
			if !ok {
				log.Println("submitScores: auto-tracked character has no id:", e.Name)
				continue
			}
			plan.Changes = append(plan.Changes, helpers.ScoreChange{CharacterID: id, Score: e.Score})
			autoScored++
		}
	}

	if err := helpers.UpsertCulvertScores(db.DB, week, plan.Changes); err != nil {
		log.Println("submitScores: upsert failed:", err)
		r.Edit("Score submission failed while writing to the database - no scores were written. See server logs for details.")
		return
	}
	// Any successful apply consumes/invalidates the pending confirmation.
	if err := pendingKey.Del(apiredis.RedisDB); err != nil && !errors.Is(err, apiredis.ErrNoRedis) {
		log.Println("submitScores: clearing pending overwrite confirmation failed:", err)
	}

	// Receipt: recorded/new/overwritten/auto-tracked + coverage +
	// announcement status, all labeled with the week helper. Text only.
	receipt := fmt.Sprintf("Recorded %d score(s) for %s.", len(plan.Changes), weekLabel)
	if len(plan.NewNames) > 0 {
		receipt += fmt.Sprintf("\nNew (%d): `%s`", len(plan.NewNames), strings.Join(capList(plan.NewNames, 20), "`, `"))
	}
	if len(plan.OverwrittenNames) > 0 {
		receipt += fmt.Sprintf("\nOverwritten (%d): `%s`", len(plan.OverwrittenNames), strings.Join(capList(plan.OverwrittenNames, 20), "`, `"))
	}
	if len(autoTracked) > 0 {
		receipt += fmt.Sprintf("\nAuto-tracked (%d): `%s` (members can `/register` to claim them)",
			len(autoTracked), strings.Join(capList(autoTracked, 20), "`, `"))
	}
	if len(unverified) > 0 {
		receipt += "\n:warning: Spelling unverified (not found on the official rankings): `" +
			strings.Join(capList(unverified, 10), "`, `") + "` - fix mistakes with `/set-culvert` after `/unregister`-ing the typo."
	}
	scoredAfter := existingCount + len(plan.NewNames) + autoScored
	totalTracked := len(tracked) + len(autoTracked)
	receipt += fmt.Sprintf("\nCoverage: %d of %d tracked characters have a score for this week.", scoredAfter, totalTracked)

	if len(plan.Changes) > 0 {
		changedIDs := make([]int64, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			changedIDs = append(changedIDs, c.CharacterID)
		}
		receipt += announcementStatusLine(apihelpers.AnnounceSubmission(s, db.DB, apiredis.RedisDB, tenant, week, changedIDs))
	}
	receipt += parseWarnings
	receipt += rosterMeta.StalenessWarning()

	r.EditChunked(receipt)
}

// canonicalizeSubmitted rewrites submitted keys that match no tracked
// character: rankings-verified spellings replace the decode (possibly folding
// onto a tracked character), unresolved names stay and are returned as the
// unverified list. One rankings cache serves the whole submission.
func canonicalizeSubmitted(tenantID string, submitted map[string]int, tracked []trackedScore) []string {
	return canonicalizeSubmittedWith(submitted, tracked, rankingsLookup(tenantID))
}

// canonicalizeSubmittedWith is the lookup-injected core (unit-tested with a
// faked rankings lookup).
func canonicalizeSubmittedWith(submitted map[string]int, tracked []trackedScore, lookup func(string) (string, bool)) (unverified []string) {
	trackedByLower := map[string]string{}
	for _, t := range tracked {
		trackedByLower[strings.ToLower(t.Name)] = t.Name
	}
	// Snapshot the keys: the loop rewrites map entries as names resolve.
	names := make([]string, 0, len(submitted))
	for name := range submitted {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		score := submitted[name]
		if _, ok := trackedByLower[strings.ToLower(name)]; ok {
			continue
		}
		canonical, verified := canonicalizeName(name, lookup)
		if !verified {
			unverified = append(unverified, name)
			continue
		}
		if canonical == name {
			continue
		}
		// Adopt the rankings spelling; when it resolves onto a tracked
		// character fold the score onto that name (never clobbering a score
		// that character already has in this submission).
		target := canonical
		if t, ok := trackedByLower[strings.ToLower(canonical)]; ok {
			target = t
		}
		if _, exists := submitted[target]; !exists {
			delete(submitted, name)
			submitted[target] = score
		}
	}
	sort.Strings(unverified)
	return unverified
}

// announcementStatusLine renders the weekly-announcement outcome for a receipt.
func announcementStatusLine(err error) string {
	switch {
	case err == nil:
		return "\nWeekly announcement updated."
	case errors.Is(err, apihelpers.ErrNoWeeklyChannel):
		return "\n:warning: Weekly announcement skipped - no weekly channel configured (`/config`)."
	default:
		return "\n:warning: Weekly announcement failed: " + err.Error()
	}
}
