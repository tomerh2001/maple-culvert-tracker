-- Weekly screenshot archive: one bot message per (guild, week) in an optional
-- per-guild channel (CONF_DISCORD_SCREENSHOT_CHANNEL_ID) holding the score
-- screenshots used for that week's submissions. The message's own attachments
-- ARE the image store; these tables track the message and which roster "page"
-- each attachment covers, so a resubmitted page replaces its older version
-- instead of piling up.
CREATE TABLE IF NOT EXISTS weekly_screenshot_archives (
  guild_id TEXT NOT NULL,
  culvert_date DATE NOT NULL,
  channel_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  PRIMARY KEY (guild_id, culvert_date)
);

-- One row per attachment on the archive message. names holds the
-- newline-joined, lowercased character names parsed from the screenshot - the
-- page's identity for the replace-or-append decision (see PlanArchiveMerge).
CREATE TABLE IF NOT EXISTS weekly_screenshot_pages (
  id BIGSERIAL PRIMARY KEY,
  guild_id TEXT NOT NULL,
  culvert_date DATE NOT NULL,
  attachment_id TEXT NOT NULL,
  names TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS weekly_screenshot_pages_guild_week
  ON weekly_screenshot_pages (guild_id, culvert_date);
