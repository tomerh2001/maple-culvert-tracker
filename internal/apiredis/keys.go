package apiredis

var (
	CONF_DISCORD_ADMIN_CHANNEL_ID        = redisInternalKey{"CONF_DISCORD_ADMIN_CHANNEL_ID", EditableTypeDiscordChannel, false}
	CONF_DISCORD_MEMBERS_MAIN_CHANNEL_ID = redisInternalKey{"CONF_DISCORD_MEMBERS_MAIN_CHANNEL_ID", EditableTypeDiscordChannel, false}
	CONF_DISCORD_WEEKLY_CHANNEL_ID       = redisInternalKey{"CONF_DISCORD_WEEKLY_CHANNEL_ID", EditableTypeDiscordChannel, false}
	CONF_DISCORD_GUILD_ROLE_IDS          = redisInternalKey{"CONF_DISCORD_GUILD_ROLE_IDS", EditableTypeDiscordRole, true}
	CONF_DISCORD_SUBMIT_ROLE_IDS         = redisInternalKey{"CONF_DISCORD_SUBMIT_ROLE_IDS", EditableTypeDiscordRole, true}
	CONF_DISCORD_SUBMIT_USER_IDS         = redisInternalKey{"CONF_DISCORD_SUBMIT_USER_IDS", EditableTypeString, true}

	OPTIONAL_CONF_DISCORD_REMINDER_SUFFIX        = redisInternalKey{"OPTIONAL_CONF_DISCORD_REMINDER_SUFFIX", EditableTypeString, false}
	OPTIONAL_CONF_MAPLE_REGION                   = redisInternalKey{"OPTIONAL_CONF_MAPLE_REGION", EditableTypeSelection, false} // default "na"
	OPTIONAL_CONF_CULVERT_DUEL_THUMBNAIL_URL     = redisInternalKey{"OPTIONAL_CONF_CULVERT_DUEL_THUMBNAIL_URL", EditableTypeString, false}
	OPTIONAL_CONF_SUBMIT_SCORES_SHOW_SANDBAGGERS = redisInternalKey{"OPTIONAL_CONF_SUBMIT_SCORES_SHOW_SANDBAGGERS", EditableTypeBool, false}
	OPTIONAL_CONF_SUBMIT_SCORES_SHOW_RATS        = redisInternalKey{"OPTIONAL_CONF_SUBMIT_SCORES_SHOW_RATS", EditableTypeBool, false}
	OPTIONAL_CONF_SANDBAG_THRESHOLD              = redisInternalKey{"OPTIONAL_CONF_SANDBAG_THRESHOLD", EditableTypeFloat64, false}
	OPTIONAL_CONF_MONTHLY_IMPROVEMENT_THRESHOLD  = redisInternalKey{"OPTIONAL_CONF_MONTHLY_IMPROVEMENT_THRESHOLD", EditableTypeFloat64, false}
	OPTIONAL_CONF_ALLOW_SELF_REPORT              = redisInternalKey{"OPTIONAL_CONF_ALLOW_SELF_REPORT", EditableTypeBool, false}

	// Internal keys below this line.
	//
	// PROCESS-GLOBAL keys (always accessed via .Global(), never .For(tenant)):
	// DATA_REDIS_VERSION, DATA_DB_VERSION, DATA_FIXES_*,
	// DATA_COMMAND_REGISTRATION_STATUS and DATA_ACTIVE_GUILDS describe the
	// deployment itself, not any one server's data. Everything else is
	// per-tenant.
	DATA_REDIS_VERSION = redisInternalKey{"DATA_REDIS_VERSION", EditableTypeNone, false}
	DATA_DB_VERSION    = redisInternalKey{"DATA_DB_VERSION", EditableTypeNone, false}

	DATA_FIXES_SUN_TO_WED = redisInternalKey{"DATA_FIXES_SUN_TO_WED", EditableTypeNone, false}
	// DATA_FIXES_TENANT_BACKFILL marks the one-shot backfill that stamped all
	// pre-tenant postgres rows (guild_id = '') with the primary env guild id.
	DATA_FIXES_TENANT_BACKFILL = redisInternalKey{"DATA_FIXES_TENANT_BACKFILL", EditableTypeNone, false}

	// DATA_ACTIVE_GUILDS is the bot-maintained registry of every guild the
	// bot is currently in (comma separated ids), written on Ready and on
	// GuildCreate/GuildDelete. periodicredis and cron jobs iterate it instead
	// of the env guild list, so freshly-added servers get background
	// refreshes too. Process-global: it registers guilds, it holds no guild's
	// data.
	DATA_ACTIVE_GUILDS = redisInternalKey{"DATA_ACTIVE_GUILDS", EditableTypeNone, false}

	DATA_DISCORD_MEMBERS = redisInternalKey{"DATA_DISCORD_MEMBERS", EditableTypeNone, false}
	// DATA_DISCORD_MEMBERS_UPDATED_AT holds the RFC3339 time of the last
	// successful member cache refresh; DATA_DISCORD_MEMBERS_LAST_ERROR holds
	// the last refresh failure (cleared to "" on success).
	DATA_DISCORD_MEMBERS_UPDATED_AT = redisInternalKey{"DATA_DISCORD_MEMBERS_UPDATED_AT", EditableTypeNone, false}
	DATA_DISCORD_MEMBERS_LAST_ERROR = redisInternalKey{"DATA_DISCORD_MEMBERS_LAST_ERROR", EditableTypeNone, false}

	DATA_DISCORD_NEW_FEATURES_ANNOUNCEMENT_VERSION = redisInternalKey{"DATA_DISCORD_NEW_FEATURES_ANNOUNCEMENT_VERSION", EditableTypeNone, false}

	// DATA_COMMAND_REGISTRATION_STATUS holds the outcome of the last slash
	// command registration pass: "ok" on success, otherwise the error string.
	DATA_COMMAND_REGISTRATION_STATUS = redisInternalKey{"DATA_COMMAND_REGISTRATION_STATUS", EditableTypeNone, false}

	// DATA_RESET_WEEK_PENDING is the name PREFIX for the /reset-week
	// run-again-to-confirm flow, scoped (tenant via .For(), invoker, week).
	// Use PendingResetWeekKey.
	DATA_RESET_WEEK_PENDING = redisInternalKey{"DATA_RESET_WEEK_PENDING", EditableTypeNone, false}
)

