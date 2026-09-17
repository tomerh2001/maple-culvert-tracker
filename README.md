# Maple Culvert Tracker

A self-hosted Discord bot that tracks your MapleStory guild's weekly **Sharenian Culvert** scores — with built-in screenshot OCR, one-message-per-week announcements, and everything driven by slash commands.

> This is [tomerh2001](https://github.com/tomerh2001)'s fork of [SLAzurin/maple-culvert-tracker](https://github.com/SLAzurin/maple-culvert-tracker).
> Full credit to **Azuri** (SLAzurin) for the original bot, the OCR font data, and the chartmaker.
> The fork removes the web admin panel and reworks the command surface; see below.

## What it does

- **Screenshots → scores**: run `/submit-scores`, paste and send your screenshots in the same channel, then click **Submit**. Collect up to 10 screenshots across multiple messages. You can also right click an existing screenshot message → Apps → **Submit Scores**. The bot reads the in-game *Guild → Member Participation Status* table (full window is fine, no cropping needed, 1x/2x scale supported) and records everyone's weekly score. Unknown names are auto-tracked (canonicalized against the official rankings), and conflicting resubmissions ask you to resubmit within 10 minutes to confirm the overwrite.
- **Screenshot history channel** (optional): point `Discord Screenshot Archive Channel ID` at a channel and the bot keeps one message per week there with the screenshots each submission was parsed from - a resubmitted page replaces its older version (matched by the names on it, not its position), so the message always shows the newest shot of every page.
- **Live weekly announcement and closing recap**: in a designated channel the bot keeps a single SUMMARY message per culvert week (coverage, top scores, guild total), with the full ranked table as the first comment of its thread - both edited in place on every data change (submissions, registrations, corrections, resets) - plus submission notes and personal-best shoutouts that @mention the member. At the Thursday 00:00 UTC reset, a completed week with submissions gets one new **Culvert recap - Week of YYYY-MM-DD** message with the same summary and thread details. No recap is posted for an empty week. See [recap scheduling and retry behavior](docs/weekly-recaps.md).
- **Members self-serve**: `/register` links a character to a Discord account, `/culvert` charts progression (yours, `name:@someone`, or any `name:SomeChar`) and stamps the chart with when those scores were last updated, right click a member → Apps → **Culvert** works too.
- **Correct week boundary**: the culvert week rolls over at the in-game reset instant, Thursday 00:00 UTC (03:00 Israel summer time) — not at Wednesday's calendar date.
- **Quiet by default**: every command reply is ephemeral (visible only to the invoker) except the public `/culvert` and `/culvert-all`. `/culvert` only goes public when it has an actual chart to show — no data, bad input and errors come back privately, so the channel never fills with non-answers. Joining a server never triggers a single unprompted message — `/setup` is the entry point.
- **Installable by ANY server**: commands are registered globally, so anyone can invite one deployment of the bot. Every server gets its own fully isolated data — characters, scores, settings, rosters and announcements never mix between servers. The deployment owner's home server(s) (see `DISCORD_GUILD_ID`/`DISCORD_EXTRA_GUILD_IDS`) can still deliberately share one dataset.

## Commands

The entire surface — 13 slash commands, 2 context menus:

| Command | Who | What |
|---|---|---|
| `/culvert-help` | everyone | User guide |
| `/register` | everyone | Link a character by `name:` (submitters can link for others with `user:@x`) |
| `/unregister` | everyone | Untrack a character by `name:`, or all of a member's characters (history kept) |
| `/characters` | everyone | List the characters a member has linked (`user:@x`, default you) |
| `/registered` | everyone | List every member who has linked a character |
| `/culvert` | everyone | Progression chart: `name:` is a character or a `@mention` (default you); optional `from:`/`to:` dates |
| `/culvert-all` | everyone | Weekly score-descending table (optional `date:`) |
| `/submit-scores` | submitters | Start collecting up to 10 screenshots sent in the channel, then click Submit; optional `date:` or `message-link:` |
| `/set-culvert` | submitters | Set one character's score for a week (unknown names auto-tracked) |
| `/config` | admins | View/change all bot settings (`setting:` + `value:`) |
| `/setup` | admins | Admin setup guide + live status |
| `/health` | admins | Full self-check for THIS server (DB, Discord permissions, config) |
| `/reset-week` | admins | Delete ALL of this server's recorded scores for the current week (run twice within 10 minutes to confirm; characters stay tracked) |
| Right click message → Apps → **Submit Scores** | submitters | Submit scores from a message's screenshots (or a `.json` scores file) |
| Right click member → Apps → **Culvert** | everyone | Their chart |

Date options accept `YYYY-MM-DD` or a Discord timestamp mention (`<t:123456>`).

### Submitting screenshots

1. Run `/submit-scores` in the channel where you want to post screenshots. Use `date:` if they belong to a different week.
2. Paste and **send** screenshots as normal messages in that same channel, one at a time or together. The bot collects up to 10 images from your messages and updates the count in its private prompt.
3. Click **Submit** in that prompt once all screenshots have been sent. The bot processes them together and returns a private receipt.

Click **Cancel** to discard the pending batch. The collection session expires after 10 minutes. Only your screenshots in that server and channel are collected; nothing is processed until you click **Submit**. The prompt and receipt are visible only to you; screenshot messages are visible to everyone who can read the channel.

For screenshots already posted, right click the message → Apps → **Submit Scores**, or run `/submit-scores message-link:` with its Discord message link. The message-link option submits that message immediately. Both approaches support the existing score validation and overwrite confirmation.

## Adding the bot to your server (admin quickstart)

If someone already hosts this bot, you only need to invite it — no hosting required:

1. Invite the bot (ask the host for the invite link; it needs the `bot applications.commands` scopes).
2. Type `/setup` — it walks you through the two-minute setup and shows your server's live status.
3. Optionally `/config` a submitter role and a weekly announcement channel.
4. Run `/submit-scores`, paste and send screenshots of the in-game *Guild → Member Participation Status* window in that channel, then click **Submit**.

Your server's data is private to your server: per-server characters, scores, settings, member rosters and announcements. Botched a submission run? `/reset-week` wipes the current week's scores (with a run-again-to-confirm guard).

## Deployment (hosting it yourself)

Images are published by CI to `ghcr.io/tomerh2001/maple-culvert-tracker/{bot,chartmaker,periodicredis,cron}:latest` on every push to `master`.

1. Create a Discord application, add a bot, enable the **Server Members** and **Message Content** intents, and invite it with permissions `137439267840` (scopes `bot applications.commands`).
2. Copy `.env.template` to `.env` and fill it in (`DISCORD_TOKEN`, `DISCORD_GUILD_ID`, postgres/redis credentials, ...).
3. `docker compose up -d` — the bot runs its own DB migrations on boot.
4. In Discord: `/setup` walks you through roles, the weekly channel, and the first submission.

Commands register globally on boot; any server that invites the bot is served, each with isolated data (tenant = the server, keyed by guild id in both postgres and redis).

The **Message Content** intent must be enabled in the Discord Developer Portal before starting the bot. Discord requires it to deliver attachments from ordinary channel messages, including pasted screenshots. The bot requests this intent when it connects; enabling it in code alone is insufficient. Button interactions use a separate handler from application commands.

### Environment variables of note

- `DISCORD_GUILD_ID` — the deployment's primary Discord server. Required: it defines the DEFAULT tenant (the home deployment's shared dataset, and the key prefix that pre-tenant versions used — existing data is picked up with zero migration).
- `DISCORD_EXTRA_GUILD_IDS` — optional comma-separated additional server ids that SHARE the primary server's dataset (commands work everywhere; announcements post to that tenant's configured channel). Deliberately env-only: this cannot be changed from Discord. Servers NOT listed here get their own isolated data automatically.
- `JWT_SECRET` — internal API auth between the bot and itself; set it to anything long and random.

