package imagesubmissions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

const testToken = "test-only-integration-token-not-a-real-credential"

func stamp(s string) time.Time {
	t, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return t
}

var testNow = stamp("2026-11-12T00:01:00Z")

func testPrincipal() Principal {
	sum := sha256.Sum256([]byte(testToken))
	return Principal{ID: "test-principal", TokenSHA256: hex.EncodeToString(sum[:]), CollectorID: "collector", DestinationID: "example-destination", MapleGuild: "ExampleGuild", DiscordGuildID: "tenant-test", TenantID: "tenant-test", ActorID: "actor", ExpiresAt: testNow.Add(time.Hour)}
}
func testBatch(t *testing.T, run string) *Batch {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	pngBytes := buf.Bytes()
	sum := sha256.Sum256(pngBytes)
	m := Manifest{SchemaVersion: 1, Origin: "submit-images", CaptureRunID: run, NativeOperationID: "native", CollectorID: "collector", DestinationID: "example-destination", MapleGuild: "ExampleGuild", ScheduleRevision: "rev1", SlotID: "slot-a", ResetAt: stamp("2026-11-12T00:00:00Z"), StartedAt: stamp("2026-11-11T23:40:00Z"), EndedAt: stamp("2026-11-11T23:40:10Z"), Week: "2026-11-04", Complete: true, FirstPage: true, LastPage: true, Pages: []Page{{Ordinal: 1, AcquisitionID: "acquisition", CapturedAt: stamp("2026-11-11T23:40:05Z"), SHA256: hex.EncodeToString(sum[:]), ByteLength: len(pngBytes), Width: 4, Height: 3, PixelFormat: "gray8"}}}
	b := &Batch{Manifest: m, Images: [][]byte{pngBytes}}
	refreshDigest(b)
	return b
}
func refreshDigest(b *Batch) {
	b.Canonical, _ = json.Marshal(b.Manifest)
	sum := sha256.Sum256(b.Canonical)
	b.Digest = hex.EncodeToString(sum[:])
}
func multipartRequest(t *testing.T, b *Batch) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormField("manifest")
	raw, _ := json.Marshal(b.Manifest)
	part.Write(raw)
	for i, img := range b.Images {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="page-`+fmtOrdinal(i+1)+`"; filename="original.png"`)
		h.Set("Content-Type", "image/png")
		part, _ := mw.CreatePart(h)
		part.Write(img)
	}
	mw.Close()
	r := httptest.NewRequest("POST", "/api/v1/guild-score-submissions", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Idempotency-Key", b.Manifest.CaptureRunID)
	return r
}

func TestManifestAndMultipartBounds(t *testing.T) {
	p := testPrincipal()
	valid := testBatch(t, "run")
	r := multipartRequest(t, valid)
	got, err := readBatch(httptest.NewRecorder(), r, p, testNow)
	if err != nil || got.Digest != valid.Digest {
		t.Fatalf("valid original grayscale PNG: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Batch)
		code   string
	}{
		{"wrong-guild", func(b *Batch) { b.Manifest.MapleGuild = "Other" }, "destination_mismatch"},
		{"missing-terminal", func(b *Batch) { b.Manifest.LastPage = false }, "incomplete_manifest"},
		{"oversize-before-decode", func(b *Batch) { b.Manifest.Pages[0].Width = 8193 }, "image_limit"},
		{"wrong-hash", func(b *Batch) { b.Manifest.Pages[0].SHA256 = strings.Repeat("0", 64) }, "image_hash_mismatch"},
		{"wrong-length", func(b *Batch) { b.Manifest.Pages[0].ByteLength++ }, "image_length_mismatch"},
		{"false-rgb-label", func(b *Batch) { b.Manifest.Pages[0].PixelFormat = "rgb8" }, "image_pixel_format_mismatch"},
		{"wrong-dimensions", func(b *Batch) { b.Manifest.Pages[0].Width++ }, "image_dimensions_mismatch"},
		{"wrong-page-order", func(b *Batch) { b.Manifest.Pages[0].Ordinal = 2 }, "invalid_page_sequence"},
		{"page-outside-capture", func(b *Batch) { b.Manifest.Pages[0].CapturedAt = b.Manifest.StartedAt.Add(-time.Second) }, "invalid_page_time"},
		{"cross-week-reset", func(b *Batch) {
			b.Manifest.StartedAt = stamp("2026-11-11T23:59:00Z")
			b.Manifest.EndedAt = stamp("2026-11-12T00:00:01Z")
		}, "cross_reset_capture"},
		{"arrival-week-must-not-relabel", func(b *Batch) { b.Manifest.Week = "2026-11-11" }, "cross_reset_capture"},
		{"excess-duration", func(b *Batch) { b.Manifest.EndedAt = b.Manifest.StartedAt.Add(4 * time.Minute) }, "invalid_capture_interval"},
		{"future", func(b *Batch) { b.Manifest.EndedAt = testNow.Add(3 * time.Minute) }, "invalid_capture_interval"},
		{"zero-pages", func(b *Batch) { b.Manifest.Pages = nil; b.Images = nil }, "page_limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := testBatch(t, "run")
			c.mutate(b)
			_, err := readBatch(httptest.NewRecorder(), multipartRequest(t, b), p, testNow)
			e, ok := isAPIError(err)
			if !ok || e.Code != c.code {
				t.Fatalf("got %v want %s", err, c.code)
			}
		})
	}
	t.Run("twenty-originals", func(t *testing.T) {
		b := testBatch(t, "twenty")
		page := b.Manifest.Pages[0]
		img := b.Images[0]
		for i := 2; i <= 20; i++ {
			n := page
			n.Ordinal = i
			n.AcquisitionID = "acq-" + fmtOrdinal(i)
			b.Manifest.Pages = append(b.Manifest.Pages, n)
			b.Images = append(b.Images, img)
		}
		_, err := readBatch(httptest.NewRecorder(), multipartRequest(t, b), p, testNow)
		if err != nil {
			t.Fatal(err)
		}
		b.Manifest.Pages = append(b.Manifest.Pages, page)
		if err := validateManifest(b.Manifest, p, testNow); err == nil {
			t.Fatal("21 pages admitted")
		}
	})
}

func TestScopedCredentials(t *testing.T) {
	p := testPrincipal()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	if _, ok := authenticate(r, []Principal{p}, testNow); !ok {
		t.Fatal("valid")
	}
	for _, ps := range [][]Principal{nil, {{ID: p.ID, TokenSHA256: p.TokenSHA256, ExpiresAt: testNow.Add(-time.Second)}}} {
		if _, ok := authenticate(r, ps, testNow); ok {
			t.Fatal("disabled/expired admitted")
		}
	}
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 40))
	if _, ok := authenticate(r, []Principal{p}, testNow); ok {
		t.Fatal("wrong token admitted")
	}
	raw, _ := json.Marshal([]Principal{p, p})
	if _, err := ParsePrincipals(string(raw)); err == nil {
		t.Fatal("duplicate principal admitted")
	}
	if _, err := ParsePrincipals(`[{"unknown":"field"}]`); err == nil {
		t.Fatal("malformed config admitted")
	}
}

func TestReboundPrincipalCannotReadOrReplayOldScope(t *testing.T) {
	dbc := testdb.TestDB(t)
	store := Store{dbc}
	original := testPrincipal()
	b := testBatch(t, "scope-bound")
	ctx := context.Background()
	r, err := store.Accept(ctx, original, b)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Principal){func(p *Principal) { p.TenantID = "new-tenant"; p.DiscordGuildID = "new-tenant" }, func(p *Principal) { p.DiscordGuildID = "another-guild" }, func(p *Principal) { p.CollectorID = "another-collector" }, func(p *Principal) { p.DestinationID = "another-destination" }, func(p *Principal) { p.MapleGuild = "AnotherGuild" }, func(p *Principal) { p.ActorID = "another-actor" }}
	for _, change := range mutations {
		p := original
		change(&p)
		if _, err := store.Get(ctx, p, r.ID); err == nil {
			t.Fatal("old-scoped receipt exposed")
		}
		if _, err := store.Accept(ctx, p, b); err == nil {
			t.Fatal("old-scoped replay exposed")
		}
		gin.SetMode(gin.TestMode)
		router := gin.New()
		h := Handler{Store: store, Principals: []Principal{p}, Now: func() time.Time { return testNow }}
		h.Register(router)
		req := httptest.NewRequest("GET", "/api/v1/guild-score-submissions/by-capture/scope-bound", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != 404 && rr.Code != 403 {
			t.Fatalf("rebound lookup status %d", rr.Code)
		}
	}
}

func fixtureProcessor(dbc *sql.DB, score int) *Processor {
	return &Processor{Store: Store{dbc}, Parse: func(_ string, imgs [][]byte) (*commands.ImageSubmission, error) {
		pages := make([]apihelpers.ScreenshotPage, len(imgs))
		for i, b := range imgs {
			pages[i] = apihelpers.ScreenshotPage{Bytes: b, Names: []string{"FixtureName"}}
		}
		return &commands.ImageSubmission{Scores: map[string]int{"FixtureName": score}, Pages: pages, Warnings: "fixture boundary; not game OCR evidence"}, nil
	}, Roster: func(*sql.DB, string) ([]int64, string, error) { return nil, "", nil }, Lookup: func(string) func(string) (string, bool) { return func(string) (string, bool) { return "", false } }, Deliver: func(context.Context, string, string, time.Time, Result, []apihelpers.ScreenshotPage) (string, json.RawMessage, error) {
		return "delivered", json.RawMessage(`{"messages":[]}`), nil
	}}
}
func scalar(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func TestDurableReplayAtomicCommitAndReconciliation(t *testing.T) {
	dbc := testdb.TestDB(t)
	store := Store{dbc}
	p := testPrincipal()
	b := testBatch(t, "run-one")
	ctx := context.Background()
	receipt, err := store.Accept(ctx, p, b)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Accept(ctx, p, b)
	if err != nil || !replay.Replayed || replay.ID != receipt.ID {
		t.Fatalf("replay: %v %+v", err, replay)
	}
	changed := testBatch(t, "run-one")
	changed.Manifest.ScheduleRevision = "rev2"
	refreshDigest(changed)
	if _, err = store.Accept(ctx, p, changed); err == nil {
		t.Fatal("same key changed body accepted")
	}
	changed.Manifest.CaptureRunID = "another-run"
	refreshDigest(changed)
	if _, err = store.Accept(ctx, p, changed); err == nil {
		t.Fatal("edited revision reopened slot")
	}
	wrong := p
	wrong.ID = "other"
	if _, err = store.Get(ctx, wrong, receipt.ID); err == nil {
		t.Fatal("cross-principal read")
	}
	proc := fixtureProcessor(dbc, 100)
	// Receipt failure AFTER score and character writes must roll back all three.
	mustExec(t, dbc, `CREATE OR REPLACE FUNCTION test_fail_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='committed' THEN RAISE EXCEPTION 'injected commit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_receipt_failure BEFORE UPDATE ON image_submissions FOR EACH ROW EXECUTE FUNCTION test_fail_receipt()`)
	_, err = proc.ProcessOne(ctx)
	if err == nil {
		t.Fatal("injected failure missing")
	}
	if scalar(t, dbc, `SELECT count(*) FROM characters`) != 0 || scalar(t, dbc, `SELECT count(*) FROM character_culvert_scores`) != 0 || scalar(t, dbc, `SELECT count(*) FROM image_submission_deliveries`) != 0 {
		t.Fatal("partial transaction escaped")
	}
	mustExec(t, dbc, `DROP TRIGGER test_receipt_failure ON image_submissions; DROP FUNCTION test_fail_receipt()`)
	if worked, err := proc.ProcessOne(ctx); !worked || err != nil {
		t.Fatalf("recovery: %v", err)
	}
	r, err := store.Get(ctx, p, receipt.ID)
	if err != nil || r.State != "committed" || len(r.Delivery) != 2 {
		t.Fatalf("receipt: %v %+v", err, r)
	}
	if scalar(t, dbc, `SELECT score FROM character_culvert_scores`) != 100 {
		t.Fatal("score")
	}
	if worked, err := proc.ProcessOne(ctx); worked || err != nil {
		t.Fatal("committed job reran")
	}
	for i := 0; i < 2; i++ {
		if worked, err := proc.DeliverOne(ctx); !worked || err != nil {
			t.Fatalf("delivery: %v", err)
		}
	}
	if worked, err := proc.DeliverOne(ctx); worked || err != nil {
		t.Fatal("delivered job reran")
	}
	// Lost HTTP acceptance response can be reconciled without sending images.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := Handler{Store: store, Principals: []Principal{p}, Now: func() time.Time { return testNow }}
	h.Register(router)
	req := httptest.NewRequest("GET", "/api/v1/guild-score-submissions/by-capture/run-one", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), receipt.ID) {
		t.Fatalf("lookup: %d %s", rr.Code, rr.Body.String())
	}
}

