package helpers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// ErrNoWeeklyChannel reports that no weekly announcement channel is
// configured: announcing is skipped, which callers may surface as a
// non-failure.
var ErrNoWeeklyChannel = errors.New("no weekly channel configured")

// AnnounceSubmission is the only place the bot posts without being asked, and
// it only posts to the TENANT's configured weekly channel
// (CONF_DISCORD_WEEKLY_CHANNEL_ID):
//   - one SUMMARY message per culvert week (coverage, top scores, guild
//     total), created on the first submission for that week and edited in
//     place afterwards
//   - a thread under it whose first bot comment is the FULL score table
//     (edited in place on every refresh), followed by submission notes and
//     personal best congratulations (mentioning the linked Discord member)
//
// Submissions for historical weeks update that week's message/thread.
// changedIDs are the character ids touched by this submission; only their new
// personal bests are congratulated. Everything - scores, roster,
// announcement records - is scoped to tenantID.
//
// The returned error tells the caller whether the announcement went out:
// nil = message (and thread note) updated, ErrNoWeeklyChannel = skipped by
// configuration, anything else = failed (details logged).
func AnnounceSubmission(s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, tenantID string, week time.Time, changedIDs []int64) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("AnnounceSubmission: recovered from panic:", r)
			err = fmt.Errorf("internal error (see server logs)")
		}
	}()
	if s == nil {
		return errors.New("no discord session")
	}
	channelID := apiredis.CONF_DISCORD_WEEKLY_CHANNEL_ID.For(tenantID).GetWithDefault(rdb, "")
	if channelID == "" {
		return ErrNoWeeklyChannel
	}

	week = cmdhelpers.GetCulvertResetDate(week)
	weekStr := week.Format(time.DateOnly)
	prevWeek := cmdhelpers.GetCulvertPreviousDate(week)

	rows, err := weekScores(dbc, tenantID, week)
	if err != nil {
		log.Println("AnnounceSubmission: query week scores:", err)
		return errors.New("querying the week's scores failed (see server logs)")
	}
	if len(rows) == 0 {
		return errors.New("no scores recorded for that week")
	}
	prevRows, err := weekScores(dbc, tenantID, prevWeek)
	if err != nil {
		// The previous-week comparison is decorative: log and degrade.
		log.Println("AnnounceSubmission: query prev week scores:", err)
		prevRows = nil
	}
	prevByID := map[int64]weekScore{}
	for _, p := range prevRows {
		prevByID[p.CharacterID] = p
	}

	// Roster size for the "N of M members submitted" header; degrade to the
	// submitted count when the roster is unavailable.
	rosterCount := len(rows)
	if chars, cerr := cmdhelpers.GetActiveCharacters(rdb, dbc, tenantID); cerr == nil && len(*chars) >= len(rows) {
		rosterCount = len(*chars)
	}

	summary := buildWeeklySummary(weekStr, rosterCount, rows)
	tableContent, tableFile := buildWeeklyTable(weekStr, rows, prevByID)
	threadID, err := upsertWeeklyArtifacts(s, dbc, tenantID, channelID, weekStr, summary, tableContent, tableFile, false)
	if err != nil {
		return err
	}
	if threadID == "" {
		return errors.New("the weekly summary was updated but its thread is unavailable")
	}

	note := buildSubmissionNote(dbc, week, weekStr, rows, changedIDs)
	for _, chunk := range chunkMessage(note, 1900) {
		if _, err := s.ChannelMessageSend(threadID, chunk); err != nil {
			log.Println("AnnounceSubmission: thread note failed:", err)
			return errors.New("the weekly table was updated but posting the thread note failed")
		}
	}
	return nil
}

// RefreshWeeklyAnnouncement re-renders the EXISTING weekly artifacts (summary
// message + full-table thread message) from the tenant's current data for
// that week - used after any data mutation outside a submission (/register,
// /unregister, /set-culvert corrections, /reset-week) so the posted summary
// and table never go stale. Unlike AnnounceSubmission it tolerates an empty
// week (zero-coverage state), never posts a thread note, and a missing
// announcement record is a silent no-op (mutations never CREATE a weekly
// post - only submissions do).
func RefreshWeeklyAnnouncement(s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, tenantID string, week time.Time) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("RefreshWeeklyAnnouncement: recovered from panic:", r)
			err = fmt.Errorf("internal error (see server logs)")
		}
	}()
	if s == nil {
		return errors.New("no discord session")
	}
	week = cmdhelpers.GetCulvertResetDate(week)
	weekStr := week.Format(time.DateOnly)

	rows, err := weekScores(dbc, tenantID, week)
	if err != nil {
		log.Println("RefreshWeeklyAnnouncement: query week scores:", err)
		return errors.New("querying the week's scores failed (see server logs)")
	}
	prevRows, err := weekScores(dbc, tenantID, cmdhelpers.GetCulvertPreviousDate(week))
	if err != nil {
		prevRows = nil
	}
	prevByID := map[int64]weekScore{}
	for _, p := range prevRows {
		prevByID[p.CharacterID] = p
	}
	rosterCount := len(rows)
	if chars, cerr := cmdhelpers.GetActiveCharacters(rdb, dbc, tenantID); cerr == nil && len(*chars) >= len(rows) {
		rosterCount = len(*chars)
	}

	summary := buildWeeklySummary(weekStr, rosterCount, rows)
	tableContent, tableFile := buildWeeklyTable(weekStr, rows, prevByID)
	_, err = upsertWeeklyArtifacts(s, dbc, tenantID, "", weekStr, summary, tableContent, tableFile, true)
	return err
}

