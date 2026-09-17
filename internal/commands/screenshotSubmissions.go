package commands

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	screenshotSubmissionTTL  = 10 * time.Minute
	maxSubmissionScreenshots = 10
	screenshotButtonPrefix   = "culvert-upload:"
)

type screenshotSubmissionKey struct {
	guild, channel, user string
}

type screenshotSubmission struct {
	mu sync.Mutex
	// Serialize prompt edits separately so a slow progress update cannot
	// overwrite a completed submission or delay acknowledging its button.
	uiMu          sync.Mutex
	key           screenshotSubmissionKey
	interaction   *discordgo.Interaction
	week, expires time.Time
	messages      map[string][]string
	count         int
	warning       string
	closed        bool
	timer         *time.Timer
}

type screenshotSubmissionManager struct {
	batches sync.Map // screenshotSubmissionKey -> *screenshotSubmission
	process func(*discordgo.Session, *reply, *discordgo.InteractionCreate, []string, time.Time)
}

var screenshotSubmissions screenshotSubmissionManager

// AddScreenshotSubmissions collects attachments only while their author has an
// open /submit-scores prompt in that exact server and channel.
func AddScreenshotSubmissions(ctx context.Context, s *discordgo.Session) {
	// DiscordGo updates State before launching event handlers. Keep a bounded
	// cache to reconcile the final upload if its handler races the Submit click.
	s.State.MaxMessageCount = 100
	s.AddHandler(screenshotSubmissions.onMessage)
	s.AddHandler(screenshotSubmissions.onInteraction)
	go func() {
		<-ctx.Done()
		screenshotSubmissions.batches.Range(func(_, value any) bool {
			screenshotSubmissions.close(value.(*screenshotSubmission))
			return true
		})
	}()
}

func screenshotKey(i *discordgo.InteractionCreate) (screenshotSubmissionKey, bool) {
	if i == nil || i.Interaction == nil || i.GuildID == "" || i.ChannelID == "" || i.Member == nil || i.Member.User == nil {
		return screenshotSubmissionKey{}, false
	}
	return screenshotSubmissionKey{i.GuildID, i.ChannelID, i.Member.User.ID}, true
}

func startScreenshotSubmission(s *discordgo.Session, i *discordgo.InteractionCreate, week time.Time) {
	screenshotSubmissions.start(s, i, week)
}

func (m *screenshotSubmissionManager) start(s *discordgo.Session, i *discordgo.InteractionCreate, week time.Time) {
	key, ok := screenshotKey(i)
	if !ok {
		registerReply(s, i, "Run `/submit-scores` in a server channel.")
		return
	}
	b := &screenshotSubmission{
		key: key, interaction: i.Interaction, week: week,
		expires: time.Now().Add(screenshotSubmissionTTL), messages: make(map[string][]string),
	}
	// A message can arrive as soon as Discord displays the prompt. Keep edits
	// waiting until its initial response exists.
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	if _, loaded := m.batches.LoadOrStore(key, b); loaded {
		registerReply(s, i, "You already have an upload open in this channel. Send your screenshots, then click **Submit** on that prompt. Use **Cancel** to start again.")
		return
	}
	b.mu.Lock()
	content, components := b.promptLocked()
	b.mu.Unlock()
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content, Components: components, Flags: discordgo.MessageFlagsEphemeral,
			AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
		},
	})
	if err != nil {
		m.close(b)
		log.Println("start screenshot submission:", err)
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.timer = m.expiryTimer(s, b)
	}
	b.mu.Unlock()
}

func (m *screenshotSubmissionManager) expiryTimer(s *discordgo.Session, b *screenshotSubmission) *time.Timer {
	return time.AfterFunc(time.Until(b.expires), func() {
		if m.close(b) {
			b.editClosed(s, b.interaction, "Upload expired. Run `/submit-scores` to start again.")
		}
	})
}

