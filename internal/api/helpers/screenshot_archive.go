package helpers

// The weekly screenshot archive: up to two bot messages per (guild, culvert week) in
// an optional channel (CONF_DISCORD_SCREENSHOT_CHANNEL_ID), collecting the
// screenshots each submission was parsed from - a browsable history of the
// raw inputs behind the recorded scores.
//
// Messages are created as the week's archive grows and edited in place
// afterwards, like the weekly announcement. Their attachments are the
// image store; weekly_screenshot_archives / weekly_screenshot_pages
// (db_migrations/9 and 11) track each message and which roster "page" each attachment
// covers. A page's identity is the set of character names parsed from it:
// when a later submission re-shoots a page (same members, fresher scores),
// the new image REPLACES the stored one instead of piling up - only the most
// recent version of every page survives. Matching is by name overlap, not
// page position, because the in-game window orders members by score: as the
// week progresses rows move between pages, but the members on a re-shot page
// stay mostly the same.

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// ErrNoScreenshotChannel reports that no guild in the tenant has a screenshot
// archive channel configured - archiving is skipped, which callers surface as
// a non-failure (the archive is opt-in).
var ErrNoScreenshotChannel = errors.New("no screenshot archive channel configured")

// ScreenshotPage is one submitted screenshot: its raw bytes and the character
// names parsed off it (the page's identity for the replace-or-append
// decision).
type ScreenshotPage struct {
	Bytes []byte
	Names []string
}

// archiveMatchThreshold is the minimum name-overlap ratio (shared names over
// the smaller page's size) for an incoming page to REPLACE a stored one.
// Below it the incoming page is treated as new coverage. 0.5 tolerates rows
// migrating between pages as the score ordering shifts during the week, while
// a genuinely new page (mostly unseen names) stays below it.
const archiveMatchThreshold = 0.5

// Discord allows ten attachments per message. Two archive parts retain up to
// twenty pages; newer submissions replace the oldest unmatched pages at capacity.
const archivePagesPerMessage = 10
const maxArchiveParts = 2
const maxArchivePages = archivePagesPerMessage * maxArchiveParts

const screenshotArchiveHeading = "**Culvert screenshots**\nWeek of %s"

func screenshotArchiveContent(weekStr string, count, dropped int, updatedAt time.Time) string {
	screenshotWord := "screenshots"
	if count == 1 {
		screenshotWord = "screenshot"
	}
	content := fmt.Sprintf(screenshotArchiveHeading+"\n%d %s\nUpdated <t:%d:f>",
		weekStr, count, screenshotWord, updatedAt.Unix())
	if dropped == 1 {
		content += fmt.Sprintf("\nRemoved the oldest screenshot. The weekly archive keeps the latest %d screenshots.", maxArchivePages)
	} else if dropped > 1 {
		content += fmt.Sprintf("\nRemoved the %d oldest screenshots. The weekly archive keeps the latest %d screenshots.", dropped, maxArchivePages)
	}
	return content
}

// storedArchivePage is one weekly_screenshot_pages row: an attachment on the
// week's archive message and the names identifying its page.
type storedArchivePage struct {
	id           int64
	attachmentID string
	names        []string
	updatedAt    time.Time
}

