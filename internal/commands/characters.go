package commands

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// charactersCommand serves /characters: list the MapleStory characters linked
// to a member (the user option, or the invoker by default) within this tenant.
// Everyone may use it - there is no permission gate - and the reply is
// ephemeral.
func charactersCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	callerID := i.Member.User.ID
	targetID := callerID
	for _, v := range i.ApplicationCommandData().Options {
		if v.Name == "user" {
			if u := v.UserValue(nil); u != nil {
				targetID = u.ID
			}
		}
	}
	isSelf := targetID == callerID

	names, err := cmdhelpers.LinkedCharacterNames(db.DB, tenant, targetID)
	if err != nil {
		log.Println("characters: query failed:", err)
		r.Edit("Something went wrong querying the database. Please try again later.")
		return
	}

	if len(names) == 0 {
		if isSelf {
			r.Edit("You haven't linked any characters yet - `/register name:YourCharacter`.")
			return
		}
		r.Edit("<@" + targetID + "> hasn't linked any characters yet - `/register name:YourCharacter`.")
		return
	}

	who := "You have"
	if !isSelf {
		who = "<@" + targetID + "> has"
	}
	r.EditChunked(fmt.Sprintf("%s %d linked character(s): `%s`", who, len(names), strings.Join(names, "`, `")))
}
