package apiredis

import (
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

func MigrationV4(vk *redis.Client) error {
	return OPTIONAL_CONF_SANDBAG_THRESHOLD.For(data.PrimaryGuildID()).Set(vk, "0.7")
}
