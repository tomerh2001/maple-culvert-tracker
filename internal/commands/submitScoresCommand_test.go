package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestSubmitScoresCommandAttachmentOptions(t *testing.T) {
	var command *discordgo.ApplicationCommand
	for _, c := range Commands {
		if c.Name == "submit-scores" {
			command = c
			break
		}
	}
	if command == nil {
		t.Fatal("submit-scores command is not registered")
	}
	if len(command.Options) != 12 {
		t.Fatalf("got %d options, want ten screenshots plus date and message-link", len(command.Options))
	}
	for idx, option := range command.Options {
		wantName := fmt.Sprintf("screenshot-%d", idx+1)
		wantType := discordgo.ApplicationCommandOptionAttachment
		if idx >= 10 {
			wantName = []string{"date", "message-link"}[idx-10]
			wantType = discordgo.ApplicationCommandOptionString
		}
		if option.Name != wantName || option.Type != wantType || option.Required {
			t.Errorf("option %d = %+v; want optional %s of type %d", idx, option, wantName, wantType)
		}
	}
}

func TestCommandImageURLs(t *testing.T) {
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "screenshot-1", Type: discordgo.ApplicationCommandOptionAttachment, Value: "a1"},
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

func TestCommandImageURLsAllTenSlots(t *testing.T) {
	data := discordgo.ApplicationCommandInteractionData{
		Resolved: &discordgo.ApplicationCommandInteractionDataResolved{
			Attachments: map[string]*discordgo.MessageAttachment{},
		},
	}
	want := make([]string, 0, 10)
	for n := 1; n <= 10; n++ {
		id := fmt.Sprintf("a%d", n)
		url := fmt.Sprintf("https://cdn/page-%d.png", n)
		// Users may pick the command fields in any order; the slot numbers
		// still determine page order, including screenshot-10 after -9.
		data.Options = append([]*discordgo.ApplicationCommandInteractionDataOption{{
			Name: fmt.Sprintf("screenshot-%d", n), Type: discordgo.ApplicationCommandOptionAttachment, Value: id,
		}}, data.Options...)
		data.Resolved.Attachments[id] = &discordgo.MessageAttachment{URL: url, Filename: fmt.Sprintf("page-%d.png", n)}
		want = append(want, url)
	}
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "date", Type: discordgo.ApplicationCommandOptionString, Value: "2026-09-23",
	})
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{Type: discordgo.InteractionApplicationCommand, Data: data}}
	if got := commandImageURLs(i); !reflect.DeepEqual(got, want) {
		t.Fatalf("attachment URLs = %v, want %v", got, want)
	}
}

func TestCommandImageURLsUnresolvedAttachments(t *testing.T) {
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "screenshot-1", Type: discordgo.ApplicationCommandOptionAttachment, Value: "missing"},
				{Name: "screenshot-2", Type: discordgo.ApplicationCommandOptionAttachment, Value: "nil"},
			},
			Resolved: &discordgo.ApplicationCommandInteractionDataResolved{
				Attachments: map[string]*discordgo.MessageAttachment{"nil": nil},
			},
		},
	}}
	if got := commandImageURLs(i); len(got) != 0 {
		t.Fatalf("unresolved attachments yielded URLs: %v", got)
	}
}

type submitScoresTestTransport func(*http.Request) (*http.Response, error)

func (f submitScoresTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSubmitScoresCommandRejectsMissingOrInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []*discordgo.ApplicationCommandInteractionDataOption
		want    string
	}{
		{name: "missing screenshots", want: "Attach a screenshot with `screenshot-1`"},
		{
			name: "invalid date",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "date", Type: discordgo.ApplicationCommandOptionString, Value: "invalid"},
			},
			want: badDateMessage,
		},
		{
			name: "invalid message link",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "message-link", Type: discordgo.ApplicationCommandOptionString, Value: "invalid"},
			},
			want: badMessageLinkMessage,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := discordgo.New("Bot test-token")
			if err != nil {
				t.Fatal(err)
			}
			var deferred bool
			var content string
			s.Client = &http.Client{Transport: submitScoresTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost && req.Method != http.MethodPatch {
					t.Fatalf("unexpected Discord request: %s %s", req.Method, req.URL.Path)
				}
				body := io.Reader(req.Body)
				mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				if mediaType == "multipart/form-data" {
					parts := multipart.NewReader(req.Body, params["boundary"])
					for {
						part, err := parts.NextPart()
						if err != nil {
							t.Fatalf("missing payload_json: %v", err)
						}
						if part.FormName() == "payload_json" {
							body = part
							break
						}
					}
				}
				var response struct {
					Type    discordgo.InteractionResponseType  `json:"type"`
					Data    *discordgo.InteractionResponseData `json:"data"`
					Content string                             `json:"content"`
				}
				if err := json.NewDecoder(body).Decode(&response); err != nil {
					t.Fatal(err)
				}
				if req.Method == http.MethodPost {
					deferred = response.Type == discordgo.InteractionResponseDeferredChannelMessageWithSource && response.Data != nil && response.Data.Flags&discordgo.MessageFlagsEphemeral != 0
				} else {
					content = response.Content
				}
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{}`)), Request: req,
				}, nil
			})}
			i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				ID: "interaction", AppID: "app", Token: "token", GuildID: t.Name(), ChannelID: "channel",
				Type: discordgo.InteractionApplicationCommand,
				Member: &discordgo.Member{
					User: &discordgo.User{ID: "submitter"}, Permissions: discordgo.PermissionAdministrator,
				},
				Data: discordgo.ApplicationCommandInteractionData{Name: "submit-scores", Options: tc.options},
			}}
			submitScoresCommand(s, i)
			if !deferred || !strings.Contains(content, tc.want) {
				t.Fatalf("deferred privately = %v, content = %q; want rejection containing %q", deferred, content, tc.want)
			}
			if !tryAcquireSubmit(tenantOf(i)) {
				t.Fatal("rejected submission did not release the tenant guard")
			}
			releaseSubmit(tenantOf(i))
		})
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
