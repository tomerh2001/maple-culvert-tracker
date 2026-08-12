package commands

import (
	"github.com/bwmarrin/discordgo"
)

var setCulvertScoreMin = float64(0)

// Commands is the ENTIRE deliberately-small surface: 9 slash commands and 2
// context menus. Score submission has exactly one path (right click a
// screenshot message -> Apps -> Submit Scores); /set-culvert covers manual
// corrections. UpdateCommands deletes anything else that is still registered
// remotely.
var Commands = []*discordgo.ApplicationCommand{
	// ── Everyone ────────────────────────────────────────────────────────────
	{
		Name:        "help",
		Description: "How to use this bot",
	},
	{
		Name:        "register",
		Description: "Link a MapleStory character to a Discord account (yours by default)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "name",
				Description: "The in-game character name",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "user",
				Description: "Register for this member instead of yourself (submitters only)",
			},
		},
	},
	{
		Name:        "unregister",
		Description: "Untrack a character by name, or all of a member's characters (history kept)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "name",
				Description: "Untrack this character (yours; submitters can untrack any)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "user",
				Description: "Untrack ALL characters of this member (submitters only; default: yourself)",
			},
		},
	},
	{
		Name:        "culvert",
		Description: "Culvert progression chart: yours, a member's, or any tracked character's",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "name",
				Description: "A character name or a @mention (default: you)",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "from",
				Description: "Start date: YYYY-MM-DD or a Discord timestamp",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "to",
				Description: "End date: YYYY-MM-DD or a Discord timestamp",
			},
		},
	},
	{
		Name:        "culvert-all",
		Description: "Guild culvert leaderboard for a week (default: this week)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Week to show: YYYY-MM-DD or a Discord timestamp",
			},
		},
	},
	// ── Submitters / admins ─────────────────────────────────────────────────
	{
		Name:        "set-culvert",
		Description: "Manually set one character's culvert score for a week",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "name",
				Description: "Character name (unknown names are tracked automatically)",
			},
			{
				Required:    true,
				Type:        discordgo.ApplicationCommandOptionInteger,
				MinValue:    &setCulvertScoreMin,
				Name:        "score",
				Description: "The culvert score",
			},
			{
				Required:    false,
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "date",
				Description: "Week: YYYY-MM-DD or a Discord timestamp (default: this week)",
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
				Description: "New value (empty to view; #channel/@role mentions and comma lists work)",
			},
		},
	},
	{
		Name:        "setup",
		Description: "ADMINS: How to set this bot up for your guild",
	},
	{
		Name:        "health",
		Description: "ADMINS: Run the bot's health checks (database, Discord permissions, config)",
	},
	// ── Context menus (right click -> Apps) ─────────────────────────────────
	{
		Name: "Submit Scores",
		Type: discordgo.MessageApplicationCommand,
	},
	{
		Name: "Culvert",
		Type: discordgo.UserApplicationCommand,
	},
}
