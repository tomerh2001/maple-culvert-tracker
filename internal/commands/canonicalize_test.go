package commands

import (
	"slices"
	"strings"
	"testing"
)

// TestBarVariantsStellaMaris pins the ordering requirement: the variants of
// "SteIIaMaris" must include "StellaMaris", ordered before every all-I form
// (bars after lowercase are tried as 'l' first).
func TestBarVariantsStellaMaris(t *testing.T) {
	got := barVariants("SteIIaMaris")
	if len(got) == 0 {
		t.Fatal("no variants generated")
	}
	if len(got) > maxBarVariants {
		t.Fatalf("generated %d variants, cap is %d", len(got), maxBarVariants)
	}
	stella := slices.Index(got, "StellaMaris")
	if stella < 0 {
		t.Fatalf("StellaMaris not among variants: %v", got)
	}
	for idx, v := range got {
		if strings.Contains(v, "ll") || !strings.Contains(v, "I") {
			continue // not an all-I form
		}
		if v == "SteIIaMaris" && idx < stella {
			t.Errorf("all-I form %q at %d ordered before StellaMaris at %d: %v", v, idx, stella, got)
		}
	}
	// The most plausible reading comes FIRST: both bars follow lowercase
	// context, so the l-l form leads the list.
	if got[0] != "StellaMaris" {
		t.Errorf("first variant = %q, want StellaMaris (preferred reading first): %v", got[0], got)
	}
}

func TestBarVariantsNoBars(t *testing.T) {
	if got := barVariants("Ymir"); got != nil {
		t.Errorf("barVariants of a bar-free name = %v, want nil", got)
	}
}

func TestBarVariantsCap(t *testing.T) {
	// 5 bars would be 32 combinations; the generator must stop at the cap.
	got := barVariants("llIll")
	if len(got) > maxBarVariants {
		t.Errorf("generated %d variants, cap is %d", len(got), maxBarVariants)
	}
}

// TestCanonicalizeName drives the decision logic with a faked rankings lookup.
func TestCanonicalizeName(t *testing.T) {
	rankings := map[string]string{ // lower(name) -> canonical capitalization
		"stellamaris": "StellaMaris",
		"htomer":      "HTomer",
	}
	calls := 0
	lookup := func(name string) (string, bool) {
		calls++
		c, ok := rankings[strings.ToLower(name)]
		return c, ok
	}

	t.Run("exact decode found adopts rankings capitalization", func(t *testing.T) {
		got, verified := canonicalizeName("htomer", lookup)
		if got != "HTomer" || !verified {
			t.Errorf("canonicalizeName(htomer) = (%q, %v), want (HTomer, true)", got, verified)
		}
	})

	t.Run("bar mixup resolves through variants", func(t *testing.T) {
		got, verified := canonicalizeName("SteIIaMaris", lookup)
		if got != "StellaMaris" || !verified {
			t.Errorf("canonicalizeName(SteIIaMaris) = (%q, %v), want (StellaMaris, true)", got, verified)
		}
	})

	t.Run("nothing resolves keeps the decode unverified", func(t *testing.T) {
		got, verified := canonicalizeName("TotalIyUnknown", lookup)
		if got != "TotalIyUnknown" || verified {
			t.Errorf("canonicalizeName(TotalIyUnknown) = (%q, %v), want (TotalIyUnknown, false)", got, verified)
		}
	})

	t.Run("bar-free unknown name does no variant lookups", func(t *testing.T) {
		before := calls
		_, verified := canonicalizeName("Zebra", lookup)
		if verified {
			t.Error("Zebra should not verify")
		}
		if calls-before != 1 {
			t.Errorf("bar-free miss made %d lookups, want exactly 1", calls-before)
		}
	})
}

// TestCanonicalizeSubmitted covers the pre-plan rewrite of a submission map.
func TestCanonicalizeSubmitted(t *testing.T) {
	tracked := []trackedScore{
		{ID: 1, Name: "StellaMaris"},
		{ID: 2, Name: "Bob"},
	}
	fake := func(rankings map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			c, ok := rankings[strings.ToLower(name)]
			return c, ok
		}
	}

	t.Run("bar mixup folds onto the tracked character", func(t *testing.T) {
		submitted := map[string]int{"SteIIaMaris": 777, "Bob": 100}
		unverified := canonicalizeSubmittedWith(submitted, tracked, fake(map[string]string{"stellamaris": "StellaMaris"}))
		if len(unverified) != 0 {
			t.Errorf("unverified = %v, want none", unverified)
		}
		if submitted["StellaMaris"] != 777 {
			t.Errorf("score not folded onto tracked name: %v", submitted)
		}
		if _, stale := submitted["SteIIaMaris"]; stale {
			t.Errorf("decode key not removed: %v", submitted)
		}
	})

	t.Run("verified new name adopts rankings spelling", func(t *testing.T) {
		submitted := map[string]int{"ymir": 555}
		unverified := canonicalizeSubmittedWith(submitted, tracked, fake(map[string]string{"ymir": "Ymir"}))
		if len(unverified) != 0 || submitted["Ymir"] != 555 {
			t.Errorf("unverified=%v submitted=%v, want Ymir adopted", unverified, submitted)
		}
	})

	t.Run("unresolved name is kept and reported unverified", func(t *testing.T) {
		submitted := map[string]int{"Mystery": 1}
		unverified := canonicalizeSubmittedWith(submitted, tracked, fake(nil))
		if len(unverified) != 1 || unverified[0] != "Mystery" || submitted["Mystery"] != 1 {
			t.Errorf("unverified=%v submitted=%v, want Mystery kept + reported", unverified, submitted)
		}
	})

	t.Run("tracked names are never looked up", func(t *testing.T) {
		submitted := map[string]int{"Bob": 9}
		called := false
		unverified := canonicalizeSubmittedWith(submitted, tracked, func(string) (string, bool) {
			called = true
			return "", false
		})
		if called {
			t.Error("lookup called for an already-tracked name")
		}
		if len(unverified) != 0 || submitted["Bob"] != 9 {
			t.Errorf("tracked name mutated: unverified=%v submitted=%v", unverified, submitted)
		}
	})
}
