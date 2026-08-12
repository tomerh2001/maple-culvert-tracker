package helpers

//lint:file-ignore ST1001 Dot imports by jet
import (
	"math"
	"slices"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func GetWeeklySandbaggers(tenantID string, characters []string, rawDate string, weeks int, threshold float64) (sandbaggers *struct {
	NewSandbaggers       []data.WeeklySandbaggersStats
	ZeroScoreSandbaggers []string
}, err error) {
	sandbaggers =
		&struct {
			NewSandbaggers       []data.WeeklySandbaggersStats
			ZeroScoreSandbaggers []string
		}{}

	for _, v := range characters {
		stmt := SELECT(CharacterCulvertScores.CulvertDate.AS("culvert_date"), CharacterCulvertScores.Score.AS("score")).FROM(Characters.INNER_JOIN(CharacterCulvertScores, CharacterCulvertScores.CharacterID.EQ(Characters.ID))).WHERE(Characters.MapleCharacterName.EQ(String(v)).AND(Characters.GuildID.EQ(String(tenantID)))).LIMIT(int64(weeks)).ORDER_BY(CharacterCulvertScores.CulvertDate.DESC())

		scoresRawDb := []struct {
			Score       int
			CulvertDate time.Time
		}{}
		err = stmt.Query(db.DB, &scoresRawDb)
		if err != nil {
			return
		}

		if scoresRawDb[0].Score <= 0 {
			sandbaggers.ZeroScoreSandbaggers = append(sandbaggers.ZeroScoreSandbaggers, v)
			continue
		}

		chartData := []data.ChartMakerPoints{}
		for _, v := range scoresRawDb {
			d := data.ChartMakerPoints{
				Label:   v.CulvertDate.Format(time.DateOnly),
				RawDate: v.CulvertDate.Format(time.DateOnly),
				Score:   v.Score,
			}
			chartData = append(chartData, d)
		}
		slices.Reverse(chartData)

		var charaStats *data.CharacterStatistics
		charaStats, err = GetCharacterStatistics(db.DB, apiredis.RedisDB, tenantID, v, rawDate, chartData)
		if err != nil {
			return
		}

		latestWeekScore := chartData[len(chartData)-1].Score

		diffPbRatio := float64(latestWeekScore) / float64(charaStats.PersonalBest)
		if diffPbRatio <= threshold {
			secondaryStats := data.WeeklySandbaggersStats{
				Name:                 v,
				RawStats:             charaStats,
				Score:                latestWeekScore,
				DiffPbPercentage:     diffPercentage(latestWeekScore, charaStats.PersonalBest),
				DiffMedianPercentage: diffPercentage(latestWeekScore, charaStats.Median),
			}
			sandbaggers.NewSandbaggers = append(sandbaggers.NewSandbaggers, secondaryStats)
		}
	}

	slices.SortStableFunc(sandbaggers.NewSandbaggers, func(a data.WeeklySandbaggersStats, b data.WeeklySandbaggersStats) int {
		return a.DiffPbPercentage - b.DiffPbPercentage
	})

	err = nil
	return
}

func diffPercentage(v int, against int) int {
	if against == 0 {
		return 0
	}
	ratio := float64(v) / float64(against)
	ratio *= 100
	return int(math.Round(ratio))
}
