package helpers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// ErrNoWeeklyChannel reports that no weekly announcement channel is
// configured: announcing is skipped, which callers may surface as a
// non-failure.
var ErrNoWeeklyChannel = errors.New("no weekly channel configured")

// AnnounceSubmission is the only place the bot posts without being asked. It
// renders the week once from the tenant's shared data and posts it into EACH
// guild in the tenant that has a weekly channel configured
// (CONF_DISCORD_WEEKLY_CHANNEL_ID, per-guild) - so two servers sharing one
// dataset each get the announcement in their own channel. Per guild:
//   - one SUMMARY message per culvert week (coverage, top scores, guild
//     total), created on the first submission for that week and edited in
//     place afterwards
//   - a thread under it whose first bot comment is the FULL score table, and a
//     single submission note (personal-best congratulations, mentioning the
//     linked Discord member) - both edited in place on every refresh
//
// Submissions for historical weeks update that week's message/thread.
// changedIDs is retained for API compatibility (the note reflects the week's
// full PB state). Scores and roster are scoped to tenantID; announcement
// records are keyed per real guild.
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
	week = cmdhelpers.GetCulvertResetDate(week)
	art, err := buildWeeklyArtifacts(dbc, rdb, tenantID, week)
	if err != nil {
		return err
	}
	if len(art.rows) == 0 {
		return errors.New("no scores recorded for that week")
	}
	// Fan out to EVERY guild in the tenant that has a weekly channel: servers
	// sharing one dataset each get the same announcement in their own channel.
	// changedIDs is retained for API compatibility; the note now reflects the
	// week's full personal-best state rather than one submission's delta.
	_ = changedIDs
	return fanOutWeekly(s, dbc, rdb, tenantID, art, false)
}

// weeklyArtifacts is the shared render of one week's data, computed once from
// the tenant's dataset and posted into each of the tenant's guild channels.
type weeklyArtifacts struct {
	weekStr    string
	rows       []weekScore
	summary    string
	tableEmbed *discordgo.MessageEmbed
	note       string
}

// buildWeeklyArtifacts renders a week's summary, full-table embed and thread
// note once from the tenant's shared data (rows may be empty).
func buildWeeklyArtifacts(dbc *sql.DB, rdb *redis.Client, tenantID string, week time.Time) (weeklyArtifacts, error) {
	weekStr := week.Format(time.DateOnly)
	rows, err := weekScores(dbc, tenantID, week)
	if err != nil {
		log.Println("weekly artifacts: query week scores:", err)
		return weeklyArtifacts{}, errors.New("querying the week's scores failed (see server logs)")
	}
	prevRows, perr := weekScores(dbc, tenantID, cmdhelpers.GetCulvertPreviousDate(week))
	if perr != nil {
		prevRows = nil // the previous-week comparison is decorative
	}
	prevByID := map[int64]weekScore{}
	for _, p := range prevRows {
		prevByID[p.CharacterID] = p
	}
	rosterCount := len(rows)
	if chars, cerr := cmdhelpers.GetActiveCharacters(rdb, dbc, tenantID); cerr == nil && len(*chars) >= len(rows) {
		rosterCount = len(*chars)
	}
	return weeklyArtifacts{
		weekStr:    weekStr,
		rows:       rows,
		summary:    buildWeeklySummary(weekStr, rosterCount, rows),
		tableEmbed: BuildWeekTableEmbed("Full table - week of "+weekStr, rosterCount, rows, prevByID),
		note:       buildWeekNote(dbc, week, weekStr, rows),
	}, nil
}

