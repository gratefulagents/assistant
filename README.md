# Assistant

`github.com/gratefulagents/assistant` is a lightweight personal AI assistant
host built in Go on top of `github.com/gratefulagents/sdk`.

The project is intentionally small: one command, a private `internal/assistant`
implementation package, config-driven integrations, and no TUI, desktop,
Kubernetes, or local-model runtime dependency.

## Features

- OpenAI provider support through OAuth credentials or an API key.
- Interactive REPL and one-shot prompt execution.
- SDK tools, guardrails, approvals, compaction, and durable memory.
- Durable scheduled prompts with one-time, interval, and cron triggers.
- Optional MCP servers from workspace, user, and extension config files.
- Optional skill discovery, install, and catalog tools.
- Outbound polling integrations for Telegram and Gmail.
- Small authenticated local JSON gateway for trusted local automation.

## Install

Requirements:

- Go 1.26.2 or newer.
- OpenAI OAuth credentials at `~/.codex/auth.json`, or an OpenAI API key.

Build from a clone:

```sh
go build ./cmd/assistant
```

Run without installing:

```sh
go run ./cmd/assistant --provider openai-oauth
```

## Quick Start

Interactive OAuth mode:

```sh
go run ./cmd/assistant --provider openai-oauth
```

Interactive API-key mode:

```sh
OPENAI_API_KEY=sk-... go run ./cmd/assistant --provider openai-api
```

Single prompt:

```sh
go run ./cmd/assistant --provider openai-api "summarize my current directory"
```

Quiet smoke test with no tools or local extensions:

```sh
go run ./cmd/assistant --provider openai-oauth --tools=false --project-state=false "reply with exactly: assistant works"
```

Telegram bot mode:

1. Open Telegram and message `@BotFather`.
2. Send `/newbot`, follow the prompts, and copy the bot token.
3. Start a chat with the new bot and send it a message once.
4. Run Assistant with the token in the process environment:

```sh
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
go run ./cmd/assistant telegram --provider openai-oauth
```

For API-key mode:

```sh
export OPENAI_API_KEY='sk-...'
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
go run ./cmd/assistant telegram --provider openai-api
```

Assistant reads environment variables from the process. If you use `.env`,
copy `.env.example`, fill in `ASSISTANT_TELEGRAM_BOT_TOKEN`, then load it with
your shell or `direnv` before running the command. Telegram polling uses
outbound requests only, so no public webhook or inbound port is required.

Schedule daemon:

```sh
go run ./cmd/assistant schedule --provider openai-oauth
```

Ask Assistant to add a reminder or recurring cron from the REPL, then keep
`assistant schedule` or `assistant poll` running for due jobs to execute.

## Common Flags

```text
--config            assistant extension config JSON
--provider          openai-oauth or openai-api
--model             model name
--workdir           workspace for SDK tools
--permission        workspace-write or read-only
--approval          ask before tool execution
--tools             enable SDK tools
--mcp               enable MCP servers
--mcp-config        extra MCP config file; repeatable
--skills            enable SDK skill search/install/list tools
--skill-catalog     optional custom skill catalog JSON
--scheduling        enable durable schedule tools
--project-state     enable durable assistant memory/tasks
--state-dir         filesystem state directory
--guardrails        enable SDK guardrails
--compaction        enable SDK context compaction
--audit             emit structured audit events to stdout and logs
--audit-level       low or full
--audit-log         append-only audit JSONL path
```

By default, Assistant enables SDK tools, guardrails, compaction, approvals, and
model-driven filesystem memory under `~/.gratefulagents/assistant/state`.
MCP and skill catalog tools are opt-in with `--mcp` and `--skills`.
Audit output is opt-in with `--audit` or `ASSISTANT_AUDIT=true`; it writes
structured events to stdout, standard logs, and
`~/.gratefulagents/assistant/state/audit.ndjson` by default. Use
`--audit-level low` for only tool calls with inputs, assistant text, and errors.

## Commands

```sh
assistant                       # interactive REPL
assistant "prompt"              # one-shot prompt
assistant serve                 # local authenticated JSON gateway
assistant telegram              # Telegram long polling
assistant gmail                 # Gmail polling
assistant schedule              # run scheduled prompts
assistant poll                  # run every configured poller
```

Polling integrations use outbound requests only. You do not need to expose a
public webhook endpoint.

## Documentation

- [Configuration](docs/configuration.md)
- [Feature and Integration Guide](docs/features.md)
- [Development](docs/development.md)
- [Security Model](docs/security.md)

## Development

Run the standard checks before opening a pull request:

```sh
gofmt -w cmd internal
go test ./...
```

This repository follows the standard Go command layout:

- `cmd/assistant`: executable entrypoint only.
- `internal/assistant`: private application implementation.
- `docs`: user and maintainer documentation.

## License

Assistant is released under the GNU General Public License v3.0. See
[LICENSE](LICENSE).
