package api

import (
	"context"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestDisabledIntegrationPreservesManualSubmission(t *testing.T) {
	t.Setenv("CULVERT_IMAGE_INTEGRATIONS", "")
	dbc := testdb.TestDB(t)
	oldDB, oldHandler := db.DB, imageHandler
	db.DB = dbc
	defer func() { db.DB = oldDB; imageHandler = oldHandler }()
	ctx := context.Background()
	if err := StartImageSubmissions(ctx); err != nil {
		t.Fatal(err)
	}
	if len(imageHandler.Principals) != 0 {
		t.Fatal("integration enabled")
	}
	tx, err := dbc.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = commands.LockSubmissionTenant(ctx, tx, "manual-test"); err != nil {
		t.Fatal(err)
	}
	week := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	if _, err = commands.ApplyScoreSubmission(ctx, tx, "manual-test", week, map[string]int{"Fixture": 123}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var score, revision int
	if err = dbc.QueryRow("SELECT score FROM character_culvert_scores").Scan(&score); err != nil || score != 123 {
		t.Fatalf("manual score %d %v", score, err)
	}
	if err = dbc.QueryRow("SELECT count(*) FROM culvert_manual_revisions WHERE tenant_id='manual-test'").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("manual revision missing %d %v", revision, err)
	}
	// Deploy also reconstructs prior manual timestamps with no integration grant.
	if _, err = dbc.Exec("DELETE FROM culvert_manual_revisions"); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../db_migrations/13_image_submissions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbc.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if err = dbc.QueryRow("SELECT count(*) FROM culvert_manual_revisions WHERE tenant_id='manual-test'").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("migration backfill missing %d %v", revision, err)
	}
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/guild-score-submissions/unknown", nil))
	if rr.Code != 401 {
		t.Fatalf("disabled route admitted %d", rr.Code)
	}
}
