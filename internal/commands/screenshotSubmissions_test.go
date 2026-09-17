package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
)

type screenshotTestResponse struct {
	Type       discordgo.InteractionResponseType `json:"type"`
	Data       *screenshotTestResponse           `json:"data"`
	Content    string                            `json:"content"`
	Flags      discordgo.MessageFlags            `json:"flags"`
	Components []struct {
		Components []struct {
			Label    string `json:"label"`
			CustomID string `json:"custom_id"`
			Disabled bool   `json:"disabled"`
		} `json:"components"`
	} `json:"components"`
}

type screenshotTestTransport struct {
	mu            sync.Mutex
	responses     []screenshotTestResponse
	failButtonAck bool
}

func (d *screenshotTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var response screenshotTestResponse
	if err := json.NewDecoder(req.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode Discord request: %w", err)
	}
	d.mu.Lock()
	d.responses = append(d.responses, response)
	status := http.StatusOK
	if d.failButtonAck && response.Type == discordgo.InteractionResponseDeferredMessageUpdate {
		d.failButtonAck = false
		status = http.StatusBadRequest
	}
	d.mu.Unlock()
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}, nil
}

func (d *screenshotTestTransport) last(t *testing.T) screenshotTestResponse {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.responses) == 0 {
		t.Fatal("no Discord response")
	}
	return d.responses[len(d.responses)-1]
}

func screenshotTestStart(t *testing.T, m *screenshotSubmissionManager) (*discordgo.Session, *discordgo.InteractionCreate, *screenshotSubmission, *screenshotTestTransport) {
	t.Helper()
	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	d := &screenshotTestTransport{}
	s.Client = &http.Client{Transport: d}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "100", AppID: "app", Token: "start-token", GuildID: t.Name(), ChannelID: "channel",
		Type: discordgo.InteractionApplicationCommand,
		Member: &discordgo.Member{
			User: &discordgo.User{ID: "submitter"}, Permissions: discordgo.PermissionAdministrator,
		},
		Data: discordgo.ApplicationCommandInteractionData{Name: "submit-scores"},
	}}
	m.start(s, i, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	key, _ := screenshotKey(i)
	value, ok := m.batches.Load(key)
	if !ok {
		t.Fatal("submission did not start")
	}
	b := value.(*screenshotSubmission)
	t.Cleanup(func() { m.close(b) })
	return s, i, b, d
}

func screenshotTestMessage(i *discordgo.InteractionCreate, id string, urls ...string) *discordgo.MessageCreate {
	msg := &discordgo.Message{
		ID: id, GuildID: i.GuildID, ChannelID: i.ChannelID, Author: &discordgo.User{ID: i.Member.User.ID},
	}
	for _, url := range urls {
		msg.Attachments = append(msg.Attachments, &discordgo.MessageAttachment{URL: url, ContentType: "image/png", Filename: "culvert.png"})
	}
	return &discordgo.MessageCreate{Message: msg}
}

func screenshotTestButton(i *discordgo.InteractionCreate, action string) *discordgo.InteractionCreate {
	interaction := *i.Interaction
	member := *i.Member
	user := *i.Member.User
	member.User = &user
	interaction.Member = &member
	interaction.ID = "2000"
	interaction.Token = "button-token"
	interaction.Type = discordgo.InteractionMessageComponent
	interaction.Data = discordgo.MessageComponentInteractionData{
		CustomID: screenshotButtonPrefix + i.ID + ":" + action,
	}
	return &discordgo.InteractionCreate{Interaction: &interaction}
}

func screenshotTestCount(b *screenshotSubmission) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

