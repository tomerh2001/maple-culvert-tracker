# Maple Culvert Tracker

A self-hosted Discord bot that tracks your MapleStory guild's weekly **Sharenian Culvert** scores — with built-in screenshot OCR, one-message-per-week announcements, and everything driven by slash commands.

> This is [tomerh2001](https://github.com/tomerh2001)'s fork of [SLAzurin/maple-culvert-tracker](https://github.com/SLAzurin/maple-culvert-tracker).
> Full credit to **Azuri** (SLAzurin) for the original bot, the OCR font data, and the chartmaker.
> The fork removes the web admin panel and reworks the command surface; see below.

## What it does

- **Screenshot → scores in one step**: screenshot the in-game *Guild → Member Participation Status* window (full window is fine) and submit it either by right clicking the posted message → Apps → **Submit Scores**, or by attaching it to `/submit-scores`. The bot OCRs the table (no cropping needed, 1x/2x scale supported) and records everyone's weekly score. Unknown names are auto-tracked (canonicalized against the official rankings), and conflicting resubmissions ask you to resubmit within 10 minutes to confirm the overwrite.
- **One announcement per week, no spam**: in a designated channel the bot keeps a single SUMMARY message per culvert week (coverage, top scores, guild total), with the full ranked table as the first comment of its thread - both edited in place on every data change (submissions, registrations, corrections, resets) - plus submission notes and personal-best shoutouts that @mention the member. Nothing else is ever posted, and only if a channel is configured.
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
| `/submit-scores` | submitters | Submit weekly scores from screenshot(s) attached to the command (up to 5) |
| `/set-culvert` | submitters | Set one character's score for a week (unknown names auto-tracked) |
| `/config` | admins | View/change all bot settings (`setting:` + `value:`) |
| `/setup` | admins | Admin setup guide + live status |
| `/health` | admins | Full self-check for THIS server (DB, Discord permissions, config) |
| `/reset-week` | admins | Delete ALL of this server's recorded scores for the current week (run twice within 10 minutes to confirm; characters stay tracked) |
| Right click message → Apps → **Submit Scores** | submitters | Submit scores from a message's screenshots (or a `.json` scores file) |
| Right click member → Apps → **Culvert** | everyone | Their chart |

Date options accept `YYYY-MM-DD` or a Discord timestamp mention (`<t:123456>`).

## Adding the bot to your server (admin quickstart)

If someone already hosts this bot, you only need to invite it — no hosting required:

1. Invite the bot (ask the host for the invite link; it needs the `bot applications.commands` scopes).
2. Type `/setup` — it walks you through the two-minute setup and shows your server's live status.
3. Optionally `/config` a submitter role and a weekly announcement channel.
4. Post a screenshot of the in-game *Guild → Member Participation Status* window and right click it → Apps → **Submit Scores**. Done.

Your server's data is private to your server: per-server characters, scores, settings, member rosters and announcements. Botched a submission run? `/reset-week` wipes the current week's scores (with a run-again-to-confirm guard).

## Deployment (hosting it yourself)

Images are published by CI to `ghcr.io/tomerh2001/maple-culvert-tracker/{bot,chartmaker,periodicredis,cron}:latest` on every push to `master`.

1. Create a Discord application, add a bot, enable the **Server Members** intent, and invite it with permissions `137439267840` (scopes `bot applications.commands`).
2. Copy `.env.template` to `.env` and fill it in (`DISCORD_TOKEN`, `DISCORD_GUILD_ID`, postgres/redis credentials, ...).
3. `docker compose up -d` — the bot runs its own DB migrations on boot.
4. In Discord: `/setup` walks you through roles, the weekly channel, and the first submission.

Commands register globally on boot; any server that invites the bot is served, each with isolated data (tenant = the server, keyed by guild id in both postgres and redis).

### Environment variables of note

- `DISCORD_GUILD_ID` — the deployment's primary Discord server. Required: it defines the DEFAULT tenant (the home deployment's shared dataset, and the key prefix that pre-tenant versions used — existing data is picked up with zero migration).
- `DISCORD_EXTRA_GUILD_IDS` — optional comma-separated additional server ids that SHARE the primary server's dataset (commands work everywhere; announcements post to that tenant's configured channel). Deliberately env-only: this cannot be changed from Discord. Servers NOT listed here get their own isolated data automatically.
- `JWT_SECRET` — internal API auth between the bot and itself; set it to anything long and random.

## Development

Go 1.25+; `go test ./...` runs the OCR suite against real fixture screenshots in `provided/gpq-tests`. The `chartmaker/` service (Node) renders the charts. DB schema lives in `db_migrations/` (golang-migrate, run automatically).

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
