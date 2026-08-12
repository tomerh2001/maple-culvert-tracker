package helpers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// FetchMembers lists one guild's members holding one of the tenant's
// configured guild roles (CONF_DISCORD_GUILD_ROLE_IDS of tenantID).
func FetchMembers(discordServerID, tenantID string, DiscordSession *discordgo.Session) ([]data.WebGuildMember, error) {
	result := []data.WebGuildMember{}

	if DiscordSession == nil {
		log.Println("Discord dead in FetchMembers, no session")
		return result, errors.New("broken discord connection")
	}

	// Get all members
	allMembers := []*discordgo.Member{}
	afterMember := ""
	for len(allMembers) == 0 || afterMember != "" {
		members, err := DiscordSession.GuildMembers(discordServerID, afterMember, 1000)
		if err != nil {
			log.Println("Failed to get members", err)
			return result, fmt.Errorf("getting guild members: %w", err)
		}
		allMembers = append(allMembers, members...)
		if len(members) == 1000 {
			afterMember = members[999].User.ID
		} else {
			afterMember = ""
		}
	}

	// Get members that are member
	roleIDs := strings.Split(apiredis.CONF_DISCORD_GUILD_ROLE_IDS.For(tenantID).GetWithDefault(apiredis.RedisDB, os.Getenv("DISCORD_GUILD_ROLE_ID")), ",")
	roleIDsMap := map[string]struct{}{}
	for _, v := range roleIDs {
		roleIDsMap[v] = struct{}{}
	}
	for _, m := range allMembers {
		for _, r := range m.Roles {
			if _, ok := roleIDsMap[r]; ok {
				wm := data.WebGuildMember{
					DiscordUsername:   m.User.Username,
					DiscordUserID:     m.User.ID,
					DiscordGlobalName: m.User.GlobalName,
					DiscordNickname:   m.Nick,
				}
				result = append(result, wm)
				break
			}
		}
	}
	// '1' means unlinked and not in guild anymore
	// '2' means unlinked and in guild
	result = append(result, data.WebGuildMember{
		DiscordUsername:   "NOT LINKED TO DISCORD YET",
		DiscordUserID:     "2",
		DiscordGlobalName: "NOT LINKED TO DISCORD YET",
		DiscordNickname:   "NOT LINKED TO DISCORD YET",
	})

	return result, nil
}

// FetchTenantMembers merges FetchMembers across every guild backing the
// tenant (all env guilds for the default tenant, the single guild otherwise),
// deduplicated by Discord user id.
func FetchTenantMembers(DiscordSession *discordgo.Session, tenantID string) ([]data.WebGuildMember, error) {
	seen := map[string]bool{}
	out := []data.WebGuildMember{}
	var lastErr error
	fetchedAny := false
	for _, guildID := range data.TenantGuildIDs(tenantID) {
		members, err := FetchMembers(guildID, tenantID, DiscordSession)
		if err != nil {
			lastErr = err
			continue
		}
		fetchedAny = true
		for _, m := range members {
			if !seen[m.DiscordUserID] {
				seen[m.DiscordUserID] = true
				out = append(out, m)
			}
		}
	}
	if !fetchedAny {
		return out, lastErr
	}
	return out, nil
}

// rosterSessionMu guards rosterSessionVal, the Discord session used for lazy
// member cache refreshes (set once the bot session is Ready).
var rosterSessionMu sync.RWMutex
var rosterSessionVal *discordgo.Session

// SetRosterSession stores the session used for lazy member cache refreshes.
func SetRosterSession(s *discordgo.Session) {
	rosterSessionMu.Lock()
	defer rosterSessionMu.Unlock()
	rosterSessionVal = s
}

func rosterSession() *discordgo.Session {
	rosterSessionMu.RLock()
	defer rosterSessionMu.RUnlock()
	return rosterSessionVal
}

// RefreshMemberCache fetches the tenant's guild members and stores them in
// the tenant's DATA_DISCORD_MEMBERS, recording the refresh time (RFC3339) in
// DATA_DISCORD_MEMBERS_UPDATED_AT. On failure DATA_DISCORD_MEMBERS_LAST_ERROR
// is set (and the previous cache is left untouched); it is cleared on success.
func RefreshMemberCache(s *discordgo.Session, tenantID string) error {
	recordFailure := func(err error) error {
		msg := err.Error()
		var restErr *discordgo.RESTError
		if errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusForbidden {
			msg = "403 Forbidden fetching guild members - the bot likely lacks the Server Members privileged intent (enable it in the Discord developer portal): " + msg
		}
		if serr := apiredis.DATA_DISCORD_MEMBERS_LAST_ERROR.For(tenantID).Set(apiredis.RedisDB, msg); serr != nil {
			log.Println("RefreshMemberCache: recording last error failed:", serr)
		}
		return err
	}

	members, err := FetchTenantMembers(s, tenantID)
	if err != nil {
		return recordFailure(err)
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return recordFailure(fmt.Errorf("marshalling members: %w", err))
	}
	if err := apiredis.DATA_DISCORD_MEMBERS.For(tenantID).Set(apiredis.RedisDB, string(raw)); err != nil {
		return recordFailure(fmt.Errorf("storing member cache: %w", err))
	}
	if err := apiredis.DATA_DISCORD_MEMBERS_UPDATED_AT.For(tenantID).Set(apiredis.RedisDB, time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Println("RefreshMemberCache: storing updated-at failed:", err)
	}
	if err := apiredis.DATA_DISCORD_MEMBERS_LAST_ERROR.For(tenantID).Set(apiredis.RedisDB, ""); err != nil {
		log.Println("RefreshMemberCache: clearing last error failed:", err)
	}
	log.Printf("RefreshMemberCache: cached %d members for tenant %s", len(members), tenantID)
	return nil
}

// memberCacheLazyMaxAge is how old the member cache may get before a
// GetActiveCharacters-dependent flow refreshes it synchronously.
const memberCacheLazyMaxAge = time.Hour

// maybeRefreshMemberCache refreshes the tenant's member cache synchronously
// when its UPDATED_AT marker is missing or older than memberCacheLazyMaxAge
// and a bot session is available. Failures are logged, never fatal: callers
// fall back to whatever cache state exists.
func maybeRefreshMemberCache(tenantID string) {
	s := rosterSession()
	if s == nil {
		return
	}
	if ts := apiredis.DATA_DISCORD_MEMBERS_UPDATED_AT.For(tenantID).GetWithDefault(apiredis.RedisDB, ""); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil && time.Since(t) < memberCacheLazyMaxAge {
			return
		}
	}
	if err := RefreshMemberCache(s, tenantID); err != nil {
		log.Println("maybeRefreshMemberCache: refresh failed:", err)
	}
}