type weekScore struct {
	CharacterID   int64
	Name          string
	DiscordUserID string
	Score         int
	Rank          int
}

// weekScores returns the tenant's scores for the week ordered by score desc,
// ranked. Untracked characters (discord_user_id '1') keep their history rows
// but are excluded from every rendered table.
func weekScores(dbc *sql.DB, tenantID string, week time.Time) ([]weekScore, error) {
	rows, err := dbc.Query(`
		SELECT c.id, c.maple_character_name, c.discord_user_id, s.score
		FROM character_culvert_scores s
		JOIN characters c ON c.id = s.character_id
		WHERE c.guild_id = $1 AND s.culvert_date = $2 AND c.discord_user_id != '1'
		ORDER BY s.score DESC, c.maple_character_name ASC`, tenantID, week.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []weekScore{}
	for rows.Next() {
		w := weekScore{}
		if err := rows.Scan(&w.CharacterID, &w.Name, &w.DiscordUserID, &w.Score); err != nil {
			return nil, err
		}
		w.Rank = len(out) + 1
		out = append(out, w)
	}
	return out, rows.Err()
}

// FormatThousands renders 260895 as "260,895".
func FormatThousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// buildWeeklySummary renders the channel message: a compact, always-current
// overview of the week's data. The full table lives in the thread (see
// buildWeeklyTable) so the channel stays scannable.
func buildWeeklySummary(weekStr string, rosterCount int, rows []weekScore) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "## Culvert week of %s\n", weekStr)
	fmt.Fprintf(b, "%d of %d members submitted - last updated <t:%d:R>\n", len(rows), rosterCount, time.Now().Unix())
	if len(rows) == 0 {
		b.WriteString("No scores recorded yet this week.")
		return b.String()
	}
	medals := []string{":first_place:", ":second_place:", ":third_place:"}
	total := 0
	for i, r := range rows {
		total += r.Score
		if i < len(medals) {
			fmt.Fprintf(b, "%s `%s` - %s\n", medals[i], r.Name, FormatThousands(r.Score))
		}
	}
	fmt.Fprintf(b, "Guild total: **%s**\n", FormatThousands(total))
	b.WriteString("Full table + submission notes: in this message's thread.")
	return b.String()
}

// buildWeeklyTable renders the full week table for the thread message. When
// it fits, the table is inline in a code block; otherwise it is attached as a
// file.
func buildWeeklyTable(weekStr string, rows []weekScore, prevByID map[int64]weekScore) (string, *discordgo.File) {
	if len(rows) == 0 {
		return "**Full table - week of " + weekStr + "**\nNo scores recorded yet this week.", nil
	}
	t := table.NewWriter()
	t.AppendHeader(table.Row{"Rank", "Character", "Score", "Last Week"})
	for _, r := range rows {
		last := "-"
		if p, ok := prevByID[r.CharacterID]; ok {
			last = fmt.Sprintf("%s (#%d)", FormatThousands(p.Score), p.Rank)
		}
		t.AppendRow(table.Row{r.Rank, r.Name, FormatThousands(r.Score), last})
	}
	rendered := t.Render()

	header := "**Full table - week of " + weekStr + "**"
	inline := header + "\n```\n" + rendered + "\n```"
	if len(inline) <= 1900 {
		return inline, nil
	}
	return header, &discordgo.File{
		Name:        "culvert-" + weekStr + ".txt",
		ContentType: "text/plain",
		Reader:      strings.NewReader(rendered),
	}
}

