package helpers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// UpdateCommands updates the slash commands in every served guild.
//
// It will update or create new commands, and delete any remaining commands.
// The commands are identified by their name. A failing guild never aborts the
// others: per-guild errors are accumulated (errors.Join) and returned together.
// The overall outcome ("ok" or the error string) is recorded in the
// DATA_COMMAND_REGISTRATION_STATUS redis key for the /health diagnostics.
func UpdateCommands(s *discordgo.Session, commands []*discordgo.ApplicationCommand) error {
	var errs []error
	for _, guildID := range data.AllGuildIDs() {
		if err := updateGuildCommands(s, guildID, commands); err != nil {
			log.Printf("UpdateCommands: guild %s failed: %v", guildID, err)
			errs = append(errs, fmt.Errorf("guild %s: %w", guildID, err))
		}
	}
	err := errors.Join(errs...)
	status := "ok"
	if err != nil {
		status = err.Error()
	}
	if serr := apiredis.DATA_COMMAND_REGISTRATION_STATUS.Set(apiredis.RedisDB, status); serr != nil {
		log.Println("UpdateCommands: recording registration status failed:", serr)
	}
	return err
}

func updateGuildCommands(s *discordgo.Session, guildID string, commands []*discordgo.ApplicationCommand) error {
	log.Println("Updating Application slash commands for guild", guildID)
	cmds, err := s.ApplicationCommands(s.State.User.ID, guildID)
	if err != nil {
		return fmt.Errorf("listing registered commands: %w", err)
	}
	m := map[string]*discordgo.ApplicationCommand{}
	for _, v := range cmds {
		m[v.Name] = v
	}

	for _, appCommand := range commands {
		if existingCommand, ok := m[appCommand.Name]; ok {
			// Update if options are different. A remote command with EXTRA
			// options (removed from the definition) must sync too, so a
			// count mismatch alone forces the update.
			needUpdate := len(existingCommand.Options) != len(appCommand.Options) ||
				existingCommand.Description != appCommand.Description
			o := map[string]*discordgo.ApplicationCommandOption{}
			for _, existingOption := range existingCommand.Options {
				o[existingOption.Name] = existingOption
			}
			for _, appCommandOption := range appCommand.Options {
				if needUpdate {
					break
				}
				rawCmdOptions, err := json.Marshal(appCommandOption)
				if err != nil {
					return err
				}
				rawNewCmdOptions, err := json.Marshal(o[appCommandOption.Name])
				if err != nil {
					return err
				}
				if string(rawCmdOptions) != string(rawNewCmdOptions) {
					needUpdate = true
					break
				}
			}
			if needUpdate {
				_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, appCommand)
				if err != nil {
					return fmt.Errorf("updating command %q: %w", appCommand.Name, err)
				}
				log.Println("Updated command " + appCommand.Name)
			}
		} else {
			// Create command
			_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, appCommand)
			if err != nil {
				return fmt.Errorf("creating command %q: %w", appCommand.Name, err)
			}
			log.Println("Added new command " + appCommand.Name)
		}
		delete(m, appCommand.Name)
	}
	for _, v := range m {
		// delete remainder
		err := s.ApplicationCommandDelete(s.State.User.ID, guildID, v.ID)
		if err != nil {
			return fmt.Errorf("deleting stale command %q: %w", v.Name, err)
		}
	}

	return nil
}