func TestConcurrentAcceptanceAndStaleCapture(t *testing.T) {
	dbc := testdb.TestDB(t)
	store := Store{dbc}
	p := testPrincipal()
	b := testBatch(t, "concurrent")
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := store.Accept(ctx, p, b)
			if e != nil {
				errs <- e
			} else {
				ids <- r.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatal("duplicate jobs")
	}
	proc := fixtureProcessor(dbc, 200)
	if _, err := proc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	older := testBatch(t, "older")
	older.Manifest.SlotID = "slot-older"
	older.Manifest.StartedAt = older.Manifest.StartedAt.Add(-time.Minute)
	older.Manifest.EndedAt = older.Manifest.EndedAt.Add(-time.Minute)
	older.Manifest.Pages[0].CapturedAt = older.Manifest.Pages[0].CapturedAt.Add(-time.Minute)
	refreshDigest(older)
	r, err := store.Accept(ctx, p, older)
	if err != nil {
		t.Fatal(err)
	}
	proc = fixtureProcessor(dbc, 1)
	if _, err = proc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	r, err = store.Get(ctx, p, r.ID)
	if err != nil || r.State != "rejected" || r.ErrorCode != "stale_capture" {
		t.Fatalf("stale %+v %v", r, err)
	}
	if scalar(t, dbc, `SELECT score FROM character_culvert_scores`) != 200 {
		t.Fatal("older capture overwrote newer")
	}
}

