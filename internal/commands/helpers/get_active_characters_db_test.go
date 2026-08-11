package helpers

// Integration tests for the roster base query and TrackCharacters against a
// real postgres, exercised only when TEST_DATABASE_URL is set (see
// internal/db/testdb). Redis is intentionally absent: apiredis falls back to
// defaults, so the roster is the unfiltered DB-canonical base set.

import (
	"database/sql"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

func insertCharacterWithOwner(t *testing.T, dbc *sql.DB, name, discordUserID string) {
	t.Helper()
	if _, err := dbc.Exec(
		`INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, $2)`,
		name, discordUserID); err != nil {
		t.Fatalf("inserting character %q (%s): %v", name, discordUserID, err)
	}
}

// The base roster query excludes only explicitly untracked characters
// (discord_user_id = '1'); unlinked-but-tracked ('2') and real Discord ids
// are included.
func TestGetActiveCharactersBaseQuerySemantics(t *testing.T) {
	dbc := testdb.TestDB(t)
	insertCharacterWithOwner(t, dbc, "Untracked", "1")
	insertCharacterWithOwner(t, dbc, "Unlinked", "2")
	insertCharacterWithOwner(t, dbc, "Linked", "123456789012345678")

	chars, meta, err := GetActiveCharactersWithMeta(apiredis.RedisDB, dbc)
	if err != nil {
		t.Fatalf("GetActiveCharactersWithMeta: %v", err)
	}
	if meta.FilterApplied {
		// Only possible when a local redis holds role config AND a member
		// cache; that is not this harness's environment.
		t.Skip("local redis with role filtering configured - base query semantics not isolatable")
	}

	got := map[string]bool{}
	for _, c := range *chars {
		got[c.MapleCharacterName] = true
	}
	if got["Untracked"] {
		t.Error("roster includes 'Untracked' (discord_user_id '1'), want excluded")
	}
	if !got["Unlinked"] {
		t.Error("roster is missing 'Unlinked' (discord_user_id '2')")
	}
	if !got["Linked"] {
		t.Error("roster is missing 'Linked' (real discord id)")
	}
	if len(*chars) != 2 {
		t.Errorf("roster size = %d, want 2 (%v)", len(*chars), got)
	}
}

func TestTrackCharactersOnConflictDoNothing(t *testing.T) {
	dbc := testdb.TestDB(t)

	created, ids, err := TrackCharacters(dbc, []string{"Alpha", "Beta"})
	if err != nil {
		t.Fatalf("first TrackCharacters: %v", err)
	}
	if len(created) != 2 || created[0] != "Alpha" || created[1] != "Beta" {
		t.Fatalf("first created = %v, want [Alpha Beta]", created)
	}
	if ids["Alpha"] == 0 || ids["Beta"] == 0 {
		t.Fatalf("first ids = %v, want non-zero ids for both", ids)
	}

	// Re-tracking an existing name must be a no-op (ON CONFLICT DO NOTHING)
	// that still resolves its id; new names are still created.
	created2, ids2, err := TrackCharacters(dbc, []string{"Alpha", "Gamma"})
	if err != nil {
		t.Fatalf("second TrackCharacters: %v", err)
	}
	if len(created2) != 1 || created2[0] != "Gamma" {
		t.Fatalf("second created = %v, want [Gamma]", created2)
	}
	if ids2["Alpha"] != ids["Alpha"] {
		t.Errorf("re-tracked Alpha id = %d, want original %d", ids2["Alpha"], ids["Alpha"])
	}
	if ids2["Gamma"] == 0 {
		t.Errorf("ids2 = %v, want non-zero id for Gamma", ids2)
	}

	var count int
	var owner string
	if err := dbc.QueryRow(
		`SELECT COUNT(*), MIN(discord_user_id) FROM characters WHERE maple_character_name = 'Alpha'`).Scan(&count, &owner); err != nil {
		t.Fatalf("querying Alpha rows: %v", err)
	}
	if count != 1 {
		t.Errorf("Alpha row count = %d, want 1 (no duplicate from re-track)", count)
	}
	if owner != "2" {
		t.Errorf("Alpha discord_user_id = %q, want '2' (unlinked-but-tracked)", owner)
	}
}
