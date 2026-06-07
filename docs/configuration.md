# Configuration

Assistant can be configured with flags, environment variables, and an optional
JSON config file. Flags have the final say for values parsed by the CLI.

## Providers

Assistant supports the following provider modes:

- `openai-oauth`: reads OAuth credentials from `~/.codex/auth.json` by default.
- `openai-api`: reads an API key from `OPENAI_API_KEY` by default.
- `openrouter`: reads an API key from `OPENROUTER_API_KEY` by default and talks
  to the OpenRouter OpenAI-compatible API. Use fully-qualified model slugs such
  as `openai/gpt-4o-mini`, `anthropic/claude-3.5-sonnet`, or
  `deepseek/deepseek-v4-pro`.

Examples:

```sh
assistant --provider openai-oauth
assistant --provider openai-oauth --openai-oauth-refresh=false
assistant oauth-refresh
OPENAI_API_KEY=sk-... assistant --provider openai-api
OPENROUTER_API_KEY=sk-or-... assistant --provider openrouter --model openai/gpt-4o-mini
```

### OpenRouter

OpenRouter gives you a single OpenAI-compatible API key that can reach models
from many providers (OpenAI, Anthropic, DeepSeek, and more).

1. Create an account at [openrouter.ai](https://openrouter.ai) and generate an
   API key from the [Keys page](https://openrouter.ai/keys). Keys start with
   `sk-or-`.
2. Export the key (or pass `--api-key`):

   ```sh
   export OPENROUTER_API_KEY=sk-or-...
   ```

3. Run Assistant with the `openrouter` provider and any OpenRouter model slug:

   ```sh
   assistant --provider openrouter --model openai/gpt-4o-mini
   ```

The base URL defaults to `https://openrouter.ai/api/v1` and the API mode to
`chat-completions`. Override them with `--base-url`/`ASSISTANT_OPENAI_BASE_URL`
and `--api-mode`/`ASSISTANT_OPENAI_API_MODE` if needed.

#### DeepSeek V4 Pro via OpenRouter

DeepSeek V4 Pro is available through OpenRouter under the slug
`deepseek/deepseek-v4-pro`. Select it with `--model` (or `ASSISTANT_MODEL`):

```sh
OPENROUTER_API_KEY=sk-or-... assistant --provider openrouter --model deepseek/deepseek-v4-pro
```

```sh
export ASSISTANT_PROVIDER=openrouter
export ASSISTANT_MODEL=deepseek/deepseek-v4-pro
export OPENROUTER_API_KEY=sk-or-...
assistant
```

Browse the full catalog of available slugs at
[openrouter.ai/models](https://openrouter.ai/models).

#### Model fallbacks

OpenRouter can automatically retry the next model when the primary one is
unavailable, rate-limited, or errors. Pass one or more `--model-fallback` flags
(repeatable), or set `ASSISTANT_MODEL_FALLBACKS` to a comma-separated list. The
primary `--model` is always tried first, then each fallback in order; Assistant
sends them as OpenRouter's request-body `models` array.

```sh
export OPENROUTER_API_KEY=sk-or-...
assistant --provider openrouter \
  --model deepseek/deepseek-v4-pro \
  --model-fallback deepseek/deepseek-chat \
  --model-fallback openrouter/auto
```

```sh
export ASSISTANT_PROVIDER=openrouter
export ASSISTANT_MODEL=deepseek/deepseek-v4-pro
export ASSISTANT_MODEL_FALLBACKS=deepseek/deepseek-chat,openrouter/auto
export OPENROUTER_API_KEY=sk-or-...
assistant
```

Fallbacks apply to OpenAI-compatible providers that honor the `models` array
(OpenRouter, and other chat-completions backends). They are ignored on the
OpenAI Responses API path.

Use `--openai-oauth-refresh=false` for long-running Assistant processes that
share one OAuth file. Then run `assistant oauth-refresh` from a single process;
it refreshes immediately and then every hour by default. Pass
`--oauth-refresh-interval=0` for a one-shot refresh.

Provider-specific environment variables:

```text
ASSISTANT_PROVIDER
ASSISTANT_MODEL
ASSISTANT_MODEL_FALLBACKS
ASSISTANT_OPENAI_BASE_URL
OPENAI_BASE_URL
ASSISTANT_OPENAI_API_MODE
ASSISTANT_OPENAI_API_KEY
OPENAI_API_KEY
ASSISTANT_OPENAI_OAUTH_PATH
OPENAI_OAUTH_AUTH_JSON_PATH
ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID
ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID_PATH
ASSISTANT_OPENAI_OAUTH_REFRESH
ASSISTANT_OPENAI_OAUTH_REFRESH_INTERVAL
ASSISTANT_OPENROUTER_API_KEY
OPENROUTER_API_KEY
```

For `openrouter`, the base URL defaults to `https://openrouter.ai/api/v1` and the
API mode defaults to `chat-completions`. Override either with
`ASSISTANT_OPENAI_BASE_URL`/`--base-url` and `ASSISTANT_OPENAI_API_MODE`/`--api-mode`.

## Runtime Defaults

Important runtime defaults:

```text
ASSISTANT_WORKDIR              current directory
ASSISTANT_INSTRUCTIONS         (unset; built-in default system prompt)
ASSISTANT_INSTRUCTIONS_FILE    (unset; read system prompt from a file)
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
ASSISTANT_EMBEDDING_MODEL       (unset; lexical-only recall)
ASSISTANT_EMBEDDING_BASE_URL    falls back to OpenAI base URL
ASSISTANT_EMBEDDING_DIMENSIONS  0 (model default)
ASSISTANT_APPROVAL             true
ASSISTANT_APPROVALS_REVIEWER   user
ASSISTANT_APPROVALS_REVIEWER_MODEL  (unset; uses main model)
ASSISTANT_APPROVALS_REVIEWER_TIMEOUT 90
ASSISTANT_MEMORY_REVIEW         off
ASSISTANT_MEMORY_REVIEW_LIMIT   8
ASSISTANT_MEMORY_REVIEWER_MODEL  (unset; uses main model)
ASSISTANT_MEMORY_REVIEWER_TIMEOUT 90
ASSISTANT_GUARDRAILS           true
ASSISTANT_COMPACTION           true
ASSISTANT_PRIVATE_NETWORK      false
ASSISTANT_AUDIT                false
ASSISTANT_AUDIT_LEVEL          full
ASSISTANT_AUDIT_LOG            ~/.gratefulagents/assistant/state/audit.ndjson
ASSISTANT_TRANSCRIPTS          true
ASSISTANT_TRANSCRIPT_LOG       ~/.gratefulagents/assistant/state/transcripts.ndjson
```

## System Prompt

Assistant ships with a built-in system prompt. Override it without rebuilding
the binary, in precedence order (first non-empty wins):

1. `--instructions "<text>"` flag (or `ASSISTANT_INSTRUCTIONS`)
2. `--instructions-file <path>` flag (or `ASSISTANT_INSTRUCTIONS_FILE`)
3. `"instructions"` / `"instructionsPath"` in the JSON config file
4. the built-in default

The file form is convenient for long prompts and for Kubernetes deployments,
where a ConfigMap can be mounted and pointed at with `ASSISTANT_INSTRUCTIONS_FILE`.
A configured prompt replaces the default base instructions; the durable-memory
prime block is still appended automatically when pinned memories or active tasks
are loaded for the run.

`--permission read-only` restricts SDK tool access. `--approval=true` asks
before approval-gated tool execution in interactive mode, and Telegram mode
sends approval cards with Approve and Deny buttons. Set
`--approvals-reviewer auto-review` to run a separate no-tools reviewer model
before prompting the user; it returns allow, deny, or escalate. Escalations use
the normal terminal or Telegram approval path when available, and fail closed
when no human approval requester exists.
`--audit=true` mirrors structured run, model, tool, approval, and result events
to stdout, standard logs, and the append-only audit log path. Set
`--audit-level low` to record only tool calls with inputs, assistant text, and
errors.
`--transcripts=true` persists redacted completed turns to the append-only
transcript log and exposes the read-only `session_search` tool. Transcripts are
for searchable chat history; durable memory remains curated through the
project-state memory tools. When transcripts and project state are both enabled,
Assistant also exposes `memory_distill` for deterministic scans and
`memory_review` for LLM-backed transcript review. Both can preview or apply
stable memory candidates from recent transcripts. `memory_review` uses the main
model by default; override it with `--memory-reviewer-model` or
`ASSISTANT_MEMORY_REVIEWER_MODEL`, and tune its timeout with
`--memory-reviewer-timeout` or `ASSISTANT_MEMORY_REVIEWER_TIMEOUT`.
After-turn review is disabled by default. Set `--memory-review preview` or
`ASSISTANT_MEMORY_REVIEW=preview` to run a review after each completed turn and
log candidate memories without saving them. Set it to `apply` to automatically
save validated non-duplicate candidates. For safety, `apply` only writes when
the turn came from the local terminal (`terminal`/`cli`); on remote channels
such as Telegram, Gmail, or scheduled runs it is automatically downgraded to
`preview` so third-party message content can never silently write durable
memory. `--memory-review-limit` and
`ASSISTANT_MEMORY_REVIEW_LIMIT` control how many recent transcript turns the
after-turn reviewer may inspect. Set `ASSISTANT_TRANSCRIPTS=false` or
`--transcripts=false` to disable transcript persistence and transcript-backed
tools.

## Hybrid Memory Recall

Durable memory recall is lexical (keyword) by default. Set an embedding model
to enable embeddings-backed hybrid recall, which fuses keyword matching with
semantic similarity so the assistant can recall memories that are relevant in
meaning even when they share no exact words with the query.

```text
ASSISTANT_EMBEDDING_MODEL       embedding model; empty disables embeddings
ASSISTANT_EMBEDDING_BASE_URL    OpenAI-compatible embeddings base URL
ASSISTANT_EMBEDDING_API_KEY     embeddings API key (env only)
ASSISTANT_EMBEDDING_DIMENSIONS  optional reduced dimensions
```

`ASSISTANT_EMBEDDING_BASE_URL` and `ASSISTANT_EMBEDDING_API_KEY` fall back to the
OpenAI base URL and API key when unset, so an OpenAI key alone is enough:

```sh
export OPENAI_API_KEY=sk-...
export ASSISTANT_EMBEDDING_MODEL=text-embedding-3-small
```

Any OpenAI-compatible `/v1/embeddings` endpoint works. For a fully local setup,
point at Ollama:

```sh
export ASSISTANT_EMBEDDING_BASE_URL=http://localhost:11434/v1
export ASSISTANT_EMBEDDING_MODEL=bge-m3
```

OpenRouter is not usable for embeddings: it serves chat/completions but not a
general `/v1/embeddings` endpoint. Use OpenAI or a local model for vectors.

Vectors are computed when a memory is stored and cached under the state
directory; memories written before embeddings were enabled are embedded lazily
on their next recall. If the embedding provider is unavailable, recall falls
back to the lexical path automatically.

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
  "approvals": {
    "reviewer": "user",
    "reviewerModel": "",
    "reviewerTimeout": 90
  },
  "features": {
    "defaults": true,
    "tools": {
      "webFetch": false,
      "signals": {
        "presentPlan": false
      }
    },
    "runtime": {
      "parallelToolCalls": true,
      "untrustedToolOutputs": true
    }
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

The `approvals` block is optional. `reviewer` accepts `user` or
`auto-review` (`auto`, `guardian`, and `guardian_subagent` are accepted aliases).
`reviewerModel` lets the approval reviewer use a different model from the main
assistant, and `reviewerTimeout` controls the reviewer timeout in seconds.

The `features` block is optional and maps to the SDK runtime feature gates.
Unset fields keep Assistant's defaults. Set `"defaults": false` to start from an
all-off SDK runtime surface and enable only the features listed in the block.

Configurable groups:

- `tools`: `listFiles`, `readFile`, `glob`, `grep`, `lsp`, `bash`, `write`,
  `edit`, `webFetch`, `asyncShell`, `extraTools`, `visionAnalyzer`, and
  `signals`.
- `tools.signals`: `askUserQuestion`, `presentPlan`, `finish`, `setPhase`.
- `mcp`: `enabled`, `allowAllServers`, `allowedServers`, `allowAllTools`,
  `allowedTools`, `resourceTools`.
- `handoffs`: `enabled`, `genericFallback`.
- `subAgents`: `syncTools`, `genericFallback`, and `async`.
- `subAgents.async`: `spawn`, `run`, `graph`, `list`, `status`, `activity`,
  `taskGraph`, `message`, `collect`, `cancel`.
- `guardrails`: `builtin`.
- `modes`: `instructions`, `phaseTracking`, `modelRouting`.
- `projectState`: `primeContext`, `taskTools`, `memoryTools`, `primeTool`.
- `runtime`: `compaction`, `approval`, `retry`, `forceFinalSummary`,
  `eventStream`, `tracing`, `immediateInputPolling`, `handoffHistory`,
  `parallelToolCalls`, `untrustedToolOutputs`.

Example all-off SDK surface with only file read, final signaling, and retry:

```json
{
  "features": {
    "defaults": false,
    "tools": {
      "readFile": true,
      "signals": {
        "finish": true
      }
    },
    "runtime": {
      "retry": true,
      "untrustedToolOutputs": true
    }
  }
}
```

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

Schedule tools and the background scheduler are enabled by default with
`--scheduling=true` and store durable jobs in the state directory. The
scheduler runs automatically in long-running modes: the interactive REPL,
`serve`, `telegram`, `gmail`, `schedule`, and `poll`. To run only the
scheduler:

```sh
assistant schedule --provider openai-oauth
```

Set `--scheduling=false` to disable both schedule tools and the background
scheduler. One-shot prompts do not keep the scheduler running.
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
ASSISTANT_TELEGRAM_ALLOWED_USERS   comma-separated allowed user IDs/usernames
ASSISTANT_TELEGRAM_ALLOWED_CHATS   comma-separated allowed chat IDs
ASSISTANT_TELEGRAM_POLL_TIMEOUT    optional; defaults to 50 seconds
ASSISTANT_TELEGRAM_ERROR_DETAILS   optional; defaults to false
```

Create the bot with
[Telegram's BotFather](https://core.telegram.org/bots/features#botfather), copy
the bot token, then export it before starting the poller:

```sh
export ASSISTANT_TELEGRAM_BOT_TOKEN='123456:bot-token'
export ASSISTANT_TELEGRAM_ALLOWED_USERS='123456789'
assistant telegram --provider openai-oauth
```

Assistant reads process environment variables. It does not automatically load a
repository `.env` file; use your shell, `direnv`, or another secret manager to
load `.env` before running the command.

Telegram messages are ignored unless the sender or chat is allowlisted. Prefer
numeric Telegram user IDs over usernames. To discover IDs, start the poller,
send one message, read the `telegram access denied` log line, then set the
matching user or chat ID and restart.

To run one Telegram bot per person across a household, see
[Family Deploy](features.md#family-deploy), which provisions a container, a
persistent volume, and a required bot token and allow list for each member.

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
