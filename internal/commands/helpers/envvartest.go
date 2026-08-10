package helpers

import (
	"log"
	"os"

	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

func EnvVarsTest() {
	log.Println("Ensuring all env vars are set")

	if os.Getenv(data.EnvVarDiscordToken) == "" {
		log.Fatalln("DISCORD_TOKEN is not set")
	}
	if os.Getenv(data.EnvVarDiscordGuildID) == "" {
		log.Fatalln("DISCORD_GUILD_ID is not set")
	}
	if os.Getenv(data.EnvVarJWTSecret) == "" {
		log.Fatalln("JWT_SECRET is not set")
	}
	if os.Getenv(data.EnvVarChartMakerHost) == "" {
		log.Fatalln("CHARTMAKER_HOST is not set")
	}
	if os.Getenv(data.EnvVarPostgresUser) == "" {
		log.Fatalln("POSTGRES_USER is not set")
	}
	if os.Getenv(data.EnvVarPostgresPassword) == "" {
		log.Fatalln("POSTGRES_PASSWORD is not set")
	}
	if os.Getenv(data.EnvVarClientPostgresPort) == "" {
		log.Fatalln("CLIENT_POSTGRES_PORT is not set")
	}
	if os.Getenv(data.EnvVarClientPostgresHost) == "" {
		log.Fatalln("CLIENT_POSTGRES_HOST is not set")
	}
	// In theory, this should not be needed. But should be set
	// if os.Getenv(data.EnvVarFrontendURL) == "" {
	//  log.Println("Frontend URL missing but should be set. This does not break anything and will work anyway without it.")
	// }
	if os.Getenv(data.EnvVarRedisHost) == "" {
		log.Fatalln("REDIS_HOST is not set")
	}
	if os.Getenv(data.EnvVarRedisPort) == "" {
		log.Fatalln("REDIS_PORT is not set")
	}

	log.Println("All env vars are set, continuing...")
}
