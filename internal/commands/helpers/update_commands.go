package helpers

import (
	"errors"
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// UpdateCommands registers the surface ONCE as GLOBAL application commands
// (bulk overwrite - creates, updates and deletes in a single call), so every
// server the bot is invited to gets the commands without any per-guild
// registration. It then deletes any leftover per-guild registrations in the
// env-listed guilds (one cleanup pass from the per-guild era, so their users
// don't see every command twice). The overall outcome ("ok" or the error
// string) is recorded in the process-global DATA_COMMAND_REGISTRATION_STATUS
// redis key for the /health diagnostics.
func UpdateCommands(s *discordgo.Session, commands []*discordgo.ApplicationCommand) error {
	log.Println("Registering global application commands (bulk overwrite)")
	var errs []error
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", commands); err != nil {
		log.Println("UpdateCommands: global bulk overwrite failed:", err)
		errs = append(errs, fmt.Errorf("global registration: %w", err))
	} else {
		log.Printf("UpdateCommands: registered %d global commands", len(commands))
		// Only clean up per-guild duplicates once the global set is in place,
		// so a transient failure never leaves servers with NO commands.
		for _, guildID := range data.AllGuildIDs() {
			if err := deleteGuildCommands(s, guildID); err != nil {
				log.Printf("UpdateCommands: guild %s cleanup failed: %v", guildID, err)
				errs = append(errs, fmt.Errorf("guild %s cleanup: %w", guildID, err))
			}
		}
	}
	err := errors.Join(errs...)
	status := "ok"
	if err != nil {
		status = err.Error()
	}
	if serr := apiredis.DATA_COMMAND_REGISTRATION_STATUS.Global().Set(apiredis.RedisDB, status); serr != nil {
		log.Println("UpdateCommands: recording registration status failed:", serr)
	}
	return err
}

// deleteGuildCommands removes every guild-scoped command registration from
// one guild (the pre-global scheme registered the full surface per guild;
// with global commands in place they would render as duplicates).
func deleteGuildCommands(s *discordgo.Session, guildID string) error {
	cmds, err := s.ApplicationCommands(s.State.User.ID, guildID)
	if err != nil {
		return fmt.Errorf("listing guild commands: %w", err)
	}
	for _, v := range cmds {
		if err := s.ApplicationCommandDelete(s.State.User.ID, guildID, v.ID); err != nil {
			return fmt.Errorf("deleting guild command %q: %w", v.Name, err)
		}
		log.Printf("UpdateCommands: deleted per-guild command %q from guild %s", v.Name, guildID)
	}
	return nil
}
