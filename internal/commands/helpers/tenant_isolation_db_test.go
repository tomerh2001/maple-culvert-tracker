package helpers

// Two-tenant isolation integration tests against a real postgres (see
// internal/db/testdb): the tenant model's core guarantee is that no data ever
// crosses tenants - same-named characters coexist, and scores, rosters,
// week counts and deletions stay inside their guild_id.

import (
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

// The same character name may be tracked by two tenants at once: distinct
// rows, distinct ids, and each tenant's roster sees only its own.
func TestSameCharacterNameCoexistsAcrossTenants(t *testing.T) {
	dbc := testdb.TestDB(t)

	createdA, idsA, err := TrackCharacters(dbc, testTenantA, []string{"SharedName"})
	if err != nil {
		t.Fatalf("tracking in tenant A: %v", err)
	}
	createdB, idsB, err := TrackCharacters(dbc, testTenantB, []string{"SharedName"})
	if err != nil {
		t.Fatalf("tracking in tenant B: %v", err)
	}
	if len(createdA) != 1 || len(createdB) != 1 {
		t.Fatalf("created = %v / %v, want one creation per tenant", createdA, createdB)
	}
	if idsA["SharedName"] == idsB["SharedName"] {
		t.Fatalf("both tenants resolved the same character id %d, want distinct rows", idsA["SharedName"])
	}

	// Re-tracking within a tenant still no-ops onto the tenant's own row.
	created2, ids2, err := TrackCharacters(dbc, testTenantA, []string{"SharedName"})
	if err != nil {
		t.Fatalf("re-tracking in tenant A: %v", err)
	}
	if len(created2) != 0 || ids2["SharedName"] != idsA["SharedName"] {
		t.Fatalf("re-track created=%v id=%d, want no creation and A's id %d",
			created2, ids2["SharedName"], idsA["SharedName"])
	}

	for tenant, wantID := range map[string]int64{testTenantA: idsA["SharedName"], testTenantB: idsB["SharedName"]} {
		chars, err := GetActiveCharacters(apiredis.RedisDB, dbc, tenant)
		if err != nil {
			t.Fatalf("roster for %s: %v", tenant, err)
		}
		if len(*chars) != 1 || (*chars)[0].ID != wantID || (*chars)[0].GuildID != tenant {
			t.Errorf("tenant %s roster = %+v, want exactly its own SharedName (id %d)", tenant, *chars, wantID)
		}
	}
}

// Scores recorded by one tenant are invisible to the other tenant's week
// count, and CountWeekScores tracks only the asked-for week.
func TestWeekScoreCountsNeverCrossTenants(t *testing.T) {
	dbc := testdb.TestDB(t)
	week := testWeek
	prevWeek := week.AddDate(0, 0, -7)

	idA := insertTestCharacterInTenant(t, dbc, testTenantA, "CountA")
	idB := insertTestCharacterInTenant(t, dbc, testTenantB, "CountB")

	if err := UpsertCulvertScores(dbc, week, []ScoreChange{{CharacterID: idA, Score: 100}}); err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	if err := UpsertCulvertScores(dbc, prevWeek, []ScoreChange{{CharacterID: idA, Score: 50}}); err != nil {
		t.Fatalf("upsert A prev: %v", err)
	}
	if err := UpsertCulvertScores(dbc, week, []ScoreChange{{CharacterID: idB, Score: 999}}); err != nil {
		t.Fatalf("upsert B: %v", err)
	}

	for _, tc := range []struct {
		tenant string
		week   time.Time
		want   int
	}{
		{testTenantA, week, 1},
		{testTenantA, prevWeek, 1},
		{testTenantB, week, 1},
		{testTenantB, prevWeek, 0},
	} {
		got, err := CountWeekScores(dbc, tc.tenant, tc.week)
		if err != nil {
			t.Fatalf("CountWeekScores(%s, %s): %v", tc.tenant, tc.week.Format(time.DateOnly), err)
		}
		if got != tc.want {
			t.Errorf("CountWeekScores(%s, %s) = %d, want %d", tc.tenant, tc.week.Format(time.DateOnly), got, tc.want)
		}
	}
}

// DeleteWeekScores (the /reset-week action) deletes exactly the invoking
// tenant's current-week rows: the other tenant's rows and the tenant's other
// weeks survive, and characters stay tracked.
func TestDeleteWeekScoresIsTenantAndWeekScoped(t *testing.T) {
	dbc := testdb.TestDB(t)
	week := testWeek
	prevWeek := week.AddDate(0, 0, -7)

	idA1 := insertTestCharacterInTenant(t, dbc, testTenantA, "ResetA1")
	idA2 := insertTestCharacterInTenant(t, dbc, testTenantA, "ResetA2")
	idB := insertTestCharacterInTenant(t, dbc, testTenantB, "ResetB")

	if err := UpsertCulvertScores(dbc, week, []ScoreChange{{CharacterID: idA1, Score: 10}, {CharacterID: idA2, Score: 20}}); err != nil {
		t.Fatalf("upsert A week: %v", err)
	}
	if err := UpsertCulvertScores(dbc, prevWeek, []ScoreChange{{CharacterID: idA1, Score: 5}}); err != nil {
		t.Fatalf("upsert A prev week: %v", err)
	}
	if err := UpsertCulvertScores(dbc, week, []ScoreChange{{CharacterID: idB, Score: 30}}); err != nil {
		t.Fatalf("upsert B week: %v", err)
	}

	deleted, err := DeleteWeekScores(dbc, testTenantA, week)
	if err != nil {
		t.Fatalf("DeleteWeekScores: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2 (both of A's current-week rows)", deleted)
	}

	if got, _ := CountWeekScores(dbc, testTenantA, week); got != 0 {
		t.Errorf("tenant A current week count after reset = %d, want 0", got)
	}
	if got, _ := CountWeekScores(dbc, testTenantA, prevWeek); got != 1 {
		t.Errorf("tenant A PREVIOUS week count after reset = %d, want 1 (other weeks untouched)", got)
	}
	if got, _ := CountWeekScores(dbc, testTenantB, week); got != 1 {
		t.Errorf("tenant B current week count after reset = %d, want 1 (other tenants untouched)", got)
	}

	// Characters stay tracked - only score rows were removed.
	chars, err := GetActiveCharacters(apiredis.RedisDB, dbc, testTenantA)
	if err != nil {
		t.Fatalf("roster after reset: %v", err)
	}
	if len(*chars) != 2 {
		t.Errorf("tenant A roster size after reset = %d, want 2 (characters stay tracked)", len(*chars))
	}

	// A second reset finds nothing (idempotent from the caller's view).
	deleted2, err := DeleteWeekScores(dbc, testTenantA, week)
	if err != nil {
		t.Fatalf("second DeleteWeekScores: %v", err)
	}
	if deleted2 != 0 {
		t.Errorf("second delete removed %d rows, want 0", deleted2)
	}
}