func TestScreenshotSubmissionPromptAndCollection(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	initial := d.last(t)
	if initial.Type != discordgo.InteractionResponseChannelMessageWithSource || initial.Data == nil || initial.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("expected an ephemeral prompt, got %+v", initial)
	}
	if !strings.Contains(initial.Data.Content, "Week of 2026-09-09") || !strings.Contains(initial.Data.Content, "0 of 10") {
		t.Fatalf("prompt does not identify week and count: %s", initial.Data.Content)
	}
	if len(initial.Data.Components) != 1 || len(initial.Data.Components[0].Components) != 2 {
		t.Fatalf("expected Submit and Cancel buttons: %+v", initial.Data.Components)
	}
	buttons := initial.Data.Components[0].Components
	if buttons[0].Label != "Submit" || !buttons[0].Disabled || buttons[1].Label != "Cancel" {
		t.Fatalf("unexpected initial buttons: %+v", buttons)
	}
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	updated := d.last(t)
	if screenshotTestCount(b) != 1 || !strings.Contains(updated.Content, "1 of 10") || updated.Components[0].Components[0].Disabled {
		t.Fatalf("collected image did not enable submission: %+v", updated)
	}
	if calls.Load() != 0 {
		t.Fatal("collecting screenshots must not process scores")
	}
	if !tryAcquireSubmit(tenantOf(i)) {
		t.Fatal("collecting screenshots holds the processing guard")
	}
	releaseSubmit(tenantOf(i))
}

func TestScreenshotSubmissionIgnoresUnrelatedMessages(t *testing.T) {
	m := &screenshotSubmissionManager{}
	s, i, b, _ := screenshotTestStart(t, m)
	cases := []struct {
		name string
		edit func(*discordgo.MessageCreate)
	}{
		{"other user", func(m *discordgo.MessageCreate) { m.Author.ID = "other" }},
		{"other guild", func(m *discordgo.MessageCreate) { m.GuildID = "other" }},
		{"other channel", func(m *discordgo.MessageCreate) { m.ChannelID = "other" }},
		{"bot", func(m *discordgo.MessageCreate) { m.Author.Bot = true }},
		{"webhook", func(m *discordgo.MessageCreate) { m.WebhookID = "webhook" }},
		{"before command", func(m *discordgo.MessageCreate) { m.ID = "99" }},
		{"same snowflake", func(m *discordgo.MessageCreate) { m.ID = i.ID }},
		{"missing author", func(m *discordgo.MessageCreate) { m.Author = nil }},
		{"non-image", func(m *discordgo.MessageCreate) {
			m.Attachments = []*discordgo.MessageAttachment{nil, {URL: "https://cdn/scores.txt", ContentType: "text/plain", Filename: "scores.txt"}}
		}},
		{"empty image URL", func(m *discordgo.MessageCreate) { m.Attachments[0].URL = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := screenshotTestMessage(i, "101", "https://cdn/one.png")
			tc.edit(msg)
			m.onMessage(s, msg)
			if got := screenshotTestCount(b); got != 0 {
				t.Fatalf("ignored message was collected: count=%d", got)
			}
		})
	}
	m.onMessage(s, nil)
	m.onMessage(s, &discordgo.MessageCreate{})
	b.mu.Lock()
	b.expires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	m.onMessage(s, screenshotTestMessage(i, "102", "https://cdn/expired.png"))
	if screenshotTestCount(b) != 0 {
		t.Fatal("expired upload collected a message")
	}
}

func TestScreenshotSubmissionOrderAndDeduplication(t *testing.T) {
	m := &screenshotSubmissionManager{}
	s, i, b, _ := screenshotTestStart(t, m)
	late := screenshotTestMessage(i, "1000", "https://cdn/three.png", "https://cdn/four.png")
	m.onMessage(s, late)
	m.onMessage(s, screenshotTestMessage(i, "102", "https://cdn/two.png"))
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	m.onMessage(s, late)
	b.mu.Lock()
	got := b.orderedURLsLocked()
	b.mu.Unlock()
	want := []string{"https://cdn/one.png", "https://cdn/two.png", "https://cdn/three.png", "https://cdn/four.png"}
	if !reflect.DeepEqual(got, want) || screenshotTestCount(b) != len(want) {
		t.Fatalf("ordered/deduplicated URLs = %v; want %v", got, want)
	}
}

func TestScreenshotSubmissionLimitRejectsWholeMessage(t *testing.T) {
	m := &screenshotSubmissionManager{}
	s, i, b, d := screenshotTestStart(t, m)
	first := []string{}
	for n := 0; n < 8; n++ {
		first = append(first, fmt.Sprintf("https://cdn/%d.png", n))
	}
	m.onMessage(s, screenshotTestMessage(i, "101", first...))
	m.onMessage(s, screenshotTestMessage(i, "102", "https://cdn/8.png", "https://cdn/9.png", "https://cdn/10.png"))
	if screenshotTestCount(b) != 8 || !strings.Contains(d.last(t).Content, "not collected") {
		t.Fatal("oversized message must be rejected completely with a warning")
	}
	b.mu.Lock()
	_, partial := b.messages["102"]
	b.mu.Unlock()
	if partial {
		t.Fatal("oversized message was partially retained")
	}
	m.onMessage(s, screenshotTestMessage(i, "103", "https://cdn/8.png", "https://cdn/9.png"))
	if screenshotTestCount(b) != 10 || strings.Contains(d.last(t).Content, "not collected") {
		t.Fatal("exactly ten images must be accepted and clear the warning")
	}
}

