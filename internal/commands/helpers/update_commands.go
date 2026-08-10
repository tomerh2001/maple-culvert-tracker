package helpers

import (
	"encoding/json"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// UpdateCommands updates the slash commands in every served guild.
//
// It will update or create new commands, and delete any remaining commands.
// The commands are identified by their name.
func UpdateCommands(s *discordgo.Session, commands []*discordgo.ApplicationCommand) error {
	for _, guildID := range data.AllGuildIDs() {
		if err := updateGuildCommands(s, guildID, commands); err != nil {
			return err
		}
	}
	return nil
}

func updateGuildCommands(s *discordgo.Session, guildID string, commands []*discordgo.ApplicationCommand) error {
	log.Println("Updating Application slash commands for guild", guildID)
	cmds, err := s.ApplicationCommands(s.State.User.ID, guildID)
	if err != nil {
		return err
	}
	m := map[string]*discordgo.ApplicationCommand{}
	for _, v := range cmds {
		m[v.Name] = v
	}

	for _, appCommand := range commands {
		if existingCommand, ok := m[appCommand.Name]; ok {
			needUpdate := false
			// Update if options are different
			o := map[string]*discordgo.ApplicationCommandOption{}
			for _, existingOption := range existingCommand.Options {
				o[existingOption.Name] = existingOption
			}
			for _, appCommandOption := range appCommand.Options {
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
					log.Panicf("Cannot create '%v' command: %v", appCommand.Name, err)
					return err
				}
				log.Println("Updated command " + appCommand.Name)
			}
		} else {
			// Create command
			_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, appCommand)
			if err != nil {
				log.Panicf("Cannot create '%v' command: %v", appCommand.Name, err)
				return err
			}
			log.Println("Added new command " + appCommand.Name)
		}
		delete(m, appCommand.Name)
	}
	for _, v := range m {
		// delete remainder
		err := s.ApplicationCommandDelete(s.State.User.ID, guildID, v.ID)
		if err != nil {
			return err
		}
	}

	return nil
}
