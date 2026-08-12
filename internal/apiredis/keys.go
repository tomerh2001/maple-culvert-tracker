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

	// Internal keys below this line
	DATA_REDIS_VERSION = redisInternalKey{"DATA_REDIS_VERSION", EditableTypeNone, false}
	DATA_DB_VERSION    = redisInternalKey{"DATA_DB_VERSION", EditableTypeNone, false}

	DATA_FIXES_SUN_TO_WED = redisInternalKey{"DATA_FIXES_SUN_TO_WED", EditableTypeNone, false}

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

	// DATA_SUBMIT_OVERWRITE_PENDING is the name PREFIX for the
	// resubmit-to-confirm overwrite flow: when a Submit Scores run hits
	// conflicting existing scores, a pending-confirmation key scoped to
	// (submitter user id, target message id, week) is stored with a short TTL;
	// a second submission matching the key applies with overwrite. Use
	// PendingOverwriteKey to build the scoped key - the prefix itself is never
	// read or written.
	DATA_SUBMIT_OVERWRITE_PENDING = redisInternalKey{"DATA_SUBMIT_OVERWRITE_PENDING", EditableTypeNone, false}
)

// PendingOverwriteKey returns the internal key holding a pending
// overwrite confirmation for one (submitter, message, week) triple. Dynamic:
// deliberately not in KeysMap.
func PendingOverwriteKey(submitterID, messageID, week string) redisInternalKey {
	return redisInternalKey{
		Name:         DATA_SUBMIT_OVERWRITE_PENDING.Name + ":" + submitterID + ":" + messageID + ":" + week,
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
	KeysMap[DATA_DISCORD_MEMBERS.Name] = DATA_DISCORD_MEMBERS
	KeysMap[DATA_DISCORD_MEMBERS_UPDATED_AT.Name] = DATA_DISCORD_MEMBERS_UPDATED_AT
	KeysMap[DATA_DISCORD_MEMBERS_LAST_ERROR.Name] = DATA_DISCORD_MEMBERS_LAST_ERROR
	KeysMap[DATA_COMMAND_REGISTRATION_STATUS.Name] = DATA_COMMAND_REGISTRATION_STATUS
}