// PendingResetWeekKey returns the internal key holding a pending /reset-week
// confirmation for one (invoker, week) pair; the caller binds the tenant with
// .For(). Dynamic: deliberately not in KeysMap.
func PendingResetWeekKey(invokerID, week string) redisInternalKey {
	return redisInternalKey{
		Name:         DATA_RESET_WEEK_PENDING.Name + ":" + invokerID + ":" + week,
		EditableType: EditableTypeNone,
	}
}

var KeysMap = map[string]redisInternalKey{}

var EditableSelectionsMap = map[redisInternalKey][]string{
	OPTIONAL_CONF_MAPLE_REGION: {"", "na", "eu"},
}

func init() {
	KeysMap[CONF_DISCORD_ADMIN_CHANNEL_ID.Name] = CONF_DISCORD_ADMIN_CHANNEL_ID
	KeysMap[CONF_DISCORD_MEMBERS_MAIN_CHANNEL_ID.Name] = CONF_DISCORD_MEMBERS_MAIN_CHANNEL_ID
	KeysMap[CONF_DISCORD_WEEKLY_CHANNEL_ID.Name] = CONF_DISCORD_WEEKLY_CHANNEL_ID
	KeysMap[CONF_DISCORD_GUILD_ROLE_IDS.Name] = CONF_DISCORD_GUILD_ROLE_IDS
	KeysMap[CONF_DISCORD_SUBMIT_ROLE_IDS.Name] = CONF_DISCORD_SUBMIT_ROLE_IDS
	KeysMap[CONF_DISCORD_SUBMIT_USER_IDS.Name] = CONF_DISCORD_SUBMIT_USER_IDS
	KeysMap[OPTIONAL_CONF_ALLOW_SELF_REPORT.Name] = OPTIONAL_CONF_ALLOW_SELF_REPORT
	KeysMap[OPTIONAL_CONF_DISCORD_REMINDER_SUFFIX.Name] = OPTIONAL_CONF_DISCORD_REMINDER_SUFFIX
	KeysMap[OPTIONAL_CONF_MAPLE_REGION.Name] = OPTIONAL_CONF_MAPLE_REGION
	KeysMap[OPTIONAL_CONF_CULVERT_DUEL_THUMBNAIL_URL.Name] = OPTIONAL_CONF_CULVERT_DUEL_THUMBNAIL_URL
	KeysMap[OPTIONAL_CONF_SUBMIT_SCORES_SHOW_SANDBAGGERS.Name] = OPTIONAL_CONF_SUBMIT_SCORES_SHOW_SANDBAGGERS
	KeysMap[OPTIONAL_CONF_SUBMIT_SCORES_SHOW_RATS.Name] = OPTIONAL_CONF_SUBMIT_SCORES_SHOW_RATS
	KeysMap[OPTIONAL_CONF_SANDBAG_THRESHOLD.Name] = OPTIONAL_CONF_SANDBAG_THRESHOLD
	KeysMap[OPTIONAL_CONF_MONTHLY_IMPROVEMENT_THRESHOLD.Name] = OPTIONAL_CONF_MONTHLY_IMPROVEMENT_THRESHOLD
	KeysMap[DATA_REDIS_VERSION.Name] = DATA_REDIS_VERSION
	KeysMap[DATA_DB_VERSION.Name] = DATA_DB_VERSION
	KeysMap[DATA_FIXES_SUN_TO_WED.Name] = DATA_FIXES_SUN_TO_WED
	KeysMap[DATA_FIXES_TENANT_BACKFILL.Name] = DATA_FIXES_TENANT_BACKFILL
	KeysMap[DATA_ACTIVE_GUILDS.Name] = DATA_ACTIVE_GUILDS
	KeysMap[DATA_DISCORD_MEMBERS.Name] = DATA_DISCORD_MEMBERS
	KeysMap[DATA_DISCORD_MEMBERS_UPDATED_AT.Name] = DATA_DISCORD_MEMBERS_UPDATED_AT
	KeysMap[DATA_DISCORD_MEMBERS_LAST_ERROR.Name] = DATA_DISCORD_MEMBERS_LAST_ERROR
	KeysMap[DATA_COMMAND_REGISTRATION_STATUS.Name] = DATA_COMMAND_REGISTRATION_STATUS
}
