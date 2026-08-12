package helpers

//lint:file-ignore ST1001 Dot imports by jet
import (
	"database/sql"
	"log"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	redis "github.com/valkey-io/valkey-go"
)

func RunSunToWedFixes(db *sql.DB, rdb *redis.Client) error {
	res, err := apiredis.DATA_FIXES_SUN_TO_WED.Global().Get(rdb)
	if err != nil && err != redis.Nil {
		log.Println("Failed to get redis val "+apiredis.DATA_FIXES_SUN_TO_WED.ToString(), err)
		return err
	}
	if res == "true" {
		log.Println("Already ran SunToWedFixes")
		return nil
	}

	// select all dates that are sundays after 2024-08-25
	stmt := SELECT(CharacterCulvertScores.CulvertDate.AS("culvert_date")).FROM(CharacterCulvertScores).WHERE(CharacterCulvertScores.CulvertDate.GT(Date(2024, 8, 25))).GROUP_BY(CharacterCulvertScores.CulvertDate)

	dates := []struct {
		CulvertDate time.Time
	}{}
	err = stmt.Query(db, &dates)
	if err != nil {
		log.Println("Failed to RunSunToWedFixes getting culvert date", err)
		return err
	}

	wrongDates := []struct {
		CulvertDate time.Time
	}{}

	for _, v := range dates {
		if v.CulvertDate.Weekday() != GetCulvertResetDay(v.CulvertDate) {
			wrongDates = append(wrongDates, v)
		}
	}

	for _, v := range wrongDates {
		// convert to wednesday
		rawDate := GetCulvertResetDate(v.CulvertDate)
		log.Println("Fixing date", v.CulvertDate, "to", rawDate)
		updateStmt := CharacterCulvertScores.UPDATE(CharacterCulvertScores.CulvertDate).SET(Date(rawDate.Year(), rawDate.Month(), rawDate.Day())).WHERE(CharacterCulvertScores.CulvertDate.EQ(DateT(v.CulvertDate)))
		_, err := updateStmt.Exec(db)
		if err != nil {
			log.Println("Failed to RunSunToWedFixes update date for wrong dates", err)
			return err
		}
	}

	log.Println("RunSunToWedFixes done")
	err = apiredis.DATA_FIXES_SUN_TO_WED.Global().Set(rdb, "true")
	if err != nil {
		log.Println("Failed to set redis val "+apiredis.DATA_FIXES_SUN_TO_WED.ToString(), err)
		return err
	}
	return nil
}
