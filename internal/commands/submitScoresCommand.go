package commands

import (
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

// submitScoresCommand is the /submit-scores slash command: it OCRs the
// screenshot(s) attached to the command and submits them, exactly like the
// right-click Submit Scores menu. Same guards, same safety gates, same
// ephemeral receipt; the only difference is the input arrives as command
// attachments instead of a target message. The resubmit-to-confirm overwrite
// window is scoped per submitter + week (there is no target message id).
func submitScoresCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	// Abuse guard: at most one concurrent submission per tenant (OCR is
	// CPU-heavy). A second concurrent run bounces with an ephemeral note.
	tenant := tenantOf(i)
	if !tryAcquireSubmit(tenant) {
		registerReply(s, i, submitBusyMessage)
		return
	}
	defer releaseSubmit(tenant)
	r := deferReply(s, i, true)

	imageURLs := commandImageURLs(i)
	if len(imageURLs) == 0 {
		r.editScreenshotFailure("Attach at least one screenshot to `/submit-scores`.")
		return
	}

	// Optional date option: submit these screenshots for a specific week
	// (YYYY-MM-DD or a Discord timestamp), refreshing that week's message.
	week := helpers.CurrentCulvertWeek(time.Now())
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "date" {
			d, err := parseFlexibleDate(opt.StringValue())
			if err != nil {
				r.Edit(badDateMessage)
				return
			}
			week = helpers.GetCulvertResetDate(d)
		}
	}

	scores := map[string]int{}
	parseWarnings, ok := scoresFromImageURLs(r, tenant, imageURLs, scores)
	if !ok {
		return
	}

	finalizeSubmitScores(s, r, i, scores, week, parseWarnings)
}

// commandImageURLs collects the image attachment URLs supplied to a slash
// command, preserving the declared option order so multi-page rosters merge
// deterministically. Non-image attachments are ignored.
func commandImageURLs(i *discordgo.InteractionCreate) []string {
	d := i.ApplicationCommandData()
	if d.Resolved == nil {
		return nil
	}
	urls := []string{}
	for _, opt := range d.Options {
		if opt.Type != discordgo.ApplicationCommandOptionAttachment {
			continue
		}
		id, _ := opt.Value.(string)
		if a, ok := d.Resolved.Attachments[id]; ok && a != nil && isImageAttachment(a) {
			urls = append(urls, a.URL)
		}
	}
	return urls
}
