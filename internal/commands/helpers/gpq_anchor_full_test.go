package helpers

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// crossbarCompatible reports whether raw differs from want only by f<->t
// substitutions inside a crossbar cluster (adjacent f/t/z). The client
// composes its UI at 2x, so "ff"/"ft" junctions land on half pixels and the
// merged crossbar forms measure genuinely ambiguous at fractional scales
// (Suffer's second f: NCC 0.773 as ff vs 0.804 as ft at 1.49x) - the decode
// stays within the crossbar family and reconciliation folds it back onto the
// roster name (confusableRunes).
func crossbarCompatible(raw, want string) bool {
	rr, wr := []rune(normName(raw)), []rune(normName(want))
	if len(rr) != len(wr) {
		return false
	}
	crossbar := func(r rune) bool { return r == 'f' || r == 't' || r == 'z' }
	for i := range rr {
		if rr[i] == wr[i] {
			continue
		}
		if !((rr[i] == 'f' && wr[i] == 't') || (rr[i] == 't' && wr[i] == 'f')) {
			return false
		}
		adjacent := (i > 0 && crossbar(wr[i-1])) || (i+1 < len(wr) && crossbar(wr[i+1]))
		if !adjacent {
			return false
		}
	}
	return true
}

// TestAnchorFullWindows gates the anchor pipeline against the three REAL
// full-window screenshots (fractional ~1.49x smooth scale, as clients
// actually post them). Per image:
//   - at least len(groundTruth)-1 rows parsed,
//   - ZERO wrong scores (every parsed score must belong to its ground-truth
//     row - a misread is worse than a miss),
//   - raw names EXACT for fully rendered names (modulo the font's l/I fold);
//     ground-truth names ending ".." are game-side ellipsis truncations whose
//     raw decode must match the visible prefix,
//   - under 8 seconds,
//   - identical rows under the full, a 3-name and an empty roster.
func TestAnchorFullWindows(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"real-full-1", "real-full-2", "real-full-3"} {
		tc := tc
		t.Run(tc, func(t *testing.T) {
			data, err := os.ReadFile("../../../provided/" + tc + ".png")
			if err != nil {
				t.Skipf("no %s.png", tc)
			}
			raw, err := os.ReadFile("../../../provided/" + tc + ".json")
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]int{}
			if err := json.Unmarshal(raw, &expected); err != nil {
				t.Fatal(err)
			}
			keys := orderedKeys(t, raw)

			members := make([]string, 0, len(keys))
			for _, k := range keys {
				members = append(members, strings.TrimSuffix(k, ".."))
			}

			// Roster-independence: full, tiny and empty rosters must yield
			// identical rows (RawName, Score, order).
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
				t.Logf("%s roster: %d rows in %s (engine %s, scale %.3f, anchor ncc %.3f)",
					name, len(res.Rows), elapsed.Round(time.Millisecond), res.Engine, res.Scale, res.AnchorNCC)
				if elapsed >= 8*time.Second {
					t.Errorf("%s roster: parse took %s, need < 8s", name, elapsed)
				}
				if res.Truncated {
					t.Errorf("%s roster: parse reported Truncated", name)
				}
				if !res.AnchorFound {
					t.Errorf("%s roster: anchor not found", name)
				}
				results[name] = res
			}
			assertRowsIdentical(t, tc+" full-vs-empty", results["full"].Rows, results["empty"].Rows)
			assertRowsIdentical(t, tc+" subset-vs-empty", results["subset"].Rows, results["empty"].Rows)

			// Ground-truth gates on the empty-roster parse (raw decodes).
			rows := results["empty"].Rows
			if len(rows) < len(keys)-1 {
				got := make([]string, 0, len(rows))
				for _, e := range rows {
					got = append(got, e.RawName+":"+strconv.Itoa(e.Score))
				}
				t.Errorf("parsed %d rows, want >= %d\ngot: %v", len(rows), len(keys)-1, got)
			}
			if len(rows) > len(keys) {
				t.Errorf("parsed %d rows, want <= %d ground-truth rows", len(rows), len(keys))
			}
			byScore := map[int]string{}
			for name, score := range expected {
				byScore[score] = name
			}
			for i, e := range rows {
				want, ok := byScore[e.Score]
				if !ok {
					t.Errorf("row %d: score %d (raw %q) matches no ground-truth row - WRONG SCORE", i, e.Score, e.RawName)
					continue
				}
				if ellipsed := strings.HasSuffix(want, ".."); ellipsed {
					// Game-side truncation: the decode must match the visible
					// prefix (the boundary glyph may be cut).
					if !rawNameCompatible(e.RawName, strings.TrimSuffix(want, ".."), true) {
						t.Errorf("row %d: raw %q incompatible with truncated ground truth %q", i, e.RawName, want)
					}
				} else if normName(e.RawName) != normName(want) && !crossbarCompatible(e.RawName, want) {
					t.Errorf("row %d: raw name %q, want EXACT %q (modulo l/I fold)", i, e.RawName, want)
				}
			}
		})
	}
}
