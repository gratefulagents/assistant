# Configuration

Assistant can be configured with flags, environment variables, and an optional
JSON config file. Flags have the final say for values parsed by the CLI.

## Providers

Assistant supports two OpenAI modes:

- `openai-oauth`: reads OAuth credentials from `~/.codex/auth.json` by default.
- `openai-api`: reads an API key from `OPENAI_API_KEY` by default.

Examples:

```sh
assistant --provider openai-oauth
OPENAI_API_KEY=sk-... assistant --provider openai-api
```

Provider-specific environment variables:

```text
ASSISTANT_PROVIDER
ASSISTANT_MODEL
ASSISTANT_OPENAI_BASE_URL
OPENAI_BASE_URL
ASSISTANT_OPENAI_API_MODE
ASSISTANT_OPENAI_API_KEY
OPENAI_API_KEY
ASSISTANT_OPENAI_OAUTH_PATH
OPENAI_OAUTH_AUTH_JSON_PATH
ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID
ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID_PATH
```

## Runtime Defaults

Important runtime defaults:

```text
ASSISTANT_WORKDIR              current directory
ASSISTANT_STATE_DIR            ~/.gratefulagents/assistant/state
ASSISTANT_CONFIG               ~/.gratefulagents/assistant/config.json
ASSISTANT_PERMISSION           workspace-write
ASSISTANT_REASONING            low
ASSISTANT_VERBOSITY            medium
ASSISTANT_MAX_TURNS            8
ASSISTANT_MAX_TOKENS           1200
ASSISTANT_TOOL_TIMEOUT         0
ASSISTANT_TOOLS                true
ASSISTANT_SCHEDULING           true
ASSISTANT_PROJECT_STATE        true
ASSISTANT_APPROVAL             true
ASSISTANT_GUARDRAILS           true
ASSISTANT_COMPACTION           true
ASSISTANT_PRIVATE_NETWORK      false
```

`--permission read-only` restricts SDK tool access. `--approval=true` asks
before approval-gated tool execution in interactive mode.

## Extension Config File

Assistant reads `~/.gratefulagents/assistant/config.json` when present. Missing
config is fine. The config can declare MCP servers directly, merge additional
MCP config files, enable skills, and group related servers as plugins or
extensions.

```json
{
  "mcpConfigPaths": [
    "~/.gratefulagents/mcp/base.json",
    "~/projects/home/.mcp.json"
  ],
  "mcpServers": {
    "calendar": {
      "type": "stdio",
      "command": "calendar-mcp",
      "args": ["serve"]
    }
  },
  "skills": {
    "enabled": true,
    "catalogPath": "~/.gratefulagents/assistant/skills.json"
  },
  "plugins": [
    {
      "name": "home-automation",
      "enabled": true,
      "mcpConfigPaths": ["~/.gratefulagents/plugins/home/mcp.json"],
      "mcpServers": {
        "lights": {
          "type": "stdio",
          "command": "lights-mcp"
        }
      }
    }
  ]
}
```

Workspace `.mcp.json` is also loaded automatically from `--workdir`. Later
config wins when two servers use the same name.

## Skills

Skill tools are disabled by default. Enable them with:

```sh
assistant --skills
```

Use a custom catalog with:

```sh
assistant --skills --skill-catalog ~/.gratefulagents/assistant/skills.json
```

The custom catalog file is JSON with a top-level `skills` array understood by
the SDK skill registry.

## Scheduling

Schedule tools are enabled by default with `--scheduling=true` and store
durable jobs in the state directory. Start the scheduler with:

```sh
assistant schedule --provider openai-oauth
```

`assistant poll` also starts the scheduler unless `--scheduling=false` is set.
Cron expressions use `github.com/robfig/cron/v3` standard five-field syntax,
for example `0 9 * * MON-FRI`.

Relevant settings:

```text
ASSISTANT_SCHEDULING            optional; defaults to true
ASSISTANT_STATE_DIR             stores schedules.json
```

## Channel Environment Variables

Telegram:

```text
ASSISTANT_TELEGRAM_BOT_TOKEN       required for `assistant telegram`
ASSISTANT_TELEGRAM_POLL_TIMEOUT    optional; defaults to 50 seconds
```

Create the bot with
[Telegram's BotFather](https://core.telegram.org/bots/features#botfather), copy
the bot token, then export it before starting the poller:

```sh
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
assistant telegram --provider openai-oauth
```

Assistant reads process environment variables. It does not automatically load a
repository `.env` file; use your shell, `direnv`, or another secret manager to
load `.env` before running the command.

Gmail:

```text
ASSISTANT_GMAIL_ACCESS_TOKEN
ASSISTANT_GMAIL_TOKEN
ASSISTANT_GMAIL_USER
ASSISTANT_GMAIL_QUERY
ASSISTANT_GMAIL_POLL_INTERVAL
ASSISTANT_GMAIL_MAX_RESULTS
ASSISTANT_GMAIL_MARK_READ
ASSISTANT_GMAIL_SEND_REPLIES
```

Gateway:

```text
ASSISTANT_GATEWAY_ADDR
ASSISTANT_GATEWAY_TOKEN
```
