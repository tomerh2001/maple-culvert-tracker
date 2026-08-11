package commands

import (
	"time"

	"github.com/bwmarrin/discordgo"
)

var CommandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
	"ping": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "I am alive! You are <@" + i.Member.User.ID + ">, who joined on " + i.Member.JoinedAt.Format(time.RFC822) + ".\nThis fork is maintained by Tomer (tomerh2001 on GitHub). Original bot by Azuri (SLAzurin on GitHub).",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	},
	"help":              helpCommand,
	"setup":             setupCommand,
	"culvert":           culvertBase,
	"leaderboard":       leaderboard,
	"register":          registerCharacter,
	"report-score":      reportScore,
	"personal-bests":    personalBests,
	"list-characters":   listCharacters,
	"export-csv":        exportcsv,
	"submit-scores":     submitScores,
	"parse-images":      parseImages,
	"track-characters":  trackCharacters,
	"untrack-character": untrackCharacter,
	"rename-character":  renameCharacterCmd,
	"set-score":         setScore,
	"config":            configCommand,
	"reset":             resetData,
	// Context menu commands (right click a message or member -> Apps).
	"Parse Images":  parseImagesFromMessage,
	"Submit Scores": submitScoresFromMessage,
	"Culvert":       culvertBase,
}
