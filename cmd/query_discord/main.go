package main

import (
	"log"
	"os"

	"github.com/bwmarrin/discordgo"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

var isConnected = make(chan struct{}, 1)

func main() {
	DiscordSession, _ := discordgo.New("Bot " + os.Getenv(data.EnvVarDiscordToken))

	DiscordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)
		isConnected <- struct{}{}
	})
	DiscordSession.Open()
	<-isConnected

	// Do stuff
	maxResults := 1000
	discordUsername := ""
	serverID := ""
	log.Println("Searching for keyword", discordUsername)

	members, _ := DiscordSession.GuildMembersSearch(serverID, discordUsername, maxResults)
	log.Println("Search results:", len(members))
	for _, m := range members {
		log.Println(m.User.Username)
	}

	DiscordSession.Close()
	log.Println("done")
}
