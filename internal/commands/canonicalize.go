package commands

import (
	"strings"

	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
)

// The game font renders lowercase 'l' and uppercase 'I' as identical bars, so
// an OCR decode (or a typed name) may have them swapped. Before auto-tracking
// a NEW name we canonicalize it against the official rankings: an exact hit
// adopts the rankings capitalization; otherwise up to maxBarVariants l/I
// variants are tried and the first rankings hit wins; if nothing resolves the
// decode is kept and the receipt notes "spelling unverified".

const maxBarVariants = 8

// barVariants generates l/I reinterpretations of name, most plausible first:
// at each ambiguous bar the preferred reading comes first - 'l' when the
// preceding character (of the variant built so far) is lowercase, 'I'
// otherwise - so "SteIIaMaris" yields "StellaMaris" before any all-I form.
// The list is capped at maxBarVariants entries and includes the original
// combination (whenever it survives the cap).
func barVariants(name string) []string {
	positions := []int{}
	runes := []rune(name)
	for i, r := range runes {
		if r == 'l' || r == 'I' {
			positions = append(positions, i)
		}
	}
	if len(positions) == 0 {
		return nil
	}
	out := []string{}
	var walk func(idx int)
	walk = func(idx int) {
		if len(out) >= maxBarVariants {
			return
		}
		if idx == len(positions) {
			out = append(out, string(runes))
			return
		}
		pos := positions[idx]
		preferred := 'I'
		if pos > 0 {
			prev := runes[pos-1]
			if prev >= 'a' && prev <= 'z' {
				preferred = 'l'
			}
		}
		other := 'I'
		if preferred == 'I' {
			other = 'l'
		}
		for _, c := range []rune{preferred, other} {
			runes[pos] = c
			walk(idx + 1)
		}
		runes[pos] = []rune(name)[pos] // restore for sibling branches
	}
	walk(0)
	return out
}

// canonicalizeName resolves a to-be-tracked name through lookup (which
// returns the rankings capitalization and whether the name exists). It
// returns the canonical spelling and whether the rankings verified it.
func canonicalizeName(name string, lookup func(string) (string, bool)) (canonical string, verified bool) {
	if c, ok := lookup(name); ok {
		return c, true
	}
	for _, v := range barVariants(name) {
		if v == name {
			continue // already tried above
		}
		if c, ok := lookup(v); ok {
			return c, true
		}
	}
	return name, false
}

// rankingsLookup builds a per-submission cached lookup against the official
// MapleStory rankings. Lookup failures (API down, not found) are non-fatal
// and simply report ok=false.
func rankingsLookup(tenantID string) func(string) (string, bool) {
	region := apiredis.OPTIONAL_CONF_MAPLE_REGION.For(tenantID).GetWithDefault(apiredis.RedisDB, "na")
	type cached struct {
		name string
		ok   bool
	}
	cache := map[string]cached{}
	return func(name string) (string, bool) {
		key := strings.ToLower(name)
		if c, hit := cache[key]; hit {
			return c.name, c.ok
		}
		charData, err := apihelpers.FetchCharacterData(name, region)
		if err != nil || charData == nil {
			cache[key] = cached{}
			return "", false
		}
		cache[key] = cached{charData.CharacterName, true}
		return charData.CharacterName, true
	}
}
