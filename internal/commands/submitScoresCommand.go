package commands

import (
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

const maxSubmitScreenshots = 20

func submitScoresCommandOptions() []*discordgo.ApplicationCommandOption {
	options := make([]*discordgo.ApplicationCommandOption, 0, maxSubmitScreenshots+2)
	for n := 1; n <= maxSubmitScreenshots; n++ {
		description := "Another page of the roster (optional)"
		if n == 1 {
			description = "A screenshot of the Guild - Member Participation Status window (or use message-link)"
		}
		options = append(options, &discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionAttachment,
			Name:        fmt.Sprintf("screenshot-%d", n),
			Description: description,
		})
	}
	return append(options,
		&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "date",
			Description: "Which week these are for (YYYY-MM-DD or a Discord timestamp; default: this week)",
		},
		&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "message-link",
			Description: "Submit an existing screenshot message's images instead (right click -> Copy Message Link)",
		},
	)
}

// submitScoresCommand is the /submit-scores slash command: it OCRs the
// screenshot(s) attached to the command (or, when a message-link is given, the
// images on that existing message) and submits them, exactly like the
// right-click Submit Scores menu. Same guards, same safety gates, same
// ephemeral receipt. Direct attachments take precedence over a message-link
// when both are supplied.
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

	// Options: an optional date (submit for a specific week, refreshing that
	// week's message) and an optional message-link (submit an existing
	// screenshot message's images into the chosen week).
	week := helpers.CurrentCulvertWeek(time.Now())
	messageLink := ""
	for _, opt := range i.ApplicationCommandData().Options {
		switch opt.Name {
		case "date":
			d, err := parseFlexibleDate(opt.StringValue())
			if err != nil {
				r.Edit(badDateMessage)
				return
			}
			week = helpers.GetCulvertResetDate(d)
		case "message-link":
			messageLink = opt.StringValue()
		}
	}

	scores := map[string]int{}
	var pages []apihelpers.ScreenshotPage
	var parseWarnings string
	var ok bool
	imageURLs := commandImageURLs(i)
	switch {
	case len(imageURLs) > 0:
		// Direct attachments win over a message-link when both are provided.
		pages, parseWarnings, ok = scoresFromImageURLs(r, tenant, imageURLs, scores)
	case messageLink != "":
		channelID, messageID, err := parseMessageLink(messageLink)
		if err != nil {
			r.Edit(badMessageLinkMessage)
			return
		}
		msg, err := s.ChannelMessage(channelID, messageID)
		if err != nil {
			log.Println("submitScoresCommand: fetch linked message:", err)
			r.Edit("Failed to fetch the linked message - check the link and that the bot can read that channel.")
			return
		}
		// A message-link must point at THIS server's data - never let one
		// tenant pull another server's screenshots (the link only carries
		// channel+message ids, so the bot could otherwise read any channel it
		// can see). A REST-fetched message has no guild_id, so fall back to the
		// channel's guild; an unresolved guild ("") fails closed.
		guildID := msg.GuildID
		if guildID == "" {
			if ch, cerr := s.Channel(channelID); cerr == nil && ch != nil {
				guildID = ch.GuildID
			}
		}
		if data.TenantID(guildID) != tenant {
			r.Edit("That message is from a different server. You can only submit a message-link from a channel in this server.")
			return
		}
		pages, parseWarnings, ok = scoresFromMessage(s, r, tenant, msg, scores)
	default:
		r.editScreenshotFailure(fmt.Sprintf("Attach a screenshot with `screenshot-1` (add more with `screenshot-2` through `screenshot-%d`), or pass a `message-link` to an existing screenshot message.", maxSubmitScreenshots))
		return
	}
	if !ok {
		return
	}

	finalizeSubmitScores(s, r, i, scores, week, parseWarnings, pages)
}

// commandImageURLs collects the image attachment URLs in screenshot slot
// order so multi-page rosters merge deterministically, regardless of the
// order in which the user selected the options. Non-image attachments are ignored.
func commandImageURLs(i *discordgo.InteractionCreate) []string {
	d := i.ApplicationCommandData()
	if d.Resolved == nil {
		return nil
	}
	attachments := make(map[string]string)
	for _, opt := range d.Options {
		if opt.Type != discordgo.ApplicationCommandOptionAttachment {
			continue
		}
		id, _ := opt.Value.(string)
		attachments[opt.Name] = id
	}
	urls := []string{}
	for n := 1; n <= maxSubmitScreenshots; n++ {
		id, supplied := attachments[fmt.Sprintf("screenshot-%d", n)]
		if !supplied {
			continue
		}
		if a, ok := d.Resolved.Attachments[id]; ok && a != nil && isImageAttachment(a) {
			urls = append(urls, a.URL)
		}
	}
	return urls
}
