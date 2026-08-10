package helpers

//lint:file-ignore ST1001 Dot imports by jet
import (
	"database/sql"
	"log"
	"math"
	"slices"
	"strconv"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/valkey-io/valkey-go"
)

func GetCharacterStatistics(db *sql.DB, vk *valkey.Client, characterName string, date string, chartData []data.ChartMakerPoints) (*data.CharacterStatistics, error) {
	r := data.CharacterStatistics{}
	dateRaw, err := time.Parse(time.DateOnly, date)
	if err != nil {
		dateRaw, err = GetLatestResetDate(db)
		if err != nil {
			log.Println(err)
			return nil, err
		}
	}

	whereClause := LOWER(String(characterName)).EQ(LOWER(Characters.MapleCharacterName)).AND(CharacterCulvertScores.CulvertDate.LT_EQ(DateT(dateRaw)))
	if dateRaw.After(data.Date2mPatch) || dateRaw.Equal(data.Date2mPatch) {
		whereClause = whereClause.AND(CharacterCulvertScores.CulvertDate.GT_EQ(DateT(data.Date2mPatch)))
	}
	stmt := SELECT(MAX(CharacterCulvertScores.Score).AS("personal_best")).FROM(
		CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID)),
	).WHERE(whereClause)

	pb := struct {
		PersonalBest int64 `sql:"personal_best"`
	}{}

	err = stmt.Query(db, &pb)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	p := struct {
		Placement int32 `sql:"placement"`
	}{}
	if chartData[len(chartData)-1].Score != 0 {
		stmt = SELECT(COUNT(CharacterCulvertScores.Score).AS("placement")).FROM(CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).WHERE(CharacterCulvertScores.Score.GT_EQ(Int32(int32(chartData[len(chartData)-1].Score))).AND(CharacterCulvertScores.CulvertDate.EQ(DateT(dateRaw))))

		err = stmt.Query(db, &p)
		if err != nil {
			log.Println(err)
			return nil, err
		}
	}

	avg := int64(0)
	lastKnownGoodScore := int64(10)
	validCount := len(chartData)
	for _, v := range chartData {
		if v.Score == 0 {
			continue
		}
		lastKnownGoodScore = int64(v.Score)
		break
	}

	week1IsBefore2mPatch := false
	if culvertDate, _ := time.Parse(time.DateOnly, chartData[0].RawDate); culvertDate.Before(data.Date2mPatch) || culvertDate.Equal(data.Date2mPatch) {
		week1IsBefore2mPatch = true
	}
	rawScoresSlice := []float64{}
	for _, v := range chartData {
		rawScoresSlice = append(rawScoresSlice, float64(v.Score))
		avg += int64(v.Score)
		if week1IsBefore2mPatch {
			culvertDate, _ := time.Parse(time.DateOnly, v.RawDate)
			if culvertDate.After(data.Date2mPatch) || culvertDate.Equal(data.Date2mPatch) {
				week1IsBefore2mPatch = false // This ensures we fallback into the else block for the rest of the chartData, no need to re-parse the culvertDate again
				if v.Score <= 0 {
					validCount -= 1
					lastKnownGoodScore = int64(10)
				} else {
					lastKnownGoodScore = int64(v.Score)
				}
				continue
			}
		}
		threshold := GetSandbagThresholdScore(vk, lastKnownGoodScore)
		if int64(v.Score) < threshold {
			validCount -= 1
		}
		if int64(v.Score) > lastKnownGoodScore {
			lastKnownGoodScore = int64(v.Score)
		}

	}
	avg /= int64(len(chartData))

	r.Median = int(math.Round(calcMedianWithoutZero(rawScoresSlice)))
	r.Average = int(avg)
	r.ParticipationCountLabel = strconv.Itoa(validCount) + "/" + strconv.Itoa(len(chartData))
	r.ParticipationPercentRatio = int(math.Round(float64(validCount) / float64(len(chartData)) * 100))
	r.PersonalBest = int(pb.PersonalBest)
	r.GuildTopPlacement = int(p.Placement)

	return &r, nil
}

func calcMedianWithoutZero(data []float64) float64 {
	dataCopyWithoutZeroScores := []float64{}

	for _, v := range data {
		if v > float64(0) {
			dataCopyWithoutZeroScores = append(dataCopyWithoutZeroScores, v)
		}
	}
	slices.Sort(dataCopyWithoutZeroScores)

	var median float64
	l := len(dataCopyWithoutZeroScores)
	if l == 0 {
		return 0
	} else if l%2 == 0 {
		median = (dataCopyWithoutZeroScores[l/2-1] + dataCopyWithoutZeroScores[l/2]) / 2
	} else {
		median = dataCopyWithoutZeroScores[l/2]
	}

	return median
}
