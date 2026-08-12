-- Tenants: every server (tenant) keeps its own characters and announcements.
-- guild_id holds the tenant id (see internal/data TenantID). Pre-tenant rows
-- default to '' and are stamped with the primary env guild id by the one-shot
-- boot backfill (DATA_FIXES_TENANT_BACKFILL).
ALTER TABLE characters ADD COLUMN IF NOT EXISTS guild_id TEXT NOT NULL DEFAULT '';
ALTER TABLE characters DROP CONSTRAINT IF EXISTS characters_maple_character_name_key;
ALTER TABLE characters DROP CONSTRAINT IF EXISTS characters_guild_id_maple_character_name_key;
ALTER TABLE characters
  ADD CONSTRAINT characters_guild_id_maple_character_name_key UNIQUE (guild_id, maple_character_name);

ALTER TABLE weekly_announcements ADD COLUMN IF NOT EXISTS guild_id TEXT NOT NULL DEFAULT '';
ALTER TABLE weekly_announcements DROP CONSTRAINT IF EXISTS weekly_announcements_pkey;
ALTER TABLE weekly_announcements
  ADD CONSTRAINT weekly_announcements_pkey PRIMARY KEY (guild_id, culvert_date);
