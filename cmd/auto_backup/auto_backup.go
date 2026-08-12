package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

var s *discordgo.Session

func startBackup(s *discordgo.Session, stopChan chan struct{}) {
	// cmd := exec.Command("/usr/local/bin/pg_dump", "-h", "db16", "-U", os.Getenv(data.EnvVarPostgresUser), "-d", os.Getenv(data.EnvVarPostgresDB), "-p", "5432") // Only use this line outside of docker container
	cmd := exec.Command("/usr/local/bin/pg_dump", "-h", os.Getenv(data.EnvVarClientPostgresHost), "-U", os.Getenv(data.EnvVarPostgresUser), "-d", os.Getenv(data.EnvVarPostgresDB), "-p", os.Getenv(data.EnvVarClientPostgresPort))
	cmd.Env = append(cmd.Env, "PGPASSWORD="+os.Getenv(data.EnvVarPostgresPassword))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.Println(stdout.String())
		log.Println(stderr.String())
		panic(err)
	}

	_, err = s.ChannelMessageSendComplex(apiredis.CONF_DISCORD_ADMIN_CHANNEL_ID.For(data.PrimaryGuildID()).GetWithDefault(apiredis.RedisDB, os.Getenv("DISCORD_REMINDER_CHANNEL_ID")), &discordgo.MessageSend{
		Content: "Automatic PostgreSQL Database backup " + time.Now().Format(time.DateOnly),
		Files:   []*discordgo.File{{Name: "dump_" + time.Now().Format(time.DateOnly) + ".sql", Reader: strings.NewReader(stdout.String())}},
	})
	if err != nil {
		s.ChannelMessageSend(apiredis.CONF_DISCORD_ADMIN_CHANNEL_ID.For(data.PrimaryGuildID()).GetWithDefault(apiredis.RedisDB, os.Getenv("DISCORD_REMINDER_CHANNEL_ID")), "Failed to backup PostgreSQL database")
		log.Println(err)
	}

	status := (*apiredis.RedisDB).Do(context.Background(), ((*apiredis.RedisDB).B().Save().Build()))
	if err := status.Error(); err != nil {
		panic(err)
	}

	f, err := os.Open("/valkey_data/dump.rdb")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	_, err = s.ChannelMessageSendComplex(apiredis.CONF_DISCORD_ADMIN_CHANNEL_ID.For(data.PrimaryGuildID()).GetWithDefault(apiredis.RedisDB, os.Getenv("DISCORD_REMINDER_CHANNEL_ID")), &discordgo.MessageSend{
		Content: "Automatic Valkey Database backup " + time.Now().Format(time.DateOnly),
		Files:   []*discordgo.File{{Name: "dump_" + time.Now().Format(time.DateOnly) + ".rdb", Reader: f}},
	})

	if err != nil {
		s.ChannelMessageSend(apiredis.CONF_DISCORD_ADMIN_CHANNEL_ID.For(data.PrimaryGuildID()).GetWithDefault(apiredis.RedisDB, os.Getenv("DISCORD_REMINDER_CHANNEL_ID")), "Failed to backup Valkey database")
		log.Println(err)
	}

	stopChan <- struct{}{}
}

func main() {
	var err error
	s, err = discordgo.New("Bot " + os.Getenv(data.EnvVarDiscordToken))
	if err != nil {
		log.Fatalf("Invalid bot parameters: %v", err)
	}
	stop := make(chan struct{}, 1)
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)
		go startBackup(s, stop)
	})
	err = s.Open()
	if err != nil {
		log.Fatalf("Cannot open the session: %v", err)
	}
	defer s.Close()
	<-stop
}
