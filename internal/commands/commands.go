package commands

import (
	"github.com/bwmarrin/discordgo"
)

var culvertMinWeeks = float64(4)
var exportCsvMinWeeks = float64(4)
var reportScoreMin = float64(0)

var Commands = []*discordgo.ApplicationCommand{
	// ── Everyone ────────────────────────────────────────────────────────────
	{
		Name:        "help",
		Description: "How to use this bot",
	},
	{
		Name:        "ping",
		Description: "Check the bot is alive",
	},
	{
		Name:        "register",
		Description: "Link your own MapleStory character to your Discord account",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Your in-game character name",
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
		Name:        "culvert",
		Description: "Culvert progression chart: yours, another member's, or any character's",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "user",
				Description: "Show this member's culvert instead of your own",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Show this character (any tracked character)",
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
				Description: "Number of weeks to display in the graph (default 8)",
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
		Name:        "leaderboard",
		Description: "Guild culvert leaderboard for a week, or a whole-guild progression chart",
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
					{Name: "Character name", Value: "name"},
					{Name: "Score", Value: "score"},
				},
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "chart",
				Description: "Show a whole-guild progression chart instead of a table",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &culvertMinWeeks,
				MaxValue:    52,
				Name:        "weeks",
				Description: "Number of weeks in the chart (default 8, chart only)",
			},
		},
	},
	{
		Name:        "personal-bests",
		Description: "List personal bests for all active characters",
	},
	{
		Name:        "list-characters",
		Description: "List all characters being tracked in the guild",
	},
	{
		Name:        "report-score",
		Description: "Report your own culvert score for this week (if enabled by admins)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &reportScoreMin,
				Name:        "score",
				Description: "Your culvert score this week",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Which of your characters (only needed if you have several)",
			},
		},
	},
	// ── Submitters / admins ─────────────────────────────────────────────────
	{
		Name:        "submit-scores",
		Description: "Submit weekly scores from screenshots or a parsed scores file",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionAttachment,
				Name:        "scores-attachment",
				Description: "A .json/.txt scores file (from /parse-images)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-link",
				Description: "Link to a message with score screenshots (or a scores file)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Culvert week (YYYY-MM-DD, a Wednesday). Defaults to this week",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "overwrite-existing",
				Description: "Overwrite scores that already exist for that week",
			},
		},
	},
	{
		Name:        "parse-images",
		Description: "OCR guild score screenshots into a scores file (without submitting)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "message-link",
				Description: "Link to the message holding the screenshot(s)",
			},
		},
	},
	{
		Name:        "track-character",
		Description: "Track a character and link it to a member (admins/submitters)",
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
		Name:        "untrack-character",
		Description: "Stop tracking a character (history is kept)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Character to untrack",
			},
		},
	},
	{
		Name:        "rename-character",
		Description: "Rename a tracked character (after an in-game name change)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Current tracked name",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "new-name",
				Description: "New character name",
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
		Name:        "set-score",
		Description: "Manually set one character's culvert score for a week",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "character-name",
				Description: "Tracked character name",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &reportScoreMin,
				Name:        "score",
				Description: "The culvert score",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Culvert week (YYYY-MM-DD, a Wednesday). Defaults to this week",
			},
		},
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
		Name:        "config",
		Description: "ADMINS: View or change bot settings",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "setting",
				Description: "The setting to view or change (leave empty to list all)",
				Choices:     ConfigSettingChoices(),
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "value",
				Description: "New value (empty to view; ids comma separated for lists)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionChannel,
				Name:        "channel",
				Description: "Pick a channel (for channel settings)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionRole,
				Name:        "role",
				Description: "Pick a role (for role settings; appends to lists)",
			},
		},
	},
	{
		Name:        "setup",
		Description: "ADMINS: How to set this bot up for your guild",
	},
	{
		Name:        "reset",
		Description: "ADMINS: Delete ALL tracked characters and scores, start from scratch",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "confirm",
				Description: "Type DELETE EVERYTHING to confirm",
			},
		},
	},
	// ── Context menus (right click -> Apps) ─────────────────────────────────
	{
		Name: "Parse Images",
		Type: discordgo.MessageApplicationCommand,
	},
	{
		Name: "Submit Scores",
		Type: discordgo.MessageApplicationCommand,
	},
	{
		Name: "Culvert",
		Type: discordgo.UserApplicationCommand,
	},
}
