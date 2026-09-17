package commands

import (
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// submitScoresCommand starts a screenshot collection session, or submits an
// existing message's images immediately when message-link is supplied.
func submitScoresCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
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
				registerReply(s, i, badDateMessage)
				return
			}
			week = helpers.GetCulvertResetDate(d)
		case "message-link":
			messageLink = opt.StringValue()
		}
	}

	if messageLink == "" {
		startScreenshotSubmission(s, i, week)
		return
	}

	// Only processing holds the tenant's OCR guard; collecting screenshots
	// leaves other submitters free to prepare their own submissions.
	tenant := tenantOf(i)
	if !tryAcquireSubmit(tenant) {
		registerReply(s, i, submitBusyMessage)
		return
	}
	defer releaseSubmit(tenant)
	r := deferReply(s, i, true)

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
	// A message-link must point at THIS server's data. A REST-fetched message
	// has no guild_id, so fall back to the channel's guild; an unresolved
	// guild ("") fails closed.
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
	scores := map[string]int{}
	pages, parseWarnings, ok := scoresFromMessage(s, r, tenant, msg, scores)
	if !ok {
		return
	}

	finalizeSubmitScores(s, r, i, scores, week, parseWarnings, pages)
}