// fanOutWeekly upserts the artifacts into every guild in the tenant that has a
// weekly channel configured (or already holds a message for the week). Each
// server posts the same shared data into its OWN channel. requireExisting only
// edits guilds that already have a message - a non-submission refresh must
// never CREATE a new post anywhere. Returns ErrNoWeeklyChannel only when no
// guild in the tenant announces at all.
func fanOutWeekly(s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, tenantID string, art weeklyArtifacts, requireExisting bool) error {
	inGuild := func(gid string) bool {
		if g, err := s.State.Guild(gid); err == nil && g != nil {
			return true
		}
		_, err := s.Guild(gid)
		return err == nil
	}
	anyConfigured := false
	var firstErr error
	for _, gid := range data.TenantGuildIDs(tenantID) {
		if gid == "" {
			continue
		}
		channelID := apiredis.CONF_DISCORD_WEEKLY_CHANNEL_ID.For(gid).GetWithDefault(rdb, "")
		if channelID == "" && !weeklyRowExists(dbc, gid, art.weekStr) {
			continue
		}
		// A shared guild the bot has not joined yet: its channel is configured
		// but unreachable - skip quietly (it announces once the bot is invited)
		// rather than surface a per-submission failure.
		if !inGuild(gid) {
			continue
		}
		anyConfigured = true
		if _, err := upsertWeeklyArtifacts(s, dbc, gid, channelID, art, requireExisting); err != nil {
			log.Println("fanOutWeekly: guild", gid, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if !anyConfigured {
		return ErrNoWeeklyChannel
	}
	return firstErr
}

// weeklyRowExists reports whether a guild already has an announcement record
// for the week (so a refresh can update it even if its channel config was
// later cleared).
func weeklyRowExists(dbc *sql.DB, guildID, weekStr string) bool {
	var one int
	err := dbc.QueryRow(
		`SELECT 1 FROM weekly_announcements WHERE guild_id = $1 AND culvert_date = $2`,
		guildID, weekStr).Scan(&one)
	return err == nil
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
	art, err := buildWeeklyArtifacts(dbc, rdb, tenantID, week)
	if err != nil {
		return err
	}
	return fanOutWeekly(s, dbc, rdb, tenantID, art, true)
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
	b.WriteString("Not on the board yet? `/register` to link your character to your Discord account - see `/culvert-help`.")
	return b.String()
}

// weekEmbedColor matches the /culvert chart embed so every culvert visual
// looks like one family.
const weekEmbedColor = 3580651

// BuildWeekTableEmbed renders the week's ranked board as a Discord-native
// embed (no ASCII tables): medals for the top three, bold names, thousands
// separators, and rank movement vs the previous week. Shared by the weekly
// thread table and /culvert-all.
func BuildWeekTableEmbed(title string, rosterCount int, rows []weekScore, prevByID map[int64]weekScore) *discordgo.MessageEmbed {
	e := &discordgo.MessageEmbed{
		Title:     title,
		Color:     weekEmbedColor,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if len(rows) == 0 {
		e.Description = "No scores recorded yet this week."
		return e
	}
	e.Footer = &discordgo.MessageEmbedFooter{
		Text: fmt.Sprintf("%d of %d members submitted", len(rows), rosterCount),
	}

	medals := []string{":first_place:", ":second_place:", ":third_place:"}
	lines := make([]string, 0, len(rows))
	for idx, r := range rows {
		prefix := fmt.Sprintf("`#%2d`", r.Rank)
		name := r.Name
		if idx < len(medals) {
			prefix = medals[idx]
			name = "**" + name + "**"
		}
		line := prefix + " " + name + " - " + FormatThousands(r.Score)
		if p, ok := prevByID[r.CharacterID]; ok {
			switch {
			case p.Rank > r.Rank:
				line += fmt.Sprintf(" (:arrow_up: %d)", p.Rank-r.Rank)
			case p.Rank < r.Rank:
				line += fmt.Sprintf(" (:arrow_down: %d)", r.Rank-p.Rank)
			}
		} else if len(prevByID) > 0 {
			line += " (new)"
		}
		lines = append(lines, line)
	}

	// Discord caps embed descriptions at 4096 characters: keep whole lines
	// and say how many were cut rather than overflowing.
	desc := ""
	for idx, line := range lines {
		add := line + "\n"
		if len(desc)+len(add)+40 > 4096 {
			desc += fmt.Sprintf("... and %d more", len(lines)-idx)
			break
		}
		desc += add
	}
	e.Description = strings.TrimRight(desc, "\n")
	return e
}

// WeekTableEmbed loads the tenant's board for the week and renders it as the
// shared embed. rowCount lets callers show their own empty state.
func WeekTableEmbed(dbc *sql.DB, rdb *redis.Client, tenantID, title string, week time.Time) (embed *discordgo.MessageEmbed, rowCount int, err error) {
	rows, err := weekScores(dbc, tenantID, week)
	if err != nil {
		return nil, 0, err
	}
	prevRows, perr := weekScores(dbc, tenantID, cmdhelpers.GetCulvertPreviousDate(week))
	if perr != nil {
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
	return BuildWeekTableEmbed(title, rosterCount, rows, prevByID), len(rows), nil
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
//
// guildID keys the announcement record (the ACTUAL server, so each server in a
// shared tenant keeps its own message); channelID is that server's configured
// weekly channel (used only when creating a fresh post - edits reuse the
// stored channel).
func upsertWeeklyArtifacts(s *discordgo.Session, dbc *sql.DB, guildID, channelID string, art weeklyArtifacts, requireExisting bool) (string, error) {
	weekStr := art.weekStr
	var storedChannel, storedMessage, storedThread, storedTableMsg, storedNoteMsg string
	err := dbc.QueryRow(
		`SELECT channel_id, message_id, thread_id, table_message_id, note_message_id FROM weekly_announcements WHERE guild_id = $1 AND culvert_date = $2`,
		guildID, weekStr).Scan(&storedChannel, &storedMessage, &storedThread, &storedTableMsg, &storedNoteMsg)
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
			Content: &art.summary,
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
						storedThread, guildID, weekStr); uerr != nil {
						log.Println("weekly artifacts: store healed thread id:", uerr)
					}
				}
			}
			if storedThread != "" {
				upsertThreadTable(s, dbc, guildID, weekStr, storedThread, storedTableMsg, art.tableEmbed)
				upsertThreadNote(s, dbc, guildID, weekStr, storedThread, storedNoteMsg, art.note)
			}
			return storedThread, nil
		}
		// The stored message is gone (deleted channel/message); drop the stale
		// record. A refresh (non-submission mutation) stops here - it must
		// never CREATE a post; the next submission recreates it.
		log.Println("weekly artifacts: stored weekly message unreachable, clearing record")
		if _, err := dbc.Exec(`DELETE FROM weekly_announcements WHERE guild_id = $1 AND culvert_date = $2`, guildID, weekStr); err != nil {
			log.Println("weekly artifacts: delete stale weekly_announcements:", err)
			return "", errors.New("the stored weekly message is unreachable and its record could not be reset (see server logs)")
		}
		if requireExisting {
			return "", nil
		}
		if channelID == "" {
			channelID = storedChannel
		}
	}

	if channelID == "" {
		return "", ErrNoWeeklyChannel
	}
	msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: art.summary})
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
		guildID, weekStr, channelID, msg.ID, threadID); err != nil {
		log.Println("weekly artifacts: insert weekly_announcements:", err)
	}
	if threadID != "" {
		// The table then the note are the thread's first bot comments.
		upsertThreadTable(s, dbc, guildID, weekStr, threadID, "", art.tableEmbed)
		upsertThreadNote(s, dbc, guildID, weekStr, threadID, "", art.note)
	}
	return threadID, nil
}

