package helpers

//lint:file-ignore ST1001 Dot imports by jet

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	"github.com/jedib0t/go-pretty/v6/table"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

const PersonalBestRankWeeks = 52

// CharacterScoreRow is one weekly culvert score used for PB rank reconstruction.
type CharacterScoreRow struct {
	CharacterName string
	CulvertDate   time.Time
	Score         int32
}

// PersonalBestRankMetric is one character's current PB leaderboard metrics.
type PersonalBestRankMetric struct {
	MapleCharacterName string
	PersonalBest       int32
	Pos                int
	Streak             int
	DeltaPos           int
	HasPrevRank        bool
}

// FetchCharacterScoresSince loads weekly scores for the given characters on/after since.
func FetchCharacterScoresSince(db *sql.DB, characterIDs []int64, since time.Time) ([]CharacterScoreRow, error) {
	if len(characterIDs) == 0 {
		return nil, nil
	}

	charIDsInExpr := make([]Expression, 0, len(characterIDs))
	for _, id := range characterIDs {
		charIDsInExpr = append(charIDsInExpr, Int64(id))
	}

	// Use "Type.field" aliases so go-jet can map into the named CharacterScoreRow type.
	stmt := SELECT(
		Characters.MapleCharacterName.AS("CharacterScoreRow.character_name"),
		CharacterCulvertScores.CulvertDate.AS("CharacterScoreRow.culvert_date"),
		CharacterCulvertScores.Score.AS("CharacterScoreRow.score"),
	).FROM(
		CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID)),
	).WHERE(
		CharacterCulvertScores.CharacterID.IN(charIDsInExpr...).AND(
			CharacterCulvertScores.CulvertDate.GT_EQ(DateT(since)),
		),
	).ORDER_BY(
		CharacterCulvertScores.CulvertDate.ASC(),
		Characters.MapleCharacterName.ASC(),
	)

	dest := []CharacterScoreRow{}
	if err := stmt.Query(db, &dest); err != nil {
		return nil, err
	}
	return dest, nil
}

// ComputePersonalBestRankMetrics rebuilds the personal-bests leaderboard over the last
// maxWeeks distinct culvert dates and returns current position, streak, and week-over-week Δpos.
func ComputePersonalBestRankMetrics(scores []CharacterScoreRow, maxWeeks int) []PersonalBestRankMetric {
	if len(scores) == 0 || maxWeeks <= 0 {
		return nil
	}

	weeks := distinctCulvertDates(scores)
	if len(weeks) > maxWeeks {
		weeks = weeks[len(weeks)-maxWeeks:]
	}
	windowStart := weeks[0]

	scoresByWeek := map[time.Time][]CharacterScoreRow{}
	for _, row := range scores {
		d := truncateDate(row.CulvertDate)
		if d.Before(windowStart) {
			continue
		}
		scoresByWeek[d] = append(scoresByWeek[d], row)
	}

	best := map[string]int32{}
	posHistory := map[string][]int{}

	for _, week := range weeks {
		for _, row := range scoresByWeek[week] {
			if prev, ok := best[row.CharacterName]; !ok || row.Score > prev {
				best[row.CharacterName] = row.Score
			}
		}
		ranking := rankByPersonalBest(best)
		for name, pos := range ranking {
			posHistory[name] = append(posHistory[name], pos)
		}
	}

	metrics := make([]PersonalBestRankMetric, 0, len(best))
	for name, pb := range best {
		hist := posHistory[name]
		if len(hist) == 0 {
			continue
		}
		currPos := hist[len(hist)-1]
		streak := 1
		for i := len(hist) - 2; i >= 0; i-- {
			if hist[i] != currPos {
				break
			}
			streak++
		}

		m := PersonalBestRankMetric{
			MapleCharacterName: name,
			PersonalBest:       pb,
			Pos:                currPos,
			Streak:             streak,
		}
		if len(hist) >= 2 {
			m.HasPrevRank = true
			m.DeltaPos = hist[len(hist)-2] - currPos
		}
		metrics = append(metrics, m)
	}

	slices.SortStableFunc(metrics, func(a, b PersonalBestRankMetric) int {
		if a.PersonalBest != b.PersonalBest {
			return int(b.PersonalBest) - int(a.PersonalBest)
		}
		return strings.Compare(a.MapleCharacterName, b.MapleCharacterName)
	})

	return metrics
}

