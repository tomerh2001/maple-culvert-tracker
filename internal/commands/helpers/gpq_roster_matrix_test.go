package helpers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// This file gates the CRITICAL determinism invariant of the parser: engine
// arbitration is roster-free, so the winning parse's rows (RawName, Score,
// count, order) must be IDENTICAL for any roster - the full ground truth, a
// tiny subset, or an empty one. Only Name/Matched/Confidence may differ.
// Every fixture is parsed under all three rosters and compared, and the
// empty-roster raw decodes are checked against ground truth.

// matrixRosters builds the three roster variants for a ground-truth key list.
func matrixRosters(keys []string) map[string][]string {
	subset := keys
	if len(subset) > 3 {
		subset = subset[:3]
	}
	return map[string][]string{
		"full":   keys,
		"subset": subset,
		"empty":  {},
	}
}

// assertRowsIdentical fails unless a and b agree on row count, order, and
// each row's roster-independent identity (RawName, Score).
func assertRowsIdentical(t *testing.T, label string, a, b []ScoreEntry) {
	t.Helper()
	if len(a) != len(b) {
		t.Errorf("%s: row count differs: %d vs %d", label, len(a), len(b))
		return
	}
	for i := range a {
		if a[i].RawName != b[i].RawName || a[i].Score != b[i].Score {
			t.Errorf("%s row %d: (%q, %d) vs (%q, %d) - the parse depends on the roster",
				label, i, a[i].RawName, a[i].Score, b[i].RawName, b[i].Score)
		}
	}
}

// rawNameCompatible reports whether an empty-roster raw decode matches a
// ground-truth name, compared under the reconciler's own normName fold (l/I
// unify). Ground-truth names longer than what fits the name column render
// truncated (with an ellipsis whose dots may fail to decode, and a boundary
// glyph the cut can damage), so a decode whose prefix - all but the last two
// glyphs - agrees with the ground truth is accepted as the legitimate
// truncated form. allowDamage additionally admits small decode damage on
// LOSSY screenshots: the font's thin-stroke confusions (t/i/1) folded away,
// then at most two edits against the name, its column-truncated prefix, or
// its beheaded variants (smooth scaling can cost the leading glyph).
func rawNameCompatible(raw, want string, allowDamage bool) bool {
	rf := []rune(normName(raw))
	wf := []rune(normName(want))
	if string(rf) == string(wf) {
		return true
	}
	lcp := 0
	for lcp < len(rf) && lcp < len(wf) && rf[lcp] == wf[lcp] {
		lcp++
	}
	if len(rf) >= 5 && lcp >= len(rf)-2 && len(wf) > lcp {
		return true // truncated-with-evidence raw form of a longer name
	}
	if !allowDamage {
		return false
	}
	rt := foldThinRunes(rf)
	variants := [][]rune{wf}
	if len(wf) > len(rf) {
		variants = append(variants, wf[:len(rf)])
	}
	if len(wf) > 1 {
		behead := wf[1:]
		variants = append(variants, behead)
		if len(behead) > len(rf) {
			variants = append(variants, behead[:len(rf)])
		}
	}
	for _, v := range variants {
		if levenshtein(rt, foldThinRunes(v)) <= 2 {
			return true
		}
	}
	return false
}

// checkRawDecodes asserts the empty-roster parse of a LOSSLESS fixture: one
// row per ground-truth key in order, exact scores, compatible raw names.
func checkRawDecodes(t *testing.T, label string, rows []ScoreEntry, expected map[string]int, keys []string) {
	t.Helper()
	if len(rows) != len(keys) {
		got := make([]string, 0, len(rows))
		for _, e := range rows {
			got = append(got, e.RawName+":"+strconv.Itoa(e.Score))
		}
		t.Errorf("%s: got %d rows, want %d\ngot: %v", label, len(rows), len(keys), got)
		return
	}
	for i, k := range keys {
		if rows[i].Score != expected[k] {
			t.Errorf("%s row %d (%s): score %d, want %d", label, i, k, rows[i].Score, expected[k])
		}
		if !rawNameCompatible(rows[i].RawName, k, false) {
			t.Errorf("%s row %d: raw decode %q does not match ground truth %q", label, i, rows[i].RawName, k)
		}
	}
}