// upsertWeeklyArtifacts creates or edits the tenant's weekly artifacts: the
// summary message in the designated channel, its thread, and the full-table
// message INSIDE the thread (the thread's first bot comment, edited in place
// afterwards). It returns the thread id ("" when the summary went out but the
// thread could not be created). A non-nil error means the summary message
// itself could not be created or updated.
//
// requireExisting makes a missing announcement record a silent no-op (used by
// refreshes: registering a character must never CREATE a weekly post).
func upsertWeeklyArtifacts(s *discordgo.Session, dbc *sql.DB, tenantID, channelID, weekStr, summary, tableContent string, tableFile *discordgo.File, requireExisting bool) (string, error) {
	var storedChannel, storedMessage, storedThread, storedTableMsg string
	err := dbc.QueryRow(
		`SELECT channel_id, message_id, thread_id, table_message_id FROM weekly_announcements WHERE guild_id = $1 AND culvert_date = $2`,
		tenantID, weekStr).Scan(&storedChannel, &storedMessage, &storedThread, &storedTableMsg)
	if err == sql.ErrNoRows && requireExisting {
		return "", nil // nothing announced for that week - nothing to refresh
	}
	if err != nil && err != sql.ErrNoRows {
		log.Println("weekly artifacts: query weekly_announcements:", err)
		return "", errors.New("querying the weekly announcement record failed (see server logs)")
	}

	if err == nil {
		summaryEdit := &discordgo.MessageEdit{
			Channel: storedChannel,
			ID:      storedMessage,
			Content: &summary,
		}
		// Drop any table file attached by the pre-thread-table layout.
		summaryEdit.Attachments = &[]*discordgo.MessageAttachment{}
		if _, err := s.ChannelMessageEditComplex(summaryEdit); err == nil {
			if storedThread == "" {
				// Self-heal: an earlier run posted the message but failed to
				// start its thread. Retry now and remember the result.
				if thread, terr := s.MessageThreadStartComplex(storedChannel, storedMessage, &discordgo.ThreadStart{
					Name:                "Culvert " + weekStr,
					AutoArchiveDuration: 10080,
				}); terr != nil {
					log.Println("weekly artifacts: thread self-heal failed:", terr)
				} else {
					storedThread = thread.ID
					if _, uerr := dbc.Exec(
						`UPDATE weekly_announcements SET thread_id = $1 WHERE guild_id = $2 AND culvert_date = $3`,
						storedThread, tenantID, weekStr); uerr != nil {
						log.Println("weekly artifacts: store healed thread id:", uerr)
					}
				}
			}
			if storedThread != "" {
				upsertThreadTable(s, dbc, tenantID, weekStr, storedThread, storedTableMsg, tableContent, tableFile)
			}
			return storedThread, nil
		}
		// The stored message is gone (deleted channel/message); recreate it.
		log.Println("weekly artifacts: stored weekly message unreachable, recreating")
		if _, err := dbc.Exec(`DELETE FROM weekly_announcements WHERE guild_id = $1 AND culvert_date = $2`, tenantID, weekStr); err != nil {
			log.Println("weekly artifacts: delete stale weekly_announcements:", err)
			return "", errors.New("the stored weekly message is unreachable and its record could not be reset (see server logs)")
		}
		if channelID == "" {
			channelID = storedChannel
		}
	}

	msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: summary})
	if err != nil {
		log.Println("weekly artifacts: send weekly message:", err)
		return "", errors.New("posting the weekly message failed - check the bot's permissions in the weekly channel")
	}
	threadID := ""
	thread, err := s.MessageThreadStartComplex(channelID, msg.ID, &discordgo.ThreadStart{
		Name:                "Culvert " + weekStr,
		AutoArchiveDuration: 10080,
	})
	if err != nil {
		log.Println("weekly artifacts: start thread:", err)
	} else {
		threadID = thread.ID
	}
	if _, err := dbc.Exec(
		`INSERT INTO weekly_announcements (guild_id, culvert_date, channel_id, message_id, thread_id) VALUES ($1, $2, $3, $4, $5)`,
		tenantID, weekStr, channelID, msg.ID, threadID); err != nil {
		log.Println("weekly artifacts: insert weekly_announcements:", err)
	}
	if threadID != "" {
		// The table is the thread's FIRST comment on a fresh week.
		upsertThreadTable(s, dbc, tenantID, weekStr, threadID, "", tableContent, tableFile)
	}
	return threadID, nil
}

