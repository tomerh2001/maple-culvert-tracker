package helpers

import (
	"log"
	"os"
	"runtime/debug"

	"github.com/bwmarrin/discordgo"
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
		if !data.IsAllowedGuild(i.GuildID) {
			log.Printf("Rejected discord command %v from %v: guild %q not served\n", name, interactionUsername(i), i.GuildID)
			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "This bot instance doesn't serve this server.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			}); err != nil {
				log.Println("disallowed-guild reply:", err)
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

		err := UpdateCommands(s, commands)
		if err != nil {
			log.Println("Failed UpdateCommands")
			return
		}
		log.Println("Done UpdateCommands Successfully")
	})
	return s, nil
}
