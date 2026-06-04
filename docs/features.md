# Feature and Integration Guide

This guide covers the runtime features exposed by the `assistant` command.

## Interactive REPL

Run without a prompt to start an interactive session:

```sh
assistant --provider openai-oauth
```

REPL commands:

```text
/exit      quit
/quit      quit
/start     show slash command help
/help      show slash command help
/version   show assistant version and build information
/plan      switch this conversation to planning mode
/chat      switch this conversation to chat mode
/mode NAME set a custom mode label
/clear     clear this conversation's in-memory history
/stop      stop an active run when supported
```

## Slash Commands and History

Slash commands are handled by the host before a message reaches the model.
`/version` reports the running assistant build. `/plan` switches the current
conversation to read-only planning mode. `/chat` returns to normal chat mode.
`/mode NAME` sets a custom mode label that is included in runtime context for
later turns. `/clear` clears only the current conversation history and keeps the
current mode.

Interactive sessions, Telegram, Gmail, and the local gateway keep separate
in-process histories for each conversation while the process is running.
Telegram keys conversations by chat ID, Gmail by thread ID, and the gateway by
`thread_id` with `user_id` as a fallback.

Assistant also persists redacted completed turns to
`~/.gratefulagents/assistant/state/transcripts.ndjson` by default. This is
separate from curated durable memory: transcripts make past chats searchable,
while `memory_remember` stores stable facts and preferences. The model can use
the read-only `session_search` tool to browse recent sessions, search prior
turns, or scroll around a specific turn. Disable transcript persistence with
`--transcripts=false` or `ASSISTANT_TRANSCRIPTS=false`.

Typical `session_search` calls:

```json
{"query": "the trip plan", "limit": 5}
```

```json
{"session_id": "sess_20260603T090000.000000000Z_abcd", "limit": 10}
```

```json
{"session_id": "sess_...", "around_turn_id": "turn_...", "window": 3}
```

## One-Shot Prompt

Pass a prompt as positional arguments:

```sh
assistant --provider openai-api "summarize my current directory"
```

The process exits after the assistant returns a final answer.

## Tools and Approvals

SDK tools are enabled by default with `--tools=true`. The default permission
mode is `workspace-write`; use `--permission read-only` for read-only tool
access.

Approvals are enabled by default. In interactive mode, approval-gated tool
calls are printed to stderr and resumed after a `y` or `yes` response. In
Telegram mode, approval-gated tool calls are sent to the chat with inline
Approve and Deny buttons; replying with `yes`, `approve`, `no`, or `deny` also
resolves the pending request.

Set `--approvals-reviewer auto-review` to run a separate no-tools reviewer model
before human prompting. The reviewer returns allow, deny, or escalate; escalated
requests use terminal or Telegram approval when available. Gmail, gateway, and
scheduled runs cannot answer interactive prompts, so escalations in those modes
fail closed unless you run with scoped tools that do not require approval.

## Audit Output

Audit output is opt-in:

```sh
assistant --audit --provider openai-api "summarize my current directory"
```

When enabled, Assistant writes structured audit events for run starts and ends,
model calls, tool calls and outputs, approval decisions, handoffs, compaction,
and assistant messages. Events are mirrored to stdout and standard logs, and
appended as JSON lines to `~/.gratefulagents/assistant/state/audit.ndjson` by
default. Override the file with `--audit-log` or `ASSISTANT_AUDIT_LOG`.

Use the low audit level when you only need the core trail:

```sh
assistant --audit --audit-level low --provider openai-api "summarize my current directory"
```

`low` records only tool calls with their inputs, assistant text, and errors.
`full` is the default and keeps the complete structured trace.

## Durable Memory and Tasks

Project-state tools are enabled by default and backed by files under
`~/.gratefulagents/assistant/state`.

Memory is model-driven: the host exposes memory tools, and the model decides
when to call `memory_recall`, `memory_remember`, `memory_list`, and
`prime_context`.

When transcripts and project state are both enabled, Assistant also exposes
`memory_distill`. Its default `preview` action scans recent transcripts for
clear user-stated preferences, facts, and routines without writing anything.
Its `apply` action writes non-duplicate candidates into durable memory. Ask for
a preview before applying when you want to review what will be remembered.
For a deeper pass, use `memory_review`; it runs a separate no-tools reviewer
model with a strict JSON schema, then the host validates, deduplicates, and
writes candidates only if `action=apply`.

Typical review workflow:

```json
{"action": "preview", "since_hours": 24, "include_skipped": true}
```

After reviewing the candidates:

