package helpers

// The weekly screenshot archive: one bot message per (guild, culvert week) in
// an optional channel (CONF_DISCORD_SCREENSHOT_CHANNEL_ID), collecting the
// screenshots each submission was parsed from - a browsable history of the
// raw inputs behind the recorded scores.
//
// The message is created on the week's first screenshot submission and EDITED
// in place afterwards, like the weekly announcement. Its attachments are the
// image store; weekly_screenshot_archives / weekly_screenshot_pages
// (db_migrations/9) track the message and which roster "page" each attachment
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

// maxArchivePages is Discord's attachments-per-message cap. When kept + new
// pages would exceed it, the OLDEST stored pages are dropped first (the new
// submission always wins).
const maxArchivePages = 10

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
		pages = pages[:maxArchivePages]
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

// ClearWeekScreenshots empties every guild's archive message for the week -
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
		channelID, messageID, _, lerr := loadGuildScreenshotArchive(dbc, gid, weekStr)
		if lerr != nil {
			if firstErr == nil {
				firstErr = lerr
			}
			continue
		}
		if messageID == "" {
			continue // no archive message this week - nothing to clear
		}
		content := "**Culvert screenshots — week of " + weekStr + "**\nCleared by `/reset-week`."
		empty := []*discordgo.MessageAttachment{}
		if _, eerr := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:     channelID,
			ID:          messageID,
			Content:     &content,
			Attachments: &empty,
		}); eerr != nil {
			// The message is gone: drop the record so the next submission
			// recreates it cleanly.
			log.Println("ClearWeekScreenshots: edit failed, dropping record:", eerr)
			if derr := deleteGuildScreenshotArchive(dbc, gid, weekStr); derr != nil && firstErr == nil {
				firstErr = derr
			}
			continue
		}
		if derr := deleteArchivePageRows(dbc, gid, weekStr); derr != nil && firstErr == nil {
			firstErr = derr
		}
	}
	return firstErr
}

// upsertGuildScreenshotArchive merges the submitted pages into one guild's
// weekly archive message: surviving attachments are kept by id, replaced and
// new pages are uploaded fresh, and the page rows are rewritten to match what
// the message now shows.
func upsertGuildScreenshotArchive(s *discordgo.Session, dbc *sql.DB, guildID, channelID, weekStr string, pages []ScreenshotPage) error {
	storedChannel, storedMessage, stored, err := loadGuildScreenshotArchive(dbc, guildID, weekStr)
	if err != nil {
		return err
	}
	if storedMessage == "" && channelID == "" {
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

	// Survivors = stored pages no incoming page replaces; oldest are dropped
	// first if the attachment cap would overflow.
	replaced := map[int]bool{}
	for _, ex := range plan {
		if ex >= 0 {
			replaced[ex] = true
		}
	}
	survivors := []storedArchivePage{}
	for i, p := range stored {
		if !replaced[i] {
			survivors = append(survivors, p)
		}
	}
	sort.SliceStable(survivors, func(a, b int) bool {
		return survivors[a].updatedAt.Before(survivors[b].updatedAt)
	})
	dropped := []storedArchivePage{}
	if over := len(survivors) + len(pages) - maxArchivePages; over > 0 {
		dropped, survivors = survivors[:over], survivors[over:]
	}

	// Upload names are matched against the response to learn each new
	// attachment's id; the page index keys them within this one edit.
	files := make([]*discordgo.File, len(pages))
	uploadNames := make([]string, len(pages))
	for i, p := range pages {
		uploadNames[i] = fmt.Sprintf("culvert-%s-page-%d-%d%s", weekStr, i+1, time.Now().UnixMilli(), imageExtension(p.Bytes))
		files[i] = &discordgo.File{
			Name:        uploadNames[i],
			ContentType: http.DetectContentType(p.Bytes),
			Reader:      bytes.NewReader(p.Bytes),
		}
	}
	content := fmt.Sprintf("**Culvert screenshots — week of %s**\n%d page(s) · last updated <t:%d:f>",
		weekStr, len(survivors)+len(pages), time.Now().Unix())
	if len(dropped) > 0 {
		content += fmt.Sprintf("\n(%d older page(s) dropped - Discord allows %d attachments per message)", len(dropped), maxArchivePages)
	}

	var msg *discordgo.Message
	if storedMessage != "" {
		// The attachments array is authoritative on edit: it must name every
		// attachment that exists AFTER the edit - survivors by their real id,
		// the fresh uploads by their multipart index placeholder ({id: N} for
		// files[N]) - anything unlisted is removed.
		keep := make([]*discordgo.MessageAttachment, 0, len(survivors)+len(files))
		for _, p := range survivors {
			keep = append(keep, &discordgo.MessageAttachment{ID: p.attachmentID})
		}
		for i := range files {
			keep = append(keep, &discordgo.MessageAttachment{ID: strconv.Itoa(i)})
		}
		msg, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:     storedChannel,
			ID:          storedMessage,
			Content:     &content,
			Attachments: &keep,
			Files:       files,
		})
		if err != nil {
			// The stored message is gone (deleted message/channel): drop the
			// record and post fresh below - a submission may always create.
			log.Println("screenshot archive: stored message unreachable, recreating:", err)
			if derr := deleteGuildScreenshotArchive(dbc, guildID, weekStr); derr != nil {
				return derr
			}
			stored, survivors, dropped = nil, nil, nil
			for i := range plan {
				plan[i] = -1
			}
			if channelID == "" {
				channelID = storedChannel
			}
			storedMessage = ""
			content = fmt.Sprintf("**Culvert screenshots — week of %s**\n%d page(s) · last updated <t:%d:f>",
				weekStr, len(pages), time.Now().Unix())
			for i, p := range pages {
				files[i].Reader = bytes.NewReader(p.Bytes) // the failed edit consumed the readers
			}
		}
	}
	if storedMessage == "" {
		msg, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content: content,
			Files:   files,
		})
		if err != nil {
			log.Println("screenshot archive: send failed:", err)
			return errors.New("posting the screenshot archive message failed - check the bot's permissions in the archive channel")
		}
		if _, err := dbc.Exec(
			`INSERT INTO weekly_screenshot_archives (guild_id, culvert_date, channel_id, message_id) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (guild_id, culvert_date) DO UPDATE SET channel_id = $3, message_id = $4`,
			guildID, weekStr, channelID, msg.ID); err != nil {
			log.Println("screenshot archive: insert record:", err)
		}
	}

	// Resolve the fresh uploads' attachment ids from the response and rewrite
	// the page rows: replaced rows are updated in place, new pages inserted,
	// cap-dropped rows deleted.
	newAttachmentIDs := map[string]string{} // upload filename -> attachment id
	keptIDs := map[string]bool{}
	for _, p := range survivors {
		keptIDs[p.attachmentID] = true
	}
	if msg != nil {
		for _, a := range msg.Attachments {
			if !keptIDs[a.ID] {
				newAttachmentIDs[a.Filename] = a.ID
			}
		}
	}
	for i := range pages {
		attachmentID, ok := newAttachmentIDs[uploadNames[i]]
		if !ok {
			log.Println("screenshot archive: uploaded page missing from response:", uploadNames[i])
			continue
		}
		pageID := int64(0)
		if ex := plan[i]; ex >= 0 {
			pageID = stored[ex].id
		}
		if err := saveArchivePage(dbc, guildID, weekStr, pageID, attachmentID, pages[i].Names); err != nil {
			log.Println("screenshot archive: save page row:", err)
		}
	}
	for _, p := range dropped {
		if err := deleteArchivePageByID(dbc, p.id); err != nil {
			log.Println("screenshot archive: delete dropped page row:", err)
		}
	}
	return nil
}