// PlanArchiveMerge decides, for each incoming page, which existing page it
// replaces: result[i] is the index into existing, or -1 for a new page. Pages
// are matched by case-insensitive name overlap (shared names / smaller page
// size), best ratio first, each side used at most once - so a full resubmission
// replaces everything, while a partial one replaces only the pages it
// re-shoots. Pure; unit tested without a database or Discord.
func PlanArchiveMerge(existing [][]string, incoming [][]string) []int {
	nameSet := func(names []string) map[string]bool {
		set := make(map[string]bool, len(names))
		for _, n := range names {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				set[n] = true
			}
		}
		return set
	}
	existingSets := make([]map[string]bool, len(existing))
	for i, names := range existing {
		existingSets[i] = nameSet(names)
	}

	type candidate struct {
		in, ex int
		ratio  float64
		shared int
	}
	candidates := []candidate{}
	for i, names := range incoming {
		inSet := nameSet(names)
		if len(inSet) == 0 {
			continue // an unidentifiable page can only be new
		}
		for e, exSet := range existingSets {
			if len(exSet) == 0 {
				continue
			}
			shared := 0
			for n := range inSet {
				if exSet[n] {
					shared++
				}
			}
			smaller := len(inSet)
			if len(exSet) < smaller {
				smaller = len(exSet)
			}
			ratio := float64(shared) / float64(smaller)
			if ratio >= archiveMatchThreshold {
				candidates = append(candidates, candidate{in: i, ex: e, ratio: ratio, shared: shared})
			}
		}
	}

	// Best matches claim their pages first; index order breaks exact ties so
	// the plan is deterministic.
	sort.SliceStable(candidates, func(a, b int) bool {
		if candidates[a].ratio != candidates[b].ratio {
			return candidates[a].ratio > candidates[b].ratio
		}
		if candidates[a].shared != candidates[b].shared {
			return candidates[a].shared > candidates[b].shared
		}
		if candidates[a].in != candidates[b].in {
			return candidates[a].in < candidates[b].in
		}
		return candidates[a].ex < candidates[b].ex
	})

	plan := make([]int, len(incoming))
	for i := range plan {
		plan[i] = -1
	}
	usedExisting := map[int]bool{}
	for _, c := range candidates {
		if plan[c.in] != -1 || usedExisting[c.ex] {
			continue
		}
		plan[c.in] = c.ex
		usedExisting[c.ex] = true
	}
	return plan
}

// ArchiveWeekScreenshots upserts the submitted screenshots into each guild's
// weekly archive message, mirroring AnnounceSubmission's fan-out: every guild
// in the tenant with a screenshot channel configured (or an existing archive
// message for the week) gets its own message. nil = archived everywhere
// applicable, ErrNoScreenshotChannel = skipped by configuration, anything
// else = failed (details logged).
func ArchiveWeekScreenshots(s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, tenantID string, week time.Time, pages []ScreenshotPage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("ArchiveWeekScreenshots: recovered from panic:", r)
			err = fmt.Errorf("internal error (see server logs)")
		}
	}()
	if s == nil {
		return errors.New("no discord session")
	}
	if len(pages) == 0 {
		return nil
	}
	if len(pages) > maxArchivePages {
		return fmt.Errorf("a screenshot submission exceeds the %d-page archive limit", maxArchivePages)
	}
	weekStr := cmdhelpers.GetCulvertResetDate(week).Format(time.DateOnly)

	inGuild := func(gid string) bool {
		if g, gerr := s.State.Guild(gid); gerr == nil && g != nil {
			return true
		}
		_, gerr := s.Guild(gid)
		return gerr == nil
	}
	anyConfigured := false
	var firstErr error
	for _, gid := range data.TenantGuildIDs(tenantID) {
		if gid == "" {
			continue
		}
		channelID := strings.TrimSpace(apiredis.CONF_DISCORD_SCREENSHOT_CHANNEL_ID.For(gid).GetWithDefault(rdb, ""))
		if channelID == "" && !archiveRowExists(dbc, gid, weekStr) {
			continue
		}
		if !inGuild(gid) {
			continue // shared guild pending an invite - archives once joined
		}
		anyConfigured = true
		if uerr := upsertGuildScreenshotArchive(s, dbc, gid, channelID, weekStr, pages); uerr != nil {
			log.Println("ArchiveWeekScreenshots: guild", gid, uerr)
			if firstErr == nil {
				firstErr = uerr
			}
		}
	}
	if !anyConfigured {
		return ErrNoScreenshotChannel
	}
	return firstErr
}

