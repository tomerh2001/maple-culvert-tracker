package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands.AddWeeklyRecaps(ctx, api.DiscordSession)
	err = api.DiscordSession.Open()
	if err != nil {
		log.Fatalf("Cannot open the session: %v", err)
	}
	if err := api.StartImageSubmissions(ctx); err != nil {
		log.Fatal(err)
	}

	go func() {
		r := api.NewRouter()
		port := os.Getenv("BACKEND_HTTP_PORT")
		if port == "" {
			port = "8080"
		}
		server := &http.Server{Addr: "0.0.0.0:" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Println("Internal API server stopped:", err)
		}
		// gin router no need to close anything
	}()

	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	log.Println("Press Ctrl+C to exit")
	<-stop
	cancel()
	if api.DiscordSession != nil {
		err = api.DiscordSession.Close()
		if err != nil {
			log.Println("Cannot close the session:", err.Error())
		}
	}
	log.Println("Bye")
}
