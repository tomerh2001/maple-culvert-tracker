package helpers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// Check statuses. Anything that is not CheckPass is worth an admin's eyes.
const (
	CheckPass = "pass"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// CheckResult is one health check's outcome. Fix, when set, tells the admin
// what to do about a warn/fail.
type CheckResult struct {
	Name   string
	Status string // CheckPass | CheckWarn | CheckFail
	Detail string
	Fix    string
}

// RunHealthChecks probes everything the bot needs to work: infrastructure
// (postgres, redis), Discord access (guilds, the Server Members intent, the
// weekly channel's permissions) and configuration (roster roles, submitters,
// command registration). It never mutates state and is safe to call from any
// goroutine holding a Ready session.
func RunHealthChecks(s *discordgo.Session) []CheckResult {
	out := []CheckResult{
		checkPostgres(),
		checkRedis(),
		checkGuildAccess(s),
		checkMembersIntent(s),
		checkMemberCache(),
		checkGuildRoleConfig(s),
	}
	out = append(out, checkWeeklyChannel(s)...)
	out = append(out, checkSubmitterConfig(), checkCommandRegistration())
	return out
}

func checkPostgres() CheckResult {
	c := CheckResult{Name: "Postgres"}
	if db.DB == nil {
		c.Status, c.Detail = CheckFail, "database handle not initialized"
		c.Fix = "Check the POSTGRES_* environment variables and restart the bot"
		return c
	}
	if err := db.DB.Ping(); err != nil {
		c.Status, c.Detail = CheckFail, "ping failed: "+err.Error()
		c.Fix = "Check the postgres container/network and the POSTGRES_* environment variables"
		return c
	}
	c.Status, c.Detail = CheckPass, "connected"
	return c
}

func checkRedis() CheckResult {
	c := CheckResult{Name: "Redis"}
	if apiredis.RedisDB == nil || *apiredis.RedisDB == nil {
		c.Status, c.Detail = CheckFail, "redis client not initialized"
		c.Fix = "Check the REDIS_HOST/REDIS_PORT environment variables and restart the bot"
		return c
	}
	rdb := *apiredis.RedisDB
	if err := rdb.Do(context.Background(), rdb.B().Ping().Build()).Error(); err != nil {
		c.Status, c.Detail = CheckFail, "ping failed: "+err.Error()
		c.Fix = "Check the redis container/network and the REDIS_HOST/REDIS_PORT environment variables"
		return c
	}
	c.Status, c.Detail = CheckPass, "connected"
	return c
}

// checkGuildAccess verifies the bot is a member of every served guild.
func checkGuildAccess(s *discordgo.Session) CheckResult {
	c := CheckResult{Name: "Guild access"}
	guildIDs := data.AllGuildIDs()
	if len(guildIDs) == 0 {
		c.Status, c.Detail = CheckFail, "no guild ids configured"
		c.Fix = "Set DISCORD_GUILD_ID (and optionally DISCORD_EXTRA_GUILD_IDS)"
		return c
	}
	missing := []string{}
	for _, gid := range guildIDs {
		if g, err := s.State.Guild(gid); err == nil && g != nil {
			continue
		}
		if _, err := s.Guild(gid); err != nil {
			missing = append(missing, gid)
		}
	}
	if len(missing) > 0 {
		c.Status = CheckFail
		c.Detail = "not a member of guild(s): " + strings.Join(missing, ", ")
		c.Fix = "Invite the bot to those servers, or remove the ids from DISCORD_GUILD_ID/DISCORD_EXTRA_GUILD_IDS"
		return c
	}
	c.Status, c.Detail = CheckPass, fmt.Sprintf("member of all %d served guild(s)", len(guildIDs))
	return c
}

// checkMembersIntent verifies the privileged Server Members intent by asking
// for a single member of the primary guild.
func checkMembersIntent(s *discordgo.Session) CheckResult {
	c := CheckResult{Name: "Server Members intent"}
	guildIDs := data.AllGuildIDs()
	if len(guildIDs) == 0 {
		c.Status, c.Detail = CheckWarn, "no guild ids configured, cannot verify"
		return c
	}
	if _, err := s.GuildMembers(guildIDs[0], "", 1); err != nil {
		var restErr *discordgo.RESTError
		if (errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusForbidden) ||
			strings.Contains(err.Error(), "Forbidden") {
			c.Status, c.Detail = CheckFail, "listing guild members is Forbidden (403)"
			c.Fix = "Enable the Server Members intent in the Discord developer portal (Bot tab)"
			return c
		}
		c.Status, c.Detail = CheckWarn, "listing guild members failed: "+err.Error()
		return c
	}
	c.Status, c.Detail = CheckPass, "member listing works"
	return c
}

// checkMemberCache inspects the roster member cache's age and last error.
func checkMemberCache() CheckResult {
	c := CheckResult{Name: "Member cache"}
	lastErr := apiredis.DATA_DISCORD_MEMBERS_LAST_ERROR.GetWithDefault(apiredis.RedisDB, "")
	if lastErr != "" {
		c.Status, c.Detail = CheckWarn, "last refresh failed: "+lastErr
		c.Fix = "Check the Server Members intent and the server logs; the cache refreshes on boot and on demand"
		return c
	}
	ts := apiredis.DATA_DISCORD_MEMBERS_UPDATED_AT.GetWithDefault(apiredis.RedisDB, "")
	if ts == "" {
		c.Status, c.Detail = CheckWarn, "never refreshed (no timestamp recorded)"
		c.Fix = "Wait for the boot refresh to finish, or check the server logs"
		return c
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		c.Status, c.Detail = CheckWarn, "unparseable refresh timestamp: "+ts
		return c
	}
	age := time.Since(t).Round(time.Minute)
	if age > rosterStaleAfter {
		c.Status, c.Detail = CheckWarn, fmt.Sprintf("stale: last refreshed %s ago", age)
		c.Fix = "Check the Server Members intent and the server logs; roster role filtering may be inaccurate"
		return c
	}
	c.Status, c.Detail = CheckPass, fmt.Sprintf("refreshed %s ago", age)
	return c
}

// checkGuildRoleConfig verifies every configured roster role id still exists
// in one of the served guilds. An unset config is a valid state (Wave 3:
// roster = all tracked characters).
func checkGuildRoleConfig(s *discordgo.Session) CheckResult {
	c := CheckResult{Name: "Roster roles"}
	raw := strings.TrimSpace(apiredis.CONF_DISCORD_GUILD_ROLE_IDS.GetWithDefault(apiredis.RedisDB, ""))
	if raw == "" {
		c.Status, c.Detail = CheckPass, "not set - roster = all tracked characters"
		return c
	}
	known := map[string]bool{}
	lookupFailed := false
	for _, gid := range data.AllGuildIDs() {
		roles, err := s.GuildRoles(gid)
		if err != nil {
			lookupFailed = true
			continue
		}
		for _, role := range roles {
			known[role.ID] = true
		}
	}
	missing := []string{}
	total := 0
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		total++
		if !known[id] {
			missing = append(missing, id)
		}
	}
	switch {
	case len(missing) > 0 && lookupFailed:
		c.Status, c.Detail = CheckWarn, "could not verify role id(s) "+strings.Join(missing, ", ")+" (role listing failed for at least one guild)"
		return c
	case len(missing) > 0:
		c.Status = CheckFail
		c.Detail = "configured role id(s) not found in any served guild: " + strings.Join(missing, ", ")
		c.Fix = "Fix or clear them with `/config` (Discord Guild Role IDs) - a deleted role silently narrows the roster"
		return c
	}
	c.Status, c.Detail = CheckPass, fmt.Sprintf("all %d configured role id(s) exist", total)
	return c
}

