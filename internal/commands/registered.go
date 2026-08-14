package commands

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// registeredCommand serves /registered: list every member in this tenant who
// has linked at least one character, with their character names. Everyone may
// use it - there is no permission gate - and the reply is ephemeral. One line
// per member, chunked by the reply helper so a 200+ member guild never exceeds
// Discord's 2000-character message limit.
func registeredCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	users, err := cmdhelpers.LinkedUsers(db.DB, tenant)
	if err != nil {
		log.Println("registered: query failed:", err)
		r.Edit("Something went wrong querying the database. Please try again later.")
		return
	}
	if len(users) == 0 {
		r.Edit("No one has linked a character yet - be the first with `/register name:YourCharacter`.")
		return
	}

	b := &strings.Builder{}
	fmt.Fprintf(b, "%d member(s) have linked characters:", len(users))
	for _, u := range users {
		fmt.Fprintf(b, "\n<@%s> - `%s`", u.DiscordUserID, strings.Join(u.Names, "`, `"))
	}
	r.EditChunked(b.String())
}
