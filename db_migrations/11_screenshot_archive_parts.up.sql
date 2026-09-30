-- Existing messages and page identities become the first archive part.
ALTER TABLE weekly_screenshot_archives
  ADD COLUMN IF NOT EXISTS archive_part SMALLINT NOT NULL DEFAULT 0 CHECK (archive_part BETWEEN 0 AND 1);
ALTER TABLE weekly_screenshot_pages
  ADD COLUMN IF NOT EXISTS archive_part SMALLINT NOT NULL DEFAULT 0 CHECK (archive_part BETWEEN 0 AND 1);
ALTER TABLE weekly_screenshot_archives DROP CONSTRAINT IF EXISTS weekly_screenshot_archives_pkey;
ALTER TABLE weekly_screenshot_archives ADD PRIMARY KEY (guild_id, culvert_date, archive_part);
CREATE INDEX IF NOT EXISTS weekly_screenshot_pages_guild_week_part
  ON weekly_screenshot_pages (guild_id, culvert_date, archive_part);
