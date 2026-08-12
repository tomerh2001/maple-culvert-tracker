package commands

import (
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// tenantOf resolves the invoking interaction's data tenant (see
// data.TenantID). Every handler scopes its redis and postgres access with it.
func tenantOf(i *discordgo.InteractionCreate) string {
	return data.TenantID(i.GuildID)
}

// isAdmin reports whether the caller has Administrator or Manage Server.
func isAdmin(i *discordgo.InteractionCreate) bool {
	if i.Member == nil {
		return false
	}
	return i.Member.Permissions&(discordgo.PermissionAdministrator|discordgo.PermissionManageServer) != 0
}

// splitIDList splits a comma separated id list conf value.
func splitIDList(raw string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}

// canSubmitScores reports whether the caller may submit/parse scores or manage
// character links: admins always can; otherwise the caller must hold one of
// the tenant's configured submitter roles or be a configured submitter user.
func canSubmitScores(i *discordgo.InteractionCreate) bool {
	if isAdmin(i) {
		return true
	}
	if i.Member == nil {
		return false
	}
	tenant := tenantOf(i)
	users := splitIDList(apiredis.CONF_DISCORD_SUBMIT_USER_IDS.For(tenant).GetWithDefault(apiredis.RedisDB, ""))
	if users[i.Member.User.ID] {
		return true
	}
	roles := splitIDList(apiredis.CONF_DISCORD_SUBMIT_ROLE_IDS.For(tenant).GetWithDefault(apiredis.RedisDB, ""))
	for _, r := range i.Member.Roles {
		if roles[r] {
			return true
		}
	}
	return false
}

// requireSubmitPermission replies with an ephemeral explanation and returns
// false when the caller may not submit scores.
func requireSubmitPermission(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	if canSubmitScores(i) {
		return true
	}
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "You don't have permission to submit scores. Ask an admin to add your role with `/config` (Submitter Role IDs) if you should be able to.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	return false
}
