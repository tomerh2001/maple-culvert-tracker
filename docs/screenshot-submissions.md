# Numbered screenshot submissions

`/submit-scores` accepts up to 20 images through `screenshot-1:` to
`screenshot-20:`. Attach the images to the command and send it. The bot processes
them together and returns a private receipt. `date:` selects another week.

The numbered attachment fields are the preferred submission interface. They were
restored on September 30, 2026 after the paste-and-submit collection flow proved
inconvenient. Keep them as the primary interface unless the owner asks to change
it. There is no pending collection session, upload prompt, Submit/Cancel button,
or requirement to post images publicly before submitting them.

For existing screenshot messages, the **Submit Scores** message context menu and
`message-link:` remain available. Direct image attachments take precedence when a
message link is also supplied. A command with neither returns usage guidance. The same
permission checks, tenant submission guard, OCR validation, and screenshot
archive processing apply to direct submissions. Resubmissions replace existing
scores for the selected week. When screenshot history is enabled, the bot stores
up to 20 images across two archive messages, with at most 10 images per message.

## Discord registration

The bot registers its global command definitions on startup. Deploy the published
bot image and restart the bot to register updated options. Verify the registered
`submit-scores` command has 20 attachment options named `screenshot-1` through
`screenshot-20`, plus `date` and `message-link`.

Slash-command attachments are resolved from the interaction payload. The bot no
longer requests the Message Content intent or subscribes to message events to
collect screenshots. Application-command dispatch still checks the interaction
type before accessing command data.

## Verification

Run the command tests with `go test ./internal/commands/...`, then the full
suite with `go test ./...`. Database integration tests require a disposable
PostgreSQL database in `TEST_DATABASE_URL`; never use the service database,
because the test harness truncates its tables. Verify the deployed command
schema with the bot's normal credentials without posting test scores.

On the home server, Go tests can run in the existing `golang:1.26.6-bookworm`
container image. Use the operator's UID/GID and the user-owned
`~/.cache/maple-culvert-go/{build,modules}` cache directories. Keep test database
credentials separate from the deployed bot's environment.
