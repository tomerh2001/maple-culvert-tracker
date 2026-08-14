package helpers

import (
	"os"
	"testing"
)

// The game truncates long names to a prefix + "..". OCR decodes that prefix
// either with the dots dropped (clean, ellipsis=false, e.g. "DegenDad") or
// with the dots misread as a stray letter (ellipsis=true, e.g. "FangAvengD").
// Registered members store the FULL IGN. These tests pin the never-guess
// reconciliation: a unique roster prefix auto-matches, a tie is flagged and
// left raw, and a short full name never folds into a longer member.

// TestSlicedNamesRealFull4 is the integration gate: with the FULL registered
// names in the roster, both truncation shapes on real-full-4.png must
// reconcile onto their full member - the clean prefix "DegenDad" onto
// "DegenDaddyX" (the previously-failing case) and the dots-misread
// "FangAvengD" onto "FangAvengerX".
func TestSlicedNamesRealFull4(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../provided/real-full-4.png")
	if err != nil {
		t.Skip("no real-full-4.png")
	}
	// Full IGNs for the two truncated rows plus a couple of full-name members.
	roster := []string{"FangAvengerX", "DegenDaddyX", "Cholom", "Stoic"}
	res, err := ParseParticipation(data, roster, font)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated {
		t.Fatal("parse reported Truncated")
	}

	want := map[int]string{ // score -> full member the truncated row must map to
		62609: "FangAvengerX",
		32792: "DegenDaddyX",
	}
	seen := map[string]bool{}
	for _, e := range res.Rows {
		full, ok := want[e.Score]
		if !ok {
			continue
		}
		if !e.Matched || e.Name != full {
			t.Errorf("row score %d: raw=%q matched=%v name=%q, want matched onto %q",
				e.Score, e.RawName, e.Matched, e.Name, full)
			continue
		}
		if e.Ambiguous {
			t.Errorf("row score %d (%q): flagged ambiguous despite a unique match", e.Score, e.RawName)
		}
		seen[full] = true
	}
	for _, full := range want {
		if !seen[full] {
			t.Errorf("no row reconciled onto %q", full)
		}
	}
}

// TestSlicedNamesUniquePrefixMatches: a clean (dots-dropped) truncation of the
// exact width the game cuts at maps onto the single registered member that
// extends it, and is NOT flagged ambiguous.
func TestSlicedNamesUniquePrefixMatches(t *testing.T) {
	roster := []string{"DegenDaddyX", "Cholom", "Stoic"}
	rows := []parsedRow{{rawName: "DegenDad", ellipsis: false, score: "32792"}}
	var defects []string
	entries := resolveRows(rows, roster, &defects)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if !e.Matched || e.Name != "DegenDaddyX" {
		t.Fatalf("DegenDad reconciled to %#v, want matched DegenDaddyX", e)
	}
	if e.Ambiguous {
		t.Errorf("unique match was flagged ambiguous")
	}
	if e.RawName != "DegenDad" {
		t.Errorf("RawName mutated to %q, want DegenDad", e.RawName)
	}
}

// TestSlicedNamesAmbiguousTie: when two registered members share the truncated
// prefix, the row must NOT be attributed to either - it stays its raw stub and
// is flagged ambiguous, carrying both candidates. A wrong match is the worst
// outcome, so a tie is never guessed.
func TestSlicedNamesAmbiguousTie(t *testing.T) {
	roster := []string{"DegenDaddyX", "DegenDaddyY"}
	rows := []parsedRow{{rawName: "DegenDad", ellipsis: false, score: "32792"}}
	var defects []string
	entries := resolveRows(rows, roster, &defects)
	e := entries[0]
	if e.Matched {
		t.Fatalf("ambiguous truncation was matched onto %q - must never guess", e.Name)
	}
	if e.Name != "DegenDad" {
		t.Errorf("ambiguous row name = %q, want the raw stub DegenDad", e.Name)
	}
	if !e.Ambiguous {
		t.Errorf("tie was not flagged ambiguous")
	}
	if len(e.AmbiguousCandidates) != 2 {
		t.Errorf("candidates = %v, want both members", e.AmbiguousCandidates)
	}
}

// TestSlicedNamesShortNameNeverFolds: a short FULL name (below the truncation
// width, no ellipsis) must keep its own row and never fold into a longer
// registered member.
func TestSlicedNamesShortNameNeverFolds(t *testing.T) {
	roster := []string{"Stoically"}
	rows := []parsedRow{{rawName: "Stoic", ellipsis: false, score: "44710"}}
	var defects []string
	entries := resolveRows(rows, roster, &defects)
	e := entries[0]
	if e.Matched || e.Name != "Stoic" {
		t.Fatalf("Stoic reconciled to %#v, want its own raw row", e)
	}
	if e.Ambiguous {
		t.Errorf("short full name flagged ambiguous")
	}
}

// TestSlicedNamesCleanPrefixNeedsGap: without ellipsis evidence, a decode only
// one glyph shorter than a member is a DIFFERENT name, not a truncation, and
// must stay literal (StellaMari vs StellaMaris). Ellipsis evidence flips it.
func TestSlicedNamesCleanPrefixNeedsGap(t *testing.T) {
	roster := []string{"StellaMaris"}

	var defects []string
	clean := resolveRows([]parsedRow{{rawName: "StellaMari", ellipsis: false, score: "1"}}, roster, &defects)
	if clean[0].Matched {
		t.Errorf("StellaMari (no ellipsis) folded into %q; a one-glyph gap is not a truncation", clean[0].Name)
	}
	if clean[0].Ambiguous {
		t.Errorf("StellaMari flagged ambiguous")
	}

	defects = nil
	flagged := resolveRows([]parsedRow{{rawName: "StellaMari", ellipsis: true, score: "1"}}, roster, &defects)
	if !flagged[0].Matched || flagged[0].Name != "StellaMaris" {
		t.Errorf("StellaMari (ellipsis) = %#v, want matched StellaMaris", flagged[0])
	}
}

// TestPrefixTruncationCandidatesShapes exercises the candidate collector
// directly across the decision boundaries.
func TestPrefixTruncationCandidatesShapes(t *testing.T) {
	cases := []struct {
		name     string
		dec      string
		ellipsis bool
		roster   []string
		want     []string
	}{
		{"clean unique width match", "DegenDad", false,
			[]string{"DegenDaddyX", "Cholom"}, []string{"DegenDaddyX"}},
		{"dots-misread with ellipsis", "FangAvengD", true,
			[]string{"FangAvengerX"}, []string{"FangAvengerX"}},
		{"tie", "DegenDad", false,
			[]string{"DegenDaddyX", "DegenDaddyY"}, []string{"DegenDaddyX", "DegenDaddyY"}},
		{"short no-ellipsis rejected", "Stoic", false,
			[]string{"Stoically"}, nil},
		{"one-glyph gap no-ellipsis rejected", "StellaMari", false,
			[]string{"StellaMaris"}, nil},
		{"l/I fold", "KIoutmast", false,
			[]string{"KloutmasterX"}, []string{"KloutmasterX"}},
		{"equal length is not a truncation", "Radehaya", false,
			[]string{"Radehaya"}, nil},
	}
	for _, c := range cases {
		got := prefixTruncationCandidates(c.dec, c.ellipsis, c.roster)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}