## Development

Go 1.26.5+; `go test ./...` runs the OCR suite against real fixture screenshots in `provided/gpq-tests`. Set `TEST_DATABASE_URL` to a disposable PostgreSQL database to include integration tests (the harness truncates its tables). The existing character-lookup test also needs outbound access to Nexon's official rankings API. The `chartmaker/` service (Node) renders the charts. DB schema lives in `db_migrations/` (golang-migrate, run automatically).

## Differences from upstream

- Web admin panel, `/login`, and the JWT web auth are **removed** — `/config` and the other slash commands cover everything.
- OCR accepts full Guild-window screenshots (header-anchored table detection) instead of pre-cropped images only.
- Weekly announcement message + thread instead of per-submission channel messages; no reminder/monthly-report crons; no sandbagger imagery.
- Publicly installable with strict per-server data isolation (upstream is single-guild). Global command registration; per-tenant redis and postgres scoping.
- The command surface is deliberately tiny (13 slash commands + 2 context menus). Upstream's duel/sandbagger/rat novelty commands, the bulk roster commands, and csv export were removed — `/submit-scores`, right click → **Submit Scores**, and `/set-culvert` cover submission entirely.
- Command replies are text-only and ephemeral by default. Exactly two replies attach an image: the `/culvert` chart, and the **Submit Scores** OCR failure help, which explains the screenshot requirements and attaches an example screenshot.
- Week keys stay Wednesday dates, but the current week is computed from the true reset instant (Thursday 00:00 UTC).

## License

MIT, same as upstream. Original work © Azuri (SLAzurin).
