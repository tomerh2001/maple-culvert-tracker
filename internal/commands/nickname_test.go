package commands

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestParseNicknameIGNs pins the server-nickname parse: the IGN list is the
// substring after the last '|' (or the whole nickname), split on '/' or ',',
// trimmed, with empties dropped and the count capped.
func TestParseNicknameIGNs(t *testing.T) {
	tests := []struct {
		name string
		nick string
		want []string
	}{
		{"tag with slash list", "Sen | Senpapi/Pichi/DegenDaddy", []string{"Senpapi", "Pichi", "DegenDaddy"}},
		{"no tag single name", "Nunez", []string{"Nunez"}},
		{"spaces and comma separators", "Tag | A / B , C", []string{"A", "B", "C"}},
		{"tag with empty list", "Tag |", nil},
		{"empty nickname", "", nil},
		{"whitespace only after tag", "Tag |    ", nil},
		{"trailing separators keep real names", "Tag | A// /B,", []string{"A", "B"}},
		{"only the last pipe splits", "A | B | C/D", []string{"C", "D"}},
		{"no tag slash list", "Alpha/Beta", []string{"Alpha", "Beta"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNicknameIGNs(tt.nick)
			if len(got) != len(tt.want) || (len(got) > 0 && !slices.Equal(got, tt.want)) {
				t.Errorf("parseNicknameIGNs(%q) = %v, want %v", tt.nick, got, tt.want)
			}
		})
	}
}

// TestParseNicknameIGNsCap asserts a pathological nickname can't register more
// than maxNicknameIGNs characters at once.
func TestParseNicknameIGNsCap(t *testing.T) {
	names := make([]string, 0, maxNicknameIGNs*2)
	for i := 0; i < maxNicknameIGNs*2; i++ {
		names = append(names, "IGN"+strconv.Itoa(i))
	}
	got := parseNicknameIGNs("Tag | " + strings.Join(names, "/"))
	if len(got) != maxNicknameIGNs {
		t.Fatalf("parsed %d names, want the cap of %d", len(got), maxNicknameIGNs)
	}
	if !slices.Equal(got, names[:maxNicknameIGNs]) {
		t.Errorf("cap kept %v, want the first %d: %v", got, maxNicknameIGNs, names[:maxNicknameIGNs])
	}
}
