# Maple Culvert Tracker

A self-hosted Discord bot that tracks your MapleStory guild's weekly **Sharenian Culvert** scores — with built-in screenshot OCR, one-message-per-week announcements, and everything driven by slash commands.

> This is [tomerh2001](https://github.com/tomerh2001)'s fork of [SLAzurin/maple-culvert-tracker](https://github.com/SLAzurin/maple-culvert-tracker).
> Full credit to **Azuri** (SLAzurin) for the original bot, the OCR font data, and the chartmaker.
> The fork removes the web admin panel and reworks the command surface; see below.

## What it does

- **Screenshot → scores in one step**: post a screenshot of the in-game *Guild → Member Participation Status* window (full window is fine), right click it → Apps → **Submit Scores**. The bot OCRs the table (no cropping needed, 1x/2x scale supported) and records everyone's weekly score.
- **One announcement per week, no spam**: in a designated channel the bot keeps a single message per culvert week with the full ranked table (edited in place on every submission), plus a thread with submission notes and personal-best shoutouts that @mention the member. Nothing else is ever posted, and only if a channel is configured.
- **Members self-serve**: `/register` links a character to a Discord account, `/culvert` charts progression (yours, `user:@someone`, or any `character-name:`), right click a member → Apps → **Culvert** works too.
- **Configurable permissions**: admins choose which roles/users may submit scores, and can optionally allow members to `/report-score` their own runs.
- **Multi-server shared database**: one deployment can serve several Discord servers over the same data (env-only config, see `DISCORD_EXTRA_GUILD_IDS`).

## Commands

| Command | Who | What |
|---|---|---|
| `/help` | everyone | User guide |
| `/register` | everyone | Link a character (yours; submitters can link for others with `user:@x`) |
| `/culvert` | everyone | Progression chart (self, `user:@x`, or `character-name:`) |
| `/leaderboard` | everyone | Weekly ranked table, or guild-wide chart with `chart:True` |
| `/personal-bests` | everyone | Best score per character |
| `/list-characters` | everyone | Tracked roster |
| `/report-score` | everyone | Self-report this week's score (admin-toggled) |
| `/submit-scores` | submitters | Submit from a scores file or a message link (OCR) |
| `/parse-images` | submitters | OCR screenshots to a scores file without submitting |
| `/untrack-character`, `/rename-character`, `/set-score` | submitters | Roster & score management |
| `/config` | admins | View/change all bot settings |
| `/setup` | admins | Admin setup guide |
| Right click message → Apps → **Parse Images** / **Submit Scores** | submitters | The fastest weekly flow |
| Right click member → Apps → **Culvert** | everyone | Their chart |

## Deployment

Images are published by CI to `ghcr.io/tomerh2001/maple-culvert-tracker/{bot,chartmaker,periodicredis,cron}:latest` on every push to `master`.

1. Create a Discord application, add a bot, enable the **Server Members** intent, and invite it with permissions `137439267840` (scopes `bot applications.commands`).
2. Copy `.env.template` to `.env` and fill it in (`DISCORD_TOKEN`, `DISCORD_GUILD_ID`, postgres/redis credentials, ...).
3. `docker compose up -d` — the bot runs its own DB migrations on boot.
4. In Discord: `/setup` walks you through roles, the weekly channel, and the first submission.

### Environment variables of note

- `DISCORD_GUILD_ID` — the primary Discord server.
- `DISCORD_EXTRA_GUILD_IDS` — optional comma-separated additional server ids sharing the same database (commands work everywhere; announcements post to the one configured channel). Deliberately env-only: this cannot be changed from Discord.
- `JWT_SECRET` — internal API auth between the bot and itself; set it to anything long and random.

## Development

Go 1.25+; `go test ./...` runs the OCR suite against real fixture screenshots in `provided/gpq-tests`. The `chartmaker/` service (Node) renders the charts. DB schema lives in `db_migrations/` (golang-migrate, run automatically).

## Differences from upstream

- Web admin panel, `/login`, and the JWT web auth are **removed** — `/config` and the other slash commands cover everything.
- OCR accepts full Guild-window screenshots (header-anchored table detection) instead of pre-cropped images only.
- Weekly announcement message + thread instead of per-submission channel messages; no reminder/monthly-report crons; no sandbagger imagery.
- Duel/sandbagger/rat/participation novelty commands removed; `culvert-anyone`, `culvert-summary`, `culvert-mega-chart`, and `submit-scores-from-attachment` were consolidated into `/culvert`, `/leaderboard`, and `/submit-scores`.

## License

MIT, same as upstream. Original work © Azuri (SLAzurin).