// ClearWeekScreenshots empties every guild's archive messages for the week -
// the /reset-week companion: the wiped scores' screenshots must not keep
// masquerading as the week's record. The message itself stays (noting the
// reset) so the next submission reuses it. Guilds without an archive message
// for the week are skipped; nil when nothing needed clearing.
func ClearWeekScreenshots(s *discordgo.Session, dbc *sql.DB, tenantID string, week time.Time) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("ClearWeekScreenshots: recovered from panic:", r)
			err = fmt.Errorf("internal error (see server logs)")
		}
	}()
	if s == nil {
		return errors.New("no discord session")
	}
	weekStr := cmdhelpers.GetCulvertResetDate(week).Format(time.DateOnly)
	var firstErr error
	for _, gid := range data.TenantGuildIDs(tenantID) {
		if gid == "" {
			continue
		}
		for part := 0; part < maxArchiveParts; part++ {
			channelID, messageID, _, lerr := loadGuildScreenshotArchive(dbc, gid, weekStr, part)
			if lerr != nil {
				if firstErr == nil {
					firstErr = lerr
				}
				continue
			}
			if messageID == "" {
				continue // no archive message this week - nothing to clear
			}
			content := fmt.Sprintf(screenshotArchiveHeading+"\nCleared by `/reset-week`.", weekStr)
			if part > 0 {
				content += fmt.Sprintf("\nPart %d", part+1)
			}
			empty := []*discordgo.MessageAttachment{}
			if _, eerr := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
				Channel:     channelID,
				ID:          messageID,
				Content:     &content,
				Attachments: &empty,
			}); eerr != nil {
				if !isUnknownArchiveMessage(eerr) {
					log.Println("ClearWeekScreenshots: edit failed, preserving record:", eerr)
					if firstErr == nil {
						firstErr = fmt.Errorf("clearing the screenshot archive message failed: %w", eerr)
					}
					continue
				}
				// Discord confirmed the message was deleted, so the next
				// submission can create a new one.
				log.Println("ClearWeekScreenshots: message deleted, dropping record:", eerr)
				if derr := deleteGuildScreenshotArchive(dbc, gid, weekStr, part); derr != nil && firstErr == nil {
					firstErr = derr
				}
				continue
			}
			if derr := deleteArchivePageRows(dbc, gid, weekStr, part); derr != nil && firstErr == nil {
				firstErr = derr
			}
		}
	}
	return firstErr
}

// archivePartUpdate holds one message's planned changes. Matching is global,
// but surviving attachments stay in their original message and never need to
// be downloaded or moved when a different part changes.
type archivePartUpdate struct {
	part           int
	channelID      string
	messageID      string
	showPart       bool
	survivors      []storedArchivePage
	dropped        []storedArchivePage
	pages          []ScreenshotPage
	replacementIDs []int64
}

