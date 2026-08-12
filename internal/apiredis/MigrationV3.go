package apiredis

import (
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

func MigrationV3(rdb *redis.Client) error {
	return OPTIONAL_CONF_SUBMIT_SCORES_SHOW_RATS.For(data.PrimaryGuildID()).Set(rdb, "false")
}
