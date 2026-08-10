CREATE TABLE IF NOT EXISTS weekly_announcements (
    culvert_date date PRIMARY KEY,
    channel_id text NOT NULL,
    message_id text NOT NULL,
    thread_id text NOT NULL DEFAULT ''
);
