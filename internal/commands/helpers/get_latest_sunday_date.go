package helpers

//lint:file-ignore ST1001 Dot imports by jet
import (
	"database/sql"
	"log"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
)

func GetLatestResetDate(db *sql.DB, tenantID string) (time.Time, error) {
	stmt := SELECT(CharacterCulvertScores.CulvertDate.AS("culvert_date")).FROM(CharacterCulvertScores.INNER_JOIN(Characters, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).WHERE(Characters.GuildID.EQ(String(tenantID))).GROUP_BY(CharacterCulvertScores.CulvertDate).ORDER_BY(CharacterCulvertScores.CulvertDate.DESC()).LIMIT(1)

	v := struct {
		CulvertDate time.Time
	}{}

	err := stmt.Query(db, &v)
	if err != nil {
		log.Println(err)
		return v.CulvertDate, err
	}

	return v.CulvertDate, nil
}
