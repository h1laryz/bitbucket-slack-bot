# git-slack-bot

A Slack bot that integrates with git hosting providers (Bitbucket, GitHub planned).
Supports multiple Slack workspaces — each workspace configures its own credentials via the management API.

## Features

- `/bb-prs <repo>` — list open pull requests for a repository
- `/bb-repos` — list all repositories in the workspace
- Multi-tenant: each Slack workspace has its own git credentials
- Provider-agnostic architecture — adding GitHub requires no changes to the Slack layer

## Requirements

- Go 1.21+
- A Slack app with bot token and signing secret ([api.slack.com/apps](https://api.slack.com/apps))
- A Bitbucket app password ([bitbucket.org/account/settings/app-passwords](https://bitbucket.org/account/settings/app-passwords))

## Build

```bash
go build -o bot ./cmd/bot
```

## Run

All configuration is passed as CLI flags — no `.env` file needed.

```bash
./bot \
  --git-provider=bitbucket \
  --slack-bot-token=xoxb-your-bot-token \
  --slack-signing-secret=your-signing-secret \
  --api-key=your-admin-secret \
  --addr=:3000
```

| Flag | Required | Default | Description |
|---|---|---|---|
| `--git-provider` | yes | — | Git hosting backend: `bitbucket`, `github` |
| `--slack-bot-token` | yes | — | Slack bot token (`xoxb-…`) |
| `--slack-signing-secret` | yes | — | Slack signing secret |
| `--api-key` | yes | — | Bearer token protecting `/api/teams/*` endpoints |
| `--addr` | no | `:3000` | Server listen address |

## Slack app setup

In your Slack app settings:

1. **OAuth & Permissions** → Bot Token Scopes: `chat:write`, `commands`, `app_mentions:read`
2. **Slash Commands** → create `/bb-prs` and `/bb-repos` pointing to `https://your-host/slack/commands`
3. **Event Subscriptions** → request URL: `https://your-host/slack/events`, subscribe to `app_mention`

## Team configuration API

Before a Slack workspace can use the bot, an admin must register the git credentials for that workspace. The Slack `team_id` is visible in Slack's slash command payloads.

### Register or update credentials

```bash
curl -X POST http://localhost:3000/api/teams/<slack-team-id>/config \
  -H "Authorization: Bearer your-admin-secret" \
  -H "Content-Type: application/json" \
  -d '{
    "workspace": "my-workspace",
    "username":  "jdoe",
    "token":     "app-password",
    "base_url":  ""
  }'
```

`base_url` is optional — defaults to `https://api.bitbucket.org/2.0` for Bitbucket.

### Get current config (token is masked)

```bash
curl http://localhost:3000/api/teams/<slack-team-id>/config \
  -H "Authorization: Bearer your-admin-secret"
```

### List all configured teams

```bash
curl http://localhost:3000/api/teams \
  -H "Authorization: Bearer your-admin-secret"
```

### Remove credentials

```bash
curl -X DELETE http://localhost:3000/api/teams/<slack-team-id>/config \
  -H "Authorization: Bearer your-admin-secret"
```

## Health check

```bash
curl http://localhost:3000/health
# {"status":"ok","git_provider":"bitbucket"}
```

## Project structure

```
cmd/bot/            entry point
internal/
  config/           CLI flag parsing
  provider/         git provider interface + Bitbucket implementation
  store/            in-memory per-team credential store
  api/              management REST API (team config CRUD)
  slack/            Slack webhook handler, slash commands, signature verification
```

## Adding a new git provider

1. Create `internal/provider/<name>.go` implementing the `provider.Provider` interface
2. Register it in `provider.New()` in [internal/provider/provider.go](internal/provider/provider.go)
3. Add the new type constant to `ParseType()`
4. Pass `--git-provider=<name>` at startup