// upsertThreadTable edits the week's full-table message inside the thread, or
// posts it (recording its id) when it doesn't exist yet or its stored id is
// unreachable. Failures are logged, never fatal: the table is derived data
// and the next refresh retries.
func upsertThreadTable(s *discordgo.Session, dbc *sql.DB, tenantID, weekStr, threadID, tableMsgID, content string, file *discordgo.File) {
	if tableMsgID != "" {
		edit := &discordgo.MessageEdit{
			Channel: threadID,
			ID:      tableMsgID,
			Content: &content,
		}
		edit.Attachments = &[]*discordgo.MessageAttachment{}
		if file != nil {
			edit.Files = []*discordgo.File{file}
		}
		if _, err := s.ChannelMessageEditComplex(edit); err == nil {
			return
		}
		log.Println("weekly artifacts: table message unreachable, reposting")
	}
	send := &discordgo.MessageSend{Content: content}
	if file != nil {
		send.Files = []*discordgo.File{file}
	}
	msg, err := s.ChannelMessageSendComplex(threadID, send)
	if err != nil {
		log.Println("weekly artifacts: post thread table:", err)
		return
	}
	if _, err := dbc.Exec(
		`UPDATE weekly_announcements SET table_message_id = $1 WHERE guild_id = $2 AND culvert_date = $3`,
		msg.ID, tenantID, weekStr); err != nil {
		log.Println("weekly artifacts: store table message id:", err)
	}
}

// buildSubmissionNote renders the thread note for one submission: timestamp,
// scope (counting real scores separately from zero-fills), and personal-best
// congratulations for the characters that changed. Tenant scoping is carried
// by the character ids and rows the caller already resolved.
func buildSubmissionNote(dbc *sql.DB, week time.Time, weekStr string, rows []weekScore, changedIDs []int64) string {
	changedSet := map[int64]bool{}
	for _, id := range changedIDs {
		changedSet[id] = true
	}
	nonZero, zeroFilled := 0, 0
	for _, row := range rows {
		if !changedSet[row.CharacterID] {
			continue
		}
		if row.Score > 0 {
			nonZero++
		} else {
			zeroFilled++
		}
	}
	scope := fmt.Sprintf("%d characters", len(changedIDs))
	if zeroFilled > 0 {
		scope = fmt.Sprintf("%d characters: %d with scores, %d zero-filled", len(changedIDs), nonZero, zeroFilled)
	}
	note := fmt.Sprintf("Scores submitted <t:%d:f> for **%s** (%s).", time.Now().Unix(),
		cmdhelpers.FormatWeekLabel(week, time.Now()), scope)

	pbs := newPersonalBests(dbc, week, rows, changedIDs)
	if len(pbs) > 0 {
		note += "\n\nNew personal bests! :tada:"
		for _, pb := range pbs {
			note += "\n" + pb
		}
	}
	return note
}

// newPersonalBests returns congratulation lines ("@member (Name): old -> new")
// for changed characters whose submitted score beats their best before week.
func newPersonalBests(dbc *sql.DB, week time.Time, rows []weekScore, changedIDs []int64) []string {
	changed := map[int64]bool{}
	for _, id := range changedIDs {
		changed[id] = true
	}

	patchCutoff := ""
	if week.After(data.Date2mPatch) {
		patchCutoff = data.Date2mPatch.Format(time.DateOnly)
	}

	out := []string{}
	for _, r := range rows {
		if !changed[r.CharacterID] || r.Score <= 0 {
			continue
		}
		var best sql.NullInt64
		var err error
		if patchCutoff != "" {
			err = dbc.QueryRow(
				`SELECT MAX(score) FROM character_culvert_scores WHERE character_id = $1 AND culvert_date < $2 AND culvert_date >= $3 AND score > 0`,
				r.CharacterID, week.Format(time.DateOnly), patchCutoff).Scan(&best)
		} else {
			err = dbc.QueryRow(
				`SELECT MAX(score) FROM character_culvert_scores WHERE character_id = $1 AND culvert_date < $2 AND score > 0`,
				r.CharacterID, week.Format(time.DateOnly)).Scan(&best)
		}
		if err != nil || !best.Valid || int(best.Int64) >= r.Score {
			continue
		}
		who := "`" + r.Name + "`"
		if r.DiscordUserID != "" && r.DiscordUserID != "1" && r.DiscordUserID != "2" {
			who = "<@" + r.DiscordUserID + "> (" + r.Name + ")"
		}
		out = append(out, fmt.Sprintf("%s: %s -> %s", who, FormatThousands(int(best.Int64)), FormatThousands(r.Score)))
	}
	return out
}

// chunkMessage splits content on line boundaries into <= limit sized chunks.
func chunkMessage(content string, limit int) []string {
	if len(content) <= limit {
		return []string{content}
	}
	chunks := []string{}
	cur := ""
	for _, line := range strings.Split(content, "\n") {
		if len(cur)+len(line)+1 > limit && cur != "" {
			chunks = append(chunks, cur)
			cur = ""
		}
		if cur != "" {
			cur += "\n"
		}
		cur += line
	}
	if cur != "" {
		chunks = append(chunks, cur)
	}
	return chunks
}
