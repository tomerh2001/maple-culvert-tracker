package helpers

import (
	"errors"
	"log"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

func FetchMembers(discordServerID string, DiscordSession *discordgo.Session) ([]data.WebGuildMember, error) {
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
			return result, errors.New("broken discord connection when getting guild members")
		}
		allMembers = append(allMembers, members...)
		if len(members) == 1000 {
			afterMember = members[999].User.ID
		} else {
			afterMember = ""
		}
	}

	// Get members that are member
	roleIDs := strings.Split(apiredis.CONF_DISCORD_GUILD_ROLE_IDS.GetWithDefault(apiredis.RedisDB, os.Getenv("DISCORD_GUILD_ROLE_ID")), ",")
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

// FetchAllMembers merges FetchMembers across every served guild, deduplicated
// by Discord user id.
func FetchAllMembers(DiscordSession *discordgo.Session) ([]data.WebGuildMember, error) {
	seen := map[string]bool{}
	out := []data.WebGuildMember{}
	var lastErr error
	fetchedAny := false
	for _, guildID := range data.AllGuildIDs() {
		members, err := FetchMembers(guildID, DiscordSession)
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