func TestFatalImageAndUncertainDeliveryNeverReapply(t *testing.T) {
	dbc := testdb.TestDB(t)
	store := Store{dbc}
	p := testPrincipal()
	b := testBatch(t, "unusable")
	ctx := context.Background()
	r, err := store.Accept(ctx, p, b)
	if err != nil {
		t.Fatal(err)
	}
	proc := fixtureProcessor(dbc, 100)
	proc.Parse = func(string, [][]byte) (*commands.ImageSubmission, error) {
		return nil, errors.New("unreadable last page")
	}
	if _, err = proc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, p, r.ID)
	if got.State != "rejected" || scalar(t, dbc, `SELECT count(*) FROM characters`) != 0 {
		t.Fatal("partial image batch committed")
	}
	b = testBatch(t, "delivery")
	b.Manifest.SlotID = "slot-b"
	refreshDigest(b)
	r, err = store.Accept(ctx, p, b)
	if err != nil {
		t.Fatal(err)
	}
	proc = fixtureProcessor(dbc, 100)
	if _, err = proc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	calls := 0
	proc.Deliver = func(context.Context, string, string, time.Time, Result, []apihelpers.ScreenshotPage) (string, json.RawMessage, error) {
		calls++
		return "", nil, errors.New("timeout after remote acceptance")
	}
	if _, err = proc.DeliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	mustExec(t, dbc, `UPDATE image_submission_deliveries SET state='delivering' WHERE state='pending'`)
	if _, err = proc.DeliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unknown delivery was retried")
	}
	if scalar(t, dbc, `SELECT count(*) FROM image_submission_deliveries WHERE state='outcome_unknown'`) != 2 {
		t.Fatal("uncertainty hidden")
	}
	if scalar(t, dbc, `SELECT count(*) FROM character_culvert_scores`) != 1 {
		t.Fatal("scores repeated")
	}
}

