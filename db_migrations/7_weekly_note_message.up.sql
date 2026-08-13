-- The thread's submission note is now a single message per week, edited in
-- place (no more one-message-per-submission pile-up). Track its id.
ALTER TABLE weekly_announcements
  ADD COLUMN IF NOT EXISTS note_message_id TEXT NOT NULL DEFAULT '';
