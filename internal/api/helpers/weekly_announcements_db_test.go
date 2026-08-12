package helpers

// Tenant-isolation integration tests for the weekly announcement path
// against a real postgres (see internal/db/testdb): week score tables and
// announcement records never cross tenants.

import (
	"database/sql"
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

const (
	annTenantA = "111111111111111111"
	annTenantB = "222222222222222222"
)

// annWeek is any fixed culvert reset day (a Wednesday).
var annWeek = time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)

func annInsertCharacter(t *testing.T, dbc *sql.DB, tenantID, name string, score int) int64 {
	t.Helper()
	var id int64
	if err := dbc.QueryRow(
		`INSERT INTO characters (maple_character_name, discord_user_id, guild_id) VALUES ($1, '2', $2) RETURNING id`,
		name, tenantID).Scan(&id); err != nil {
		t.Fatalf("inserting character %q: %v", name, err)
	}
	if _, err := dbc.Exec(
		`INSERT INTO character_culvert_scores (character_id, culvert_date, score) VALUES ($1, $2, $3)`,
		id, annWeek.Format(time.DateOnly), score); err != nil {
		t.Fatalf("inserting score for %q: %v", name, err)
	}
	return id
}

// weekScores returns only the asked-for tenant's rows, ranked over that
// tenant alone (never over the global table).
func TestWeekScoresAreTenantScoped(t *testing.T) {
	dbc := testdb.TestDB(t)
	annInsertCharacter(t, dbc, annTenantA, "AnnAlpha", 100)
	annInsertCharacter(t, dbc, annTenantA, "AnnBeta", 300)
	annInsertCharacter(t, dbc, annTenantB, "AnnOther", 99999)

	rows, err := weekScores(dbc, annTenantA, annWeek)
	if err != nil {
		t.Fatalf("weekScores: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("weekScores returned %d rows, want 2 (tenant A only)", len(rows))
	}
	if rows[0].Name != "AnnBeta" || rows[0].Rank != 1 || rows[1].Name != "AnnAlpha" || rows[1].Rank != 2 {
		t.Errorf("rows = %+v, want AnnBeta ranked 1 then AnnAlpha ranked 2 (ranks computed within the tenant)", rows)
	}

	rowsB, err := weekScores(dbc, annTenantB, annWeek)
	if err != nil {
		t.Fatalf("weekScores B: %v", err)
	}
	if len(rowsB) != 1 || rowsB[0].Name != "AnnOther" || rowsB[0].Rank != 1 {
		t.Errorf("tenant B rows = %+v, want only AnnOther ranked 1", rowsB)
	}
}

// Both tenants may hold an announcement record for the SAME week (composite
// primary key), and each tenant reads back only its own.
func TestWeeklyAnnouncementRecordsPerTenant(t *testing.T) {
	dbc := testdb.TestDB(t)
	weekStr := annWeek.Format(time.DateOnly)

	for tenant, msg := range map[string]string{annTenantA: "msgA", annTenantB: "msgB"} {
		if _, err := dbc.Exec(
			`INSERT INTO weekly_announcements (guild_id, culvert_date, channel_id, message_id, thread_id) VALUES ($1, $2, $3, $4, $5)`,
			tenant, weekStr, "chan-"+tenant, msg, "thread-"+tenant); err != nil {
			t.Fatalf("inserting announcement for %s: %v", tenant, err)
		}
	}

	// Same (guild, week) twice must violate the composite PK.
	if _, err := dbc.Exec(
		`INSERT INTO weekly_announcements (guild_id, culvert_date, channel_id, message_id, thread_id) VALUES ($1, $2, 'c', 'm', 't')`,
		annTenantA, weekStr); err == nil {
		t.Error("duplicate (guild_id, culvert_date) insert succeeded, want composite-PK violation")
	}

	var msgID string
	if err := dbc.QueryRow(
		`SELECT message_id FROM weekly_announcements WHERE guild_id = $1 AND culvert_date = $2`,
		annTenantA, weekStr).Scan(&msgID); err != nil {
		t.Fatalf("reading tenant A announcement: %v", err)
	}
	if msgID != "msgA" {
		t.Errorf("tenant A message_id = %q, want msgA", msgID)
	}
}