func TestManualCorrectionRejectsDelayedCapture(t *testing.T) {
	for _, mutation := range []string{"UPDATE character_culvert_scores SET score=7", "DELETE FROM character_culvert_scores"} {
		t.Run(mutation, func(t *testing.T) {
			db := testdb.TestDB(t)
			ctx := context.Background()
			mustExec(t, db, "INSERT INTO characters(id,maple_character_name,discord_user_id,guild_id) VALUES(1,'FixtureName','2','tenant-test'); INSERT INTO character_culvert_scores(character_id,culvert_date,score) VALUES(1,'2026-11-04',40)")
			b := testBatch(t, "manual-delay")
			b.Manifest.EndedAt = time.Now().UTC().Add(-time.Minute)
			b.Manifest.StartedAt = b.Manifest.EndedAt.Add(-10 * time.Second)
			refreshDigest(b)
			mustExec(t, db, mutation)
			rec, err := (Store{db}).Accept(ctx, testPrincipal(), b)
			if err != nil {
				t.Fatal(err)
			}
			proc := fixtureProcessor(db, 100)
			if _, err = proc.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := (Store{db}).Get(ctx, testPrincipal(), rec.ID)
			if err != nil || r.State != "rejected" || r.ErrorCode != "newer_manual_change" {
				t.Fatalf("receipt %+v %v", r, err)
			}
			if scalar(t, db, "SELECT count(*) FROM image_submission_deliveries") != 0 {
				t.Fatal("rejection delivered")
			}
			if strings.HasPrefix(mutation, "UPDATE") && scalar(t, db, "SELECT score FROM character_culvert_scores") != 7 {
				t.Fatal("manual lower correction overwritten")
			}
			if strings.HasPrefix(mutation, "DELETE") && scalar(t, db, "SELECT count(*) FROM character_culvert_scores") != 0 {
				t.Fatal("reset undone")
			}
		})
	}
}

