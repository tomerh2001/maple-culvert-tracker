package helpers

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRealFull4AllRows pins the narrow-"1" score bug. real-full-4.png is a real
// ~2.01x capture the anchor-smooth engine used to misread three ways: it
// decoded the tens "1" of a score as a thousands comma and dropped it (Cholom
// 61,516 -> 6156, Stoic 44,710 -> 4470), and where that skipped enough ink to
// trip the dropped-digit guard it lost the whole row (Klout 107,193 vanished).
// The window shows all 17 rows in descending score order, so the parse must
// return EXACTLY those 17 scores in that order - a single dropped digit here
// would be a SILENT wrong score in a 200+ player server. The decode is also
// roster-independent, so empty/subset/full rosters must yield identical rows.
func TestRealFull4AllRows(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../provided/real-full-4.png")
	if err != nil {
		t.Skip("no real-full-4.png")
	}
	raw, err := os.ReadFile("../../../provided/real-full-4.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	keys := orderedKeys(t, raw) // descending score order, as the window renders
	if len(keys) != 17 {
		t.Fatalf("ground truth has %d rows, want 17", len(keys))
	}

	members := make([]string, 0, len(keys))
	for _, k := range keys {
		members = append(members, strings.TrimSuffix(k, ".."))
	}

	// Roster independence: empty, subset and full must yield identical rows.
	rosters := map[string][]string{
		"full":   members,
		"subset": members[:3],
		"empty":  {},
	}
	results := map[string]*ParseResult{}
	for name, roster := range rosters {
		start := time.Now()
		res, err := ParseParticipation(data, roster, font)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("%s roster: %v", name, err)
		}
		t.Logf("%s roster: %d rows in %s (engine %s, scale %.3f)",
			name, len(res.Rows), elapsed.Round(time.Millisecond), res.Engine, res.Scale)
		if res.Truncated {
			t.Errorf("%s roster: parse reported Truncated", name)
		}
		if elapsed >= 8*time.Second {
			t.Errorf("%s roster: parse took %s, need < 8s", name, elapsed)
		}
		results[name] = res
	}
	assertRowsIdentical(t, "real-full-4 full-vs-empty", results["full"].Rows, results["empty"].Rows)
	assertRowsIdentical(t, "real-full-4 subset-vs-empty", results["subset"].Rows, results["empty"].Rows)

	// Every one of the 17 rows must be present with EXACTLY its ground-truth
	// score, in descending order.
	rows := results["empty"].Rows
	if len(rows) != 17 {
		got := make([]string, 0, len(rows))
		for _, e := range rows {
			got = append(got, e.RawName+":"+strconv.Itoa(e.Score))
		}
		t.Fatalf("parsed %d rows, want EXACTLY 17\ngot: %v", len(rows), got)
	}
	for i, k := range keys {
		want := expected[k]
		if rows[i].Score != want {
			t.Errorf("row %d (%s): score %d, want EXACTLY %d", i, k, rows[i].Score, want)
		}
		if ellipsed := strings.HasSuffix(k, ".."); ellipsed {
			if !rawNameCompatible(rows[i].RawName, strings.TrimSuffix(k, ".."), true) {
				t.Errorf("row %d: raw %q incompatible with truncated ground truth %q", i, rows[i].RawName, k)
			}
		} else if !rawNameCompatible(rows[i].RawName, k, true) {
			t.Errorf("row %d: raw name %q incompatible with ground truth %q", i, rows[i].RawName, k)
		}
	}
}

// TestRealFull5Manqa gates the roster's second page: Manqa=5927 must decode
// (its other rows are 0-culvert and intentionally skipped). This shares the
// score-column decode path with real-full-4, so it guards against a narrow-"1"
// fix that over-corrects into spurious digits on a low, comma-free score.
func TestRealFull5Manqa(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../provided/real-full-5.png")
	if err != nil {
		t.Skip("no real-full-5.png")
	}
	res, err := ParseParticipation(data, nil, font)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range res.Rows {
		if e.Score == 5927 {
			found = true
		}
		if e.Score != 5927 {
			t.Errorf("unexpected non-zero row raw=%q score=%d (only Manqa=5927 is culvert-positive)", e.RawName, e.Score)
		}
	}
	if !found {
		got := make([]string, 0, len(res.Rows))
		for _, e := range res.Rows {
			got = append(got, e.RawName+":"+strconv.Itoa(e.Score))
		}
		t.Errorf("Manqa=5927 not found; rows: %v", got)
	}
}
