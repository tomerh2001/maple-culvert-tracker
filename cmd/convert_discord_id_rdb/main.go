package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"slices"
	"strings"

	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

type tconfig struct {
	DefaultValues map[string]string `json:"default_values"`
}

func main() {
	fromID := flag.String("from", "", "from discord id")
	toID := flag.String("to", "", "to discord id")

	flag.Parse()

	if *fromID == "" {
		panic("from flag is required")
	}
	if *toID == "" {
		*toID = os.Getenv(data.EnvVarDiscordGuildID)
	}
	if *toID == "" {
		panic("to flag is required or set env var DISCORD_GUILD_ID")
	}
	log.Println("Converting discord id from", *fromID, "to", *toID)

	f, err := os.Open("convert_discord_id_config.json")
	if err != nil {
		log.Fatalln(err)
	}
	defer f.Close()
	jsonRaw, err := io.ReadAll(f)
	if err != nil {
		log.Fatalln(err)
	}
	config := tconfig{}
	err = json.Unmarshal(jsonRaw, &config)
	if err != nil {
		log.Fatalln(err)
	}

	defaultKeys := []interface {
		ToString() string
	}{apiredis.CONF_DISCORD_ADMIN_CHANNEL_ID, apiredis.CONF_DISCORD_GUILD_ROLE_IDS, apiredis.CONF_DISCORD_MEMBERS_MAIN_CHANNEL_ID}

	for _, v := range defaultKeys {
		if v2, ok := config.DefaultValues[v.ToString()]; !ok || v2 == "" {
			log.Fatalln("Default value ", v.ToString(), "not found")
		}
	}

	helpers.EnvVarsTest()
	defer (*apiredis.RedisDB).Close()

	keysRaw, err := (*apiredis.RedisDB).Do(context.Background(), (*apiredis.RedisDB).B().Keys().Pattern(*fromID+"_*").Build()).ToArray()
	if err != nil {
		panic(err)
	}
	var keys []string = make([]string, len(keysRaw))
	for i, key := range keysRaw {
		keyVal, err := key.ToString()
		if err != nil {
			panic(err)
		}
		keys[i] = keyVal
	}

	log.Println("Found", len(keys), "keys to convert", keys)

	for _, v := range keys {
		oldKey := v
		keyName := oldKey[len(*fromID)+1:]
		newKey := *toID + "_" + keyName

		if _, found := slices.BinarySearchFunc(defaultKeys, keyName, func(e interface{ ToString() string }, v string) int {
			return strings.Compare(e.ToString(), v)
		}); found {
			log.Println("Deleting value for", oldKey)
			err := (*apiredis.RedisDB).Do(context.Background(), (*apiredis.RedisDB).B().Del().Key(oldKey).Build()).Error()
			if err != nil {
				log.Fatalln(err)
			}
			log.Println("Using config's default value of "+config.DefaultValues[keyName]+" for", newKey)
			err = (*apiredis.RedisDB).Do(context.Background(), (*apiredis.RedisDB).B().Set().Key(newKey).Value(config.DefaultValues[keyName]).Build()).Error()
			if err != nil {
				log.Fatalln(err)
			}
		} else {
			log.Println("Renaming", oldKey, "to", newKey)
			err := (*apiredis.RedisDB).Do(context.Background(), (*apiredis.RedisDB).B().Rename().Key(oldKey).Newkey(newKey).Build()).Error()
			if err != nil {
				log.Println("Failed to rename" + oldKey + " to " + newKey)
				panic(err)
			}
		}
	}
}
