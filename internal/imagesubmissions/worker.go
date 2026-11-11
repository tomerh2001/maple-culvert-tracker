package imagesubmissions

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands"
)

type Result struct {
	Scores   *commands.ScoreSubmissionResult `json:"scores"`
	Warnings string                          `json:"warnings"`
}
type Processor struct {
	Store   Store
	Parse   func(string, [][]byte) (*commands.ImageSubmission, error)
	Roster  func(*sql.DB, string) ([]int64, string, error)
	Lookup  func(string) func(string) (string, bool)
	Deliver func(context.Context, string, string, time.Time, Result, []apihelpers.ScreenshotPage) (string, json.RawMessage, error)
}

func NewProcessor(s Store, session *discordgo.Session) *Processor {
	p := &Processor{Store: s, Parse: commands.ParseSubmissionImages, Roster: commands.SubmissionRoster, Lookup: commands.SubmissionRankings}
	p.Deliver = func(ctx context.Context, kind, tenant string, week time.Time, r Result, pages []apihelpers.ScreenshotPage) (string, json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		var err error
		if kind == "announcement" {
			err = apihelpers.AnnounceSubmission(session, s.DB, apiredis.RedisDB, tenant, week, r.Scores.ChangedIDs, discordgo.WithContext(ctx), discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false))
		} else {
			err = apihelpers.ArchiveWeekScreenshots(session, s.DB, apiredis.RedisDB, tenant, week, pages, discordgo.WithContext(ctx), discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false))
		}
		deliveryErr := err
		if ctx.Err() != nil {
			deliveryErr = errors.Join(deliveryErr, ctx.Err())
		}
		if ctx.Err() == nil && (errors.Is(deliveryErr, apihelpers.ErrNoWeeklyChannel) || errors.Is(deliveryErr, apihelpers.ErrNoScreenshotChannel)) {
			return "skipped", json.RawMessage(`{"reason":"channel_not_configured"}`), nil
		}
		// Actual known Discord identifiers, never assumed from an attempted send.
		table := "weekly_announcements"
		if kind == "archive" {
			table = "weekly_screenshot_archives"
		}
		receiptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		refs := apihelpers.KnownDeliveryMessages(deliveryErr)
		failedReadback := func(readErr error) (string, json.RawMessage, error) {
			raw, _ := json.Marshal(map[string]any{"messages": refs})
			return "", raw, errors.Join(deliveryErr, readErr)
		}
		rows, err := s.DB.QueryContext(receiptCtx, `SELECT guild_id,channel_id,message_id FROM `+table+` WHERE culvert_date=$1`, week.Format(time.DateOnly))
		if err != nil {
			return failedReadback(err)
		}
		defer rows.Close()
		for rows.Next() {
			var gid, cid, mid string
			if err := rows.Scan(&gid, &cid, &mid); err != nil {
				return failedReadback(err)
			}
			if belongsToTenant(gid, tenant) {
				refs = append(refs, apihelpers.MessageReference{GuildID: gid, ChannelID: cid, MessageID: mid})
			}
		}
		if err := rows.Err(); err != nil {
			return failedReadback(err)
		}
		raw, _ := json.Marshal(map[string]any{"messages": refs})
		return "delivered", raw, deliveryErr
	}
	return p
}

