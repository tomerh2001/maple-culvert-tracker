package apiredis

import (
	"log"
	"os"

	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

var RedisDB *redis.Client

func init() {
	redisDB, err := redis.NewClient(redis.ClientOption{
		InitAddress: []string{os.Getenv(data.EnvVarRedisHost) + ":" + os.Getenv(data.EnvVarRedisPort)},
		Password:    "", // always use no pw, this is a private redis.
		SelectDB:    0,
	})
	if err != nil {
		log.Println("Failed to initialize Redis Client", err)
		log.Println("Ensure you will pass the preflight check later on!")
	}
	RedisDB = &redisDB
}