// loadGuildScreenshotArchive reads a guild's archive record and page rows for
// a week. A missing record returns empty strings and no error.
func loadGuildScreenshotArchive(dbc *sql.DB, guildID, weekStr string) (channelID, messageID string, pages []storedArchivePage, err error) {
	err = dbc.QueryRow(
		`SELECT channel_id, message_id FROM weekly_screenshot_archives WHERE guild_id = $1 AND culvert_date = $2`,
		guildID, weekStr).Scan(&channelID, &messageID)
	if err == sql.ErrNoRows {
		return "", "", nil, nil
	}
	if err != nil {
		log.Println("screenshot archive: query record:", err)
		return "", "", nil, errors.New("querying the screenshot archive record failed (see server logs)")
	}
	rows, err := dbc.Query(
		`SELECT id, attachment_id, names, updated_at FROM weekly_screenshot_pages WHERE guild_id = $1 AND culvert_date = $2 ORDER BY updated_at, id`,
		guildID, weekStr)
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

// saveArchivePage writes one page row: pageID 0 inserts a new page, otherwise
// the existing row is re-pointed at the fresh attachment (a replaced page).
func saveArchivePage(dbc *sql.DB, guildID, weekStr string, pageID int64, attachmentID string, names []string) error {
	if pageID == 0 {
		_, err := dbc.Exec(
			`INSERT INTO weekly_screenshot_pages (guild_id, culvert_date, attachment_id, names) VALUES ($1, $2, $3, $4)`,
			guildID, weekStr, attachmentID, encodeArchiveNames(names))
		return err
	}
	_, err := dbc.Exec(
		`UPDATE weekly_screenshot_pages SET attachment_id = $1, names = $2, updated_at = NOW() WHERE id = $3`,
		attachmentID, encodeArchiveNames(names), pageID)
	return err
}

// deleteArchivePageByID removes one page row (its attachment was dropped from
// the message).
func deleteArchivePageByID(dbc *sql.DB, id int64) error {
	_, err := dbc.Exec(`DELETE FROM weekly_screenshot_pages WHERE id = $1`, id)
	return err
}

// deleteArchivePageRows removes all of a week's page rows (the message was
// cleared but kept).
func deleteArchivePageRows(dbc *sql.DB, guildID, weekStr string) error {
	_, err := dbc.Exec(`DELETE FROM weekly_screenshot_pages WHERE guild_id = $1 AND culvert_date = $2`, guildID, weekStr)
	return err
}

// deleteGuildScreenshotArchive removes a week's archive record and page rows
// (the message itself is unreachable).
func deleteGuildScreenshotArchive(dbc *sql.DB, guildID, weekStr string) error {
	if err := deleteArchivePageRows(dbc, guildID, weekStr); err != nil {
		return err
	}
	_, err := dbc.Exec(`DELETE FROM weekly_screenshot_archives WHERE guild_id = $1 AND culvert_date = $2`, guildID, weekStr)
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