func TestScreenshotSubmissionSubmitOnceWithPinnedWeek(t *testing.T) {
	var calls atomic.Int32
	wantWeek := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	m := &screenshotSubmissionManager{process: func(_ *discordgo.Session, r *reply, i *discordgo.InteractionCreate, urls []string, week time.Time) {
		calls.Add(1)
		if !reflect.DeepEqual(urls, []string{"https://cdn/one.png", "https://cdn/two.png"}) || !week.Equal(wantWeek) || !r.ephemeral {
			t.Errorf("wrong batch delivered to processor: urls=%v week=%v ephemeral=%v", urls, week, r.ephemeral)
		}
		if tryAcquireSubmit(tenantOf(i)) {
			releaseSubmit(tenantOf(i))
			t.Error("processing must hold the tenant guard")
		}
	}}
	s, i, b, _ := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "102", "https://cdn/two.png"))
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.onInteraction(s, screenshotTestButton(i, "submit"))
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("processor called %d times, want 1", calls.Load())
	}
	if _, open := m.batches.Load(b.key); open {
		t.Fatal("submitted batch remains open")
	}
	if !tryAcquireSubmit(tenantOf(i)) {
		t.Fatal("processing guard was not released")
	}
	releaseSubmit(tenantOf(i))
}

func TestScreenshotSubmissionRejectsStaleAndStrangerButtons(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	stranger := screenshotTestButton(i, "submit")
	stranger.Member.User.ID = "stranger"
	m.onInteraction(s, stranger)
	stale := screenshotTestButton(i, "submit")
	stale.Data = discordgo.MessageComponentInteractionData{CustomID: screenshotButtonPrefix + "99:submit"}
	m.onInteraction(s, stale)
	if calls.Load() != 0 || screenshotTestCount(b) != 1 {
		t.Fatal("an unrelated button changed the pending batch")
	}
	if _, open := m.batches.Load(b.key); !open {
		t.Fatal("an unrelated button closed the batch")
	}
	if reply := d.last(t); reply.Data == nil || reply.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatal("rejected button must receive a private response")
	}
}

func TestScreenshotSubmissionCancelDoesNotProcess(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	m.onInteraction(s, screenshotTestButton(i, "cancel"))
	if calls.Load() != 0 {
		t.Fatal("cancelled upload processed scores")
	}
	if _, open := m.batches.Load(b.key); open {
		t.Fatal("cancelled upload remains open")
	}
	if last := d.last(t); !strings.Contains(last.Content, "cancelled") || len(last.Components) != 0 {
		t.Fatalf("cancel did not close the prompt: %+v", last)
	}
	if b.collect(screenshotTestMessage(i, "102", "https://cdn/two.png").Message, time.Now()) {
		t.Fatal("cancelled upload accepted another image")
	}
}

func TestScreenshotSubmissionBusyRetainsBatch(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	if !tryAcquireSubmit(tenantOf(i)) {
		t.Fatal("could not reserve processing guard")
	}
	t.Cleanup(func() { releaseSubmit(tenantOf(i)) })
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if calls.Load() != 0 || screenshotTestCount(b) != 1 {
		t.Fatal("busy submission processed or discarded the batch")
	}
	if _, open := m.batches.Load(b.key); !open {
		t.Fatal("busy submission closed the batch")
	}
	if last := d.last(t); last.Data == nil || !strings.Contains(last.Data.Content, "still collected") {
		t.Fatalf("busy response does not explain retry: %+v", last)
	}
	releaseSubmit(tenantOf(i))
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if calls.Load() != 1 {
		t.Fatal("retained batch could not be submitted after the guard cleared")
	}
}