```json
{"action": "apply", "since_hours": 24}
```

Use `memory_distill` for a fast deterministic scan. Use `memory_review` when
you want the reviewer model to interpret transcript chunks more broadly. Add
`"include_heuristic": true` to `memory_review` to combine both candidate sets.

After-turn review is opt-in. Set `--memory-review preview` to run
`memory_review` after each completed turn and log candidate memories without
saving them. Set `--memory-review apply` to automatically save validated,
non-duplicate candidates. This uses recent transcript turns from the just
completed run and the same reviewer safeguards as the manual `memory_review`
tool. To prevent memory poisoning, after-turn `apply` is honored only for the
local terminal; turns from remote channels (Telegram, Gmail, scheduled runs)
fall back to `preview`, and the after-turn reviewer never auto-applies raw
deterministic regex matches or pins memories into the primed system prompt.

For a daily memory review, create a scheduled prompt such as:

```text
Run memory_review with action=preview, since_hours=24, and include_heuristic=true.
Summarize any candidate memories for review, but do not save them unless I
explicitly ask.
```

If you want fully automatic promotion of clear candidates, use
`action=apply` in that scheduled prompt.

Recall is lexical (keyword) by default. Set `ASSISTANT_EMBEDDING_MODEL` to
enable embeddings-backed hybrid recall, which fuses keyword matching with
semantic similarity so the assistant recalls memories relevant in meaning even
without exact word overlap. With an OpenAI key already set, this is enough:

```sh
export OPENAI_API_KEY=sk-...
export ASSISTANT_EMBEDDING_MODEL=text-embedding-3-small
```

