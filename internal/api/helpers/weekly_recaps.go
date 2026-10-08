package helpers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// AnnounceWeeklyRecaps posts the most recently completed week's summary and
// thread once per guild. It runs at reset and catches up after a restart, but
// never replays older history. Empty/unconfigured weeks are durably skipped:
// submitting old scores after reset must not trigger an extra recap.
func AnnounceWeeklyRecaps(ctx context.Context, s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, guildIDs []string, now time.Time) error {
	return announceWeeklyRecaps(ctx, s, dbc, rdb, guildIDs, now, func(guildID string) (string, error) {
		channelID, err := apiredis.CONF_DISCORD_WEEKLY_CHANNEL_ID.For(guildID).Get(rdb)
		if errors.Is(err, redis.Nil) {
			return "", nil
		}
		return channelID, err
	})
}

func announceWeeklyRecaps(ctx context.Context, s *discordgo.Session, dbc *sql.DB, rdb *redis.Client, guildIDs []string, now time.Time, configuredChannel func(string) (string, error)) error {
	week := cmdhelpers.GetCulvertPreviousDate(cmdhelpers.CurrentCulvertWeek(now))
	weekStr := week.Format(time.DateOnly)
	artifacts := map[string]weeklyArtifacts{}
	seen := map[string]bool{}
	var failures []error
	for _, guildID := range guildIDs {
		if guildID == "" || seen[guildID] {
			continue
		}
		seen[guildID] = true
		if err := ctx.Err(); err != nil {
			return err
		}
		var channelID, tableID string
		var skipped bool
		err := dbc.QueryRowContext(ctx, `
			SELECT channel_id, table_message_id, skipped FROM weekly_recaps
			WHERE guild_id = $1 AND culvert_date = $2`, guildID, weekStr).Scan(&channelID, &tableID, &skipped)
		if err == nil && (tableID != "" || skipped) {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			failures = append(failures, fmt.Errorf("recap guild %s: %w", guildID, err))
			continue
		}
		tenantID := data.TenantID(guildID)
		art, ok := artifacts[tenantID]
		if !ok {
			art, err = buildWeeklyArtifacts(dbc, rdb, tenantID, week)
			if err != nil {
				failures = append(failures, fmt.Errorf("recap tenant %s: %w", tenantID, err))
				continue
			}
			art.summaryEmbed.Title = "Culvert recap - Week of " + weekStr
			artifacts[tenantID] = art
		}
		if len(art.rows) > 0 && channelID == "" {
			// Reuse the original destination when available. Scores can exist
			// without an announcement, including after a message was deleted.
			err = dbc.QueryRowContext(ctx, `SELECT channel_id FROM weekly_announcements
				WHERE guild_id = $1 AND culvert_date = $2`, guildID, weekStr).Scan(&channelID)
			if errors.Is(err, sql.ErrNoRows) {
				channelID, err = configuredChannel(guildID)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("recap channel for guild %s: %w", guildID, err))
				continue
			}
		}
		if err := postWeeklyRecap(ctx, s, dbc, guildID, channelID, art, len(art.rows) == 0 || channelID == ""); err != nil {
			failures = append(failures, fmt.Errorf("recap guild %s: %w", guildID, err))
		}
	}
	return errors.Join(failures...)
}

