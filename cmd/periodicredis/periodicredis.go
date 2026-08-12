package main

import (
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
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
	// Backstop refresher: the bot process refreshes member caches itself (on
	// Ready and lazily); this keeps every tenant's cache warm even when the
	// bot is wedged. Tenants come from the bot-maintained DATA_ACTIVE_GUILDS
	// registry (falling back to the env guild list when it is empty), so
	// freshly-added servers are covered without a redeploy.
	go func() {
		for {
			for _, tenant := range helpers.ActiveTenants(apiredis.RedisDB) {
				if err := helpers.RefreshMemberCache(s, tenant); err != nil {
					log.Println("Failed to fetch members periodically for tenant", tenant, ":", err)
				} else {
					log.Println("Set", apiredis.DATA_DISCORD_MEMBERS.Name, "for tenant", tenant)
				}
			}
			time.Sleep(time.Minute * 30)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	log.Println("Press Ctrl+C to exit")
	<-stop
}