func (b *screenshotSubmission) promptLocked() (string, []discordgo.MessageComponent) {
	content := fmt.Sprintf("**Submit Culvert screenshots**\nWeek of %s\n\nPaste and send screenshots in this channel. Then click **Submit**.\n**%d of %d screenshots collected**\nExpires <t:%d:R>.",
		b.week.Format("2006-01-02"), b.count, maxSubmissionScreenshots, b.expires.Unix())
	if b.warning != "" {
		content += "\n\n" + b.warning
	}
	return content, []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "Submit", Style: discordgo.SuccessButton, CustomID: screenshotButtonPrefix + b.interaction.ID + ":submit", Disabled: b.count == 0},
		discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: screenshotButtonPrefix + b.interaction.ID + ":cancel"},
	}}}
}

// Snowflakes have a timestamp prefix. Compare numerically without assuming
// equal string lengths, retaining the order of each message's attachments.
func snowflakeLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// collect is deliberately independent of OCR and score storage. Gateway
// handlers may run out of order or repeat after reconnecting.
func (b *screenshotSubmission) collect(msg *discordgo.Message, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !now.Before(b.expires) || msg == nil || msg.Author == nil || msg.Author.Bot || msg.WebhookID != "" ||
		msg.GuildID != b.key.guild || msg.ChannelID != b.key.channel || msg.Author.ID != b.key.user ||
		!snowflakeLess(b.interaction.ID, msg.ID) {
		return false
	}
	if _, seen := b.messages[msg.ID]; seen {
		return false
	}
	urls := make([]string, 0, len(msg.Attachments))
	for _, a := range msg.Attachments {
		if a != nil && a.URL != "" && isImageAttachment(a) {
			urls = append(urls, a.URL)
		}
	}
	if len(urls) == 0 {
		return false
	}
	if b.count+len(urls) > maxSubmissionScreenshots {
		b.warning = fmt.Sprintf("That message would exceed the %d-screenshot limit, so its screenshots were not collected. Click Submit for the %d already collected, or Cancel to start again.", maxSubmissionScreenshots, b.count)
		return true
	}
	b.messages[msg.ID] = urls
	b.count += len(urls)
	b.warning = ""
	return true
}

func (b *screenshotSubmission) orderedURLsLocked() []string {
	return b.orderedURLsBeforeLocked("")
}

func (b *screenshotSubmission) orderedURLsBeforeLocked(beforeID string) []string {
	ids := make([]string, 0, len(b.messages))
	for id := range b.messages {
		if beforeID == "" || snowflakeLess(id, beforeID) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return snowflakeLess(ids[i], ids[j]) })
	urls := make([]string, 0, b.count)
	for _, id := range ids {
		urls = append(urls, b.messages[id]...)
	}
	return urls
}

func (b *screenshotSubmission) reconcileReceived(s *discordgo.Session, beforeID string) {
	channel, err := s.State.Channel(b.key.channel)
	if err != nil {
		return
	}
	s.State.RLock()
	defer s.State.RUnlock()
	for _, msg := range channel.Messages {
		if msg != nil && snowflakeLess(msg.ID, beforeID) {
			b.collect(msg, time.Now())
		}
	}
}

func (m *screenshotSubmissionManager) onMessage(s *discordgo.Session, event *discordgo.MessageCreate) {
	if event == nil || event.Message == nil {
		return
	}
	s.State.RLock()
	if event.Author == nil {
		s.State.RUnlock()
		return
	}
	key := screenshotSubmissionKey{event.GuildID, event.ChannelID, event.Author.ID}
	value, ok := m.batches.Load(key)
	if !ok {
		s.State.RUnlock()
		return
	}
	b := value.(*screenshotSubmission)
	changed := b.collect(event.Message, time.Now())
	s.State.RUnlock()
	if !changed {
		return
	}
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	content, components := b.promptLocked()
	b.mu.Unlock()
	if _, err := s.InteractionResponseEdit(b.interaction, &discordgo.WebhookEdit{Content: &content, Components: &components}); err != nil {
		log.Println("update screenshot submission:", err)
	}
}

// closeLocked claims this batch once. The caller holds b.mu.
func (m *screenshotSubmissionManager) closeLocked(b *screenshotSubmission) {
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
	}
	m.batches.CompareAndDelete(b.key, b)
}

func (m *screenshotSubmissionManager) close(b *screenshotSubmission) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	m.closeLocked(b)
	return true
}