// parseVariants parses one image under every roster variant and asserts the
// invariant, returning the empty-roster result.
func parseVariants(t *testing.T, label string, data []byte, keys []string, font *GPQFont) *ParseResult {
	t.Helper()
	results := map[string]*ParseResult{}
	for name, roster := range matrixRosters(keys) {
		res, err := ParseParticipation(data, roster, font)
		if err != nil {
			t.Fatalf("%s (%s roster): %v", label, name, err)
		}
		if res.Truncated {
			t.Errorf("%s (%s roster): parse reported Truncated", label, name)
		}
		results[name] = res
	}
	assertRowsIdentical(t, label+" full-vs-empty", results["full"].Rows, results["empty"].Rows)
	assertRowsIdentical(t, label+" subset-vs-empty", results["subset"].Rows, results["empty"].Rows)
	return results["empty"]
}

// TestRosterMatrixFixtures runs the roster matrix over the 12 pre-cropped
// fixture tables.
func TestRosterMatrixFixtures(t *testing.T) {
	const total = 12
	expected := loadExpected(t, total)
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatalf("load font: %v", err)
	}
	for i := 1; i <= total; i++ {
		data, err := os.ReadFile(filepath.Join(gpqTestsDir, strconv.Itoa(i)+".png"))
		if err != nil {
			t.Fatalf("read %d.png: %v", i, err)
		}
		jsonData, err := os.ReadFile(filepath.Join(gpqTestsDir, strconv.Itoa(i)+".json"))
		if err != nil {
			t.Fatalf("read %d.json: %v", i, err)
		}
		keys := orderedKeys(t, jsonData)
		label := strconv.Itoa(i) + ".png"
		empty := parseVariants(t, label, data, keys, font)
		checkRawDecodes(t, label, empty.Rows, expected[i], keys)
	}
}

// assertLossyRawDecodes checks the empty-roster parse of a LOSSY image: every
// row's score must exist in the ground truth with a compatible raw name (a
// wrong score is worse than a missed row), and at least minRows rows must be
// recovered. Ground-truth scores are unique, so rows map by score.
func assertLossyRawDecodes(t *testing.T, label string, rows []ScoreEntry, expected map[string]int, minRows int) {
	t.Helper()
	byScore := map[int]string{}
	for name, score := range expected {
		byScore[score] = name
	}
	good := 0
	for i, e := range rows {
		want, ok := byScore[e.Score]
		if !ok {
			t.Errorf("%s row %d: score %d (raw %q) matches no ground-truth row", label, i, e.Score, e.RawName)
			continue
		}
		if !rawNameCompatible(e.RawName, want, true) {
			t.Errorf("%s row %d: raw decode %q incompatible with ground truth %q", label, i, e.RawName, want)
			continue
		}
		good++
	}
	if good < minRows {
		t.Errorf("%s: only %d rows decoded correctly, want >= %d", label, good, minRows)
	}
}

// TestRosterMatrixRealSample runs the roster matrix over the real user
// screenshot (smooth 2x, quantized colors), with a per-variant time gate.
func TestRosterMatrixRealSample(t *testing.T) {
	data, err := os.ReadFile("../../../provided/real-sample.png")
	if err != nil {
		t.Skip("no real sample present")
	}
	raw, err := os.ReadFile("../../../provided/real-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	keys := orderedKeys(t, raw)
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}

	rosters := matrixRosters(keys)
	rosters["subset"] = []string{"HTomer", "Senpapi", "StarryLeyva"}
	results := map[string]*ParseResult{}
	for name, roster := range rosters {
		start := time.Now()
		res, err := ParseParticipation(data, roster, font)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("%s roster: %v", name, err)
		}
		t.Logf("%s roster: %d rows in %s (engine %s)", name, len(res.Rows), elapsed.Round(time.Millisecond), res.Engine)
		if elapsed >= 10*time.Second {
			t.Errorf("%s roster: parse took %s, need < 10s", name, elapsed)
		}
		if res.Truncated {
			t.Errorf("%s roster: parse reported Truncated", name)
		}
		results[name] = res
	}
	assertRowsIdentical(t, "real-sample full-vs-empty", results["full"].Rows, results["empty"].Rows)
	assertRowsIdentical(t, "real-sample subset-vs-empty", results["subset"].Rows, results["empty"].Rows)

	assertLossyRawDecodes(t, "real-sample", results["empty"].Rows, expected, 15)
}

