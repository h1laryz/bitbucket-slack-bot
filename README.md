# bitbucket-slack-bot

A Slack bot that forwards Bitbucket pull request activity to Slack channels in real time. Supports multiple Slack workspaces — each workspace connects its own Bitbucket account via OAuth2.

## Features

- Real-time PR notifications: opened, merged, declined, approved, unapproved, commented
- Pipeline build status updates on the PR card (started / passed / failed / stopped)
- Thread replies for every PR event and build update
- Per-channel repository subscriptions
- Bitbucket OAuth2 — no manual credential setup, workspaces connect via browser
- User identity linking — Bitbucket display names → Slack mentions
- All bot responses are ephemeral (only visible to you)

## Slash commands

| Command | Description |
|---|---|
| `/repo connect <workspace>` | Connect a Bitbucket workspace to this Slack team via OAuth |
| `/repo add <workspace/repo>` | Subscribe the current channel to PR notifications for a repository |
| `/repo list` | List all subscribed repositories in the current channel |
| `/repo delete` | Show subscribed repositories with Delete buttons |
| `/login` | Link your Bitbucket account for direct Slack mentions (DM only) |

## PR card

Each PR notification is posted as a structured card and updated in-place as the PR progresses:

```
Pull request               Repository
#42: Fix login bug         workspace/my-repo

Build                      Branch
✅ Build passed: CI        feature/fix → main

Reviewers                  Author
@alice, @bob               @carol
```

Thread replies are posted for: approved, unapproved, commented, merged, declined, build started/passed/failed/stopped.

## Requirements

- Go 1.25+
- PostgreSQL
- A Slack app ([api.slack.com/apps](https://api.slack.com/apps))
- A Bitbucket OAuth2 consumer ([bitbucket.org/account/settings/api](https://bitbucket.org/account/settings/api))
- A public URL for webhooks (e.g. [ngrok](https://ngrok.com))

## Setup

### 1. Bitbucket OAuth consumer

1. Go to Bitbucket → Workspace settings → OAuth consumers → **Add consumer**
2. Callback URL: `https://<your-public-url>/bitbucket/oauth/callback`
3. Permissions: **Repositories** (Read), **Pull requests** (Read), **Account** (Read)
4. Copy the **Key** (client ID) and **Secret**

### 2. Slack app

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App**
2. **OAuth & Permissions** → Bot Token Scopes:
   - `chat:write`
   - `chat:write.public`
   - `commands`
   - `app_mentions:read`
3. **Slash Commands** → create the following, all pointing to `https://<your-public-url>/slack/commands`:
   - `/repo`
   - `/login`
4. **Interactivity & Shortcuts** → enable, set Request URL to `https://<your-public-url>/slack/interactions`
5. **Event Subscriptions** → enable, set Request URL to `https://<your-public-url>/slack/events`, subscribe to `app_mention`
6. Install the app to your workspace and copy the **Bot Token** and **Signing Secret**

### 3. PostgreSQL

Create a database and user:

```sql
CREATE USER gitslackbot WITH PASSWORD 'password';
CREATE DATABASE gitslackbot OWNER gitslackbot;
```

### 4. Environment

Copy `.env.example` to `.env` and fill in your values:

```env
SLACK_BOT_TOKEN=xoxb-...
SLACK_SIGNING_SECRET=...
BITBUCKET_CLIENT_ID=...
BITBUCKET_CLIENT_SECRET=...
POSTGRES_USER=gitslackbot
POSTGRES_PASSWORD=change-me
POSTGRES_DB=gitslackbot
PUBLIC_URL=https://your-ngrok-url.ngrok-free.app
```

### 5. Run

```bash
make start
```

Or build and run manually:

```bash
go build -o ./build/bot ./cmd/bot

./build/bot \
  --slack-bot-token=xoxb-... \
  --slack-signing-secret=... \
  --bitbucket-client-id=... \
  --bitbucket-client-secret=... \
  --db-url=postgres://gitslackbot:password@localhost:5432/gitslackbot \
  --public-url=https://your-ngrok-url.ngrok-free.app \
  --addr=:3000
```

| Flag | Required | Default | Description |
|---|---|---|---|
| `--slack-bot-token` | yes | — | Slack bot token (`xoxb-…`) |
| `--slack-signing-secret` | yes | — | Slack signing secret |
| `--bitbucket-client-id` | yes | — | Bitbucket OAuth2 consumer key |
| `--bitbucket-client-secret` | yes | — | Bitbucket OAuth2 consumer secret |
| `--db-url` | yes | — | PostgreSQL connection URL |
| `--public-url` | yes | — | Externally reachable base URL |
| `--addr` | no | `:3000` | Server listen address |

### 6. Docker Compose (recommended)

The fastest way to run the bot — no Go or PostgreSQL install needed.

```bash
# Download the compose file and example env
curl -O https://raw.githubusercontent.com/h1laryz/bitbucket-slack-bot/master/docker-compose.yml
curl -O https://raw.githubusercontent.com/h1laryz/bitbucket-slack-bot/master/.env.example
cp .env.example .env
```

Edit `.env` with your values, then:

```bash
docker compose up -d
```

This pulls the pre-built image from `ghcr.io` and starts PostgreSQL alongside the bot. Data is persisted in `.postgresql/` next to the compose file.

To build from source instead of pulling the image, uncomment `# build: .` in `docker-compose.yml` and run:

```bash
docker compose build && docker compose up -d
```

## Connecting a workspace

Once the bot is running:

1. In Slack, run `/repo connect <your-bitbucket-workspace>`
2. Click the OAuth link — authorize in the browser
3. You'll see a confirmation in Slack
4. Run `/repo add <workspace/repo>` to subscribe a channel
5. In Bitbucket → Repository settings → Webhooks → Add webhook:
   - URL: `https://<your-public-url>/bitbucket/webhook`
   - Secret: shown by `/repo add` (copy it exactly)
   - Triggers: select **All**

## Linking your Bitbucket account

Send the bot a DM and run `/login`. Click the link to authorize. After that, your Bitbucket display name will be resolved to your Slack mention in PR cards and thread replies.

## Health check

```bash
curl https://<your-public-url>/health
# {"status":"ok"}
```

## Project structure

```
cmd/bot/              entry point, wiring
internal/
  config/             CLI flag parsing
  db/                 PostgreSQL connection pool
  provider/           Bitbucket API client (OAuth bearer auth)
  store/              PostgreSQL store — subscriptions, tokens, PR messages, build statuses
  bitbucket/          Webhook handler, OAuth2 callback
  slack/              Slash commands, events, interactions, signature verification
.github/workflows/    Docker build + push to ghcr.io on every push to master
```
