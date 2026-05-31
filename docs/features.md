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
/clear     clear in-memory chat history
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
calls are printed to stderr and resumed after a `y` or `yes` response.

Non-interactive channel modes cannot prompt for approval. For unattended
Telegram, Gmail, or gateway use, either keep tools scoped so approvals are not
required or explicitly run with `--approval=false`.

## Durable Memory and Tasks

Project-state tools are enabled by default and backed by files under
`~/.gratefulagents/assistant/state`.

Memory is model-driven: the host exposes memory tools, and the model decides
when to call `memory_recall`, `memory_remember`, `memory_list`, and
`prime_context`.

Examples:

```sh
assistant --provider openai-oauth --approval=false "remember that my preferred editor is vim"
assistant --provider openai-oauth --approval=false "what editor do I prefer?"
```

With approvals enabled, approve the relevant memory tool calls when prompted.

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

```sh
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
assistant telegram --provider openai-oauth
```

Useful flags:

```text
--telegram-bot-token       Telegram bot token
--telegram-poll-timeout    Telegram long-poll timeout in seconds
```

The last processed Telegram update offset is stored in the assistant state
directory.

## Gmail Polling

Gmail uses outbound polling against the Gmail API.

```sh
export ASSISTANT_GMAIL_ACCESS_TOKEN='oauth-access-token-with-gmail-scope'
assistant gmail --provider openai-oauth --gmail-query "is:unread"
```

Use a Gmail OAuth token with `gmail.readonly` for polling. Add `gmail.modify`
for `--gmail-mark-read`, and `gmail.send` for `--gmail-send-replies`.

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

## Combined Polling

Run every configured polling channel together:

```sh
assistant poll --provider openai-oauth
```

At least one of `ASSISTANT_TELEGRAM_BOT_TOKEN`,
`ASSISTANT_GMAIL_ACCESS_TOKEN`, or `ASSISTANT_GMAIL_TOKEN` must be set.

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

The gateway fails closed unless `ASSISTANT_GATEWAY_TOKEN` or `--gateway-token`
is set and supplied as a bearer token.