// FormatPersonalBestStreak returns the streak label; "over 1 year" at/above 52 weeks.
func FormatPersonalBestStreak(streak int) string {
	if streak >= PersonalBestRankWeeks {
		return "over 1 year"
	}
	return strconv.Itoa(streak)
}

// FormatPersonalBestDeltaPos formats week-over-week position change.
func FormatPersonalBestDeltaPos(deltaPos int, hasPrevRank bool) string {
	if !hasPrevRank {
		return "—"
	}
	if deltaPos > 0 {
		return fmt.Sprintf("+%d", deltaPos)
	}
	return strconv.Itoa(deltaPos)
}

// FormatPersonalBestPos formats rank with optional delta and streak beside it.
// Zero delta is omitted; new entrants show (—). Streak is shown only when > 1.
func FormatPersonalBestPos(pos int, deltaPos int, hasPrevRank bool, streak int) string {
	out := strconv.Itoa(pos)
	if delta := FormatPersonalBestDeltaPos(deltaPos, hasPrevRank); delta != "0" {
		out = fmt.Sprintf("%s (%s)", out, delta)
	}
	if streak > 1 {
		out = fmt.Sprintf("%s ×%s", out, FormatPersonalBestStreak(streak))
	}
	return out
}

// PersonalBestRankWindowStart returns the score query floor: Date2mPatch.
func PersonalBestRankWindowStart() time.Time {
	return data.Date2mPatch
}

// LoadPersonalBestRankMetrics loads active-character PB rank metrics for the rolling window.
func LoadPersonalBestRankMetrics(db *sql.DB, rdb *redis.Client) ([]PersonalBestRankMetric, error) {
	chars, err := GetActiveCharacters(rdb, db)
	if err != nil {
		return nil, err
	}
	if len(*chars) == 0 {
		return nil, nil
	}

	charIDs := make([]int64, 0, len(*chars))
	for _, v := range *chars {
		charIDs = append(charIDs, v.ID)
	}

	scores, err := FetchCharacterScoresSince(db, charIDs, PersonalBestRankWindowStart())
	if err != nil {
		return nil, err
	}
	return ComputePersonalBestRankMetrics(scores, PersonalBestRankWeeks), nil
}

// FormatPersonalBestsTable renders the personal-bests leaderboard as an ASCII table.
func FormatPersonalBestsTable(metrics []PersonalBestRankMetric) string {
	return FormatNthColumnList(1, metrics, table.Row{"Rank", "Character", "Personal Best"}, func(row PersonalBestRankMetric, idx int) table.Row {
		return table.Row{
			FormatPersonalBestPos(row.Pos, row.DeltaPos, row.HasPrevRank, row.Streak),
			row.MapleCharacterName,
			row.PersonalBest,
		}
	})
}

func distinctCulvertDates(scores []CharacterScoreRow) []time.Time {
	seen := map[time.Time]struct{}{}
	for _, row := range scores {
		seen[truncateDate(row.CulvertDate)] = struct{}{}
	}
	weeks := make([]time.Time, 0, len(seen))
	for d := range seen {
		weeks = append(weeks, d)
	}
	slices.SortFunc(weeks, func(a, b time.Time) int {
		return a.Compare(b)
	})
	return weeks
}

func rankByPersonalBest(best map[string]int32) map[string]int {
	type entry struct {
		name  string
		score int32
	}
	entries := make([]entry, 0, len(best))
	for name, score := range best {
		entries = append(entries, entry{name: name, score: score})
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		if a.score != b.score {
			return int(b.score) - int(a.score)
		}
		return strings.Compare(a.name, b.name)
	})
	out := make(map[string]int, len(entries))
	for i, e := range entries {
		out[e.name] = i + 1
	}
	return out
}

func truncateDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
