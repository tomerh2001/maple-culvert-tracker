-- Refuse a rollback that would discard the second part's image references.
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM weekly_screenshot_archives WHERE archive_part <> 0)
     OR EXISTS (SELECT 1 FROM weekly_screenshot_pages WHERE archive_part <> 0) THEN
    RAISE EXCEPTION 'Cannot remove archive parts while second-part screenshots are stored';
  END IF;
END $$;
DROP INDEX IF EXISTS weekly_screenshot_pages_guild_week_part;
ALTER TABLE weekly_screenshot_archives DROP CONSTRAINT IF EXISTS weekly_screenshot_archives_pkey;
ALTER TABLE weekly_screenshot_archives DROP COLUMN archive_part;
ALTER TABLE weekly_screenshot_archives ADD PRIMARY KEY (guild_id, culvert_date);
ALTER TABLE weekly_screenshot_pages DROP COLUMN archive_part;
