# Paste-and-submit screenshots

Run `/submit-scores`, paste and send images as normal messages in that channel,
then click **Submit** on the private prompt. The batch holds up to 10 images for
10 minutes. **Cancel** discards the batch. Images posted in the channel keep the
channel's normal visibility; only the prompt and receipt are private.

The bot collects attachments from the command's author in that exact guild and
channel. It ignores other users, bots, webhooks, non-image attachments, and
messages from before the command. A second command in the same location points
the user back to their existing prompt. Starting in another channel creates an
independent batch. A restart discards unfinished batches; stale buttons explain
how to start again.

The command fixes the destination week when the session starts. Submit rechecks
permissions, acquires the existing tenant submission guard, and sends the whole
batch through the existing OCR, conflict, overwrite-confirmation, and archive
pipeline. Collection alone never writes scores. Failed button acknowledgements
restore the batch for retry if it has not expired or been superseded.

## Discord configuration

Ordinary guild messages require the **Message Content** privileged intent for
their attachments to reach the bot. Enable it for the application before
deploying code that requests it; otherwise Discord rejects the Gateway login
with code 4014.

For an eligible application below Discord's verification threshold, Bot-authenticated
`PATCH /applications/@me` accepts the limited intent flags. Read the current
flags, preserve every existing bit, and set `GATEWAY_MESSAGE_CONTENT_LIMITED`
(`1 << 19`). Read the application again to verify the change. Applications
requiring approval must use Discord's approval process instead.

DiscordGo v0.29.0 delivers component interactions through the same event type as
commands. Check `InteractionApplicationCommand` before calling
`ApplicationCommandData()`; component data must go to its own handler. Use a
deferred message update to edit the original private upload prompt into the
receipt, clearing the buttons when the batch closes.

DiscordGo launches event handlers concurrently. Submission sorts message
snowflakes to retain screenshot order and deduplicates repeated MessageCreate
events. A bounded 100-message cache per channel lets Submit reconcile recent
uploads that Discord has received but whose handler has not run yet. Only
messages preceding the button interaction belong to that submission. Prompt
edits are serialized separately from batch state so a slow count update cannot
overwrite the final receipt or delay acknowledging Submit.

References: [Message Content intent](https://docs.discord.com/developers/events/gateway#message-content-intent),
[application flags and edits](https://docs.discord.com/developers/resources/application#edit-current-application),
and [interaction responses](https://docs.discord.com/developers/interactions/receiving-and-responding#interaction-response-object-interaction-callback-type).
