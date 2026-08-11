package apiredis

import (
	"context"
	"errors"
	"os"

	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// ErrNoRedis reports that the redis client was never initialized (e.g. in
// tests without a redis server). GetWithDefault turns it into the default.
var ErrNoRedis = errors.New("redis client not initialized")

type editableType string

const (
	EditableTypeString         editableType = "string"
	EditableTypeUInt           editableType = "uint"
	EditableTypeFloat64        editableType = "float64"
	EditableTypeDiscordRole    editableType = "discord_role"
	EditableTypeDiscordChannel editableType = "discord_channel"
	EditableTypeSelection      editableType = "selection"
	EditableTypeNone           editableType = "none"
	EditableTypeBool           editableType = "bool"
)

type redisInternalKey struct {
	Name         string
	EditableType editableType
	Multiple     bool
}

func (k redisInternalKey) ToString() string {
	return k.Name
}
func (k redisInternalKey) Get(rdb *redis.Client) (string, error) {
	if rdb == nil || *rdb == nil {
		return "", ErrNoRedis
	}
	q := (*rdb).Do(context.Background(), (*rdb).B().Get().Key(os.Getenv(data.EnvVarDiscordGuildID)+"_"+k.ToString()).Build())
	if err := q.Error(); err != nil {
		return "", err
	}
	return q.ToString()
}
func (k redisInternalKey) Set(rdb *redis.Client, v string) error {
	if rdb == nil || *rdb == nil {
		return ErrNoRedis
	}
	return (*rdb).Do(context.Background(), (*rdb).B().Set().Key(os.Getenv(data.EnvVarDiscordGuildID)+"_"+k.ToString()).Value(v).Build()).Error()
}
func (k redisInternalKey) GetWithDefault(rdb *redis.Client, defaultVal string) string {
	v, err := k.Get(rdb)
	if err == nil {
		return v
	}
	return defaultVal
}

type redisInternalValue string

func (k redisInternalValue) ToString() string {
	return string(k)
}
