package commands

import (
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// reply is a handle to a deferred interaction response, replacing the
// editContent closures that used to be duplicated across command handlers.
type reply struct {
	s *discordgo.Session
	i *discordgo.InteractionCreate
}

// deferReply sends the Deferred interaction response (the "thinking..."
// placeholder) and returns a handle used to fill it in later with Edit.
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
	return &reply{s: s, i: i}
}

// Edit replaces the deferred response content, attaching any given files.
func (r *reply) Edit(msg string, files ...*discordgo.File) {
	edit := &discordgo.WebhookEdit{Content: &msg}
	if len(files) > 0 {
		edit.Files = files
	}
	if _, err := r.s.InteractionResponseEdit(r.i.Interaction, edit); err != nil {
		log.Println("reply.Edit: InteractionResponseEdit:", err)
	}
}

// EditData edits the deferred response from an InteractionResponseData
// (embeds + files + content), for handlers whose output is built elsewhere.
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

// EditWithTable edits the deferred response with msg plus a rendered text
// table: inline as a code block when it fits Discord's content limit,
// otherwise attached as filename. extraFiles are attached either way.
func (r *reply) EditWithTable(msg, tbl, filename string, extraFiles ...*discordgo.File) {
	inline := msg + "\n```\n" + tbl + "\n```"
	if len(inline) <= 1900 {
		r.Edit(inline, extraFiles...)
		return
	}
	files := append([]*discordgo.File{{
		Name:        filename,
		ContentType: "text/plain",
		Reader:      strings.NewReader(tbl),
	}}, extraFiles...)
	r.Edit(msg, files...)
}

// registerReply sends an immediate ephemeral response (no defer). Kept under
// its historical name because handlers outside this wave still call it; the
// repo-wide rename is a later wave.
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
