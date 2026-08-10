package main

import (
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

var s *discordgo.Session

func main() {
	log.Println("env", os.Getenv(data.EnvVarRedisHost), os.Getenv(data.EnvVarRedisPort))

	var err error
	s, err = discordgo.New("Bot " + os.Getenv(data.EnvVarDiscordToken))
	if err != nil {
		log.Fatalln("Cannot init discord session", err)
	}
	err = s.Open()
	if err != nil {
		log.Fatalln("Cannot open discord session", err)
	}
	defer s.Close()
	go func() {
		for {
			result, err := helpers.FetchAllMembers(s)
			if err != nil {
				log.Println("Failed to fetch members periodically")
			} else {
				resultArr, _ := json.Marshal(result)
				err = apiredis.DATA_DISCORD_MEMBERS.Set(apiredis.RedisDB, string(resultArr))
				log.Println("Set", apiredis.DATA_DISCORD_MEMBERS.Name, err)
			}
			time.Sleep(time.Minute * 30)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	log.Println("Press Ctrl+C to exit")
	<-stop
}
