package helpers

//lint:file-ignore ST1001 Dot imports by jet
import (
	"database/sql"

	. "github.com/go-jet/jet/v2/postgres"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"

	"github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/model"
)

func GetCharactersByDiscordID(db *sql.DB, discordID string) (*[]model.Characters, error) {
	stmt := SELECT(Characters.AllColumns).FROM(
		Characters,
	).WHERE(LOWER(String(discordID)).EQ(LOWER(Characters.DiscordUserID)))
	char := []model.Characters{}

	err := stmt.Query(db, &char)
	if err != nil {
		return nil, err
	}

	return &char, nil
}
