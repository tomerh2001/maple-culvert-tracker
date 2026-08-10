package helpers

import (
	"database/sql"
	"log"
	"strconv"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/model"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/valkey-io/valkey-go"
)

// I was overcaffeinated when writing this

func GetStinkyRats(db *sql.DB, vk *valkey.Client, characters []model.Characters, date string, weeks int, threshold float64, typeshit string) (foundYouYoureFucked []struct {
	SixSeven string
	LWeeks   int
	WWeeks   int
}, err error) {
	foundYouYoureFucked = []struct {
		SixSeven string
		LWeeks   int
		WWeeks   int
	}{}

	for _, char := range characters {
		charID := char.ID
		charName := char.MapleCharacterName

		additionalWhere := ""
		if date != "" {
			additionalWhere += " AND character_culvert_scores.culvert_date <= $2"
		}
		// query score
		sqlq := `SELECT culvert_date, score from (SELECT character_culvert_scores.culvert_date as culvert_date, character_culvert_scores.score as score FROM characters INNER JOIN character_culvert_scores ON character_culvert_scores.character_id = characters.id WHERE characters.id = $1` + additionalWhere + ` ORDER BY character_culvert_scores.culvert_date DESC LIMIT ` + strconv.Itoa(weeks) + ") ORDER BY culvert_date ASC"
		// Concat here is not an sql injection because I trust discord sanitizing the `weeks` variable
		var stmt *sql.Stmt
		stmt, err = db.Prepare(sqlq)
		if err != nil {
			log.Println("Failed 1st prepare at get_stinky_rats", err)
			// content := "Failed 1st prepare sniffOutRats"
			// s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			// 	Content: &content,
			// })
			return
		}
		defer stmt.Close()
		args := []any{charID}
		if date != "" {
			args = append(args, date)
		}
		var rows *sql.Rows
		rows, err = stmt.Query(args...)
		if err != nil {
			log.Println("Query at culvert command", err)
			// content := "Failed query character data sniffOutRats"
			// s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			// 	Content: &content,
			// })
			return
		}
		defer rows.Close()

		sandbaggedScores := 0
		rollerCoasterUphillCount := 0
		scoreAtFloor := false
		var week1IsBefore2mPatch *bool
		personalWeeks := 0
		lastKnownGoodScore := int64(-1)

		for rows.Next() {
			personalWeeks++
			// ascending dates
			rawDate := ""
			score := int64(-1)
			rows.Scan(&rawDate, &score)
			rawDate = rawDate[:10]

			if lastKnownGoodScore <= 10 {
				lastKnownGoodScore = score
				if sniffOutRatsScoreIsSandbag(vk, typeshit, lastKnownGoodScore, score) {
					scoreAtFloor = true
				}
			}

			if week1IsBefore2mPatch == nil {
				culvertDate, _ := time.Parse(time.DateOnly, rawDate)
				b := culvertDate.Before(data.Date2mPatch) || culvertDate.Equal(data.Date2mPatch)
				week1IsBefore2mPatch = &b
			}

			if *week1IsBefore2mPatch {
				culvertDate, _ := time.Parse(time.DateOnly, rawDate)
				if culvertDate.After(data.Date2mPatch) || culvertDate.Equal(data.Date2mPatch) {
					*week1IsBefore2mPatch = false // This ensures we fallback into the else block for the rest of the chartData, no need to re-parse the culvertDate again
					lastKnownGoodScore = int64(score)
				}
			}

			if sniffOutRatsScoreIsSandbag(vk, typeshit, lastKnownGoodScore, score) {
				sandbaggedScores++
				if !scoreAtFloor {
					scoreAtFloor = true
				}
				continue
			}

			// not sandbag here onwards
			if scoreAtFloor {
				rollerCoasterUphillCount++
				scoreAtFloor = false
			}
			if score > lastKnownGoodScore {
				lastKnownGoodScore = score
			}
		}
		if personalWeeks == 0 {
			// next char
			continue
		}

		if float64(rollerCoasterUphillCount)/float64(personalWeeks) >= threshold {
			foundYouYoureFucked = append(foundYouYoureFucked, struct {
				SixSeven string
				LWeeks   int
				WWeeks   int
			}{
				SixSeven: charName,
				LWeeks:   rollerCoasterUphillCount,
				WWeeks:   personalWeeks,
			})
		}
	}

	err = nil
	return
}

func sniffOutRatsScoreIsSandbag(vk *valkey.Client, typeshit string, pbScore int64, score int64) bool {
	if typeshit == "zero" {
		return score == int64(0)
	}
	return score < GetSandbagThresholdScore(vk, pbScore)
}