// weeklyRequiredPermissions is everything AnnounceSubmission needs in the
// weekly channel, with human-readable names for the fix message.
var weeklyRequiredPermissions = []struct {
	Bit  int64
	Name string
}{
	{discordgo.PermissionViewChannel, "View Channel"},
	{discordgo.PermissionSendMessages, "Send Messages"},
	{discordgo.PermissionAttachFiles, "Attach Files"},
	{discordgo.PermissionCreatePublicThreads, "Create Public Threads"},
	{discordgo.PermissionReadMessageHistory, "Read Message History"},
}

// checkWeeklyChannel verifies the weekly announcement channel's permissions
// and this week's announcement thread. Returns 1-2 results depending on what
// is applicable.
func checkWeeklyChannel(s *discordgo.Session) []CheckResult {
	c := CheckResult{Name: "Weekly channel"}
	channelID := strings.TrimSpace(apiredis.CONF_DISCORD_WEEKLY_CHANNEL_ID.GetWithDefault(apiredis.RedisDB, ""))
	if channelID == "" {
		c.Status, c.Detail = CheckWarn, "not set - weekly announcements are disabled"
		c.Fix = "`/config setting:Discord Weekly Announcement Channel ID channel:#your-channel`"
		return []CheckResult{c}
	}
	botID := ""
	if s.State != nil && s.State.User != nil {
		botID = s.State.User.ID
	}
	perms, err := s.UserChannelPermissions(botID, channelID)
	if err != nil {
		c.Status, c.Detail = CheckFail, "cannot resolve my permissions in <#"+channelID+">: "+err.Error()
		c.Fix = "Make sure the channel still exists and I can see it, or re-set it with `/config`"
		return []CheckResult{c}
	}
	missing := []string{}
	for _, p := range weeklyRequiredPermissions {
		if perms&p.Bit == 0 {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		c.Status = CheckFail
		c.Detail = "missing permission(s) in <#" + channelID + ">: " + strings.Join(missing, ", ")
		c.Fix = "Grant the bot those permissions in the channel settings"
		return []CheckResult{c}
	}
	c.Status, c.Detail = CheckPass, "all required permissions in <#"+channelID+">"

	out := []CheckResult{c}
	if tc, ok := checkWeeklyThread(); ok {
		out = append(out, tc)
	}
	return out
}

// checkWeeklyThread warns when the current week's announcement message exists
// but its notes thread was never created. Returns ok=false when there is
// nothing to report (no announcement yet, or DB unavailable - the postgres
// check covers that).
func checkWeeklyThread() (CheckResult, bool) {
	c := CheckResult{Name: "Weekly thread"}
	if db.DB == nil {
		return c, false
	}
	week := CurrentCulvertWeek(time.Now()).Format(time.DateOnly)
	var threadID string
	err := db.DB.QueryRow(
		`SELECT thread_id FROM weekly_announcements WHERE culvert_date = $1`, week).Scan(&threadID)
	switch {
	case err == sql.ErrNoRows:
		return c, false // no announcement this week yet - nothing to check
	case err != nil:
		return c, false // postgres check already covers DB trouble
	case threadID == "":
		c.Status = CheckWarn
		c.Detail = "this week's announcement message exists but its notes thread is missing"
		c.Fix = "Submit scores again to self-heal it (needs Create Public Threads in the weekly channel)"
		return c, true
	}
	c.Status, c.Detail = CheckPass, "this week's announcement thread exists"
	return c, true
}

// checkSubmitterConfig reports who can submit scores. Admins-only submission
// is the intended DEFAULT, so an empty submitter config is a PASS (it must
// never show up in the boot report, which only posts warns/fails).
func checkSubmitterConfig() CheckResult {
	c := CheckResult{Name: "Submitters"}
	roles := strings.TrimSpace(apiredis.CONF_DISCORD_SUBMIT_ROLE_IDS.GetWithDefault(apiredis.RedisDB, ""))
	users := strings.TrimSpace(apiredis.CONF_DISCORD_SUBMIT_USER_IDS.GetWithDefault(apiredis.RedisDB, ""))
	if roles == "" && users == "" {
		c.Status, c.Detail = CheckPass, "only admins can submit (default); optionally add submitter roles via /config"
		return c
	}
	c.Status, c.Detail = CheckPass, "submitter roles/users configured"
	return c
}

// checkCommandRegistration surfaces the last UpdateCommands outcome.
func checkCommandRegistration() CheckResult {
	c := CheckResult{Name: "Command registration"}
	status := apiredis.DATA_COMMAND_REGISTRATION_STATUS.GetWithDefault(apiredis.RedisDB, "")
	switch status {
	case "ok":
		c.Status, c.Detail = CheckPass, "last registration succeeded"
	case "":
		c.Status, c.Detail = CheckWarn, "no registration outcome recorded yet (bot may still be starting)"
	default:
		c.Status, c.Detail = CheckFail, "last registration failed: "+status
		c.Fix = "Check the bot token and its `applications.commands` scope; commands re-register on every boot"
	}
	return c
}

// FormatCheckResults renders check results as a compact Discord-markdown list
// with pass/warn/fail markers and Fix lines for actionable items.
func FormatCheckResults(results []CheckResult) string {
	var b strings.Builder
	for _, c := range results {
		marker := ":white_check_mark:"
		switch c.Status {
		case CheckWarn:
			marker = ":warning:"
		case CheckFail:
			marker = ":x:"
		}
		b.WriteString("\n" + marker + " **" + c.Name + "** - " + c.Detail)
		if c.Fix != "" && c.Status != CheckPass {
			b.WriteString("\n-# Fix: " + c.Fix)
		}
	}
	return b.String()
}

// FilterProblems returns the non-pass results, fails before warns (original
// order within each group).
func FilterProblems(results []CheckResult) []CheckResult {
	out := []CheckResult{}
	for _, c := range results {
		if c.Status == CheckFail {
			out = append(out, c)
		}
	}
	for _, c := range results {
		if c.Status == CheckWarn {
			out = append(out, c)
		}
	}
	return out
}

// ReportBootHealth runs the health checks after startup, logs every problem,
// and - only when there ARE problems - posts a summary to the admin channel
// (CONF_DISCORD_ADMIN_CHANNEL_ID). A healthy boot posts nothing.
func ReportBootHealth(s *discordgo.Session) {
	problems := FilterProblems(RunHealthChecks(s))
	if len(problems) == 0 {
		log.Println("health: all startup checks passed")
		return
	}
	for _, c := range problems {
		log.Printf("health: %s: %s - %s", strings.ToUpper(c.Status), c.Name, c.Detail)
	}
	adminChannel := strings.TrimSpace(apiredis.CONF_DISCORD_ADMIN_CHANNEL_ID.GetWithDefault(apiredis.RedisDB, ""))
	if adminChannel == "" {
		return
	}
	msg := fmt.Sprintf("**Startup health check** - %d issue(s) found (`/health` for a full report):%s",
		len(problems), FormatCheckResults(problems))
	for _, chunk := range chunkByLines(msg, 1900) {
		if _, err := s.ChannelMessageSend(adminChannel, chunk); err != nil {
			log.Println("health: posting boot summary to admin channel failed:", err)
			return
		}
	}
}

// chunkByLines splits content on line boundaries into <= limit sized chunks.
func chunkByLines(content string, limit int) []string {
	if len(content) <= limit {
		return []string{content}
	}
	chunks := []string{}
	cur := ""
	for _, line := range strings.Split(content, "\n") {
		if len(cur)+len(line)+1 > limit && cur != "" {
			chunks = append(chunks, cur)
			cur = ""
		}
		if cur != "" {
			cur += "\n"
		}
		cur += line
	}
	if cur != "" {
		chunks = append(chunks, cur)
	}
	return chunks
}
