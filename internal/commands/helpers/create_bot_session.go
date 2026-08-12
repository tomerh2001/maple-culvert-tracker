package helpers

import (
	"log"
	"os"
	"runtime/debug"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// interactionUsername names the invoking user for logging, tolerating DM
// interactions where i.Member is nil.
func interactionUsername(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.Username
	}
	if i.User != nil {
		return i.User.Username
	}
	return "unknown"
}

// replyToPanickedInteraction best-effort tells the user their command blew up.
// The handler may or may not have acknowledged the interaction already, so try
// a fresh response first and fall back to editing the deferred one.
func replyToPanickedInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	msg := "Something went wrong - the error has been logged."
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg}); err != nil {
			log.Println("panic reply: could not notify user:", err)
		}
	}
}

func CreateBotSessionWithCommands(commands []*discordgo.ApplicationCommand, commandHandlers map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate)) (*discordgo.Session, error) {
	s, err := discordgo.New("Bot " + os.Getenv(data.EnvVarDiscordToken))
	if err != nil {
		return nil, err
	}
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		name := i.ApplicationCommandData().Name
		h, ok := commandHandlers[name]
		if !ok {
			return
		}
		// Commands are guild-scoped (a tenant is derived from the guild id):
		// keep the DM guard. Any guild is served - per-server data isolation
		// is handled by the tenant model, not by rejecting servers.
		if i.GuildID == "" || i.Member == nil {
			log.Printf("Rejected discord command %v from %v: not invoked from a server\n", name, interactionUsername(i))
			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "This bot works inside servers only - invoke the command from a server channel.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			}); err != nil {
				log.Println("dm-guard reply:", err)
			}
			return
		}
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in discord command %v from %v: %v\n%s", name, interactionUsername(i), r, debug.Stack())
				replyToPanickedInteraction(s, i)
			}
		}()
		log.Printf("Got discord command %v from %v\n", name, interactionUsername(i))
		h(s, i)
		log.Printf("Done discord command %v from %v\n", name, interactionUsername(i))
	})

	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)

		// Overwrite the active-guild registry with the session's actual guild
		// list: it is authoritative on Ready (covers joins/leaves that
		// happened while the bot was down).
		guildIDs := make([]string, 0, len(r.Guilds))
		for _, g := range r.Guilds {
			guildIDs = append(guildIDs, g.ID)
		}
		StoreActiveGuilds(apiredis.RedisDB, guildIDs)

		// Remember the session so roster-dependent flows can refresh the
		// member cache lazily when it goes stale, then do the boot work in
		// the background, in a deterministic order: register commands,
		// refresh the default tenant's member cache, then run the startup
		// health checks (which read both outcomes). Foreign tenants refresh
		// lazily on first use and via periodicredis.
		SetRosterSession(s)
		go func() {
			if err := UpdateCommands(s, commands); err != nil {
				log.Println("Failed UpdateCommands:", err)
			} else {
				log.Println("Done UpdateCommands Successfully")
			}
			if err := RefreshMemberCache(s, data.PrimaryGuildID()); err != nil {
				log.Println("Ready: member cache refresh failed:", err)
			}
			ReportBootHealth(s)
		}()
	})

	// Track guild membership in the active-guild registry. Joining a guild
	// sends NOTHING anywhere (owner mandate: zero unprompted messages) -
	// /setup is the entry point for new servers.
	s.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		if g == nil || g.Guild == nil {
			return
		}
		AddActiveGuild(apiredis.RedisDB, g.ID)
	})
	s.AddHandler(func(s *discordgo.Session, g *discordgo.GuildDelete) {
		if g == nil || g.Guild == nil {
			return
		}
		// Unavailable=true is a Discord outage, not a removal.
		if g.Unavailable {
			return
		}
		// The tenant's data stays: rejoining finds everything as it was.
		RemoveActiveGuild(apiredis.RedisDB, g.ID)
	})
	return s, nil
}