func TestConcurrentManualRowWaitRollsBackEntireBatch(t *testing.T) {
	db := testdb.TestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mustExec(t, db, "INSERT INTO characters(id,maple_character_name,discord_user_id,guild_id) VALUES(100,'FixtureName','2','tenant-test'); INSERT INTO character_culvert_scores(character_id,culvert_date,score) VALUES(100,'2026-11-04',40); UPDATE culvert_manual_revisions SET changed_at=NOW()-INTERVAL '2 minutes'")
	b := testBatch(t, "manual-race")
	b.Manifest.EndedAt = time.Now().UTC().Add(-time.Minute)
	b.Manifest.StartedAt = b.Manifest.EndedAt.Add(-10 * time.Second)
	refreshDigest(b)
	rec, err := (Store{db}).Accept(ctx, testPrincipal(), b)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manual.Rollback()
	if _, err = manual.ExecContext(ctx, "UPDATE character_culvert_scores SET score=7 WHERE character_id=100"); err != nil {
		t.Fatal(err)
	}
	proc := fixtureProcessor(db, 100)
	parse := proc.Parse
	proc.Parse = func(tenant string, imgs [][]byte) (*commands.ImageSubmission, error) {
		parsed, err := parse(tenant, imgs)
		parsed.Scores["NewFixture"] = 30
		return parsed, err
	}
	proc.Roster = func(*sql.DB, string) ([]int64, string, error) { return []int64{100}, "", nil }
	done := make(chan error, 1)
	go func() { _, err := proc.ProcessOne(ctx); done <- err }()
	// Observe PostgreSQL's row-lock wait rather than assuming a sleep was enough.
	waiting := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var n int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO character_culvert_scores%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			waiting = true
			break
		}
		select {
		case err := <-done:
			t.Fatalf("worker finished before row wait: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !waiting {
		t.Fatal("worker did not reach observed score-row wait")
	}
	if err = manual.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	r, err := (Store{db}).Get(ctx, testPrincipal(), rec.ID)
	if err != nil || r.State != "rejected" || r.ErrorCode != "newer_manual_change" {
		t.Fatalf("receipt %+v %v", r, err)
	}
	if scalar(t, db, "SELECT score FROM character_culvert_scores WHERE character_id=100") != 7 ||
		scalar(t, db, "SELECT count(*) FROM characters") != 1 ||
		scalar(t, db, "SELECT count(*) FROM character_culvert_scores") != 1 ||
		scalar(t, db, "SELECT count(*) FROM image_submission_deliveries") != 0 {
		t.Fatal("partial score/auto-track/delivery escaped rollback")
	}
	var marker string
	if err = db.QueryRow("SELECT COALESCE(current_setting('app.culvert_image_origin',true),'')").Scan(&marker); err != nil || marker != "" {
		t.Fatalf("origin marker leaked %q %v", marker, err)
	}
}