// ProcessOne holds its row and tenant transaction locks until validation and
// atomic score+receipt+delivery-intent commit. A crash releases the locks and
// leaves the immutable received batch available to another worker.
func (p *Processor) ProcessOne(ctx context.Context) (bool, error) {
	tx, err := p.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, tenant, guild string
	var captured, captureStarted time.Time
	var week time.Time
	err = tx.QueryRowContext(ctx, `SELECT id,tenant_id,maple_guild,captured_at,week,(manifest->>'captured_started_at_utc')::timestamptz FROM image_submissions WHERE state='received' ORDER BY received_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &tenant, &guild, &captured, &week, &captureStarted)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = commands.LockSubmissionTenant(ctx, tx, tenant); err != nil {
		return true, err
	}
	var newer bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM image_submissions WHERE state='committed' AND tenant_id=$1 AND maple_guild=$2 AND week=$3 AND captured_at >= $4)`, tenant, guild, week, captured).Scan(&newer); err != nil {
		return true, err
	}
	if newer {
		if err = reject(ctx, tx, id, "stale_capture"); err != nil {
			return true, err
		}
		return true, tx.Commit()
	}
	manualNewer := func() (bool, error) {
		var found bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM culvert_manual_revisions WHERE tenant_id=$1 AND week=$2 AND changed_at>$3)`, tenant, week, captureStarted).Scan(&found)
		return found, err
	}
	manual, err := manualNewer()
	if err != nil {
		return true, err
	}
	if manual {
		if err = reject(ctx, tx, id, "newer_manual_change"); err != nil {
			return true, err
		}
		return true, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT original_png FROM image_submission_pages WHERE submission_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return true, err
	}
	var images [][]byte
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return true, err
		}
		images = append(images, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return true, err
	}
	parsed, err := p.Parse(tenant, images)
	if err != nil {
		if err = reject(ctx, tx, id, "image_validation_failed"); err != nil {
			return true, err
		}
		return true, tx.Commit()
	}
	ids, rosterWarning, err := p.Roster(p.Store.DB, tenant)
	if err != nil {
		return true, err
	}
	lookup := p.Lookup(tenant)
	boundedLookup := func(name string) (string, bool) {
		if ctx.Err() != nil {
			return "", false
		}
		return lookup(name)
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.culvert_image_origin',$1,true)`, id); err != nil {
		return true, err
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT scores_apply`); err != nil {
		return true, err
	}
	scores, err := commands.ApplyScoreSubmission(ctx, tx, tenant, week, parsed.Scores, ids, boundedLookup)
	if err != nil {
		return true, err
	}
	// A manual writer may have held a score row while our upsert waited. Recheck
	// after those locks resolve. Roll back ALL score/character changes before
	// persisting rejection, retaining the outer batch claim and tenant lock.
	manual, err = manualNewer()
	if err != nil {
		return true, err
	}
	if manual {
		if _, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT scores_apply`); err != nil {
			return true, err
		}
		if err = reject(ctx, tx, id, "newer_manual_change"); err != nil {
			return true, err
		}
		return true, tx.Commit()
	}
	result := Result{Scores: scores, Warnings: parsed.Warnings + rosterWarning}
	raw, err := json.Marshal(result)
	if err != nil {
		return true, err
	}
	if len(parsed.Pages) != len(images) {
		return true, errors.New("parser page count mismatch")
	}
	for i, page := range parsed.Pages {
		names, _ := json.Marshal(page.Names)
		if _, err = tx.ExecContext(ctx, `UPDATE image_submission_pages SET parsed_names=$3::jsonb WHERE submission_id=$1 AND ordinal=$2`, id, i+1, string(names)); err != nil {
			return true, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE image_submissions SET state='committed',result=$2::jsonb,completed_at=NOW() WHERE id=$1`, id, string(raw)); err != nil {
		return true, err
	}
	if len(scores.ChangedIDs) > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO image_submission_deliveries(submission_id,kind) VALUES($1,'announcement')`, id); err != nil {
			return true, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO image_submission_deliveries(submission_id,kind) VALUES($1,'archive')`, id); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

// DeliverOne obtains a session lock before durably marking delivery started.
// If a previous process disappeared after that marker, its outcome is unknown:
// never blindly create another Discord message or reapply score writes.
func (p *Processor) DeliverOne(ctx context.Context) (bool, error) {
	conn, err := p.Store.DB.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, kind, state, tenant, guild string
	var week, captured time.Time
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT d.submission_id,d.kind,d.state,s.tenant_id,s.maple_guild,s.week,s.captured_at,s.result FROM image_submission_deliveries d JOIN image_submissions s ON s.id=d.submission_id WHERE d.state IN ('pending','delivering') ORDER BY s.received_at,d.kind FOR UPDATE OF d SKIP LOCKED LIMIT 1`).Scan(&id, &kind, &state, &tenant, &guild, &week, &captured, &raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,45139))`, tenant).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		tx.Rollback()
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(releaseCtx, `SELECT pg_advisory_unlock(hashtextextended($1,45139))`, tenant); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	finish := func(state string, detail string) error {
		_, err := tx.ExecContext(ctx, `UPDATE image_submission_deliveries SET state=$3,detail=$4::jsonb,updated_at=NOW() WHERE submission_id=$1 AND kind=$2`, id, kind, state, detail)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if state == "delivering" {
		return true, finish("outcome_unknown", `{"reason":"interrupted_delivery_requires_reconciliation"}`)
	}
	var newer bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM image_submissions WHERE state='committed' AND tenant_id=$1 AND maple_guild=$2 AND week=$3 AND captured_at>$4)`, tenant, guild, week, captured).Scan(&newer); err != nil {
		return true, err
	}
	if newer {
		return true, finish("skipped", `{"reason":"superseded_by_newer_capture"}`)
	}
	result := Result{}
	if err = json.Unmarshal(raw, &result); err != nil {
		return true, err
	}
	pages := []apihelpers.ScreenshotPage{}
	rows, err := tx.QueryContext(ctx, `SELECT original_png,parsed_names FROM image_submission_pages WHERE submission_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return true, err
	}
	for rows.Next() {
		var page apihelpers.ScreenshotPage
		var names []byte
		if err = rows.Scan(&page.Bytes, &names); err != nil {
			rows.Close()
			return true, err
		}
		if err = json.Unmarshal(names, &page.Names); err != nil {
			rows.Close()
			return true, err
		}
		pages = append(pages, page)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return true, err
	}
	if err = finish("delivering", `{}`); err != nil {
		return true, err
	}
	final, detail, sendErr := p.Deliver(ctx, kind, tenant, week, result, pages)
	if sendErr != nil {
		final = "outcome_unknown"
		fields := map[string]any{}
		_ = json.Unmarshal(detail, &fields)
		fields["reason"] = "delivery_result_unconfirmed"
		detail, _ = json.Marshal(fields)
	}
	if final != "delivered" && final != "skipped" && final != "outcome_unknown" {
		final = "outcome_unknown"
		detail = json.RawMessage(`{"reason":"invalid_delivery_result"}`)
	}
	// A canceled request must not hide the uncertainty marker. Use an independent
	// bounded context only for recording the already attempted result.
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = conn.ExecContext(recordCtx, `UPDATE image_submission_deliveries SET state=$3,detail=$4::jsonb,updated_at=NOW() WHERE submission_id=$1 AND kind=$2`, id, kind, final, string(detail))
	return true, err
}

func (p *Processor) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		worked, err := p.ProcessOne(runCtx)
		cancel()
		if err != nil {
			log.Println("image submission: processing deferred")
		}
		if ctx.Err() != nil {
			return
		}
		deliveryCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		_, err = p.DeliverOne(deliveryCtx)
		cancel()
		if err != nil {
			log.Println("image submission: delivery reconciliation pending")
		}
		if !worked {
			// Bounded private image retention; durable receipts and hashes survive.
			_, _ = p.Store.DB.ExecContext(ctx, `DELETE FROM image_submission_pages p USING image_submissions s WHERE p.submission_id=s.id AND s.state!='received' AND s.completed_at<NOW()-INTERVAL '7 days' AND NOT EXISTS(SELECT 1 FROM image_submission_deliveries d WHERE d.submission_id=s.id AND d.state IN ('pending','delivering'))`)
		}
	}
}