// postWeeklyRecap persists each successful Discord step before attempting the
// next. The same per-guild/week database lock serializes skip decisions and
// delivery. A persisted skip or completed delivery is final, even if another
// worker computed a different decision before acquiring the lock.
func postWeeklyRecap(ctx context.Context, s *discordgo.Session, dbc *sql.DB, guildID, channelID string, art weeklyArtifacts, skip bool) error {
	conn, err := dbc.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	keyHash := sha256.Sum256([]byte("culvert-recap:" + guildID + ":" + art.weekStr))
	lockKey := int64(binary.BigEndian.Uint64(keyHash[:8]))
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
			// Never return a connection that may still own the session lock
			// to the pool. Dropping it also releases the PostgreSQL lock.
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	result, err := conn.ExecContext(ctx, `INSERT INTO weekly_recaps (guild_id, culvert_date, channel_id, skipped)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, guildID, art.weekStr, channelID, skip)
	if err != nil {
		return err
	}
	if inserted, _ := result.RowsAffected(); skip && inserted == 1 {
		log.Printf("weekly recap: skipped guild %s week %s (no visible scores or channel at reset check)", guildID, art.weekStr)
	}
	var messageID, threadID, tableID string
	var skipped bool
	if err := conn.QueryRowContext(ctx, `SELECT channel_id, message_id, thread_id, table_message_id, skipped
		FROM weekly_recaps WHERE guild_id = $1 AND culvert_date = $2`, guildID, art.weekStr).
		Scan(&channelID, &messageID, &threadID, &tableID, &skipped); err != nil {
		return err
	}
	if tableID != "" || skipped || skip {
		return nil
	}
	if messageID == "" {
		msg, err := sendRecapMessage(ctx, s, channelID, guildID+":"+art.weekStr+":summary", []*discordgo.MessageEmbed{art.summaryEmbed})
		if err != nil {
			return fmt.Errorf("post summary: %w", err)
		}
		messageID = msg.ID
		if _, err := conn.ExecContext(ctx, `UPDATE weekly_recaps SET message_id = $1
			WHERE guild_id = $2 AND culvert_date = $3`, messageID, guildID, art.weekStr); err != nil {
			return err
		}
	}
	if threadID == "" {
		thread, err := s.MessageThreadStartComplex(channelID, messageID, &discordgo.ThreadStart{
			Name: "Culvert recap " + art.weekStr, AutoArchiveDuration: 10080,
		}, discordgo.WithContext(ctx))
		if err != nil {
			// Discord threads created from a message use that message's ID.
			// Recover if a prior request succeeded before its DB write did.
			var restErr *discordgo.RESTError
			if errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == 160004 {
				thread, err = s.Channel(messageID, discordgo.WithContext(ctx))
			}
			if err != nil {
				return fmt.Errorf("start recap thread: %w", err)
			}
		}
		threadID = thread.ID
		if _, err := conn.ExecContext(ctx, `UPDATE weekly_recaps SET thread_id = $1
			WHERE guild_id = $2 AND culvert_date = $3`, threadID, guildID, art.weekStr); err != nil {
			return err
		}
	}
	msg, err := sendRecapMessage(ctx, s, threadID, guildID+":"+art.weekStr+":table", []*discordgo.MessageEmbed{art.tableEmbed, art.pbEmbed})
	if err != nil {
		return fmt.Errorf("post recap details: %w", err)
	}
	_, err = conn.ExecContext(ctx, `UPDATE weekly_recaps SET table_message_id = $1
		WHERE guild_id = $2 AND culvert_date = $3`, msg.ID, guildID, art.weekStr)
	if err == nil {
		log.Printf("weekly recap: completed guild %s week %s message %s", guildID, art.weekStr, messageID)
	}
	return err
}

// Discord's enforced nonce covers retries where a send succeeded but its
// response was lost. Durable message IDs cover subsequent bot restarts.
// DiscordGo v0.29 does not expose enforce_nonce in MessageSend.
func sendRecapMessage(ctx context.Context, s *discordgo.Session, channelID, identity string, embeds []*discordgo.MessageEmbed) (*discordgo.Message, error) {
	hash := sha256.Sum256([]byte("culvert-recap:" + identity))
	payload := struct {
		Embeds          []*discordgo.MessageEmbed         `json:"embeds"`
		AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
		Nonce           string                            `json:"nonce"`
		EnforceNonce    bool                              `json:"enforce_nonce"`
	}{embeds, &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}, fmt.Sprintf("%x", hash[:12]), true}
	endpoint := discordgo.EndpointChannelMessages(channelID)
	body, err := s.RequestWithBucketID("POST", endpoint, payload, endpoint, discordgo.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	var msg discordgo.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	if msg.ID == "" {
		return nil, errors.New("Discord returned a recap without a message ID")
	}
	return &msg, nil
}
