package apiredis

import (
	"context"
	"errors"
	"time"

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

// perGuildKeyNames are settings scoped to the ACTUAL invoking guild rather
// than shared across the whole tenant. Everything else (roles, submitters,
// thresholds) is tenant-shared, but a channel only exists inside one server:
// two servers sharing one dataset each announce into their OWN channel.
var perGuildKeyNames = map[string]bool{
	"CONF_DISCORD_WEEKLY_CHANNEL_ID":     true,
	"CONF_DISCORD_SCREENSHOT_CHANNEL_ID": true,
}

// IsPerGuild reports whether this setting is scoped per invoking guild instead
// of per tenant (see perGuildKeyNames).
func (k redisInternalKey) IsPerGuild() bool {
	return perGuildKeyNames[k.Name]
}

// ScopedKey is a redisInternalKey bound to one tenant's key space. Every read
// or write goes through a ScopedKey, so no call site can touch redis without
// first saying WHOSE data it means: k.For(tenantID) for tenant data,
// k.Global() for the few process-global keys.
type ScopedKey struct {
	key    redisInternalKey
	tenant string
}

// For scopes the key to one tenant (see data.TenantID). The stored key is
// "<tenant>_<name>" - for the default tenant that is byte-identical to the
// pre-tenant "<primary guild id>_<name>" scheme, so existing data is reused
// as-is.
func (k redisInternalKey) For(tenantID string) ScopedKey {
	return ScopedKey{key: k, tenant: tenantID}
}

// Global scopes a PROCESS-GLOBAL key (schema versions, one-shot data fixes,
// command registration status, the active-guild registry): state about the
// deployment itself, not about any one server's data. Historically these were
// stored under the primary guild's prefix like everything else; Global keeps
// those exact key strings (so nothing re-runs or is lost) while naming the
// intent honestly at the call site.
func (k redisInternalKey) Global() ScopedKey {
	return ScopedKey{key: k, tenant: data.PrimaryGuildID()}
}

// RedisKey returns the full key string stored in redis.
func (s ScopedKey) RedisKey() string {
	return s.tenant + "_" + s.key.Name
}

func (s ScopedKey) Get(rdb *redis.Client) (string, error) {
	if rdb == nil || *rdb == nil {
		return "", ErrNoRedis
	}
	q := (*rdb).Do(context.Background(), (*rdb).B().Get().Key(s.RedisKey()).Build())
	if err := q.Error(); err != nil {
		return "", err
	}
	return q.ToString()
}

func (s ScopedKey) Set(rdb *redis.Client, v string) error {
	if rdb == nil || *rdb == nil {
		return ErrNoRedis
	}
	return (*rdb).Do(context.Background(), (*rdb).B().Set().Key(s.RedisKey()).Value(v).Build()).Error()
}

// SetEx stores the value with an expiry - used for short-lived confirmation
// keys (the resubmit-to-confirm overwrite and /reset-week flows).
func (s ScopedKey) SetEx(rdb *redis.Client, v string, ttl time.Duration) error {
	if rdb == nil || *rdb == nil {
		return ErrNoRedis
	}
	return (*rdb).Do(context.Background(), (*rdb).B().Set().Key(s.RedisKey()).Value(v).ExSeconds(int64(ttl.Seconds())).Build()).Error()
}

// Del removes the key (no-op when absent).
func (s ScopedKey) Del(rdb *redis.Client) error {
	if rdb == nil || *rdb == nil {
		return ErrNoRedis
	}
	return (*rdb).Do(context.Background(), (*rdb).B().Del().Key(s.RedisKey()).Build()).Error()
}

func (s ScopedKey) GetWithDefault(rdb *redis.Client, defaultVal string) string {
	v, err := s.Get(rdb)
	if err == nil {
		return v
	}
	return defaultVal
}

type redisInternalValue string

func (k redisInternalValue) ToString() string {
	return string(k)
}