// TestMatchRowExpiredDeadlineFailsClosed: an expired clock must never yield a
// partial word (which could reconcile onto the wrong member) and must flag
// the run truncated.
func TestMatchRowExpiredDeadlineFailsClosed(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	// Render "Senpapi" into a band using the embedded templates.
	band := renderBandForTest(t, font, "Senpapi")

	run := &parseRun{}
	expired := run.until(time.Now().Add(-time.Second))
	if got := matchRowDual(band, band, font, expired); got != "" {
		t.Errorf("expired-deadline matchRowDual returned %q, want \"\" (fail closed)", got)
	}
	if !run.truncated.Load() {
		t.Errorf("expired clock did not flag the run truncated")
	}

	// Sanity: the same band decodes fine with a live clock.
	live := (&parseRun{}).until(time.Now().Add(time.Minute))
	if got := matchRowDual(band, band, font, live); got != "Senpapi" {
		t.Errorf("live-deadline matchRowDual returned %q, want Senpapi", got)
	}
	// And with no clock at all (legacy paths).
	if got := matchRowDual(band, band, font, nil); got != "Senpapi" {
		t.Errorf("nil-clock matchRowDual returned %q, want Senpapi", got)
	}
}

// renderBandForTest rasterizes a word into a bool ink grid via the embedded
// glyph templates.
func renderBandForTest(t *testing.T, font *GPQFont, s string) [][]bool {
	t.Helper()
	const h = 16
	rows := make([][]bool, h)
	width := 0
	glyphs := []*gpqGlyph{}
	for _, r := range s {
		g := glyphFor(font, r)
		if g == nil {
			t.Fatalf("no glyph template for %q", r)
		}
		glyphs = append(glyphs, g)
		width += g.w + 1
	}
	for y := range rows {
		rows[y] = make([]bool, width+4)
	}
	x := 0
	for _, g := range glyphs {
		for gy := 0; gy < g.h && gy < h; gy++ {
			for gx := 0; gx < g.w; gx++ {
				if g.bits[gy][gx] {
					rows[gy+2][x+gx] = true
				}
			}
		}
		x += g.w + 1
	}
	return rows
}

// TestInvalidMidTableScoreSkipsRowOnly: a row whose score failed to decode is
// skipped with a defect note - it must NOT abort the remaining rows (the old
// merge stopped at the first invalid score).
func TestInvalidMidTableScoreSkipsRowOnly(t *testing.T) {
	rows := []parsedRow{
		{rawName: "Alpha", score: "100"},
		{rawName: "Broken", score: ""}, // failed decode
		{rawName: "Gamma", score: "50"},
	}
	var defects []string
	entries := resolveRows(rows, nil, &defects)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (invalid row skipped, later rows kept): %#v", len(entries), entries)
	}
	if entries[0].RawName != "Alpha" || entries[0].Score != 100 {
		t.Errorf("entry 0 = %#v, want Alpha/100", entries[0])
	}
	if entries[1].RawName != "Gamma" || entries[1].Score != 50 {
		t.Errorf("entry 1 = %#v, want Gamma/50", entries[1])
	}
	if entries[1].Row != 2 {
		t.Errorf("Gamma Row = %d, want 2 (band order preserved)", entries[1].Row)
	}
	if len(defects) != 1 {
		t.Errorf("got %d defects, want 1 (the skipped row): %v", len(defects), defects)
	}
}

// TestResolveRowsCollisionKeepsHigherConfidence: when two rows reconcile to
// the same member, the higher-confidence row keeps the match and the other
// reverts to its raw decode with a defect recorded.
func TestResolveRowsCollisionKeepsHigherConfidence(t *testing.T) {
	roster := []string{"Heartorias"}
	rows := []parsedRow{
		{rawName: "Heartorjas", score: "90476"}, // 1 interior misread
		{rawName: "Heartorias", score: "12345"}, // exact decode - higher confidence
	}
	var defects []string
	entries := resolveRows(rows, roster, &defects)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[1].Name != "Heartorias" || !entries[1].Matched {
		t.Errorf("exact row lost the member: %#v", entries[1])
	}
	if entries[0].Matched || entries[0].Name != "Heartorjas" {
		t.Errorf("lower-confidence row should have reverted to raw: %#v", entries[0])
	}
	if len(defects) != 1 {
		t.Errorf("got %d defects, want 1 (the collision): %v", len(defects), defects)
	}
}

// TestResolveRowsNeverDedupes: two identical parsed rows stay two entries -
// deduplication is the cross-image merger's job, keyed on row identity.
func TestResolveRowsNeverDedupes(t *testing.T) {
	rows := []parsedRow{
		{rawName: "Twin", score: "100"},
		{rawName: "Twin", score: "100"},
	}
	var defects []string
	entries := resolveRows(rows, nil, &defects)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (no dedupe by name): %#v", len(entries), entries)
	}
}