Any OpenAI-compatible `/v1/embeddings` endpoint works (including a local Ollama
server). See [Configuration](configuration.md#hybrid-memory-recall) for the full
set of embedding options and provider notes.

Examples:

```sh
assistant --provider openai-oauth --approval=false "remember that my preferred editor is vim"
assistant --provider openai-oauth --approval=false "what editor do I prefer?"
```

With approvals enabled, approve the relevant memory tool calls when prompted.

## Scheduling

Scheduling tools are enabled by default. Ask the assistant to create a
reminder, recurring cron, or scheduled follow-up from the REPL or any channel
that can run tools. The assistant can also invoke an existing schedule
immediately with `schedule_run` by id or exact name without changing that
schedule's next cron run.

Scheduled jobs are stored in
`~/.gratefulagents/assistant/state/schedules.json` by default. Override the
state directory with `--state-dir` or `ASSISTANT_STATE_DIR`.

Supported triggers:

```text
cron           standard five-field cron, parsed by github.com/robfig/cron/v3
every_seconds  fixed interval in seconds, minimum 10
run_at         one-time RFC3339 or YYYY-MM-DD HH:MM timestamp
timezone       optional IANA timezone such as America/New_York
```

Schedules can deliver completed output to Telegram when the process has a
Telegram bot token. For example, a daily weather report can store:

```json
{
  "name": "daily weather",
  "prompt": "Write a concise daily weather report for Auroville.",
  "cron": "0 7 * * *",
  "timezone": "Asia/Kolkata",
  "deliver": {
    "channel": "telegram",
    "chat_id": "123456789"
  }
}
```

Run only the scheduler:

```sh
assistant schedule --provider openai-oauth
```

The scheduler is enabled by default in long-running modes: the interactive
REPL, `serve`, `telegram`, `gmail`, `schedule`, and `poll`. One-shot prompts
still exit after the reply. Set `--scheduling=false` to disable both schedule
tools and the background scheduler.

For unattended scheduled prompts, review the tool and approval settings. If a
scheduled prompt tries to use an approval-gated tool, the run records an error
because the scheduler cannot answer interactive approval prompts.

## MCP

MCP is disabled by default. Enable it with:

```sh
assistant --mcp
```

Assistant merges MCP servers from:

- The workspace `.mcp.json` in `--workdir`.
- Repeated `--mcp-config` files.
- `mcpConfigPaths` in the assistant config file.
- Inline `mcpServers` in the assistant config file.
- Enabled plugin and extension entries in the assistant config file.

When two sources define the same server name, the later source wins.

## Skills

Skill tools are disabled by default. Enable them with:

```sh
assistant --skills
```

Optional custom catalogs are configured with `--skill-catalog` or the
`skills.catalogPath` config-file field. Installed skills become MCP servers
through `.mcp.json` and are available on the next turn.

## Telegram Polling

Telegram uses outbound long polling. No public webhook is required.

Create and configure a Telegram bot with
[Telegram's BotFather](https://core.telegram.org/bots/features#botfather):

1. Open Telegram and message `@BotFather`.
2. Send `/newbot` and follow the prompts for the bot display name and
   username.
3. Copy the token BotFather returns. It looks like
   `123456789:AA...`. Keep this token secret because it can control the bot.
4. Start a chat with the new bot from the Telegram account that should use
   Assistant. The bot only receives messages after a user opens the chat and
   sends it a message.

Set the token in the process environment before starting Assistant:

```sh
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
export ASSISTANT_TELEGRAM_ALLOWED_USERS='123456789'
assistant telegram --provider openai-oauth
```

For API-key provider mode, set the OpenAI key too:

```sh
export OPENAI_API_KEY='sk-...'
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
export ASSISTANT_TELEGRAM_ALLOWED_USERS='123456789'
assistant telegram --provider openai-api
```

If you keep local environment values in `.env`, copy `.env.example` and fill in
the token:

```sh
cp .env.example .env
$EDITOR .env
```

Assistant reads environment variables from the process. It does not load
`.env` files by itself, so use your shell, `direnv`, or another secret manager
to load the file before running the command. For a POSIX shell:

```sh
set -a
. ./.env
set +a
assistant telegram
```

Useful flags:

```text
--telegram-bot-token       Telegram bot token
--telegram-allowed-user    Telegram user ID or username allowed to use the bot
--telegram-allowed-chat    Telegram chat ID allowed to use the bot
--telegram-poll-timeout    Telegram long-poll timeout in seconds
```

Equivalent environment variables:

```text
ASSISTANT_TELEGRAM_BOT_TOKEN       required unless --telegram-bot-token is set
ASSISTANT_TELEGRAM_ALLOWED_USERS   comma-separated allowed user IDs/usernames
ASSISTANT_TELEGRAM_ALLOWED_CHATS   comma-separated allowed chat IDs
ASSISTANT_TELEGRAM_POLL_TIMEOUT    optional; defaults to 50 seconds
```

Telegram access is deny-by-default. At least one allowed user or chat must be
configured before any Telegram message can reach the assistant. Prefer numeric
Telegram user IDs over usernames. If you need to discover the IDs, start the
poller, send the bot one message, read the `telegram access denied` line from
the process logs, then set the matching user or chat ID and restart.
Messages outside the allowlist are ignored without a Telegram reply.

Telegram replies are sent with Bot API HTML formatting enabled. Assistant may
use Telegram-supported rich text such as bold, italic, underline,
strikethrough, spoilers, links, custom emoji, time entities, inline code,
preformatted code, and block quotes. Telegram messages do not support native
table markup, so tabular answers are rendered as aligned text inside
preformatted blocks.

When the Telegram poller starts, Assistant registers a command menu for common
actions: `/start`, `/help`, `/version`, `/clear`, `/plan`, `/chat`, and
`/stop`. Replies also include inline buttons for clearing chat history,
switching between plan and chat mode, showing help, and checking the running
version. Telegram supports bot command menus and message-attached inline
keyboards; it does not provide bots with a custom fixed toolbar at the top of
the chat.

The last processed Telegram update offset is stored in the assistant state
directory. By default that is
`~/.gratefulagents/assistant/state/telegram_offset.json`; override the state
directory with `--state-dir` or `ASSISTANT_STATE_DIR`.

Telegram conversation history is kept per chat ID for the lifetime of the
poller process. Send `/clear` in a chat, select it from the bot command menu,
or tap the `Clear history` inline button to clear only that chat's history.

With approvals enabled, Telegram pauses approval-gated tool calls and sends an
approval card to the chat. Tap Approve or Deny, or reply with `yes`/`no`, to
resume the run. With `--approvals-reviewer auto-review`, Telegram approval cards
are only sent when the reviewer escalates. For other unattended channel modes,
either run with narrow or read-only tool access, or set `--approval=false` only
for a workspace and tool set you trust.

## Google Connect (SSO)

Pasting a raw Gmail access token works but the token expires in about an hour.
For an always-on assistant, connect a Google account through a hosted **Connect
broker** that performs Google SSO once and then mints short-lived access tokens
on demand.

The broker owns a single verified Google Cloud OAuth web app, so end users never
create their own Google project. Someone hosts the broker; each assistant pairs
with it.

The broker itself is not part of this open-source assistant — it is a separate
service. You can run a hosted one or self-host a compatible broker; the full
HTTP/JSON contract is documented in
[docs/google-connect-protocol.md](google-connect-protocol.md).

Connect an assistant to a broker:

```sh
export ASSISTANT_GOOGLE_CONNECT_URL='https://connect.gratefulagents.dev'
assistant google-connect --google-scope gmail.readonly
```

`google-connect` prints a URL. Open it, complete Google SSO, and grant the
requested scopes. The assistant stores only a pairing credential (an
`assistant_id` plus a secret) in `google-auth.json`; the Google refresh token
stays on the broker. Keep the access token fresh with a refresher daemon,
mirroring `oauth-refresh`:

```sh
assistant google-refresh
```

Disconnect and revoke at any time:

```sh
assistant google-disconnect
```

Once connected, `assistant gmail` and `assistant poll` use the brokered
credential automatically when no static `--gmail-token` is set.

### Calendar tools

When the connected Google account includes a Calendar scope and `--enable-tools`
is set, the assistant registers read-only Google Calendar agent tools that act
through the same brokered access token:

```text
calendar_list_events    list upcoming events (needs calendar or calendar.readonly)
calendar_get_event      fetch the full details of one event by id
```

Both tools are read-only. Granting `calendar.readonly` is enough:

```sh
assistant google-connect --google-scope gmail.readonly --google-scope calendar.readonly
```

How the flow works:

```text
google-connect  -> POST /device/start (assistant_id + secret hash + scopes)
                -> open verification URL -> Google SSO + consent
broker callback -> exchanges code for a refresh token, stores it server-side
google-connect  -> polls /device/token until authorized -> writes google-auth.json
gmail/refresh   -> POST /token (assistant_id + secret) -> short-lived access token
```

The complete broker protocol, including request/response shapes and the
server-side responsibilities, is in
[docs/google-connect-protocol.md](google-connect-protocol.md).

Client flags:

```text
--google-connect-url       base URL of the Connect broker
--google-scope             Google scope to request; repeatable
--google-auth-path         credential path; defaults to state-dir/google-auth.json
--oauth-refresh-interval   google-refresh interval; 0 runs once
```

Security notes: the OAuth client secret and refresh tokens live only on the
broker; assistants only ever hold short-lived access tokens and a pairing
secret. A well-behaved broker validates requested scopes against an allowlist,
stores the pairing secret as a hash compared in constant time, and can encrypt
refresh tokens at rest.

## Gmail Polling

Gmail uses outbound polling against the Gmail API.

```sh
export ASSISTANT_GMAIL_ACCESS_TOKEN='oauth-access-token-with-gmail-scope'
assistant gmail --provider openai-oauth --gmail-query "is:unread"
```

Use a Gmail OAuth token with `gmail.readonly` for polling. Add `gmail.modify`
for `--gmail-mark-read`, and `gmail.send` for `--gmail-send-replies`. Instead of
a static token, you can connect a Google account through the Connect broker (see
[Google Connect](#google-connect-sso)); `assistant gmail` then uses the brokered
credential automatically and refreshes it as needed.

By default, Gmail replies are printed to stdout. Sending mail requires an
explicit opt-in:

```sh
assistant gmail --provider openai-oauth --gmail-send-replies
```

Useful flags:

```text
--gmail-token              Gmail OAuth access token
--gmail-user               Gmail user id; defaults to me
--gmail-query              Gmail search query; defaults to is:unread
--gmail-poll-interval      Gmail poll interval in seconds
--gmail-max-results        Gmail messages fetched per poll
--gmail-mark-read          remove UNREAD after processing
--gmail-send-replies       send assistant replies through Gmail
```

Processed Gmail message IDs are stored in the assistant state directory.
Gmail conversation history is kept per Gmail thread ID for the lifetime of the
poller process.

## Combined Polling

Run every configured polling channel together:

```sh
assistant poll --provider openai-oauth
```

`assistant poll` starts Telegram when `ASSISTANT_TELEGRAM_BOT_TOKEN` is set,
Gmail when `ASSISTANT_GMAIL_ACCESS_TOKEN`/`ASSISTANT_GMAIL_TOKEN` is set or a
Google account is connected via `assistant google-connect`, and the scheduler
unless `--scheduling=false` is set.

## Local Gateway

The local gateway is for trusted local/private automation. It is not used by
Telegram or Gmail polling.

```sh
export ASSISTANT_GATEWAY_TOKEN='long-random-token'
assistant serve --provider openai-oauth --addr :8080
```

Health check:

```sh
curl -s http://localhost:8080/healthz
```

Generic JSON endpoint:

```sh
curl -s http://localhost:8080/v1/messages \
  -H 'content-type: application/json' \
  -H "authorization: Bearer $ASSISTANT_GATEWAY_TOKEN" \
  -d '{"channel":"generic","user_id":"me","text":"remember that I like vim"}'
```

Set `thread_id` to keep independent gateway conversations. If `thread_id` is
omitted, Assistant uses `user_id` as the conversation key. Send `/clear` to
clear that conversation's in-process history.

The gateway fails closed unless `ASSISTANT_GATEWAY_TOKEN` or `--gateway-token`
is set and supplied as a bearer token.

## Family Deploy

`assistant family-deploy` interactively configures and manages a fleet of
containerized assistants: one per family member, plus any "freeloaders" you add.
It prompts for how many family members there are, their names, and the
freeloaders, then writes an `assistant.yaml` deployment file and drives Docker
to bring up one container per person plus one OpenAI OAuth refresher container.

Each member's container:

- runs `assistant telegram` with the OpenAI OAuth provider, restricted to a
  required allow list of Telegram users,
- mounts the host Codex `auth.json` read-only at `/codex/auth.json`,
- gets a persistent named Docker volume at `/state`, so durable memory and
  scheduler data survive container restarts,
- restarts automatically (`--restart unless-stopped`),
- is individually configurable (Telegram bot token, allowed users/chats, model,
  and extra environment variables) through `assistant.yaml`.

The refresher container mounts the host Codex `auth.json` writable and runs
`assistant oauth-refresh` every hour by default. Member containers never refresh
OAuth themselves.

```sh
# Interactive: generate assistant.yaml and deploy
assistant family-deploy

# Regenerate the config only, without deploying
assistant family-deploy init

# Apply an existing/edited assistant.yaml
assistant family-deploy up --file ./assistant.yaml

# Inspect or tear down (named volumes are kept)
assistant family-deploy status
assistant family-deploy down
```

Preview the Docker commands without running them using `--dry-run`. Override the
image repository with `--image`, the image tag with `--version`, and the mounted
Codex auth path with `--codex-auth`. By default, generated family configs use
the running assistant release version as the image tag; development builds fall
back to `latest`. Release image tags are plain semver, such as `0.7.2`; a
leading `v` is stripped when resolving the Docker image tag.

The interactive flow asks how many family members there are, then for each
member a name, a Telegram bot token, and a comma-separated allow list; it repeats
the same prompts for freeloaders. Name, token, and allow list are all required.
For example:

```text
How many family members? 1

Family member #1
  Name: Alice
  Telegram bot token (from BotFather): 123456:alice-bot-token
  Allowed Telegram user IDs/usernames (comma-separated): 123456789
How many freeloaders? 1

Freeloader #1
  Name: Charlie
  Telegram bot token (from BotFather): 234567:charlie-bot-token
  Allowed Telegram user IDs/usernames (comma-separated): 234567890
```

Running from a source checkout (no installed binary), substitute
`go run ./cmd/assistant` for `assistant`:

```sh
go run ./cmd/assistant family-deploy --dry-run
```

`go run` keeps the current working directory, so `assistant.yaml` is written to
the directory you run it from unless you pass `--file`.

`assistant.yaml`:

```yaml
image: ghcr.io/gratefulagents/assistant
version: latest
provider: openai-oauth
codexAuthPath: ~/.codex/auth.json
restart: unless-stopped
user: "0:0"
refresher:
  container: assistant-oauth-refresher
  interval: 1h
members:
  - name: Alice
    role: family
    container: assistant-family-alice
    volume: assistant-family-alice-state
    telegramBotToken: "123456:alice-bot-token"
    telegramAllowedUsers: ["123456789"]
    model: ""
    env: {}
  - name: Charlie
    role: freeloader
    container: assistant-freeloader-charlie
    volume: assistant-freeloader-charlie-state
    telegramBotToken: "234567:charlie-bot-token"
    telegramAllowedUsers: ["234567890"]
```

`container` and `volume` are derived from the role and name when omitted.
Containers run as `user: "0:0"` by default so the assistant can write to its
named volume; override per deployment if your image runs as a non-root user that
owns the volume. Each member needs their own Telegram bot token from
[BotFather](https://core.telegram.org/bots/features#botfather) and at least one
allowed Telegram user; both are required when configuring a member interactively
and are validated when an edited `assistant.yaml` is loaded.
Family member containers mount the shared Codex OAuth file read-only and run
with `--openai-oauth-refresh=false`. OpenAI OAuth deployments also run one
refresher container with a writable auth mount; by default it refreshes
immediately and then every hour.