// upsertThreadTable edits the week's full-table embed message inside the
// thread, or posts it (recording its id) when it doesn't exist yet or its
// stored id is unreachable. Failures are logged, never fatal: the table is
// derived data and the next refresh retries.
func upsertThreadTable(s *discordgo.Session, dbc *sql.DB, guildID, weekStr, threadID, tableMsgID string, tableEmbed *discordgo.MessageEmbed) {
	embeds := []*discordgo.MessageEmbed{tableEmbed}
	if tableMsgID != "" {
		empty := ""
		edit := &discordgo.MessageEdit{
			Channel: threadID,
			ID:      tableMsgID,
			Content: &empty, // clears pre-embed ASCII content on transition
			Embeds:  &embeds,
		}
		edit.Attachments = &[]*discordgo.MessageAttachment{}
		if _, err := s.ChannelMessageEditComplex(edit); err == nil {
			return
		}
		log.Println("weekly artifacts: table message unreachable, reposting")
	}
	msg, err := s.ChannelMessageSendComplex(threadID, &discordgo.MessageSend{Embeds: embeds})
	if err != nil {
		log.Println("weekly artifacts: post thread table:", err)
		return
	}
	if _, err := dbc.Exec(
		`UPDATE weekly_announcements SET table_message_id = $1 WHERE guild_id = $2 AND culvert_date = $3`,
		msg.ID, guildID, weekStr); err != nil {
		log.Println("weekly artifacts: store table message id:", err)
	}
}

