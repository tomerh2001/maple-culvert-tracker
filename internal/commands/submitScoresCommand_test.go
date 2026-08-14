package commands

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCommandImageURLs(t *testing.T) {
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "screenshot", Type: discordgo.ApplicationCommandOptionAttachment, Value: "a1"},
				{Name: "screenshot-2", Type: discordgo.ApplicationCommandOptionAttachment, Value: "a2"},
				{Name: "screenshot-3", Type: discordgo.ApplicationCommandOptionAttachment, Value: "a3"},
			},
			Resolved: &discordgo.ApplicationCommandInteractionDataResolved{
				Attachments: map[string]*discordgo.MessageAttachment{
					"a1": {URL: "https://cdn/one.png", ContentType: "image/png", Filename: "one.png"},
					"a2": {URL: "https://cdn/notes.txt", ContentType: "text/plain", Filename: "notes.txt"},
					"a3": {URL: "https://cdn/two.jpg", ContentType: "image/jpeg", Filename: "two.jpg"},
				},
			},
		},
	}}

	got := commandImageURLs(i)
	// Non-image (a2) dropped; image slots kept in declared option order.
	want := []string{"https://cdn/one.png", "https://cdn/two.jpg"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Errorf("url %d = %q, want %q", idx, got[idx], want[idx])
		}
	}
}

func TestCommandImageURLsEmpty(t *testing.T) {
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{},
	}}
	if got := commandImageURLs(i); len(got) != 0 {
		t.Errorf("no resolved attachments must yield no urls, got %v", got)
	}
}

// TestParseMessageLink pins the channel/message extraction from a Discord
// message link (the message-link option of /submit-scores): the guild segment
// is ignored, host variants are accepted, and non-links are rejected.
func TestParseMessageLink(t *testing.T) {
	ok := []struct {
		in          string
		chID, msgID string
	}{
		{"https://discord.com/channels/111/222/333", "222", "333"},
		{"https://discord.com/channels/111/222/333/", "222", "333"},
		{"  https://discord.com/channels/111/222/333  ", "222", "333"},
		{"https://discordapp.com/channels/111/222/333", "222", "333"},
		{"https://canary.discord.com/channels/111/222/333", "222", "333"},
		{"https://ptb.discord.com/channels/111/222/333", "222", "333"},
	}
	for _, c := range ok {
		chID, msgID, err := parseMessageLink(c.in)
		if err != nil {
			t.Errorf("parseMessageLink(%q) errored: %v", c.in, err)
			continue
		}
		if chID != c.chID || msgID != c.msgID {
			t.Errorf("parseMessageLink(%q) = (%q, %q), want (%q, %q)", c.in, chID, msgID, c.chID, c.msgID)
		}
	}

	bad := []string{
		"",
		"not a link",
		"https://discord.com/channels/111/222", // missing message id
		"https://example.com/channels/111/222/333",
		"discord.com/channels/111/222/333", // no scheme
		"http://discord.com/channels/111/222/333",
	}
	for _, in := range bad {
		if _, _, err := parseMessageLink(in); err == nil {
			t.Errorf("parseMessageLink(%q) = nil error, want rejection", in)
		}
	}
}
