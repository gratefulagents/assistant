# Security Model

Assistant is a local personal-agent host. It can run tools, call model
providers, read local config, and interact with external services when you
enable integrations. Treat it like any automation process with access to your
workspace and credentials.

## Defaults

Security-relevant defaults:

```text
--approval=true
--guardrails=true
--permission=workspace-write
--private-network=false
--gmail-send-replies=false
--mcp=false
--skills=false
```

The local gateway refuses `/v1/messages` unless a bearer token is configured
and supplied. Telegram and Gmail do not expose inbound HTTP ports.

## Credentials

Do not commit credentials. Common sensitive values include:

- `OPENAI_API_KEY`
- `ASSISTANT_OPENAI_API_KEY`
- `ASSISTANT_TELEGRAM_BOT_TOKEN`
- `ASSISTANT_GMAIL_ACCESS_TOKEN`
- `ASSISTANT_GATEWAY_TOKEN`
- OAuth JSON files
- MCP server tokens and local config files

Use environment variables, your shell secret manager, or files outside the
repository for secrets.

## Tool Execution

Use `--permission read-only` when the assistant should inspect but not modify a
workspace. Keep `--approval=true` when running interactively so approval-gated
tool calls must be confirmed.

For unattended channel operation, avoid broad tools, avoid broad MCP configs,
and use a dedicated working directory. If you set `--approval=false`, the model
can execute enabled tools without an interactive confirmation prompt.

## Network Access

Private network access is disabled by default for web tools. Turn it on only
when the assistant must reach private or loopback URLs:

```sh
assistant --private-network
```

## Gmail Replies

Gmail polling prints suggested replies to stdout by default. It sends mail only
when `--gmail-send-replies` or `ASSISTANT_GMAIL_SEND_REPLIES=true` is set.

## Reporting Vulnerabilities

Please report suspected vulnerabilities privately. See
[../SECURITY.md](../SECURITY.md) for the current reporting process.
