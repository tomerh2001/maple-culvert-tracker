package helpers

import (
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// GenerateDiscordCulvertMultiOutput builds the public embed for a member's
// multi-character culvert chart: one line per character (colors + legend live
// in the chart image itself). names are the charted character names in chart
// order; more>0 appends a "+N more" note for characters the series cap dropped.
// date is the optional `to` week label. updatedAt/latestWeek describe how
// current the charted scores are (see StampLastUpdated). It mirrors the
// single-series output's EditData shape (embed + image attachment).
func GenerateDiscordCulvertMultiOutput(chartImageBinData io.ReadCloser, date string, names []string, more int, updatedAt time.Time, latestWeek string) *discordgo.InteractionResponseData {
	title := strings.TrimSpace("Culvert " + date)

	desc := "Comparing " + strconv.Itoa(len(names)) + " characters"
	if len(names) > 0 {
		desc += ": `" + strings.Join(names, "`, `") + "`"
	}
	if more > 0 {
		desc += "\n(+" + strconv.Itoa(more) + " more not shown)"
	}

	embeddedData := &discordgo.MessageEmbed{
		Title:       title,
		Description: desc,
		// Same accent color as the single-series chart embed (hex 36A2EB).
		Color: 3580651,
		Image: &discordgo.MessageEmbedImage{
			URL: "attachment://image.png",
		},
	}

	StampLastUpdated(embeddedData, updatedAt, latestWeek)

	return &discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{embeddedData},
		Files:  []*discordgo.File{{Name: "image.png", Reader: chartImageBinData}},
	}
}
