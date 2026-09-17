# End-of-week Culvert recaps

At Thursday 00:00 UTC, the bot posts a new message titled
**Culvert recap - Week of YYYY-MM-DD** in the same channel as the completed
week's original announcement. It includes the same summary layout and a new
thread containing the score table and personal bests. The original message
and its screenshot archive link remain unchanged.

This follows the in-game reset: 03:00 Thursday in Israel during summer time,
02:00 during winter time. Week labels remain the Wednesday database keys;
for example, the reset on September 17, 2026 closes the week of September 9.

A guild receives a recap only if that week has a submission announcement and
still has visible recorded scores. Empty weeks and scores cleared with
`/reset-week` produce no recap. Each actual Discord guild gets its own message,
including guilds that share one score dataset.

The bot checks at UTC minute boundaries and when it connects. On startup it
catches up only the most recently completed week. It does not replay older
history. Delivery progress lives in `weekly_recaps`, separately from the live
announcement. A database lock prevents concurrent deliveries, and successful
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
