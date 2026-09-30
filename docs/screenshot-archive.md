# Weekly screenshot archive updates

Each guild has up to two archive messages per culvert week, with at most 10
images per message and 20 images in total. A partial submission
replaces the screenshot with the closest matching character names and retains
the other pages. PostgreSQL stores each archive part, its message ID, attachment
IDs, and the character names used for matching. Matching spans both parts, so a replacement
updates the correct message even when the submitted page order changes.

The message puts the title, week, screenshot count, and update time on separate
lines:

```text
**Culvert screenshots**
Week of 2026-09-09
4 screenshots
Updated <t:1789528908:f>
```

Discord renders the timestamp in the reader's local time. A single screenshot
uses `1 screenshot`. Creation, updates, and recreation after deletion share
the same formatter. A reset keeps the title and week, followed by
`Cleared by /reset-week.`

## Twenty-image batches

A submission can contain up to 20 images. The archive splits them into messages
without requiring the submitter to divide the batch. Existing archives keep
their current message and page records as the first part when the database
migration runs. A reset clears both parts.

If new coverage would take the weekly archive beyond 20 images, the oldest
stored pages are removed first; new submitted images take priority. A failed
update preserves the affected part's stored references. Only a confirmed
Discord `Unknown Message` response permits that part to be recreated.

## Discord attachment edits

A content-only edit must omit `attachments` entirely; an empty attachment
list removes every screenshot. Preserve the original update time when editing
only the message text.

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
