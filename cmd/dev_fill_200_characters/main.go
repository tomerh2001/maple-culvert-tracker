package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"

	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func main() {
	helpers.EnvVarsTest()

	// new http client
	client := &http.Client{}

	// for loop page through characters in maple rankings
	page := 1
	characterCount := 0
	allCharacters := []string{}

	for characterCount < 200 {
		req, err := http.NewRequest("GET", "https://www.nexon.com/api/maplestory/no-auth/v1/ranking/na", nil)
		if err != nil {
			log.Fatalln(err)
		}

		q := req.URL.Query()
		q.Add("type", "world")
		q.Add("id", "45")
		q.Add("page_index", strconv.Itoa(page))
		req.URL.RawQuery = q.Encode()

		log.Println("REQ Page", page)
		rawResp, err := client.Do(req)
		if err != nil {
			log.Fatalln(err)
		}
		rawbody, err := io.ReadAll(rawResp.Body)
		if err != nil {
			log.Fatalln(err)
		}
		defer rawResp.Body.Close()
		resp := data.PlayerRankingResponse{}

		err = json.Unmarshal(rawbody, &resp)
		if err != nil {
			log.Fatalln(err)
		}
		for _, player := range resp.Ranks {
			characterCount++
			log.Println("Character", characterCount, "of 200", player.CharacterName)
			allCharacters = append(allCharacters, player.CharacterName)
		}
		page += len(resp.Ranks)
	}

	// write all characters to database
	for _, character := range allCharacters {
		log.Println("INSERTING", character, "into database")
		_, err := db.DB.Exec("INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, $2) ON CONFLICT (maple_character_name) DO NOTHING;", character, "2")
		if err != nil {
			log.Fatalln(err)
		}
	}
	log.Println("DONE")

}
