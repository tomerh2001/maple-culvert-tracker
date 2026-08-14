package commands

// Integration test for the multi-IGN registration invariant (see
// registerCharacter): one member may hold many character rows (mules), while a
// name already owned by a DIFFERENT real member is refused to a non-submitter.
// Exercised only when TEST_DATABASE_URL is set (see internal/db/testdb).

import (
	"database/sql"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

const (
	regTenant = "111111111111111111"
	regUserA  = "123123123123123123"
	regUserB  = "456456456456456456"
)

// regInsert mirrors /register's brand-new-IGN INSERT (the sql.ErrNoRows branch)
// so the test tracks the real code path rather than a paraphrase of it.
func regInsert(t *testing.T, dbc *sql.DB, name, owner string) {
	t.Helper()
	if _, err := dbc.Exec(
		`INSERT INTO characters (maple_character_name, discord_user_id, guild_id) VALUES ($1, $2, $3)`,
		name, owner, regTenant); err != nil {
		t.Fatalf("insert %q for %s: %v", name, owner, err)
	}
}

func TestRegisterMultiIGNInvariant(t *testing.T) {
	dbc := testdb.TestDB(t)

	// One member registers three IGNs: each new name is a fresh row (mules).
	regInsert(t, dbc, "Main", regUserA)
	regInsert(t, dbc, "MuleTwo", regUserA)
	regInsert(t, dbc, "MuleThree", regUserA)

	var count int
	if err := dbc.QueryRow(
		`SELECT COUNT(*) FROM characters WHERE guild_id = $1 AND discord_user_id = $2`,
		regTenant, regUserA).Scan(&count); err != nil {
		t.Fatalf("counting userA characters: %v", err)
	}
	if count != 3 {
		t.Fatalf("userA holds %d characters, want 3 (multi-IGN must be allowed)", count)
	}

	// A fourth IGN already owned by a DIFFERENT real member.
	regInsert(t, dbc, "Foreign", regUserB)

	// /register looks the name up case-insensitively within the tenant.
	var existingOwner string
	if err := dbc.QueryRow(
		`SELECT discord_user_id FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND guild_id = $2`,
		"foreign", regTenant).Scan(&existingOwner); err != nil {
		t.Fatalf("looking up Foreign: %v", err)
	}
	if existingOwner != regUserB {
		t.Fatalf("Foreign owner = %q, want %q", existingOwner, regUserB)
	}

	// The ownership gate /register applies: userA (a non-submitter targeting
	// themselves) is refused, because the row belongs to another real member.
	if !characterOwnedByAnother(existingOwner, regUserA) {
		t.Errorf("characterOwnedByAnother(%q, %q) = false, want true (owned by another member -> non-submitter refused)", existingOwner, regUserA)
	}
	// The owner re-registering their OWN name is never treated as another's
	// (that path is the no-op / relink case, not a refusal).
	if characterOwnedByAnother(existingOwner, regUserB) {
		t.Errorf("characterOwnedByAnother(%q, %q) = true, want false (the row's own owner)", existingOwner, regUserB)
	}
	// Sentinels and pre-tenant rows are movable, never "another member".
	for _, s := range []string{"1", "2", ""} {
		if characterOwnedByAnother(s, regUserA) {
			t.Errorf("characterOwnedByAnother(%q, target) = true, want false (sentinel is not a real owner)", s)
		}
	}
}
