# End-of-week Culvert recaps

At Thursday 00:00 UTC, the bot posts a new message titled
**Culvert recap - Week of YYYY-MM-DD** in the same channel as the completed
week's original announcement. It includes the same summary layout and a new
thread containing the score table and personal bests. The original message
and screenshot archive remain unchanged.

This follows the in-game reset: 03:00 Thursday in Israel during summer time,
02:00 during winter time. Week labels remain the Wednesday database keys;
for example, the reset on September 17, 2026 closes the week of September 9.

A guild receives a recap only if the completed week has visible recorded
scores when the reset check runs. The bot uses the original announcement's
channel, or the configured weekly channel when no announcement record exists.
Each actual Discord guild gets its own message, including guilds that share
one score dataset.

Empty weeks, weeks cleared with `/reset-week`, and weeks without a destination
are recorded as skipped. That decision persists across reconnects and restarts.
Submitting historical scores or configuring a channel later does not create a
late recap. A late submission still creates or updates its ordinary weekly
announcement. A configuration read failure is retried rather than recorded as
an unconfigured week.

The bot checks at UTC minute boundaries and when it connects. On startup it
catches up only the most recently completed week. It does not replay older
history. Delivery progress and skipped weeks live in `weekly_recaps`, separately
from the live announcement. A database lock serializes skip decisions and
deliveries, and successful
summary, thread, and detail-message IDs are saved individually so retries
resume at the failed step. Completed recaps are snapshots and are not edited
when a historical score is later corrected.

Message sends use deterministic Discord nonces with `enforce_nonce`, covering
recent retries if a response is lost. Discord only deduplicates nonces for a
few minutes; a process failure between sending and recording its message ID,
followed by a long outage, can still require manual duplicate cleanup.
See Discord's [message creation API](https://docs.discord.com/developers/resources/message#create-message).

Tests use a disposable PostgreSQL database and fake Discord HTTP responses.
They do not send test messages to Discord. The bot's Go process owns the
schedule; no additional cron container or external automation is required.

## Diagnosing apparent duplicate recaps

Check message titles and `weekly_recaps` alongside `weekly_announcements`.
The ordinary submission summary and the reset recap have similar content but
separate delivery records. Discord snowflake IDs encode their send times.
Deleting an ordinary announcement does not remove a completed recap record.
Failed edits preserve existing announcement IDs unless Discord specifically
returns Unknown Message (10008); a permission or transport error must not
trigger a replacement post.

On October 8, 2026, one guild received its recap at 00:00 UTC. Another guild's
first announcement for the completed week was created at 22:05 UTC, followed
by a recap at 22:06 UTC. The old minute check reconsidered empty weeks until
a late submission made them eligible. Persisted skip decisions prevent that
sequence. Only one recap delivery was recorded for the affected guild; deleted
posts did not leave enough evidence to establish a second recap send.
