package helpers

import (
	"database/sql"
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

// AnnounceSubmission is the only place the bot posts without being asked, and
// it only posts to the configured weekly channel (CONF_DISCORD_WEEKLY_CHANNEL_ID):
//   - one message per culvert week holding the FULL score table, created on the
//     first submission for that week and edited in place afterwards
//   - a thread under that message collecting submission notes and personal
//     best congratulations (mentioning the linked Discord member)
//
// Submissions for historical weeks update that week's message/thread.
// changedIDs are the character ids touched by this submission; only their new
// personal bests are congratulated.
func AnnounceSubmission(s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, week time.Time, changedIDs []int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("AnnounceSubmission: recovered from panic:", r)
		}
	}()
	if s == nil {
		return
	}
	channelID := apiredis.CONF_DISCORD_WEEKLY_CHANNEL_ID.GetWithDefault(rdb, "")
	if channelID == "" {
		return
	}

	week = cmdhelpers.GetCulvertResetDate(week)
	weekStr := week.Format(time.DateOnly)
	prevWeek := cmdhelpers.GetCulvertPreviousDate(week)

	rows, err := weekScores(dbc, week)
	if err != nil {
		log.Println("AnnounceSubmission: query week scores:", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	prevRows, err := weekScores(dbc, prevWeek)
	if err != nil {
		log.Println("AnnounceSubmission: query prev week scores:", err)
		return
	}
	prevByID := map[int64]weekScore{}
	for _, p := range prevRows {
		prevByID[p.CharacterID] = p
	}

	content, file := buildWeeklyMessage(weekStr, rows, prevByID)
	threadID := upsertWeeklyMessage(s, dbc, channelID, weekStr, content, file)
	if threadID == "" {
		return
	}

	note := buildSubmissionNote(dbc, week, weekStr, rows, changedIDs)
	for _, chunk := range chunkMessage(note, 1900) {
		if _, err := s.ChannelMessageSend(threadID, chunk); err != nil {
			log.Println("AnnounceSubmission: thread note failed:", err)
			return
		}
	}
}

type weekScore struct {
	CharacterID   int64
	Name          string
	DiscordUserID string
	Score         int
	Rank          int
}

// weekScores returns the week's scores ordered by score desc, ranked.
func weekScores(dbc *sql.DB, week time.Time) ([]weekScore, error) {
	rows, err := dbc.Query(`
		SELECT c.id, c.maple_character_name, c.discord_user_id, s.score
		FROM character_culvert_scores s
		JOIN characters c ON c.id = s.character_id
		WHERE s.culvert_date = $1
		ORDER BY s.score DESC, c.maple_character_name ASC`, week.Format(time.DateOnly))
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

// buildWeeklyMessage renders the full week table. When it fits, the table is
// inline in a code block; otherwise it is attached as a file.
func buildWeeklyMessage(weekStr string, rows []weekScore, prevByID map[int64]weekScore) (string, *discordgo.File) {
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

	header := "## Culvert week of " + weekStr + "\n" +
		fmt.Sprintf("%d members - last updated <t:%d:R>", len(rows), time.Now().Unix())
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

// upsertWeeklyMessage creates or edits the week's designated-channel message
// and returns the id of its thread ("" when unavailable).
func upsertWeeklyMessage(s *discordgo.Session, dbc *sql.DB, channelID, weekStr, content string, file *discordgo.File) string {
	var storedChannel, storedMessage, storedThread string
	err := dbc.QueryRow(
		`SELECT channel_id, message_id, thread_id FROM weekly_announcements WHERE culvert_date = $1`,
		weekStr).Scan(&storedChannel, &storedMessage, &storedThread)
	if err != nil && err != sql.ErrNoRows {
		log.Println("AnnounceSubmission: query weekly_announcements:", err)
		return ""
	}

	if err == nil {
		edit := &discordgo.MessageEdit{
			Channel: storedChannel,
			ID:      storedMessage,
			Content: &content,
		}
		// Replace any previously attached table file.
		edit.Attachments = &[]*discordgo.MessageAttachment{}
		if file != nil {
			edit.Files = []*discordgo.File{file}
		}
		if _, err := s.ChannelMessageEditComplex(edit); err == nil {
			return storedThread
		}
		// The stored message is gone (deleted channel/message); recreate it.
		log.Println("AnnounceSubmission: stored weekly message unreachable, recreating")
		if _, err := dbc.Exec(`DELETE FROM weekly_announcements WHERE culvert_date = $1`, weekStr); err != nil {
			log.Println("AnnounceSubmission: delete stale weekly_announcements:", err)
			return ""
		}
	}

	send := &discordgo.MessageSend{Content: content}
	if file != nil {
		send.Files = []*discordgo.File{file}
	}
	msg, err := s.ChannelMessageSendComplex(channelID, send)
	if err != nil {
		log.Println("AnnounceSubmission: send weekly message:", err)
		return ""
	}
	threadID := ""
	thread, err := s.MessageThreadStartComplex(channelID, msg.ID, &discordgo.ThreadStart{
		Name:                "Culvert " + weekStr,
		AutoArchiveDuration: 10080,
	})
	if err != nil {
		log.Println("AnnounceSubmission: start thread:", err)
	} else {
		threadID = thread.ID
	}
	if _, err := dbc.Exec(
		`INSERT INTO weekly_announcements (culvert_date, channel_id, message_id, thread_id) VALUES ($1, $2, $3, $4)`,
		weekStr, channelID, msg.ID, threadID); err != nil {
		log.Println("AnnounceSubmission: insert weekly_announcements:", err)
	}
	return threadID
}

// buildSubmissionNote renders the thread note for one submission: timestamp,
// scope, and personal-best congratulations for the characters that changed.
func buildSubmissionNote(dbc *sql.DB, week time.Time, weekStr string, rows []weekScore, changedIDs []int64) string {
	note := fmt.Sprintf("Scores submitted <t:%d:f> for week **%s** (%d characters).", time.Now().Unix(), weekStr, len(changedIDs))

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
