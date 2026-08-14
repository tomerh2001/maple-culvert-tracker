package commands

import "testing"

// mergedOwner decides the surviving owner when /rename merges two rows. The
// key rule: a tracked-but-unlinked '2' must beat an untracked '1'/'' so merged
// scores stay VISIBLE on the weekly board (the bug this locks: merging a '2'
// source into a '1' target used to keep '1' and hide the scores).
func TestMergedOwner(t *testing.T) {
	cases := []struct {
		tgt, src, want string
	}{
		{"999real", "888real", "999real"}, // target real id wins
		{"2", "888real", "888real"},       // source real id wins over '2'
		{"1", "888real", "888real"},       // source real id wins over '1'
		{"", "888real", "888real"},        // source real id wins over ''
		{"1", "2", "2"},                   // promote untracked target to visible
		{"2", "1", "2"},                   // source untracked, target already visible
		{"", "2", "2"},                    // promote empty target to visible
		{"2", "2", "2"},                   // both tracked-unlinked
		{"1", "1", "1"},                   // both untracked -> keep target
		{"", "", ""},                      // both empty -> keep target
		{"1", "", "1"},                    // neither tracked, keep target's value
	}
	for _, c := range cases {
		if got := mergedOwner(c.tgt, c.src); got != c.want {
			t.Errorf("mergedOwner(%q, %q) = %q, want %q", c.tgt, c.src, got, c.want)
		}
	}
}
