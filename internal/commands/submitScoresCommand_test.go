package commands

import "testing"

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
