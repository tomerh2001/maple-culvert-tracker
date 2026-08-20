package commands

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// pendingResetWeekTTL is how long a /reset-week run-again-to-confirm window
// stays open.
const pendingResetWeekTTL = 10 * time.Minute

// resetWeekDecision is the pure run-again-to-confirm rule for /reset-week
// ("removes all of the inputs from this week"). Given the tenant's recorded
// score count for the current week and whether a pending confirmation
// matching (tenant, invoker, week) exists:
//   - nothing recorded: nothing to delete, nothing pending to store
//   - scores + matching pending key: delete NOW (and the caller clears the key)
//   - scores + no pending key: delete nothing, store the pending key and ask
//     the admin to run /reset-week again within the TTL
func resetWeekDecision(scoreCount int, pendingMatch bool) (deleteNow, storePending bool) {
	switch {
	case scoreCount == 0:
		return false, false
	case pendingMatch:
		return true, false
	default:
		return false, true
	}
}

// resetWeekCommand deletes ALL of the invoking server's culvert scores for
// the CURRENT week - the recovery hatch for a botched submission run.
// Characters stay tracked; only the week's score rows go. Admin-gated (same
// gate as /config), ephemeral, tenant-scoped, and guarded by the bot's
// destructive-action idiom: the first invocation shows what would be deleted
// and asks to run the command again within 10 minutes.
func resetWeekCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !isAdmin(i) {
		registerReply(s, i, "Only server admins can use `/reset-week`.")
		return
	}
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	week := helpers.CurrentCulvertWeek(time.Now())
	weekStr := week.Format(time.DateOnly)
	weekLabel := helpers.FormatWeekLabel(week, time.Now())

	n, err := helpers.CountWeekScores(db.DB, tenant, week)
	if err != nil {
		log.Println("reset-week: count failed:", err)
		r.Edit("Something went wrong counting this week's scores. Please try again later.")
		return
	}

	pendingKey := apiredis.PendingResetWeekKey(i.Member.User.ID, weekStr).For(tenant)
	pendingMatch := pendingKey.GetWithDefault(apiredis.RedisDB, "") != ""
	deleteNow, storePending := resetWeekDecision(n, pendingMatch)

	if !deleteNow && !storePending {
		r.Edit("Nothing recorded yet for " + weekLabel + " - nothing to reset.")
		return
	}
	if storePending {
		if err := pendingKey.SetEx(apiredis.RedisDB, "1", pendingResetWeekTTL); err != nil {
			log.Println("reset-week: storing pending confirmation failed:", err)
			r.Edit("Something went wrong preparing the confirmation. Please try again later.")
			return
		}
		r.Edit(fmt.Sprintf(":warning: This will delete **%d** recorded score(s) for %s.\nRun `/reset-week` again within 10 minutes to confirm.", n, weekLabel))
		return
	}

	deleted, err := helpers.DeleteWeekScores(db.DB, tenant, week)
	if err != nil {
		log.Println("reset-week: delete failed:", err)
		r.Edit("Something went wrong deleting the scores - nothing may have been deleted. See server logs.")
		return
	}
	if err := pendingKey.Del(apiredis.RedisDB); err != nil && !errors.Is(err, apiredis.ErrNoRedis) {
		log.Println("reset-week: clearing pending confirmation failed:", err)
	}

	receipt := fmt.Sprintf("Deleted **%d** score(s) for %s. Characters stay tracked - resubmit anytime.", deleted, weekLabel)

	// Refresh the week's announcement (if one was posted) so the table never
	// keeps showing deleted scores; the empty state renders as zero coverage.
	if err := apihelpers.RefreshWeeklyAnnouncement(s, db.DB, apiredis.RedisDB, tenant, week); err != nil {
		receipt += "\n:warning: Weekly announcement refresh failed: " + err.Error()
	}
	// The wiped scores' screenshots must not keep posing as the week's record:
	// empty the archive message too (it stays, noting the reset, and the next
	// submission refills it).
	if err := apihelpers.ClearWeekScreenshots(s, db.DB, tenant, week); err != nil {
		receipt += "\n:warning: Screenshot archive clear failed: " + err.Error()
	}
	r.Edit(receipt)
}
