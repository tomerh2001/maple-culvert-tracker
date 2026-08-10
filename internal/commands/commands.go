package commands

import (
	"github.com/bwmarrin/discordgo"
)

var culvertMinWeeks = float64(4)
var exportCsvMinWeeks = float64(4)
var minimumPercentageSniffOutRats = float64(0)

var Commands = []*discordgo.ApplicationCommand{
	{
		Name:        "ping",
		Description: "Shows user details",
	},
	{
		Name:        "culvert",
		Description: "Shows your past culvert progression",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Your character's name",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format to check historical data",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &culvertMinWeeks,
				MaxValue:    52,
				Name:        "weeks",
				Description: "Number of weeks to display in the graph",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "y-axis-start-at-0",
				Description: "Start the Y axis at 0 for better visualization",
			},
		},
	},
	{
		Name:        "culvert-anyone",
		Description: "Shows the past culvert progression for a given character",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "The character's name",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format to check historical data",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &culvertMinWeeks,
				MaxValue:    52 * 5,
				Name:        "weeks",
				Description: "Number of weeks to display in the graph",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "y-axis-start-at-0",
				Description: "Start the Y axis at 0 for better visualization",
			},
		},
	},
	{
		Name:        "login",
		Description: "Gives you a temporary login code for the Admin Console",
	},
	{
		Name:        "culvert-duel",
		Description: "Flex your culvert score against your Guildmate",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "your-character",
				Description: "Your character's name",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "their-character",
				Description: "Their character's name",
			},
		},
	},
	{
		Name:        "culvert-duel-anyone",
		Description: "MODS ONLY: Select 2 guild members to duel their culvert scores",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "your-character",
				Description: "Your character's name",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "their-character",
				Description: "Their character's name",
			},
		},
	},
	{
		Name:        "sandbaggers",
		Description: "Shows players with most sandbagged runs over the past 12 weeks",
	},
	{
		Name:        "export-csv",
		Description: "Export weeks of data to csv format (Compatible with all spreadsheet software)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &exportCsvMinWeeks,
				MaxValue:    52,
				Name:        "weeks",
				Description: "Number of weeks to export",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format to export historical data",
			},
		},
	},
	{
		Name:        "track-character",
		Description: "Track a new character in the Guild",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Character name",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "discord-user-id",
				Description: "Discord member this character belongs to (default: untracked)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "skip-name-check",
				Description: "Skip character name check with maple rankings",
			},
		},
	},
	{
		Name:        "culvert-mega-chart",
		Description: "Shows the past culvert progression for the whole entire guild",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format to check historical data",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &culvertMinWeeks,
				MaxValue:    52,
				Name:        "weeks",
				Description: "Number of weeks to display in the graph (default 8)",
			},
		},
	},
	{
		Name:        "culvert-summary",
		Description: "Shows the culvert progression for the whole entire guild for a specific week",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format to check historical data",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "order-by",
				Description: "Order the results by name or score (default score)",
				Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "Character name",
						Value: "name",
					},
					{
						Name:  "Score",
						Value: "score",
					},
				},
			},
		},
	},
	{
		Name:        "inactive-players",
		Description: "Shows characters whose latest culvert score is a zero streak on the newest date",
	},
	{
		Name:        "list-characters",
		Description: "List all characters being tracked in the guild",
	},
	{
		Name:        "personal-bests",
		Description: "List personal bests for all active characters",
	},
	{
		Name:        "submit-scores-from-attachment",
		Description: "Submit culvert scores via a discord message attachment as .txt or .json file",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionAttachment,
				Name:        "scores-attachment",
				Description: "The attachment containing the culvert scores in .txt or .json format (copy-pasted from the OCR app)",
				Required:    true,
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Culvert date in YYYY-MM-DD format. If not provided, defaults to the most recent Wednesday.",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "overwrite-existing",
				Description: "Overwrite existing scores for characters that already have a score for the specified date.",
			},
		},
	},
	{
		Name:        "submit-scores",
		Description: "Submit culvert scores by reading a .txt or .json attachment from an existing discord message",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "channel-id",
				Description: "Channel of the message (accepts <#id> or raw id)",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-id",
				Description: "Discord message ID containing the .txt or .json scores attachment",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Culvert date in YYYY-MM-DD format. If not provided, defaults to the most recent Wednesday.",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "overwrite-existing",
				Description: "Overwrite existing scores for characters that already have a score for the specified date.",
			},
		},
	},
	{
		Name:        "sniff-out-rats",
		Description: "Find members who culvert bi-weekly, or sandbag semi-consistently",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &culvertMinWeeks,
				MaxValue:    52 * 5,
				Name:        "weeks",
				Description: "Number of weeks to analyze",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &minimumPercentageSniffOutRats,
				MaxValue:    100,
				Name:        "weeks-percentage-threshold",
				Description: "Percentage of sandbagged weeks to consider character a repeat offenders. Default: 30%" + " of weeks",
			},
			{
				Required: false,
				Type:     discordgo.ApplicationCommandOptionString,
				Name:     "value-as-offense",
				Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "Use 0 for sandbagged run",
						Value: "zero",
					},
					{
						Name:  "Use threshold value for sandbagged run (typical formula for calculating participation)",
						Value: "threshold",
					},
				},
				Description: "Treat 0 or weekly score below sandbag threshold as an offense. Default: 0",
			},
		},
	},
	{
		Name:        "weekly-sandbaggers",
		Description: "Shows stats for all sandbaggers of a given week",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format, default latest week",
			},
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "pb-diff-threshold",
				MinValue:    &minimumPercentageSniffOutRats,
				MaxValue:    100,
				Description: "Shows all characters under threshold%" + " of their pb. Default: 70%",
			},
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "weeks",
				MinValue:    &culvertMinWeeks,
				MaxValue:    52 * 5,
				Description: "Number of weeks to analyze. Default: 12",
			},
		},
	},
	{
		Name:        "participation",
		Description: "Show participation for members, %, full weeks",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &exportCsvMinWeeks,
				MaxValue:    52,
				Name:        "weeks",
				Description: "Number of weeks to export",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Date in YYYY-MM-DD format to export historical data",
			},
		},
	},
	{
		Name:        "parse-images",
		Description: "Parse GPQ score images from Discord message(s) into a JSON of character scores",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "channel-id",
				Description: "Channel of the messages (accepts <#id> or raw id)",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-id-1",
				Description: "Discord message ID containing score image attachment(s)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-id-2",
				Description: "Additional Discord message ID",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-id-3",
				Description: "Additional Discord message ID",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-id-4",
				Description: "Additional Discord message ID",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-id-5",
				Description: "Additional Discord message ID",
			},
		},
	},
	{
		Name: "Parse Images",
		Type: discordgo.MessageApplicationCommand,
	},
	{
		Name: "Submit Scores",
		Type: discordgo.MessageApplicationCommand,
	},
}
