package helpers

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestRealFull6NunezName pins a name-column over-segmentation regression.
// provided/real-full-6.png is a real 1045x1007 anchor-smooth capture (scale
// ~2.01) of the same table as real-full-4 at a different capture size. Its
// row-1 name "Nunez" used to decode as "Nuntfz": the trailing "ez" fell to the
// crossbar-ligature composite "tfz", which - correlating only at the ~0.62
// acceptance floor - beat the correct e+z because the composite's /nRunes cost
// discount let a floor-quality 3-rune template out-cheap two well-formed
// glyphs by one boundary. The bloated 6-glyph smooth decode then also happened
// to out-length the binary engine's (wrong) "Iunez", so the misread survived
// engine arbitration.
//
// The parse is roster-independent, so the raw decode must be exactly "Nunez"
// under an EMPTY roster (no reconciliation to lean on), and every one of the
// 17 scores must be present in descending order - a dropped or transposed
// digit here is a SILENT wrong score in a 200+ player server. real-full-4
// (crisper, same table) must keep reading "Nunez" too.
func TestRealFull6NunezName(t *testing.T) {
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../provided/real-full-6.json")
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

	for _, tc := range []string{"real-full-6", "real-full-4"} {
		tc := tc
		t.Run(tc, func(t *testing.T) {
			data, err := os.ReadFile("../../../provided/" + tc + ".png")
			if err != nil {
				t.Skipf("no %s.png", tc)
			}
			// EMPTY roster: the raw decode gets no reconciliation help.
			res, err := ParseParticipation(data, nil, font)
			if err != nil {
				t.Fatal(err)
			}
			rows := res.Rows
			if len(rows) != 17 {
				got := make([]string, 0, len(rows))
				for _, e := range rows {
					got = append(got, e.RawName+":"+strconv.Itoa(e.Score))
				}
				t.Fatalf("parsed %d rows, want EXACTLY 17\ngot: %v", len(rows), got)
			}
			// Row 1's name must decode EXACTLY "Nunez" - not "Nuntfz", not the
			// binary engine's "Iunez".
			if rows[1].RawName != "Nunez" {
				t.Errorf("row 1 RawName = %q, want EXACT %q", rows[1].RawName, "Nunez")
			}
			// Every score present, in descending ground-truth order.
			for i, k := range keys {
				if want := expected[k]; rows[i].Score != want {
					t.Errorf("row %d (%s): score %d, want EXACTLY %d", i, k, rows[i].Score, want)
				}
			}
			// Sanity: the name column never leaks the ligature-family "tfz"/"ttz"
			// tail that the regression produced.
			for i, e := range rows {
				if strings.Contains(e.RawName, "tfz") || strings.HasSuffix(e.RawName, "ttz") {
					t.Errorf("row %d: raw name %q carries an over-segmented crossbar tail", i, e.RawName)
				}
			}
		})
	}
}