func TestScreenshotSubmissionChecksCurrentPermissions(t *testing.T) {
	previousRedis := apiredis.RedisDB
	apiredis.RedisDB = nil
	t.Cleanup(func() { apiredis.RedisDB = previousRedis })
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	button := screenshotTestButton(i, "submit")
	button.Member.Permissions = 0
	m.onInteraction(s, button)
	if calls.Load() != 0 {
		t.Fatal("former admin was allowed to process a submission")
	}
	if _, open := m.batches.Load(b.key); !open {
		t.Fatal("permission denial discarded the batch")
	}
	if last := d.last(t); last.Data == nil || !strings.Contains(last.Data.Content, "don't have permission") {
		t.Fatalf("missing permission denial: %+v", last)
	}
}

func TestScreenshotSubmissionExpiredButtonDoesNotProcess(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	b.mu.Lock()
	b.expires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if calls.Load() != 0 {
		t.Fatal("expired batch processed scores")
	}
	if _, open := m.batches.Load(b.key); open {
		t.Fatal("expired batch remains open")
	}
	if last := d.last(t); last.Data == nil || !strings.Contains(last.Data.Content, "expired") {
		t.Fatalf("missing expiry reply: %+v", last)
	}
}

func TestScreenshotSubmissionFailedButtonAckRetainsBatch(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onMessage(s, screenshotTestMessage(i, "101", "https://cdn/one.png"))
	d.mu.Lock()
	d.failButtonAck = true
	d.mu.Unlock()
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if calls.Load() != 0 {
		t.Fatal("unacknowledged submission processed scores")
	}
	value, open := m.batches.Load(b.key)
	if !open || value != b || screenshotTestCount(b) != 1 {
		t.Fatal("failed button acknowledgement lost the collected screenshots")
	}
	if !tryAcquireSubmit(tenantOf(i)) {
		t.Fatal("failed acknowledgement leaked the processing guard")
	}
	releaseSubmit(tenantOf(i))
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if calls.Load() != 1 {
		t.Fatal("retry did not process the retained batch")
	}
}

func TestScreenshotSubmissionIncludesReceivedImageBeforeHandlerRuns(t *testing.T) {
	var got []string
	m := &screenshotSubmissionManager{process: func(_ *discordgo.Session, _ *reply, _ *discordgo.InteractionCreate, urls []string, _ time.Time) {
		got = urls
	}}
	s, i, _, _ := screenshotTestStart(t, m)
	s.State.MaxMessageCount = 100
	if err := s.State.GuildAdd(&discordgo.Guild{ID: i.GuildID, Channels: []*discordgo.Channel{{ID: i.ChannelID, GuildID: i.GuildID}}}); err != nil {
		t.Fatal(err)
	}
	// DiscordGo records events in State before dispatching its asynchronous
	// handlers. Simulate a final image whose collection handler has not run.
	for _, msg := range []*discordgo.MessageCreate{
		screenshotTestMessage(i, "101", "https://cdn/received.png"),
		screenshotTestMessage(i, "2001", "https://cdn/after-click.png"),
	} {
		if err := s.State.MessageAdd(msg.Message); err != nil {
			t.Fatal(err)
		}
	}
	// Even if the later image's handler runs first, Submit uses the click's
	// snowflake as its cutoff and still picks up the earlier cached image.
	m.onMessage(s, screenshotTestMessage(i, "2001", "https://cdn/after-click.png"))
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if !reflect.DeepEqual(got, []string{"https://cdn/received.png"}) {
		t.Fatalf("Submit processed %v; want only the image received before the click", got)
	}
}

func TestScreenshotSubmissionEmptyButtonKeepsCollecting(t *testing.T) {
	var calls atomic.Int32
	m := &screenshotSubmissionManager{process: func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time) { calls.Add(1) }}
	s, i, b, d := screenshotTestStart(t, m)
	m.onInteraction(s, screenshotTestButton(i, "submit"))
	if calls.Load() != 0 {
		t.Fatal("empty upload processed scores")
	}
	if _, open := m.batches.Load(b.key); !open {
		t.Fatal("empty Submit closed collection")
	}
	if last := d.last(t); last.Data == nil || !strings.Contains(last.Data.Content, "at least one screenshot") {
		t.Fatalf("empty Submit did not explain how to continue: %+v", last)
	}
}
