-- Reverse of 5_tenants.up.sql. NOTE: only safe when a single tenant's rows
-- remain (character names must be globally unique again).
ALTER TABLE weekly_announcements DROP CONSTRAINT IF EXISTS weekly_announcements_pkey;
ALTER TABLE weekly_announcements
  ADD CONSTRAINT weekly_announcements_pkey PRIMARY KEY (culvert_date);
ALTER TABLE weekly_announcements DROP COLUMN IF EXISTS guild_id;

ALTER TABLE characters DROP CONSTRAINT IF EXISTS characters_guild_id_maple_character_name_key;
ALTER TABLE characters
  ADD CONSTRAINT characters_maple_character_name_key UNIQUE (maple_character_name);
ALTER TABLE characters DROP COLUMN IF EXISTS guild_id;