// upsertGuildScreenshotArchive matches all pages across the week's two parts,
// retains at most twenty pages, then updates each affected Discord message.
// Each successful part is saved independently; a failed part keeps its prior
// database references and is reported to the submitter.
func upsertGuildScreenshotArchive(s *discordgo.Session, dbc *sql.DB, guildID, channelID, weekStr string, pages []ScreenshotPage) error {
	if len(pages) > maxArchivePages {
		return fmt.Errorf("a screenshot submission exceeds the %d-page archive limit", maxArchivePages)
	}
	parts := make([]archivePartUpdate, maxArchiveParts)
	var stored []storedArchivePage
	var pageParts []int
	for part := range parts {
		storedChannel, storedMessage, rows, err := loadGuildScreenshotArchive(dbc, guildID, weekStr, part)
		if err != nil {
			return err
		}
		parts[part] = archivePartUpdate{part: part, channelID: storedChannel, messageID: storedMessage}
		stored = append(stored, rows...)
		for range rows {
			pageParts = append(pageParts, part)
		}
		// Clearing channel configuration does not detach an existing archive.
		if channelID == "" && storedChannel != "" {
			channelID = storedChannel
		}
	}
	if channelID == "" {
		return ErrNoScreenshotChannel
	}
	existingNames := make([][]string, len(stored))
	for i, p := range stored {
		existingNames[i] = p.names
	}
	incomingNames := make([][]string, len(pages))
	for i, p := range pages {
		incomingNames[i] = p.Names
	}
	plan := PlanArchiveMerge(existingNames, incomingNames)
	replaced := make(map[int]bool)
	for _, ex := range plan {
		if ex >= 0 {
			replaced[ex] = true
		}
	}
	var survivors []int
	for i := range stored {
		if !replaced[i] {
			survivors = append(survivors, i)
		}
	}
	sort.SliceStable(survivors, func(a, b int) bool {
		x, y := stored[survivors[a]], stored[survivors[b]]
		if x.updatedAt.Equal(y.updatedAt) {
			return x.id < y.id
		}
		return x.updatedAt.Before(y.updatedAt)
	})
	dropCount := len(survivors) + len(pages) - maxArchivePages
	for n, ex := range survivors {
		part := &parts[pageParts[ex]]
		if n < dropCount {
			part.dropped = append(part.dropped, stored[ex])
		} else {
			part.survivors = append(part.survivors, stored[ex])
		}
	}
	// Replacements keep their part. Reserve those slots before allocating new
	// pages so a fresh page cannot displace a matched one into another message.
	counts := make([]int, maxArchiveParts)
	for p := range parts {
		counts[p] = len(parts[p].survivors)
	}
	for _, ex := range plan {
		if ex >= 0 {
			counts[pageParts[ex]]++
		}
	}
	for i, page := range pages {
		part, pageID := 0, int64(0)
		if ex := plan[i]; ex >= 0 {
			part, pageID = pageParts[ex], stored[ex].id
		} else {
			for part < maxArchiveParts && counts[part] >= archivePagesPerMessage {
				part++
			}
			if part == maxArchiveParts {
				return errors.New("screenshot archive capacity planning failed")
			}
			counts[part]++
		}
		parts[part].pages = append(parts[part].pages, page)
		parts[part].replacementIDs = append(parts[part].replacementIDs, pageID)
	}
	for n := range parts {
		parts[n].showPart = counts[1] > 0
	}
	var firstErr error
	for _, part := range parts {
		if len(part.pages) == 0 && len(part.dropped) == 0 {
			continue
		}
		if err := upsertGuildScreenshotArchivePart(s, dbc, guildID, channelID, weekStr, part); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("archive part %d: %w", part.part+1, err)
		}
	}
	return firstErr
}

