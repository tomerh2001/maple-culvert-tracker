# Weekly screenshot archive updates

Each guild has one archive message per culvert week. A partial submission
replaces the screenshot with the closest matching character names and retains
the other pages. PostgreSQL stores the message ID, attachment IDs, and character
names used for matching.

The third row links to that guild's weekly announcement:
`Weekly Message: https://discord.com/channels/<guild>/<channel>/<message>`.
It uses the announcement's stored channel and message IDs, so it still works
after the weekly channel setting changes. Guilds that share scores each link
to their own announcement. If no announcement exists, the row is omitted.
Screenshot submissions must announce the week before updating the archive so
the first archive includes the new weekly message link.

`RefreshWeekScreenshotLinks` updates existing archive links without changing
their original update time or screenshots. It reads the message, changes only
the link row, and skips messages whose link already matches. A content-only
edit must omit `attachments` entirely; an empty attachment list removes them.
Read or edit failures preserve the archive record and page mappings, including
when Discord reports that the message was deleted.

## Discord attachment edits

An edit's `attachments` array lists every attachment that should remain after
the edit, including new multipart uploads. Retained attachments use their
Discord IDs; uploads use their multipart file indices.

DiscordGo v0.29.0 serializes `MessageAttachment.Filename` even when it is empty.
Constructing retained attachments with only an ID therefore sends
`"filename":""`, which Discord rejects with HTTP 400 and
`BASE_TYPE_BAD_LENGTH`. Read the stored message's attachment metadata and retain
its filenames when building partial updates. Explicitly name new uploads too.
The bot needs Read Message History in the archive channel to read that metadata,
alongside View Channel, Send Messages, and Attach Files; the channel health
check verifies all four permissions.

Only Discord's `Unknown Message` error (10008) establishes that the archive
message was deleted. Validation failures, permission failures, rate limits,
server errors, and transport failures must return an error while preserving
the stored message and page references. The same rule applies to clearing an
archive after a weekly reset.

This distinction prevents a failed partial update from creating a duplicate
message and losing the mappings for all the retained pages. If an old version
has already done that, preserve both Discord posts and recover page mappings
from a database backup before repairing the archive. Do not infer that missing
database mappings mean the original Discord message or screenshots are gone.

The integration regressions use a disposable PostgreSQL database and a fake
Discord HTTP transport. Set `TEST_DATABASE_URL` to that test database and run
`go test -count=1 ./...`; the test harness truncates its tables, so never point
it at the service database.

References: [Discord message edits](https://docs.discord.com/developers/resources/message#edit-message)
and [Discord error codes](https://docs.discord.com/developers/topics/opcodes-and-status-codes#json-json-error-codes).
