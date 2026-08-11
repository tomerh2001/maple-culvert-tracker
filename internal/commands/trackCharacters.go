package commands

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// trackCharacters bulk-tracks characters by name without linking them to a
// Discord account (discord_user_id = '2'); members can /register to claim
// them later. Names come from the `names` option (comma/newline separated)
// and/or an attached .txt file. Submitter-gated.
func trackCharacters(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, true)

	namesRaw := ""
	var attachment *discordgo.MessageAttachment
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "names":
			namesRaw = v.StringValue()
		case "names-attachment":
			if a, ok := i.ApplicationCommandData().Resolved.Attachments[v.Value.(string)]; ok {
				attachment = a
			}
		}
	}

	if attachment != nil {
		if attachment.Size > 2048*1024 {
			r.Edit("Attachment size exceeds 2MB limit! Please upload a smaller file.")
			return
		}
		if !strings.HasSuffix(attachment.Filename, ".txt") {
			r.Edit("Invalid attachment format! Please upload a .txt file with one character name per line (or comma separated).")
			return
		}
		body, err := downloadBytes(attachment.URL)
		if err != nil {
			log.Println("track-characters: failed to download attachment:", err)
			r.Edit("Failed to download your attachment! Please try again.")
			return
		}
		namesRaw += "\n" + string(body)
	}

	names := helpers.SplitNameList(namesRaw)
	if len(names) == 0 {
		r.Edit("Provide character names, e.g. `/track-characters names:Alice,Bob,Carol` (commas or new lines), or attach a .txt file with one name per line.")
		return
	}

	created, _, err := helpers.TrackCharacters(db.DB, names)
	if err != nil {
		log.Println("track-characters:", err)
		r.Edit("Something went wrong saving the characters. Please try again later.")
		return
	}

	createdSet := map[string]bool{}
	for _, n := range created {
		createdSet[n] = true
	}
	already := []string{}
	for _, n := range names {
		if !createdSet[n] {
			already = append(already, n)
		}
	}

	msg := fmt.Sprintf("Tracked %d new character(s).", len(created))
	if len(created) > 0 {
		msg += "\nNew: `" + strings.Join(capList(created, 25), "`, `") + "` (members can `/register` to claim them)"
	}
	if len(already) > 0 {
		msg += fmt.Sprintf("\nAlready existed (%d): `%s`", len(already), strings.Join(capList(already, 25), "`, `"))
	}
	r.Edit(msg)
}