// upsertThreadNote edits the week's single submission-note message in the
// thread (personal-best celebrations), or posts it (recording its id) when it
// does not exist yet or its stored id is unreachable. One note per week edited
// in place - never one message per submission. Allowed-mentions default lets
// the PB @mentions ping on the note's first creation. Failures are logged,
// never fatal.
func upsertThreadNote(s *discordgo.Session, dbc *sql.DB, guildID, weekStr, threadID, noteMsgID, note string) {
	if noteMsgID != "" {
		edit := &discordgo.MessageEdit{Channel: threadID, ID: noteMsgID, Content: &note}
		if _, err := s.ChannelMessageEditComplex(edit); err == nil {
			return
		}
		log.Println("weekly artifacts: note message unreachable, reposting")
	}
	msg, err := s.ChannelMessageSend(threadID, note)
	if err != nil {
		log.Println("weekly artifacts: post thread note:", err)
		return
	}
	if _, err := dbc.Exec(
		`UPDATE weekly_announcements SET note_message_id = $1 WHERE guild_id = $2 AND culvert_date = $3`,
		msg.ID, guildID, weekStr); err != nil {
		log.Println("weekly artifacts: store note message id:", err)
	}
}

// buildWeekNote renders the single thread note for a week, edited in place on
// every submission or refresh (never one message per submission). It is
// deterministic in the week's data: it lists every character whose current
// score is a personal best over prior weeks, so the same data always renders
// the same note regardless of how many submissions produced it.
func buildWeekNote(dbc *sql.DB, week time.Time, weekStr string, rows []weekScore) string {
	if len(rows) == 0 {
		return "No scores recorded yet this week."
	}
	pbs := weekPersonalBests(dbc, week, rows)
	if len(pbs) == 0 {
		return "No new personal bests recorded this week yet."
	}
	note := "**New personal bests this week** :tada:"
	// Keep the note within Discord's 2000-char message limit.
	for idx, pb := range pbs {
		add := "\n" + pb
		if len(note)+len(add)+40 > 1990 {
			note += fmt.Sprintf("\n... and %d more", len(pbs)-idx)
			break
		}
		note += add
	}
	return note
}

// weekPersonalBests returns congratulation lines ("@member (Name): old -> new")
// for every character whose this-week score beats their best in prior weeks.
func weekPersonalBests(dbc *sql.DB, week time.Time, rows []weekScore) []string {
	patchCutoff := ""
	if week.After(data.Date2mPatch) {
		patchCutoff = data.Date2mPatch.Format(time.DateOnly)
	}

	out := []string{}
	for _, r := range rows {
		if r.Score <= 0 {
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
