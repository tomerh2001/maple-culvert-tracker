package imagesubmissions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type Store struct{ DB *sql.DB }
type Delivery struct {
	State  string          `json:"state"`
	Detail json.RawMessage `json:"detail"`
}
type Receipt struct {
	ID          string              `json:"submission_id"`
	State       string              `json:"state"`
	StatusURL   string              `json:"status_url"`
	Replayed    bool                `json:"replayed"`
	Manifest    Manifest            `json:"manifest"`
	Result      json.RawMessage     `json:"result"`
	ErrorCode   string              `json:"error_code,omitempty"`
	ReceivedAt  time.Time           `json:"received_at_utc"`
	CompletedAt *time.Time          `json:"completed_at_utc,omitempty"`
	Delivery    map[string]Delivery `json:"delivery"`
}

func (s Store) Get(ctx context.Context, p Principal, id string) (*Receipt, error) {
	r := &Receipt{ID: id, StatusURL: "/api/v1/guild-score-submissions/" + id, Delivery: map[string]Delivery{}}
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT state,manifest,result,error_code,received_at,completed_at FROM image_submissions WHERE id=$1 AND principal_id=$2 AND tenant_id=$3 AND discord_guild_id=$4 AND collector_id=$5 AND destination_id=$6 AND maple_guild=$7 AND actor_id=$8`, id, p.ID, p.TenantID, p.DiscordGuildID, p.CollectorID, p.DestinationID, p.MapleGuild, p.ActorID).Scan(&r.State, &raw, &r.Result, &r.ErrorCode, &r.ReceivedAt, &r.CompletedAt)
	if err == sql.ErrNoRows {
		return nil, &APIError{404, "not_found"}
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &r.Manifest); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT kind,state,detail FROM image_submission_deliveries WHERE submission_id=$1 ORDER BY kind`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var d Delivery
		if err := rows.Scan(&k, &d.State, &d.Detail); err != nil {
			return nil, err
		}
		r.Delivery[k] = d
	}
	return r, rows.Err()
}

func (s Store) Accept(ctx context.Context, p Principal, b *Batch) (*Receipt, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Serializes quotas and replays for this integration across processes.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,45140))`, p.ID); err != nil {
		return nil, err
	}
	var id, digest string
	err = tx.QueryRowContext(ctx, `SELECT id,digest FROM image_submissions WHERE principal_id=$1 AND capture_run_id=$2`, p.ID, b.Manifest.CaptureRunID).Scan(&id, &digest)
	if err == nil {
		// A rotated token may keep its principal ID, but rebinding that ID must
		// never grant access to receipts from an earlier destination/tenant.
		r, scopeErr := s.Get(ctx, p, id)
		if scopeErr != nil {
			return nil, scopeErr
		}
		if digest != b.Digest {
			return nil, &APIError{409, "idempotency_conflict"}
		}
		tx.Rollback()
		r.Replayed = true
		return r, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var pending int
	var stored int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM image_submissions WHERE principal_id=$1 AND state='received'`, p.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(octet_length(p.original_png)),0) FROM image_submission_pages p JOIN image_submissions s ON s.id=p.submission_id WHERE s.principal_id=$1`, p.ID).Scan(&stored); err != nil {
		return nil, err
	}
	added := int64(0)
	for _, b := range b.Images {
		added += int64(len(b))
	}
	if pending >= 3 || stored+added > 512<<20 {
		return nil, &APIError{409, "integration_capacity"}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	id = hex.EncodeToString(random[:])
	m := b.Manifest
	res, err := tx.ExecContext(ctx, `INSERT INTO image_submissions(id,principal_id,capture_run_id,collector_id,destination_id,tenant_id,discord_guild_id,actor_id,maple_guild,slot_id,reset_at,captured_at,week,digest,manifest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb) ON CONFLICT DO NOTHING`, id, p.ID, m.CaptureRunID, p.CollectorID, p.DestinationID, p.TenantID, p.DiscordGuildID, p.ActorID, p.MapleGuild, m.SlotID, m.ResetAt, m.EndedAt, m.Week, b.Digest, string(b.Canonical))
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, &APIError{409, "slot_already_claimed"}
	}
	for i, png := range b.Images {
		if _, err = tx.ExecContext(ctx, `INSERT INTO image_submission_pages(submission_id,ordinal,original_png) VALUES($1,$2,$3)`, id, i+1, png); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, p, id)
}

func reject(ctx context.Context, tx *sql.Tx, id, code string) error {
	_, err := tx.ExecContext(ctx, `UPDATE image_submissions SET state='rejected',error_code=$2,completed_at=NOW() WHERE id=$1`, id, code)
	return err
}
func isAPIError(err error) (*APIError, bool) { var e *APIError; ok := errors.As(err, &e); return e, ok }
