# Assistant

`github.com/gratefulagents/assistant` is a lightweight personal AI assistant
host built in Go on top of
[`github.com/gratefulagents/sdk`](https://github.com/gratefulagents/sdk). It
provides a single `assistant` command for interactive chat, one-shot prompts,
scheduled jobs, Telegram and Gmail polling, and a local JSON gateway.

## Features

- OpenAI provider support through OAuth credentials or an API key, plus OpenRouter via an API key.
- Interactive REPL and one-shot prompt execution.
- Per-conversation in-process history with host-side slash commands.
- SDK tools, guardrails, approvals, compaction, and durable memory.
- Durable scheduled prompts with one-time, interval, and cron triggers.
- Optional MCP servers from workspace, user, and extension config files.
- Optional skill discovery, install, and catalog tools.
- Outbound polling integrations for Telegram and Gmail.
- Small authenticated local JSON gateway for trusted local automation.

## Install

Requirements:

- OpenAI OAuth credentials at `~/.codex/auth.json`, or an OpenAI API key.
  To create the OAuth file, run `npx @openai/codex login` and complete the
  browser sign-in flow.
- Go 1.26.2 or newer, only if you install with `go install` or build from
  source.

Download a prebuilt binary from
[GitHub Releases](https://github.com/gratefulagents/assistant/releases):

1. Open the latest release.
2. Download the binary for your OS and CPU, such as
   `assistant-darwin-arm64`, `assistant-linux-amd64`, or
   `assistant-windows-amd64.exe`.
3. Make it executable and place it somewhere on your `PATH`.

For macOS Apple Silicon:

```sh
curl -L -o assistant \
  https://github.com/gratefulagents/assistant/releases/latest/download/assistant-darwin-arm64
chmod +x assistant
sudo mv assistant /usr/local/bin/assistant
```

For Linux x86_64:

```sh
curl -L -o assistant \
  https://github.com/gratefulagents/assistant/releases/latest/download/assistant-linux-amd64
chmod +x assistant
sudo mv assistant /usr/local/bin/assistant
```

Install with Go:

```sh
go install github.com/gratefulagents/assistant/cmd/assistant@latest
```

`go install` places the binary in `GOBIN`, or `GOPATH/bin` when `GOBIN` is not
set. Make sure that directory is on your `PATH`.

Build from a clone for development:

```sh
go build ./cmd/assistant
```

Check the installed binary:

```sh
assistant version
```

From a source checkout, run without installing:

```sh
go run ./cmd/assistant --provider openai-oauth
```

## Quick Start

If you have not populated Codex OAuth credentials yet, run:

```sh
npx @openai/codex login
```

This writes the OpenAI OAuth auth file that Assistant reads from
`~/.codex/auth.json` by default.

The examples below run with read-only workspace access and a 100-turn run
budget:

Telegram bot with OpenAI OAuth:

1. Open Telegram and message `@BotFather`.
2. Send `/newbot`, follow the prompts, and copy the bot token.
3. Start a chat with the new bot and send it a message once.
4. Run Assistant with the token in the process environment:

```sh
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
export ASSISTANT_TELEGRAM_ALLOWED_USERS='123456789'
assistant telegram --provider openai-oauth --permission read-only --max-turns 100
```

Assistant reads OpenAI OAuth credentials from `~/.codex/auth.json` by default.
Telegram polling uses outbound requests only, so no public webhook or inbound
port is required. Telegram access is deny-by-default: set
`ASSISTANT_TELEGRAM_ALLOWED_USERS` to your numeric Telegram user ID, or
`ASSISTANT_TELEGRAM_ALLOWED_CHATS` to a specific chat ID. Messages outside the
allowlist are ignored before an assistant run starts.

Interactive OAuth mode:

```sh
assistant --provider openai-oauth --permission read-only --max-turns 100
```

Single prompt:

```sh
assistant --provider openai-api --permission read-only --max-turns 100 "summarize my current directory"
```

Interactive API-key mode:

```sh
OPENAI_API_KEY=sk-... assistant --provider openai-api --permission read-only --max-turns 100
```

Telegram with API-key mode:

```sh
export OPENAI_API_KEY='sk-...'
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
export ASSISTANT_TELEGRAM_ALLOWED_USERS='123456789'
assistant telegram --provider openai-api --permission read-only --max-turns 100
```

Quiet smoke test with no tools or local extensions:

```sh
assistant --provider openai-oauth --permission read-only --max-turns 100 --tools=false --project-state=false "reply with exactly: assistant works"
```

If you use `.env`, copy `.env.example`, fill in the values you need, then load
it with your shell or `direnv` before running the command:

```sh
set -a
. ./.env
set +a
```

Schedule daemon:

```sh
assistant schedule --provider openai-oauth --permission read-only --max-turns 100
```

Ask Assistant to add a reminder or recurring cron from the REPL. The scheduler
runs by default in long-running modes, including the REPL, `serve`, `telegram`,
`gmail`, `schedule`, and `poll`; use `assistant schedule` when you want a
standalone scheduler process.

## Common Flags

```text
--config            assistant extension config JSON
--provider          openai-oauth, openai-api, or openrouter
--model             model name
--workdir           workspace for SDK tools
--permission        workspace-write or read-only
--approval          ask before tool execution
--tools             enable SDK tools
--mcp               enable MCP servers
--mcp-config        extra MCP config file; repeatable
--skills            enable SDK skill search/install/list tools
--skill-catalog     optional custom skill catalog JSON
--scheduling        enable durable schedule tools and the background scheduler
--project-state     enable durable assistant memory/tasks
--embedding-model   embedding model for hybrid memory recall; empty = lexical
--embedding-base-url  OpenAI-compatible embeddings base URL
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
Durable memory recall is lexical by default; set `--embedding-model` (or
`ASSISTANT_EMBEDDING_MODEL`) to enable embeddings-backed hybrid semantic recall.
Audit output is opt-in with `--audit` or `ASSISTANT_AUDIT=true`; it writes
structured events to stdout, standard logs, and
`~/.gratefulagents/assistant/state/audit.ndjson` by default. Use
`--audit-level low` for only tool calls with inputs, assistant text, and errors.

Interactive and channel conversations retain history for the lifetime of the
running process. Telegram keys history by chat, Gmail by thread, and the local
gateway by `thread_id` with `user_id` as a fallback. Slash commands are handled
by the host: `/start`, `/help`, `/version`, `/plan`, `/chat`, `/mode <name>`,
`/clear`, and `/stop`. Telegram also exposes the common commands through its
bot menu and adds inline action buttons to assistant replies.

## Security

Assistant runs tools on your machine, so the defaults are conservative:

- Approvals, guardrails, compaction, and SDK tools are on by default.
- MCP servers, skill installation, private-network web access, audit logging,
  and Gmail reply sending are opt-in.
- `--permission read-only` restricts the tool surface to read-only tools.
  `workspace-write` allows workspace edits, but still uses approvals and
  guardrails by default.
- With `--approval=true`, non-read-only tools pause for human confirmation in
  interactive mode before execution.

Tool runs are isolated where practical:

- Built-in guardrails block obvious destructive shell commands and detect
  likely secrets in tool inputs and outputs.
- Tool output is marked as untrusted before it is fed back into the next model
  turn, reducing prompt-injection risk from web, file, shell, and MCP output.
- Shell commands and MCP stdio servers run through the SDK command executor.
  Subprocesses receive a sanitized environment, scratch cache directories,
  disabled git prompts, output caps, timeouts, and process-group cleanup. On
  Linux, read-only runs use Bubblewrap when available.

Integrations are scoped explicitly:

- MCP is disabled unless `--mcp` is set. Assistant supports stdio MCP servers,
  qualifies server names into tool names, treats server descriptions as
  untrusted, and strips credential-like environment variables unless they are
  explicitly listed in `allowEnv`.
- Private and loopback URL access for web tools is disabled unless
  `--private-network` is set.
- Telegram and Gmail poll outbound only. The local gateway requires a bearer
  token before accepting `/v1/messages`.
- Audit logging is opt-in with `--audit`. Audit events redact common bearer
  tokens, API keys, GitHub tokens, Telegram bot tokens, and secret-like JSON
  fields.

For operational guidance and caveats, see [Security Model](docs/security.md).

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
