package helpers

// Integration tests for the /characters and /registered queries against a real
// postgres (see internal/db/testdb): both are tenant-scoped, exclude the
// untracked ('1') and unlinked ('2') sentinels, and never leak across tenants.
// Exercised only when TEST_DATABASE_URL is set.

import (
	"reflect"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

// LinkedCharacterNames returns only the TARGET member's linked characters,
// ordered by name, scoped to the tenant - never another member's, a sentinel
// row, or the same user's characters in a different tenant.
func TestLinkedCharacterNamesScoping(t *testing.T) {
	dbc := testdb.TestDB(t)
	const userA = "123456789012345678"
	const userB = "876543210987654321"

	// userA has three IGNs (mules) in tenant A, inserted out of name order.
	insertCharacterWithOwner(t, dbc, testTenantA, "Charlie", userA)
	insertCharacterWithOwner(t, dbc, testTenantA, "Alpha", userA)
	insertCharacterWithOwner(t, dbc, testTenantA, "Bravo", userA)
	// Noise that must never appear in userA's list:
	insertCharacterWithOwner(t, dbc, testTenantA, "OtherMember", userB) // another member
	insertCharacterWithOwner(t, dbc, testTenantA, "Untracked", "1")     // untracked sentinel
	insertCharacterWithOwner(t, dbc, testTenantA, "Unlinked", "2")      // unlinked sentinel
	insertCharacterWithOwner(t, dbc, testTenantB, "CrossTenant", userA) // same user, other tenant

	got, err := LinkedCharacterNames(dbc, testTenantA, userA)
	if err != nil {
		t.Fatalf("LinkedCharacterNames: %v", err)
	}
	want := []string{"Alpha", "Bravo", "Charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v (ordered by name, tenant- and user-scoped)", got, want)
	}

	// A member who has linked nothing gets an empty (non-nil) slice.
	empty, err := LinkedCharacterNames(dbc, testTenantA, "000000000000000000")
	if err != nil {
		t.Fatalf("LinkedCharacterNames empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("no-characters member = %v, want empty", empty)
	}
}

// LinkedUsers returns every DISTINCT linked member (excluding the '1'/'2'
// sentinels), each with their character names, tenant-scoped and stable.
func TestLinkedUsersScoping(t *testing.T) {
	dbc := testdb.TestDB(t)
	const userA = "111000000000000001"
	const userB = "111000000000000002"

	insertCharacterWithOwner(t, dbc, testTenantA, "Beta", userA)
	insertCharacterWithOwner(t, dbc, testTenantA, "Alpha", userA) // userA has 2 IGNs
	insertCharacterWithOwner(t, dbc, testTenantA, "Gamma", userB) // userB has 1
	insertCharacterWithOwner(t, dbc, testTenantA, "Untracked", "1")
	insertCharacterWithOwner(t, dbc, testTenantA, "Unlinked", "2")
	insertCharacterWithOwner(t, dbc, testTenantB, "OtherGuild", "999000000000000000")

	got, err := LinkedUsers(dbc, testTenantA)
	if err != nil {
		t.Fatalf("LinkedUsers: %v", err)
	}
	want := []LinkedUser{
		{DiscordUserID: userA, Names: []string{"Alpha", "Beta"}},
		{DiscordUserID: userB, Names: []string{"Gamma"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("linked users = %+v, want %+v (distinct linked members, names ordered)", got, want)
	}

	// Tenant isolation: tenant B sees only its own linked member.
	gotB, err := LinkedUsers(dbc, testTenantB)
	if err != nil {
		t.Fatalf("LinkedUsers B: %v", err)
	}
	if len(gotB) != 1 || gotB[0].DiscordUserID != "999000000000000000" {
		t.Errorf("tenant B linked users = %+v, want only its own member", gotB)
	}

	// A tenant with no linked members returns none.
	gotEmpty, err := LinkedUsers(dbc, "333333333333333333")
	if err != nil {
		t.Fatalf("LinkedUsers empty: %v", err)
	}
	if len(gotEmpty) != 0 {
		t.Errorf("empty-tenant linked users = %+v, want none", gotEmpty)
	}
}
