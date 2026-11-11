package commands

//lint:file-ignore ST1001 Dot imports by jet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// finalizeSubmitScores runs the score submission flow once a scores map
// (character name -> score) has been parsed: it canonicalizes unknown names
// against the official rankings, plans the submission against the tracked
// roster and existing week data (always overwriting existing scores),
// auto-tracks unknown names, applies the changes and replies with an ephemeral
// embed receipt matching the weekly embeds. pages carries the parsed
// screenshots for the weekly archive channel (nil when the submission had no
// images, e.g. the .txt/.json path).
func finalizeSubmitScores(s *discordgo.Session, r *reply, i *discordgo.InteractionCreate, submitted map[string]int, week time.Time, parseWarnings string, pages []apihelpers.ScreenshotPage) {
	if len(submitted) == 0 {
		r.Edit("Nothing was parsed from that input - no changes were made.")
		return
	}
	tenant := tenantOf(i)
	weekLabel := helpers.FormatWeekLabel(week, time.Now())

	ids, rosterWarning, err := SubmissionRoster(db.DB, tenant)
	if err != nil {
		r.Edit("Internal server error while querying active characters.")
		return
	}
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		r.Edit("Score submission failed - no scores were written.")
		return
	}
	defer tx.Rollback()
	if err := LockSubmissionTenant(context.Background(), tx, tenant); err != nil {
		r.Edit("Score submission failed - no scores were written.")
		return
	}
	result, err := ApplyScoreSubmission(context.Background(), tx, tenant, week, submitted, ids, rankingsLookup(tenant))
	if err != nil {
		log.Println("submitScores: shared apply failed:", err)
		r.Edit("Score submission failed - no scores were written.")
		return
	}
	if err := tx.Commit(); err != nil {
		r.Edit("Score submission outcome is uncertain; check the recorded scores before retrying.")
		return
	}
	plan, autoTracked, unverified := result.Plan, result.AutoTracked, result.Unverified

	// Receipt embed: inline stat tiles (recorded + coverage) and one labeled
	// field per name list, matching the weekly grid. Contextual notes
	// (unverified spellings, announcement status, warnings) go in the
	// description. Long lists are trimmed with capList to stay under the limit.
	scoredAfter := result.ScoredAfter
	totalTracked := result.TotalTracked
	fields := []*discordgo.MessageEmbedField{
		{Name: "Recorded", Value: fmt.Sprintf("**%d** score(s)", len(plan.Changes)), Inline: true},
		{Name: "Coverage", Value: fmt.Sprintf("%d of %d tracked", scoredAfter, totalTracked), Inline: true},
	}
	if len(plan.NewNames) > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("New (%d)", len(plan.NewNames)),
			Value: "`" + strings.Join(capList(plan.NewNames, 20), "`, `") + "`",
		})
	}
	if len(plan.OverwrittenNames) > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("Overwritten (%d)", len(plan.OverwrittenNames)),
			Value: "`" + strings.Join(capList(plan.OverwrittenNames, 20), "`, `") + "`",
		})
	}
	if len(autoTracked) > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("Auto-tracked (%d)", len(autoTracked)),
			Value: "`" + strings.Join(capList(autoTracked, 20), "`, `") + "` - members can `/register` to claim them",
		})
	}

	desc := &strings.Builder{}
	if len(unverified) > 0 {
		fmt.Fprintf(desc, ":warning: Spelling unverified (not found on the official rankings): `%s` - fix mistakes with `/set-culvert` after `/unregister`-ing the typo.",
			strings.Join(capList(unverified, 10), "`, `"))
	}
	if len(plan.Changes) > 0 {
		changedIDs := make([]int64, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			changedIDs = append(changedIDs, c.CharacterID)
		}
		desc.WriteString(announcementStatusLine(apihelpers.AnnounceSubmission(s, db.DB, apiredis.RedisDB, tenant, week, changedIDs)))
	}
	// The screenshot archive updates on EVERY screenshot submission, even a
	// no-change resubmission: the images themselves are newer, and the
	// freshest version of each page is exactly what the archive keeps.
	if len(pages) > 0 {
		desc.WriteString(archiveStatusLine(apihelpers.ArchiveWeekScreenshots(s, db.DB, apiredis.RedisDB, tenant, week, pages)))
	}
	desc.WriteString(parseWarnings)
	desc.WriteString(rosterWarning)

	r.EditData(&discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{{
			Title:       "Scores recorded - " + weekLabel,
			Color:       apihelpers.WeekEmbedColor,
			Description: strings.TrimSpace(desc.String()),
			Fields:      fields,
		}},
	})
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
// archiveStatusLine renders the screenshot-archive outcome for the receipt.
// An unconfigured archive is SILENT - it is an opt-in extra, not something to
// nag every submission about (unlike the weekly channel).
func archiveStatusLine(err error) string {
	switch {
	case err == nil:
		return "\nScreenshot archive updated."
	case errors.Is(err, apihelpers.ErrNoScreenshotChannel):
		return ""
	default:
		return "\n:warning: Screenshot archive update failed: " + err.Error()
	}
}

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
