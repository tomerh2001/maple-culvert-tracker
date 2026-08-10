package helpers

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

func GenerateDiscordCulvertOutput(chartImageBinData io.ReadCloser, charName string, date string, otherStatsStruct *data.CharacterStatistics) *discordgo.InteractionResponseData {
	// date is possibly empty
	content := strings.Trim(charName+" "+date, " ")

	embeddedData := &discordgo.MessageEmbed{
		Title: content,
		// Convert hex to int here
		// https://www.rapidtables.com/convert/number/hex-to-decimal.html?x=36A2EB
		Color: 3580651,
		Image: &discordgo.MessageEmbedImage{
			URL: "attachment://image.png",
		},
		Fields: []*discordgo.MessageEmbedField{},
	}

	charData, err := FetchCharacterData(charName, apiredis.OPTIONAL_CONF_MAPLE_REGION.GetWithDefault(apiredis.RedisDB, os.Getenv("MAPLE_REGION")))
	if err == nil {
		embeddedData.Title = strings.Trim(charData.CharacterName+" "+date, " ")
		embeddedData.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: charData.CharacterImgURL}
		embeddedData.Description = charName + " is a Level " + strconv.Itoa(charData.Level) + " " + charData.JobName

		// For more embed examples, visit: https://discordjs.guide/popular-topics/embeds.html#using-the-embed-constructor
	}

	if otherStatsStruct != nil {
		placementStr := ""
		if otherStatsStruct.GuildTopPlacement == 0 {
			placementStr = "Not in the guild's top 200 this week"
		} else {
			placementStr = "#" + strconv.Itoa(otherStatsStruct.GuildTopPlacement) + " in the guild"
			if date != "" {
				placementStr += " on " + date
			}
		}

		embeddedData.Fields = append(embeddedData.Fields, &discordgo.MessageEmbedField{
			Inline: false,
			Name:   "", // leave this empty
			Value:  placementStr,
		},
			&discordgo.MessageEmbedField{
				Name:   "Personal Best",
				Value:  strconv.Itoa(otherStatsStruct.PersonalBest),
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "Period Average",
				Value:  strconv.Itoa(otherStatsStruct.Average),
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "",
				Value:  "",
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "Participation",
				Value:  otherStatsStruct.ParticipationCountLabel,
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "Ratio",
				Value:  strconv.Itoa(otherStatsStruct.ParticipationPercentRatio) + "%",
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "",
				Value:  "",
				Inline: true,
			},
		)
	}

	return &discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{embeddedData},
		Files:  []*discordgo.File{{Name: "image.png", Reader: chartImageBinData}},
	}
}
