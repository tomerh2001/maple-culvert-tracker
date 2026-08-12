package commands

import (
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
)

// resetWeekDecision is the pure run-again-to-confirm rule for /reset-week,
// unit-tested exhaustively like overwriteDecision.
func TestResetWeekDecision(t *testing.T) {
	cases := []struct {
		name             string
		scoreCount       int
		pendingMatch     bool
		wantDelete       bool
		wantStorePending bool
	}{
		{"nothing recorded, no pending", 0, false, false, false},
		// N=0 with a stale pending key (scores deleted by other means since
		// the first invocation): still a friendly no-op, never a delete.
		{"nothing recorded, stale pending", 0, true, false, false},
		{"scores, first invocation", 3, false, false, true},
		{"scores, confirmed within window", 3, true, true, false},
		{"single score, first invocation", 1, false, false, true},
		{"single score, confirmed", 1, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deleteNow, storePending := resetWeekDecision(tc.scoreCount, tc.pendingMatch)
			if deleteNow != tc.wantDelete || storePending != tc.wantStorePending {
				t.Errorf("resetWeekDecision(%d, %v) = (delete %v, store %v), want (delete %v, store %v)",
					tc.scoreCount, tc.pendingMatch, deleteNow, storePending, tc.wantDelete, tc.wantStorePending)
			}
		})
	}
}

// The pending-key expiry semantics live in redis (TTL); what the command must
// guarantee is that a pending key for ANOTHER (tenant, invoker, week) never
// confirms this one - that scoping is carried by the stored key itself.
func TestPendingResetWeekKeyScoping(t *testing.T) {
	key := func(tenant, invoker, week string) string {
		return apiredis.PendingResetWeekKey(invoker, week).For(tenant).RedisKey()
	}
	base := key("guildA", "user1", "2026-08-12")
	if base == key("guildA", "user2", "2026-08-12") {
		t.Error("pending keys for different invokers collide")
	}
	if base == key("guildA", "user1", "2026-08-05") {
		t.Error("pending keys for different weeks collide")
	}
	if base == key("guildB", "user1", "2026-08-12") {
		t.Error("pending keys for different tenants collide")
	}
	if base != key("guildA", "user1", "2026-08-12") {
		t.Error("pending key is not deterministic for the same (tenant, invoker, week)")
	}
}
