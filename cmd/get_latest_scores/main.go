package main

//lint:file-ignore ST1001 Dot imports by jet
import (
	"encoding/json"
	"log"
	"time"

	. "github.com/go-jet/jet/v2/postgres"
	_ "github.com/joho/godotenv/autoload"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func main() {
	helpers.EnvVarsTest()
	var err error
	log.Println("Getting latest scores, only use this for development purposes...")

	stmt := SELECT(CharacterCulvertScores.CulvertDate.AS("culvert_date")).FROM(CharacterCulvertScores).ORDER_BY(CharacterCulvertScores.CulvertDate.DESC()).LIMIT(1)

	culvertDateResult := []struct {
		CulvertDate time.Time
	}{}
	err = stmt.Query(db.DB, &culvertDateResult)
	if err != nil {
		panic(err)
	}

	culvertDate := culvertDateResult[0].CulvertDate

	log.Println("Latest culvert date is", culvertDate.Format(time.DateOnly))

	stmt = SELECT(Characters.MapleCharacterName.AS("name"), CharacterCulvertScores.Score.AS("score")).FROM(Characters.INNER_JOIN(CharacterCulvertScores, Characters.ID.EQ(CharacterCulvertScores.CharacterID))).WHERE(Characters.GuildID.EQ(String(data.PrimaryGuildID())).AND(Characters.DiscordUserID.NOT_EQ(String(data.INTERNAL_DISCORD_ID_UNTRACKED))).AND(CharacterCulvertScores.Score.GT(Int(0)).AND(CharacterCulvertScores.CulvertDate.EQ(DateT((culvertDate)))))).ORDER_BY(CharacterCulvertScores.Score.DESC())

	result := []struct {
		Name  string
		Score int
	}{}
	err = stmt.Query(db.DB, &result)
	if err != nil {
		panic(err)
	}

	m := map[string]int{}
	for _, r := range result {
		m[r.Name] = r.Score
	}

	jsonBody, _ := json.Marshal(m)

	log.Println(string(jsonBody))

}
