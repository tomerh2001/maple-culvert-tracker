package helpers

import (
	"strings"
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/model"
)

func TestFilterCharactersByMembers(t *testing.T) {
	chars := []model.Characters{
		{ID: 1, MapleCharacterName: "Linked", DiscordUserID: "111"},
		{ID: 2, MapleCharacterName: "Gone", DiscordUserID: "222"},
		{ID: 3, MapleCharacterName: "Unlinked", DiscordUserID: "2"},
		{ID: 4, MapleCharacterName: "Untracked", DiscordUserID: "1"},
		{ID: 5, MapleCharacterName: "EmptyOwner", DiscordUserID: ""},
	}
	members := map[string]bool{"111": true}

	got := FilterCharactersByMembers(chars, members)
	names := []string{}
	for _, c := range got {
		names = append(names, c.MapleCharacterName)
	}
	// Linked (cached member) kept; Gone (real id absent from cache) excluded;
	// Unlinked ('2') and EmptyOwner kept; Untracked ('1') excluded.
	if want := "Linked,Unlinked,EmptyOwner"; strings.Join(names, ",") != want {
		t.Fatalf("filtered roster = %v, want %s", names, want)
	}
}

func TestFilterCharactersByMembersEmptyCacheMapExcludesOnlyRealIDs(t *testing.T) {
	chars := []model.Characters{
		{ID: 1, MapleCharacterName: "Linked", DiscordUserID: "111"},
		{ID: 2, MapleCharacterName: "Unlinked", DiscordUserID: "2"},
	}
	got := FilterCharactersByMembers(chars, map[string]bool{})
	if len(got) != 1 || got[0].MapleCharacterName != "Unlinked" {
		t.Fatalf("filtered roster = %+v, want only Unlinked", got)
	}
}

func TestRosterMetaStalenessWarning(t *testing.T) {
	cases := []struct {
		name string
		meta RosterMeta
		warn bool
	}{
		{"no role filter configured -> no warning even without cache", RosterMeta{}, false},
		{"configured with fresh cache -> no warning",
			RosterMeta{FilterConfigured: true, CacheAvailable: true, CacheAgeKnown: true, CacheAge: 10 * time.Minute}, false},
		{"configured but cache missing -> warning",
			RosterMeta{FilterConfigured: true}, true},
		{"configured but cache age unknown -> warning",
			RosterMeta{FilterConfigured: true, CacheAvailable: true}, true},
		{"configured but cache older than 2h -> warning",
			RosterMeta{FilterConfigured: true, CacheAvailable: true, CacheAgeKnown: true, CacheAge: 3 * time.Hour}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.meta.StalenessWarning()
			if (got != "") != c.warn {
				t.Fatalf("StalenessWarning() = %q, want warning=%v", got, c.warn)
			}
			if c.warn && !strings.Contains(got, "Member cache is stale/unavailable") {
				t.Fatalf("warning text = %q", got)
			}
		})
	}
}

func TestSplitNameList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{" , \n ", nil},
		{"Alice,Bob,Carol", []string{"Alice", "Bob", "Carol"}},
		{"Alice\nBob\r\nCarol", []string{"Alice", "Bob", "Carol"}},
		{"  Alice , Bob\n, ,Carol  ", []string{"Alice", "Bob", "Carol"}},
		{"Alice,alice,ALICE,Bob", []string{"Alice", "Bob"}}, // case-insensitive dedupe, first wins
	}
	for _, c := range cases {
		got := SplitNameList(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("SplitNameList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
