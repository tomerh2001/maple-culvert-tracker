package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

/*
main parses the discord_nickname from Valkey and automatically INSERT them to the PostgresDB with pattern: "preferred_name - char_name1/char_name2"

It is not recommended to run this on a repeat schedule because it cannot handle name changes, and will easily end up with duplicate names.
*/
func main() {
	val, err := apiredis.DATA_DISCORD_MEMBERS.Get(apiredis.RedisDB)
	if err != nil {
		log.Fatalln(err)
	}
	discordMembers := []data.WebGuildMember{}

	err = json.Unmarshal([]byte(val), &discordMembers)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("[DB] BEGIN TX")
	tx, err := db.DB.Begin()
	if err != nil {
		log.Fatalln(err)
		return
	}

	for _, m := range discordMembers {
		if !strings.Contains(m.DiscordNickname, " - ") {
			log.Println("[WARN] Skipping user", m, "because of missing dash separator")
			continue
		}
		// The line below is responsible for pattern matching with the discord server nickname
		log.Println("Processing", m.DiscordNickname)
		chars := strings.Split(strings.Split(m.DiscordNickname, " - ")[1], "/")
		for _, char := range chars {
			trimmedCharName := strings.Trim(char, " ")
			trimmedCharName = strings.Trim(trimmedCharName, ".")
			trimmedCharName = strings.Trim(trimmedCharName, " ⭐")
			log.Println("Onto", trimmedCharName)
			time.Sleep(time.Second)
			charData, err := helpers.FetchCharacterData(trimmedCharName, apiredis.OPTIONAL_CONF_MAPLE_REGION.GetWithDefault(apiredis.RedisDB, "na"))

			if err != nil {
				log.Println("[WARN]", trimmedCharName, "not found in official rankings and will be skipped")
				continue
			}
			log.Println(charData.CharacterName, "will be inserted")

			// insert with conflict handling
			rows, err := db.DB.Query("SELECT maple_character_name from characters where maple_character_name = $1", charData.CharacterName)

			if err != nil {
				log.Fatalln(err)
				return
			}

			if rows.Next() {
				rows.Close()
				log.Println("[WARN] Duplicate character name found in DB, skipping.")
				continue
			}
			rows.Close()

			// DB guaranteed safe to insert
			log.Println("INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, $2)", charData.CharacterName, m.DiscordUserID)
			_, err = tx.Exec("INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, $2)", charData.CharacterName, m.DiscordUserID)
			if err != nil {
				log.Fatalln(err)
				return
			}
		}
	}

	// Add switch for dry run
	if os.Getenv("DRY_RUN") != "" {
		log.Println("[WARN] Dry run enabled, no queries will be executed")
		log.Println("[DB] ROLLBACK")
		log.Println(tx.Rollback())
		return
	}
	log.Println("[DB] COMMIT")
	log.Println(tx.Commit())
}