func (b *screenshotSubmission) editClosed(s *discordgo.Session, i *discordgo.Interaction, content string) {
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	components := []discordgo.MessageComponent{}
	if _, err := s.InteractionResponseEdit(i, &discordgo.WebhookEdit{Content: &content, Components: &components}); err != nil {
		log.Println("close screenshot submission prompt:", err)
	}
}

// No processing has started when the button acknowledgement fails. Keep the
// screenshots retryable, unless the user already opened a newer batch.
func (m *screenshotSubmissionManager) restoreAfterAckFailure(s *discordgo.Session, b *screenshotSubmission) {
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	b.mu.Lock()
	restored := false
	if time.Now().Before(b.expires) {
		if _, loaded := m.batches.LoadOrStore(b.key, b); !loaded {
			b.closed = false
			b.warning = "The button request failed. Your screenshots are still collected. Click Submit or Cancel again."
			b.timer = m.expiryTimer(s, b)
			restored = true
		}
	}
	content := "The button request failed. Run `/submit-scores` to start again."
	components := []discordgo.MessageComponent{}
	if restored {
		content, components = b.promptLocked()
	}
	b.mu.Unlock()
	if _, err := s.InteractionResponseEdit(b.interaction, &discordgo.WebhookEdit{Content: &content, Components: &components}); err != nil {
		log.Println("restore screenshot submission prompt:", err)
	}
}

func (m *screenshotSubmissionManager) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i == nil || i.Interaction == nil || i.Type != discordgo.InteractionMessageComponent {
		return
	}
	customID := i.MessageComponentData().CustomID
	if !strings.HasPrefix(customID, screenshotButtonPrefix) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(customID, screenshotButtonPrefix), ":")
	if len(parts) != 2 || (parts[1] != "submit" && parts[1] != "cancel") {
		registerReply(s, i, "That upload button is invalid. Run `/submit-scores` to start again.")
		return
	}
	key, valid := screenshotKey(i)
	value, found := m.batches.Load(key)
	if !valid || !found || value.(*screenshotSubmission).interaction.ID != parts[0] {
		registerReply(s, i, "This upload is no longer open. Run `/submit-scores` to start your own upload.")
		return
	}
	b := value.(*screenshotSubmission)
	if parts[1] == "submit" && !requireSubmitPermission(s, i) {
		return
	}
	if parts[1] == "submit" {
		b.reconcileReceived(s, i.ID)
	}
	b.mu.Lock()
	if b.closed || !time.Now().Before(b.expires) {
		m.closeLocked(b)
		b.mu.Unlock()
		registerReply(s, i, "This upload has expired. Run `/submit-scores` to start again.")
		return
	}
	urls := b.orderedURLsBeforeLocked(i.ID)
	if parts[1] == "submit" && len(urls) == 0 {
		b.mu.Unlock()
		registerReply(s, i, "Paste and send at least one screenshot in this channel first.")
		return
	}
	tenant := tenantOf(i)
	if parts[1] == "submit" {
		if !tryAcquireSubmit(tenant) {
			b.mu.Unlock()
			registerReply(s, i, submitBusyMessage+" Your screenshots are still collected.")
			return
		}
		defer releaseSubmit(tenant)
	}
	m.closeLocked(b)
	b.mu.Unlock()
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
		log.Println("acknowledge screenshot submission button:", err)
		m.restoreAfterAckFailure(s, b)
		return
	}
	if parts[1] == "cancel" {
		b.editClosed(s, i.Interaction, "Upload cancelled. No scores submitted.")
		return
	}
	r := &reply{s: s, i: i, ephemeral: true}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("PANIC in screenshot submission: %v\n%s", recovered, debug.Stack())
			r.Edit("Something went wrong. The error has been logged; check your weekly scores before retrying.")
		}
	}()
	b.editClosed(s, i.Interaction, fmt.Sprintf("Reading %d screenshots...", len(urls)))
	process := m.process
	if process == nil {
		process = processScreenshotSubmission
	}
	process(s, r, i, urls, b.week)
}

func processScreenshotSubmission(s *discordgo.Session, r *reply, i *discordgo.InteractionCreate, urls []string, week time.Time) {
	scores := map[string]int{}
	pages, warnings, ok := scoresFromImageURLs(r, tenantOf(i), urls, scores)
	if ok {
		finalizeSubmitScores(s, r, i, scores, week, warnings, pages)
	}
}
