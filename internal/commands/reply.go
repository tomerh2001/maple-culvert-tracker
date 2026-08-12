package commands

import (
	"log"

	"github.com/bwmarrin/discordgo"
)

// reply is a handle to a deferred interaction response, replacing the
// editContent closures that used to be duplicated across command handlers.
// Command replies are TEXT-ONLY by design: no file attachments, with exactly
// two sanctioned exceptions that go through EditData - the /culvert chart
// embed, and the Submit Scores OCR failure help, which attaches an example
// screenshot (editScreenshotFailure, a product-owner-mandated exception).
type reply struct {
	s         *discordgo.Session
	i         *discordgo.InteractionCreate
	ephemeral bool
}

// deferReply sends the Deferred interaction response (the "thinking..."
// placeholder) and returns a handle used to fill it in later with Edit.
// ephemeral=true keeps the reply (and every followup chunk) visible only to
// the invoking user - the default for all commands except /culvert and
// /culvert-board.
func deferReply(s *discordgo.Session, i *discordgo.InteractionCreate, ephemeral bool) *reply {
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}
	if ephemeral {
		resp.Data = &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}
	}
	if err := s.InteractionRespond(i.Interaction, resp); err != nil {
		log.Println("deferReply: InteractionRespond:", err)
	}
	return &reply{s: s, i: i, ephemeral: ephemeral}
}

// Edit replaces the deferred response content.
func (r *reply) Edit(msg string) {
	if _, err := r.s.InteractionResponseEdit(r.i.Interaction, &discordgo.WebhookEdit{Content: &msg}); err != nil {
		log.Println("reply.Edit: InteractionResponseEdit:", err)
	}
}

// EditData edits the deferred response from an InteractionResponseData
// (embeds + files + content) - the /culvert chart path.
func (r *reply) EditData(d *discordgo.InteractionResponseData) {
	edit := &discordgo.WebhookEdit{}
	if d.Content != "" {
		edit.Content = &d.Content
	}
	if len(d.Embeds) > 0 {
		edit.Embeds = &d.Embeds
	}
	if len(d.Files) > 0 {
		edit.Files = d.Files
	}
	if _, err := r.s.InteractionResponseEdit(r.i.Interaction, edit); err != nil {
		log.Println("reply.EditData: InteractionResponseEdit:", err)
	}
}

// EditChunked splits msg on line boundaries under Discord's content limit and
// delivers the first chunk via Edit and the rest as followups matching the
// reply's visibility.
func (r *reply) EditChunked(msg string) {
	for idx, chunk := range chunkText(msg, 1900) {
		if idx == 0 {
			r.Edit(chunk)
			continue
		}
		params := &discordgo.WebhookParams{Content: chunk}
		if r.ephemeral {
			params.Flags = discordgo.MessageFlagsEphemeral
		}
		if _, err := r.s.FollowupMessageCreate(r.i.Interaction, true, params); err != nil {
			log.Println("reply.EditChunked: FollowupMessageCreate:", err)
		}
	}
}

// EditWithTable edits the deferred response with msg plus a rendered text
// table in code fences, chunking long tables so every chunk keeps its own
// fences (replies are text-only - tables are never attached as files). The
// first message carries msg + the first table chunk; the rest follow as
// fenced followups matching the reply's visibility.
func (r *reply) EditWithTable(msg, tbl string) {
	if inline := msg + "\n```\n" + tbl + "\n```"; len(inline) <= 1900 {
		r.Edit(inline)
		return
	}
	chunks := chunkText(tbl, 1700)
	if first := msg + "\n```\n" + chunks[0] + "\n```"; len(first) <= 1990 {
		r.Edit(first)
		chunks = chunks[1:]
	} else {
		r.Edit(msg)
	}
	for _, chunk := range chunks {
		params := &discordgo.WebhookParams{Content: "```\n" + chunk + "\n```"}
		if r.ephemeral {
			params.Flags = discordgo.MessageFlagsEphemeral
		}
		if _, err := r.s.FollowupMessageCreate(r.i.Interaction, true, params); err != nil {
			log.Println("reply.EditWithTable: FollowupMessageCreate:", err)
		}
	}
}

// registerReply sends an immediate ephemeral response (no defer), for
// permission denials and other pre-defer errors.
func registerReply(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Println("registerReply: InteractionRespond:", err)
	}
}
