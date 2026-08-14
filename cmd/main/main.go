package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands"
	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

func main() {
	helpers.EnvVarsTest()
	helpers.PreflightTest()
	stop := make(chan os.Signal, 1)

	log.Println("Starting discord bot")
	var err error
	api.DiscordSession, err = helpers.CreateBotSessionWithCommands(commands.Commands, commands.CommandHandlers)
	if err != nil {
		log.Fatalf("Invalid bot parameters: %v", err)
	}
	commands.AddStartupWeeklyRefresh(api.DiscordSession)
	err = api.DiscordSession.Open()
	if err != nil {
		log.Fatalf("Cannot open the session: %v", err)
	}

	go func() {
		r := api.NewRouter()
		port := os.Getenv("BACKEND_HTTP_PORT")
		if port == "" {
			port = "8080"
		}
		r.Run("0.0.0.0:" + port)
		// gin router no need to close anything
	}()

	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	log.Println("Press Ctrl+C to exit")
	<-stop
	if api.DiscordSession != nil {
		err = api.DiscordSession.Close()
		if err != nil {
			log.Println("Cannot close the session:", err.Error())
		}
	}
	log.Println("Bye")
}
