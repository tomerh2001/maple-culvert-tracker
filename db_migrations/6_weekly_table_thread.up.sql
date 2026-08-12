-- The weekly channel message becomes a summary; the full table moves into the
-- week's thread as its own bot message, edited in place. Track that message.
ALTER TABLE weekly_announcements
  ADD COLUMN IF NOT EXISTS table_message_id TEXT NOT NULL DEFAULT '';
