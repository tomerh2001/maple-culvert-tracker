package helpers

import "testing"

// The production roster of the screenshot that shipped the Xenpapi->Senpapi
// misreconciliation bug.
var productionRoster = []string{"HTomer", "Senpapi", "StarryLeyva"}

// A full (non-truncated) decode of a name that is NOT in the roster must stay
// literal even when it is one letter away from a roster member. This is the
// exact bug that shipped: "Xenpapi" was snapped to "Senpapi".
func TestReconcileRejectsXenpapiAsSenpapi(t *testing.T) {
	if got := reconcileName("Xenpapi", false, productionRoster); got != "Xenpapi" {
		t.Fatalf("reconcileName(Xenpapi) = %q, want the literal decode back", got)
	}
	// The real member still reconciles (exact match).
	if got := reconcileName("Senpapi", false, productionRoster); got != "Senpapi" {
		t.Fatalf("reconcileName(Senpapi) = %q, want Senpapi", got)
	}
}

// A strict-prefix decode may only snap to the longer member when the decode
// carried ellipsis evidence (the game truncates long names with "...").
func TestReconcileTruncationRequiresEllipsisEvidence(t *testing.T) {
	roster := []string{"StellaMaris"}
	if got := reconcileName("StellaMari", false, roster); got != "StellaMari" {
		t.Fatalf("without ellipsis evidence: got %q, want literal StellaMari", got)
	}
	if got := reconcileName("StellaMari", true, roster); got != "StellaMaris" {
		t.Fatalf("with ellipsis evidence: got %q, want StellaMaris", got)
	}
}

func TestCleanDecodedNameReportsEllipsisEvidence(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		ellipsis bool
	}{
		{"StellaMari..", "StellaMari", true},
		{"StellaMari...", "StellaMari", true},
		{"StellaMari,.", "StellaMari", true}, // ellipsis dot decoded as comma
		{"CarlosJunk,,", "CarlosJunk", true},
		{"StellaMari", "StellaMari", false},
		{"Senpapi", "Senpapi", false},
	}
	for _, c := range cases {
		got, ellipsis := cleanDecodedName(c.in)
		if got != c.want || ellipsis != c.ellipsis {
			t.Errorf("cleanDecodedName(%q) = (%q, %v), want (%q, %v)", c.in, got, ellipsis, c.want, c.ellipsis)
		}
	}
}

// The runner-up margin: a decode that two members explain about equally well
// stays literal instead of guessing between them.
func TestReconcileAmbiguousBetweenMembersStaysLiteral(t *testing.T) {
	roster := []string{"AbcdefgX", "AbcdefgY"}
	if got := reconcileName("Abcdefg", true, roster); got != "Abcdefg" {
		t.Fatalf("ambiguous truncated decode reconciled to %q, want literal Abcdefg", got)
	}
}

// Interior misreads within tight edit distance still reconcile when
// unambiguous and the first glyph agrees.
func TestReconcileToleratesInteriorMisread(t *testing.T) {
	roster := []string{"Heartorias"}
	if got := reconcileName("Heartorjas", false, roster); got != "Heartorias" {
		t.Fatalf("reconcileName(Heartorjas) = %q, want Heartorias", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"xenpapi", "senpapi", 1},
		{"stellamari", "stellamaris", 1},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
	}
	for _, c := range cases {
		if got := levenshtein([]rune(c.a), []rune(c.b)); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
