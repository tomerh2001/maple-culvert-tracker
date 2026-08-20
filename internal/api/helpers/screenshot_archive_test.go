package helpers

import (
	"testing"
)

// pageNames builds a page from a name range of the synthetic roster (r0..rN),
// mimicking how the in-game window slices the member list into pages.
func pageNames(from, to int) []string {
	names := []string{}
	for i := from; i < to; i++ {
		names = append(names, "member"+itoa(i))
	}
	return names
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func planEquals(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The user's exact scenario: day 1 submits pages 1+2, day 3 resubmits pages
// 1+2+3. The fresh page 1 and 2 replace their stored versions; page 3 is new.
func TestPlanArchiveMergeResubmitReplacesAndAppends(t *testing.T) {
	existing := [][]string{pageNames(0, 35), pageNames(35, 70)}
	incoming := [][]string{pageNames(0, 35), pageNames(35, 70), pageNames(70, 90)}

	got := PlanArchiveMerge(existing, incoming)
	if want := []int{0, 1, -1}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// First submission of the week: nothing stored, everything is new.
func TestPlanArchiveMergeAllNew(t *testing.T) {
	got := PlanArchiveMerge(nil, [][]string{pageNames(0, 35), pageNames(35, 70)})
	if want := []int{-1, -1}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// The participation window orders members by score, so rows migrate between
// pages during the week. A page still replaces the stored page it mostly
// overlaps, and each stored page is claimed at most once.
func TestPlanArchiveMergeToleratesRowMigration(t *testing.T) {
	existing := [][]string{pageNames(0, 35), pageNames(35, 70)}
	// A third of each page shifted across the boundary since the last shot.
	incoming := [][]string{pageNames(0, 25), pageNames(25, 70)}

	got := PlanArchiveMerge(existing, incoming)
	if want := []int{0, 1}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// A page of entirely unseen names never replaces anything, no matter how the
// sizes compare.
func TestPlanArchiveMergeUnrelatedPageIsNew(t *testing.T) {
	existing := [][]string{pageNames(0, 35)}
	got := PlanArchiveMerge(existing, [][]string{pageNames(100, 135)})
	if want := []int{-1}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// A small overlap-only shot (a boundary re-take) must not steal the full
// page's slot when the true full re-shot is also in the submission: the
// higher-ratio match wins, the leftover becomes a new page.
func TestPlanArchiveMergeBestOverlapWins(t *testing.T) {
	existing := [][]string{pageNames(0, 35)}
	incoming := [][]string{
		pageNames(30, 41), // 5 of its 11 names overlap the stored page (0.45)
		pageNames(0, 35),  // the actual re-shot (1.0)
	}
	got := PlanArchiveMerge(existing, incoming)
	if want := []int{-1, 0}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// Matching is case-insensitive: OCR and rankings canonicalization may case
// the same member differently across submissions.
func TestPlanArchiveMergeCaseInsensitive(t *testing.T) {
	existing := [][]string{{"HTomer", "Senpapi", "Pichi"}}
	got := PlanArchiveMerge(existing, [][]string{{"htomer", "SENPAPI", "pichi"}})
	if want := []int{0}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// Pages with no parsed names can only ever be new - never silently replace a
// real page - and never panic the planner.
func TestPlanArchiveMergeEmptyNames(t *testing.T) {
	existing := [][]string{{}, pageNames(0, 10)}
	got := PlanArchiveMerge(existing, [][]string{{}, pageNames(0, 10)})
	if want := []int{-1, 1}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// Exactly at the threshold (half the smaller page shared) still replaces:
// ">= 0.5" is the contract.
func TestPlanArchiveMergeThresholdInclusive(t *testing.T) {
	existing := [][]string{pageNames(0, 10)}
	incoming := [][]string{pageNames(5, 15)} // 5 shared / min(10,10) = 0.5
	got := PlanArchiveMerge(existing, incoming)
	if want := []int{0}; !planEquals(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// Names round-trip through the storage encoding unchanged (lowercased,
// blanks dropped).
func TestArchiveNamesEncodingRoundTrip(t *testing.T) {
	in := []string{"HTomer", "  Senpapi ", "", "Pichi"}
	got := decodeArchiveNames(encodeArchiveNames(in))
	want := []string{"htomer", "senpapi", "pichi"}
	if len(got) != len(want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round trip = %v, want %v", got, want)
		}
	}
	if decodeArchiveNames("") != nil {
		t.Fatal("empty encoding must decode to nil")
	}
}