func upsertGuildScreenshotArchivePart(s *discordgo.Session, dbc *sql.DB, guildID, channelID, weekStr string, part archivePartUpdate) error {
	files := make([]*discordgo.File, len(part.pages))
	uploadNames := make([]string, len(part.pages))
	for i, p := range part.pages {
		uploadNames[i] = fmt.Sprintf("culvert-%s-part-%d-page-%d-%d%s", weekStr, part.part+1, i+1, time.Now().UnixMilli(), imageExtension(p.Bytes))
		files[i] = &discordgo.File{Name: uploadNames[i], ContentType: http.DetectContentType(p.Bytes), Reader: bytes.NewReader(p.Bytes)}
	}
	content := screenshotArchiveContent(weekStr, len(part.survivors)+len(part.pages), len(part.dropped), time.Now())
	if part.showPart {
		content += fmt.Sprintf("\nPart %d", part.part+1)
	}
	var msg *discordgo.Message
	var err error
	if part.messageID != "" {
		msg, err = editScreenshotArchive(s, part.channelID, part.messageID, content, part.survivors, files)
		if err != nil {
			if !isUnknownArchiveMessage(err) {
				return fmt.Errorf("updating the screenshot archive message failed: %w", err)
			}
			// Only this deleted part loses its page identities. Other parts and
			// their attachments remain tracked even when recreation fails.
			if err := deleteGuildScreenshotArchive(dbc, guildID, weekStr, part.part); err != nil {
				return err
			}
			part.survivors, part.dropped = nil, nil
			for i := range part.replacementIDs {
				part.replacementIDs[i] = 0
			}
			channelID = part.channelID
			part.messageID = ""
			content = screenshotArchiveContent(weekStr, len(part.pages), 0, time.Now())
			if part.showPart {
				content += fmt.Sprintf("\nPart %d", part.part+1)
			}
			for i, p := range part.pages {
				files[i].Reader = bytes.NewReader(p.Bytes)
			}
		}
	}
	if part.messageID == "" {
		msg, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: content, Files: files})
		if err != nil {
			return fmt.Errorf("posting the screenshot archive message failed: %w", err)
		}
		part.channelID = channelID
	}
	if msg == nil {
		return errors.New("Discord returned no screenshot archive message")
	}
	// Validate every upload before persisting the returned message and page
	// identities. Database failures must reach the receipt, never claim success.
	newAttachmentIDs := make(map[string]string)
	for _, a := range msg.Attachments {
		newAttachmentIDs[a.Filename] = a.ID
	}
	for _, name := range uploadNames {
		if newAttachmentIDs[name] == "" {
			return fmt.Errorf("uploaded screenshot %s is missing from Discord's response", name)
		}
	}
	tx, err := dbc.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO weekly_screenshot_archives (guild_id, culvert_date, archive_part, channel_id, message_id)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (guild_id, culvert_date, archive_part)
		DO UPDATE SET channel_id = $4, message_id = $5`, guildID, weekStr, part.part, part.channelID, msg.ID); err != nil {
		return err
	}
	for i, p := range part.pages {
		if err := saveArchivePage(tx, guildID, weekStr, part.part, part.replacementIDs[i], newAttachmentIDs[uploadNames[i]], p.Names); err != nil {
			return err
		}
	}
	for _, p := range part.dropped {
		if err := deleteArchivePageByID(tx, p.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// editScreenshotArchive keeps the original metadata for surviving attachments.
// discordgo serializes Filename even when it is empty, so ID-only attachments
// produce invalid filename fields in the edit payload (including new uploads).
func editScreenshotArchive(s *discordgo.Session, channelID, messageID, content string, survivors []storedArchivePage, files []*discordgo.File) (*discordgo.Message, error) {
	keep := make([]*discordgo.MessageAttachment, 0, len(survivors)+len(files))
	if len(survivors) > 0 {
		current, err := s.ChannelMessage(channelID, messageID)
		if err != nil {
			return nil, err
		}
		attachments := make(map[string]*discordgo.MessageAttachment, len(current.Attachments))
		for _, attachment := range current.Attachments {
			attachments[attachment.ID] = attachment
		}
		for _, p := range survivors {
			attachment, ok := attachments[p.attachmentID]
			if !ok || attachment.Filename == "" {
				return nil, fmt.Errorf("stored screenshot attachment %s is missing or has no filename", p.attachmentID)
			}
			keep = append(keep, attachment)
		}
	}
	// The array describes every attachment after the edit: survivors by
	// Discord ID and new uploads by their multipart file index.
	for i, file := range files {
		keep = append(keep, &discordgo.MessageAttachment{ID: strconv.Itoa(i), Filename: file.Name})
	}
	return s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:     channelID,
		ID:          messageID,
		Content:     &content,
		Attachments: &keep,
		Files:       files,
	})
}

func isUnknownArchiveMessage(err error) bool {
	var restErr *discordgo.RESTError
	return errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == discordgo.ErrCodeUnknownMessage
}

// loadGuildScreenshotArchive reads one part's message and page rows for a
// guild and week. A missing record returns empty strings and no error.
func loadGuildScreenshotArchive(dbc *sql.DB, guildID, weekStr string, part int) (channelID, messageID string, pages []storedArchivePage, err error) {
	err = dbc.QueryRow(
		`SELECT channel_id, message_id FROM weekly_screenshot_archives WHERE guild_id = $1 AND culvert_date = $2 AND archive_part = $3`,
		guildID, weekStr, part).Scan(&channelID, &messageID)
	if err == sql.ErrNoRows {
		return "", "", nil, nil
	}
	if err != nil {
		log.Println("screenshot archive: query record:", err)
		return "", "", nil, errors.New("querying the screenshot archive record failed (see server logs)")
	}
	rows, err := dbc.Query(
		`SELECT id, attachment_id, names, updated_at FROM weekly_screenshot_pages WHERE guild_id = $1 AND culvert_date = $2 AND archive_part = $3 ORDER BY updated_at, id`,
		guildID, weekStr, part)
	if err != nil {
		log.Println("screenshot archive: query pages:", err)
		return "", "", nil, errors.New("querying the screenshot archive pages failed (see server logs)")
	}
	defer rows.Close()
	for rows.Next() {
		p := storedArchivePage{}
		var names string
		if err := rows.Scan(&p.id, &p.attachmentID, &names, &p.updatedAt); err != nil {
			log.Println("screenshot archive: scan page:", err)
			return "", "", nil, errors.New("reading the screenshot archive pages failed (see server logs)")
		}
		p.names = decodeArchiveNames(names)
		pages = append(pages, p)
	}
	return channelID, messageID, pages, rows.Err()
}

type archiveDBExecutor interface {
	Exec(string, ...any) (sql.Result, error)
}

// saveArchivePage writes one page row: pageID 0 inserts a new page, otherwise
// the existing row is re-pointed at the fresh attachment (a replaced page).
func saveArchivePage(dbc archiveDBExecutor, guildID, weekStr string, part int, pageID int64, attachmentID string, names []string) error {
	if pageID == 0 {
		_, err := dbc.Exec(
			`INSERT INTO weekly_screenshot_pages (guild_id, culvert_date, archive_part, attachment_id, names) VALUES ($1, $2, $3, $4, $5)`,
			guildID, weekStr, part, attachmentID, encodeArchiveNames(names))
		return err
	}
	_, err := dbc.Exec(
		`UPDATE weekly_screenshot_pages SET attachment_id = $1, names = $2, updated_at = NOW() WHERE id = $3`,
		attachmentID, encodeArchiveNames(names), pageID)
	return err
}

// deleteArchivePageByID removes one page row (its attachment was dropped from
// the message).
func deleteArchivePageByID(dbc archiveDBExecutor, id int64) error {
	_, err := dbc.Exec(`DELETE FROM weekly_screenshot_pages WHERE id = $1`, id)
	return err
}

// deleteArchivePageRows removes one part's page rows (the message was
// cleared but kept).
func deleteArchivePageRows(dbc *sql.DB, guildID, weekStr string, part int) error {
	_, err := dbc.Exec(`DELETE FROM weekly_screenshot_pages WHERE guild_id = $1 AND culvert_date = $2 AND archive_part = $3`, guildID, weekStr, part)
	return err
}

// deleteGuildScreenshotArchive removes one part's archive record and page rows
// (Discord confirmed the message itself was deleted).
func deleteGuildScreenshotArchive(dbc *sql.DB, guildID, weekStr string, part int) error {
	if err := deleteArchivePageRows(dbc, guildID, weekStr, part); err != nil {
		return err
	}
	_, err := dbc.Exec(`DELETE FROM weekly_screenshot_archives WHERE guild_id = $1 AND culvert_date = $2 AND archive_part = $3`, guildID, weekStr, part)
	return err
}

// archiveRowExists reports whether a guild already has an archive message for
// the week (so it keeps updating even if the channel config was later
// cleared, matching weeklyRowExists).
func archiveRowExists(dbc *sql.DB, guildID, weekStr string) bool {
	var one int
	err := dbc.QueryRow(
		`SELECT 1 FROM weekly_screenshot_archives WHERE guild_id = $1 AND culvert_date = $2`,
		guildID, weekStr).Scan(&one)
	return err == nil
}

// encodeArchiveNames renders a page's identity for storage: lowercased,
// trimmed, newline-joined (IGNs never contain newlines), empty names dropped.
func encodeArchiveNames(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, "\n")
}

// decodeArchiveNames is encodeArchiveNames' inverse.
func decodeArchiveNames(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// imageExtension picks the upload filename's extension from the image bytes
// so the archived file opens as what it is; unknown types fall back to .png
// (Discord previews by content anyway).
func imageExtension(b []byte) string {
	switch http.DetectContentType(b) {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
