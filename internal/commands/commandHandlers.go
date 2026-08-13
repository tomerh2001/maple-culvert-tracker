package commands

import (
	"github.com/bwmarrin/discordgo"
)

// CommandHandlers maps every surface command (slash commands and context
// menus) to its handler. All replies are EPHEMERAL except /culvert,
// /culvert, /culvert-all and the Culvert user menu, which are public.
var CommandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
	"culvert-help":  helpCommand,
	"setup":         setupCommand,
	"health":        healthCommand,
	"culvert":       culvertBase,
	"culvert-all":   culvertBoard,
	"register":      registerCharacter,
	"unregister":    unregisterCharacter,
	"submit-scores": submitScoresCommand,
	"set-culvert":   setCulvert,
	"config":        configCommand,
	"reset-week":    resetWeekCommand,
	// Context menu commands (right click a message or member -> Apps).
	"Submit Scores": submitScoresFromMessage,
	"Culvert":       culvertBase,
}
