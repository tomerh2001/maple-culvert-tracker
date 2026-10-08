ALTER TABLE weekly_recaps ADD COLUMN IF NOT EXISTS skipped boolean NOT NULL DEFAULT false;
