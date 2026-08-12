package apiredis

import (
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

func MigrationV5(vk *redis.Client) error {
	return OPTIONAL_CONF_MONTHLY_IMPROVEMENT_THRESHOLD.For(data.PrimaryGuildID()).Set(vk, "10")
}
