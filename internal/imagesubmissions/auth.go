package imagesubmissions

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Explicit integration grant, loaded by the server. No tenant IDs are accepted
// from requests. TokenSHA256 is a digest, never the raw bearer credential.
type Principal struct {
	ID             string    `json:"id"`
	TokenSHA256    string    `json:"token_sha256"`
	CollectorID    string    `json:"collector_id"`
	DestinationID  string    `json:"destination_id"`
	MapleGuild     string    `json:"maple_guild"`
	DiscordGuildID string    `json:"discord_guild_id"`
	TenantID       string    `json:"tenant_id"`
	ActorID        string    `json:"actor_id"`
	ExpiresAt      time.Time `json:"expires_at_utc"`
}

func ParsePrincipals(raw string) ([]Principal, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ps []Principal
	if len(raw) > 64<<10 || strictJSON([]byte(raw), &ps) != nil || len(ps) > 16 {
		return nil, errors.New("invalid image integration configuration")
	}
	ids := map[string]bool{}
	tokens := map[string]bool{}
	for _, p := range ps {
		if !safeID.MatchString(p.ID) || !safeID.MatchString(p.CollectorID) || !safeID.MatchString(p.DestinationID) || !hexSHA.MatchString(p.TokenSHA256) || p.MapleGuild == "" || len(p.MapleGuild) > 64 || p.DiscordGuildID == "" || p.TenantID == "" || p.ActorID == "" || p.ExpiresAt.IsZero() || ids[p.ID] || tokens[p.TokenSHA256] {
			return nil, errors.New("invalid image integration configuration")
		}
		ids[p.ID] = true
		tokens[p.TokenSHA256] = true
	}
	return ps, nil
}

func authenticate(r *http.Request, ps []Principal, now time.Time) (Principal, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(auth) < 39 || len(auth) > 263 {
		return Principal{}, false
	}
	raw := strings.TrimPrefix(auth, "Bearer ")
	if strings.ContainsAny(raw, " \t\r\n") {
		return Principal{}, false
	}
	sum := sha256.Sum256([]byte(raw))
	var matched Principal
	ok := false
	for _, p := range ps {
		want, _ := hex.DecodeString(p.TokenSHA256)
		if subtle.ConstantTimeCompare(sum[:], want) == 1 && now.Before(p.ExpiresAt) {
			matched = p
			ok = true
		}
	}
	return matched, ok
}
