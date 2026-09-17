CREATE TABLE IF NOT EXISTS weekly_recaps (
    guild_id text NOT NULL,
    culvert_date date NOT NULL,
    channel_id text NOT NULL,
    message_id text NOT NULL DEFAULT '',
    thread_id text NOT NULL DEFAULT '',
    table_message_id text NOT NULL DEFAULT '',
    PRIMARY KEY (guild_id, culvert_date)
);
