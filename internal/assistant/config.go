// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

const (
	defaultOAuthRefreshInterval     = time.Hour
	defaultOAuthRefreshIntervalText = "1h"
	approvalReviewerUser            = "user"
	approvalReviewerAutoReview      = "auto-review"
)

type appConfig struct {
	Provider                    string
	Model                       string
	ModelFallbacks              stringListFlag
	BaseURL                     string
	APIMode                     string
	APIKey                      string
	OpenAIOAuthPath             string
	OpenAIOAuthAccountID        string
	OpenAIAccountIDPath         string
	DisableOpenAIOAuthRefresh   bool
	OAuthRefreshInterval        time.Duration
	WorkDir                     string
	StateDir                    string
	EmbeddingModel              string
	EmbeddingBaseURL            string
	EmbeddingAPIKey             string
	EmbeddingDimensions         int
	ConfigPath                  string
	MCPConfigPaths              stringListFlag
	SkillCatalogPath            string
	Permission                  string
	Reasoning                   string
	Verbosity                   string
	MaxTurns                    int
	MaxTokens                   int
	ToolTimeout                 int
	EnableTools                 bool
	EnableMCP                   bool
	EnableSkills                bool
	EnableScheduling            bool
	EnableProjectState          bool
	EnableApproval              bool
	ApprovalsReviewer           string
	ApprovalsReviewerModel      string
	ApprovalsReviewerTimeout    int
	MemoryReviewMode            string
	MemoryReviewLimit           int
	MemoryReviewerModel         string
	MemoryReviewerTimeout       int
	ApprovalsReviewerFlagSet    bool
	ApprovalsReviewerModelSet   bool
	ApprovalsReviewerTimeoutSet bool
	EnableGuardrails            bool
	EnableCompaction            bool
	AllowPrivateNetwork         bool
	Audit                       bool
	AuditLevel                  string
	AuditLogPath                string
	EnableTranscripts           bool
	TranscriptLogPath           string
	Debug                       bool
	Command                     string
	SessionMode                 agentsdk.SessionMode
	ActiveMode                  string
	ActivePhase                 string
	ModeDirectiveText           string
	Serve                       bool
	GatewayAddr                 string
	GatewayToken                string
	UserID                      string
	TokenLimit                  int64
	UsagePath                   string
	LangfuseEnabled             bool
	LangfuseHost                string
	LangfusePublicKey           string
	LangfuseSecretKey           string
	SentryEnabled               bool
	SentryDSN                   string
	SentryEnvironment           string
	TelegramBotToken            string
	TelegramAPIBase             string
	TelegramAllowedUsers        stringListFlag
	TelegramAllowedChats        stringListFlag
	TelegramPollTimeout         int
	TelegramErrorDetails        bool
	GmailToken                  string
	GmailUser                   string
	GmailQuery                  string
	GmailPollInterval           int
	GmailMaxResults             int
	GmailMarkRead               bool
	GmailSendReplies            bool
	GoogleAuthPath              string
	GoogleConnectURL            string
	GoogleScopes                stringListFlag
	Prompt                      string
	Instructions                string
	InstructionsPath            string
	FeatureOverrides            assistantFeaturesConfig
	FileConfig                  assistantConfigFile
}

func parseConfig(args []string) (appConfig, error) {
	cfg := defaultConfig()
	fs := flag.NewFlagSet("assistant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "assistant extension config JSON")
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "provider: openai-oauth, openai-api, or openrouter")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model name")
	fs.Var(&cfg.ModelFallbacks, "model-fallback", "fallback model tried after --model when unavailable (OpenRouter \"models\" routing); may be repeated")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "OpenAI base URL override")
	fs.StringVar(&cfg.APIMode, "api-mode", cfg.APIMode, "OpenAI API mode override")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "OpenAI API key for --provider openai-api")
	fs.StringVar(&cfg.OpenAIOAuthPath, "openai-oauth-path", cfg.OpenAIOAuthPath, "OpenAI OAuth auth JSON path")
	fs.StringVar(&cfg.OpenAIOAuthAccountID, "openai-oauth-account-id", cfg.OpenAIOAuthAccountID, "OpenAI OAuth account ID override")
	fs.StringVar(&cfg.OpenAIAccountIDPath, "openai-oauth-account-id-path", cfg.OpenAIAccountIDPath, "OpenAI OAuth account ID file")
	openAIOAuthRefresh := !cfg.DisableOpenAIOAuthRefresh
	fs.BoolVar(&openAIOAuthRefresh, "openai-oauth-refresh", openAIOAuthRefresh, "allow OpenAI OAuth access-token refresh during assistant runs")
	fs.DurationVar(&cfg.OAuthRefreshInterval, "oauth-refresh-interval", cfg.OAuthRefreshInterval, "repeat oauth-refresh every duration; 0 runs once")
	fs.StringVar(&cfg.WorkDir, "workdir", cfg.WorkDir, "workspace for assistant tools")
	fs.StringVar(&cfg.Instructions, "instructions", cfg.Instructions, "override the assistant system prompt (inline text); empty uses the built-in default")
	fs.StringVar(&cfg.InstructionsPath, "instructions-file", cfg.InstructionsPath, "read the assistant system prompt from a file; used only when --instructions is empty")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "durable assistant state directory")
	fs.StringVar(&cfg.Permission, "permission", cfg.Permission, "tool permission: workspace-write or read-only")
	fs.StringVar(&cfg.Reasoning, "reasoning", cfg.Reasoning, "reasoning level: none, low, medium, high, xhigh")
	fs.StringVar(&cfg.Verbosity, "verbosity", cfg.Verbosity, "text verbosity: low, medium, high")
	fs.StringVar(&cfg.SkillCatalogPath, "skill-catalog", cfg.SkillCatalogPath, "optional SDK skill catalog JSON")
	fs.Var(&cfg.MCPConfigPaths, "mcp-config", "additional MCP config JSON path; may be repeated")
	fs.IntVar(&cfg.MaxTurns, "max-turns", cfg.MaxTurns, "maximum SDK turns per user message")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", cfg.MaxTokens, "maximum output tokens")
	fs.IntVar(&cfg.ToolTimeout, "tool-timeout", cfg.ToolTimeout, "tool timeout in seconds")
	fs.BoolVar(&cfg.EnableTools, "tools", cfg.EnableTools, "enable SDK tools")
	fs.BoolVar(&cfg.EnableMCP, "mcp", cfg.EnableMCP, "load MCP tools from extension config and .mcp.json")
	fs.BoolVar(&cfg.EnableSkills, "skills", cfg.EnableSkills, "enable SDK skill discovery/install tools")
	fs.BoolVar(&cfg.EnableScheduling, "scheduling", cfg.EnableScheduling, "enable schedule tools and background scheduler")
	fs.BoolVar(&cfg.EnableProjectState, "project-state", cfg.EnableProjectState, "enable durable memory and task tools")
	fs.StringVar(&cfg.EmbeddingModel, "embedding-model", cfg.EmbeddingModel, "embedding model for hybrid memory recall; empty disables embeddings (lexical only)")
	fs.StringVar(&cfg.EmbeddingBaseURL, "embedding-base-url", cfg.EmbeddingBaseURL, "OpenAI-compatible embeddings base URL")
	fs.IntVar(&cfg.EmbeddingDimensions, "embedding-dimensions", cfg.EmbeddingDimensions, "optional embedding output dimensions when the model supports it")
	fs.BoolVar(&cfg.EnableApproval, "approval", cfg.EnableApproval, "ask before tool execution")
	fs.StringVar(&cfg.ApprovalsReviewer, "approvals-reviewer", cfg.ApprovalsReviewer, "approval reviewer: user or auto-review")
	fs.StringVar(&cfg.ApprovalsReviewerModel, "approvals-reviewer-model", cfg.ApprovalsReviewerModel, "model override for --approvals-reviewer auto-review")
	fs.IntVar(&cfg.ApprovalsReviewerTimeout, "approvals-reviewer-timeout", cfg.ApprovalsReviewerTimeout, "auto-review approval timeout in seconds")
	fs.StringVar(&cfg.MemoryReviewMode, "memory-review", cfg.MemoryReviewMode, "after-turn memory review: off, preview, or apply")
	fs.IntVar(&cfg.MemoryReviewLimit, "memory-review-limit", cfg.MemoryReviewLimit, "maximum recent transcript turns for after-turn memory review")
	fs.StringVar(&cfg.MemoryReviewerModel, "memory-reviewer-model", cfg.MemoryReviewerModel, "model override for LLM-backed memory_review")
	fs.IntVar(&cfg.MemoryReviewerTimeout, "memory-reviewer-timeout", cfg.MemoryReviewerTimeout, "memory_review timeout in seconds")
	fs.BoolVar(&cfg.EnableGuardrails, "guardrails", cfg.EnableGuardrails, "enable SDK guardrails")
	fs.BoolVar(&cfg.EnableCompaction, "compaction", cfg.EnableCompaction, "enable SDK context compaction")
	fs.BoolVar(&cfg.AllowPrivateNetwork, "private-network", cfg.AllowPrivateNetwork, "allow web tools to reach private network URLs")
	fs.BoolVar(&cfg.Audit, "audit", cfg.Audit, "emit structured audit events to stdout and logs")
	fs.StringVar(&cfg.AuditLevel, "audit-level", cfg.AuditLevel, "audit verbosity: low or full")
	fs.StringVar(&cfg.AuditLogPath, "audit-log", cfg.AuditLogPath, "append-only audit log path; defaults to state-dir/audit.ndjson")
	fs.BoolVar(&cfg.EnableTranscripts, "transcripts", cfg.EnableTranscripts, "persist redacted conversation transcripts for session_search")
	fs.StringVar(&cfg.TranscriptLogPath, "transcript-log", cfg.TranscriptLogPath, "legacy transcript JSONL path imported once into state-dir/state.db; defaults to state-dir/transcripts.ndjson")
	fs.BoolVar(&cfg.Debug, "debug", cfg.Debug, "enable SDK debug logging")
	fs.StringVar(&cfg.GatewayAddr, "addr", cfg.GatewayAddr, "gateway listen address for serve mode")
	fs.StringVar(&cfg.GatewayToken, "gateway-token", cfg.GatewayToken, "bearer token for generic gateway endpoint")
	fs.StringVar(&cfg.TelegramBotToken, "telegram-bot-token", cfg.TelegramBotToken, "Telegram bot token for long polling")
	fs.StringVar(&cfg.TelegramAPIBase, "telegram-api-base", cfg.TelegramAPIBase, "Telegram API root override (e.g. an ingress gateway); empty uses https://api.telegram.org")
	fs.Var(&cfg.TelegramAllowedUsers, "telegram-allowed-user", "Telegram user ID or username allowed to use the bot; repeat or comma-separate")
	fs.Var(&cfg.TelegramAllowedChats, "telegram-allowed-chat", "Telegram chat ID allowed to use the bot; repeat or comma-separate")
	fs.IntVar(&cfg.TelegramPollTimeout, "telegram-poll-timeout", cfg.TelegramPollTimeout, "Telegram long-poll timeout in seconds")
	fs.BoolVar(&cfg.TelegramErrorDetails, "telegram-error-details", cfg.TelegramErrorDetails, "include raw run error details in Telegram failure replies")
	fs.StringVar(&cfg.GmailToken, "gmail-token", cfg.GmailToken, "Gmail OAuth access token for polling")
	fs.StringVar(&cfg.GmailUser, "gmail-user", cfg.GmailUser, "Gmail user id; usually me")
	fs.StringVar(&cfg.GmailQuery, "gmail-query", cfg.GmailQuery, "Gmail search query for polling")
	fs.IntVar(&cfg.GmailPollInterval, "gmail-poll-interval", cfg.GmailPollInterval, "Gmail polling interval in seconds")
	fs.IntVar(&cfg.GmailMaxResults, "gmail-max-results", cfg.GmailMaxResults, "maximum Gmail messages to fetch per poll")
	fs.BoolVar(&cfg.GmailMarkRead, "gmail-mark-read", cfg.GmailMarkRead, "mark Gmail messages read after processing")
	fs.BoolVar(&cfg.GmailSendReplies, "gmail-send-replies", cfg.GmailSendReplies, "send assistant replies through Gmail")
	fs.StringVar(&cfg.GoogleAuthPath, "google-auth-path", cfg.GoogleAuthPath, "path to the brokered Google auth JSON; defaults to state-dir/google-auth.json")
	fs.StringVar(&cfg.GoogleConnectURL, "google-connect-url", cfg.GoogleConnectURL, "base URL of the Google Connect broker for google-connect/refresh")
	fs.Var(&cfg.GoogleScopes, "google-scope", "Google OAuth scope to request during google-connect; repeat or comma-separate")

	if err := fs.Parse(args); err != nil {
		return appConfig{}, fmt.Errorf("%w\n\n%s", err, usage())
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "approvals-reviewer":
			cfg.ApprovalsReviewerFlagSet = true
		case "approvals-reviewer-model":
			cfg.ApprovalsReviewerModelSet = true
		case "approvals-reviewer-timeout":
			cfg.ApprovalsReviewerTimeoutSet = true
		}
	})
	cfg.DisableOpenAIOAuthRefresh = !openAIOAuthRefresh
	cfg.Prompt = strings.Join(fs.Args(), " ")
	return cfg, nil
}

func defaultConfig() appConfig {
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		wd = "."
	}
	return appConfig{
		ConfigPath:                firstNonEmpty(os.Getenv("ASSISTANT_CONFIG"), defaultConfigPath()),
		Provider:                  firstNonEmpty(os.Getenv("ASSISTANT_PROVIDER"), providerOpenAIOAuth),
		Model:                     strings.TrimSpace(os.Getenv("ASSISTANT_MODEL")),
		ModelFallbacks:            splitCommaListEnv(os.Getenv("ASSISTANT_MODEL_FALLBACKS")),
		BaseURL:                   firstNonEmpty(os.Getenv("ASSISTANT_OPENAI_BASE_URL"), os.Getenv("OPENAI_BASE_URL")),
		APIMode:                   strings.TrimSpace(os.Getenv("ASSISTANT_OPENAI_API_MODE")),
		APIKey:                    firstNonEmpty(os.Getenv("ASSISTANT_OPENAI_API_KEY"), os.Getenv("OPENAI_API_KEY")),
		OpenAIOAuthPath:           firstNonEmpty(os.Getenv("ASSISTANT_OPENAI_OAUTH_PATH"), os.Getenv("OPENAI_OAUTH_AUTH_JSON_PATH"), defaultOAuthPath()),
		OpenAIOAuthAccountID:      strings.TrimSpace(os.Getenv("ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID")),
		OpenAIAccountIDPath:       strings.TrimSpace(os.Getenv("ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID_PATH")),
		DisableOpenAIOAuthRefresh: !envBool("ASSISTANT_OPENAI_OAUTH_REFRESH", true),
		OAuthRefreshInterval:      envDuration("ASSISTANT_OPENAI_OAUTH_REFRESH_INTERVAL", defaultOAuthRefreshInterval),
		WorkDir:                   firstNonEmpty(os.Getenv("ASSISTANT_WORKDIR"), wd),
		Instructions:              strings.TrimSpace(os.Getenv("ASSISTANT_INSTRUCTIONS")),
		InstructionsPath:          strings.TrimSpace(os.Getenv("ASSISTANT_INSTRUCTIONS_FILE")),
		StateDir:                  firstNonEmpty(os.Getenv("ASSISTANT_STATE_DIR"), defaultStateDir()),
		EmbeddingModel:            strings.TrimSpace(os.Getenv("ASSISTANT_EMBEDDING_MODEL")),
		EmbeddingBaseURL:          firstNonEmpty(os.Getenv("ASSISTANT_EMBEDDING_BASE_URL"), os.Getenv("ASSISTANT_OPENAI_BASE_URL"), os.Getenv("OPENAI_BASE_URL")),
		EmbeddingAPIKey:           firstNonEmpty(os.Getenv("ASSISTANT_EMBEDDING_API_KEY"), os.Getenv("ASSISTANT_OPENAI_API_KEY"), os.Getenv("OPENAI_API_KEY")),
		EmbeddingDimensions:       envInt("ASSISTANT_EMBEDDING_DIMENSIONS", 0),
		MCPConfigPaths:            splitListEnv(os.Getenv("ASSISTANT_MCP_CONFIGS")),
		SkillCatalogPath:          strings.TrimSpace(os.Getenv("ASSISTANT_SKILL_CATALOG")),
		Permission:                firstNonEmpty(os.Getenv("ASSISTANT_PERMISSION"), string(sdkpolicy.PermissionModeWorkspaceWrite)),
		Reasoning:                 firstNonEmpty(os.Getenv("ASSISTANT_REASONING"), string(agentsdk.ReasoningLow)),
		Verbosity:                 firstNonEmpty(os.Getenv("ASSISTANT_VERBOSITY"), string(agentsdk.TextVerbosityMedium)),
		MaxTurns:                  envInt("ASSISTANT_MAX_TURNS", 8),
		MaxTokens:                 envInt("ASSISTANT_MAX_TOKENS", 1200),
		ToolTimeout:               envInt("ASSISTANT_TOOL_TIMEOUT", 0),
		EnableTools:               envBool("ASSISTANT_TOOLS", true),
		EnableMCP:                 envBool("ASSISTANT_MCP", false),
		EnableSkills:              envBool("ASSISTANT_SKILLS", false),
		EnableScheduling:          envBool("ASSISTANT_SCHEDULING", true),
		EnableProjectState:        envBool("ASSISTANT_PROJECT_STATE", true),
		EnableApproval:            envBool("ASSISTANT_APPROVAL", true),
		ApprovalsReviewer:         firstNonEmpty(os.Getenv("ASSISTANT_APPROVALS_REVIEWER"), approvalReviewerUser),
		ApprovalsReviewerModel:    strings.TrimSpace(os.Getenv("ASSISTANT_APPROVALS_REVIEWER_MODEL")),
		ApprovalsReviewerTimeout:  envInt("ASSISTANT_APPROVALS_REVIEWER_TIMEOUT", 90),
		MemoryReviewMode:          firstNonEmpty(os.Getenv("ASSISTANT_MEMORY_REVIEW"), memoryReviewModeOff),
		MemoryReviewLimit:         envInt("ASSISTANT_MEMORY_REVIEW_LIMIT", 8),
		MemoryReviewerModel:       strings.TrimSpace(os.Getenv("ASSISTANT_MEMORY_REVIEWER_MODEL")),
		MemoryReviewerTimeout:     envInt("ASSISTANT_MEMORY_REVIEWER_TIMEOUT", 90),
		EnableGuardrails:          envBool("ASSISTANT_GUARDRAILS", true),
		EnableCompaction:          envBool("ASSISTANT_COMPACTION", true),
		AllowPrivateNetwork:       envBool("ASSISTANT_PRIVATE_NETWORK", false),
		Audit:                     envBool("ASSISTANT_AUDIT", false),
		AuditLevel:                firstNonEmpty(os.Getenv("ASSISTANT_AUDIT_LEVEL"), auditLevelFull),
		AuditLogPath:              strings.TrimSpace(os.Getenv("ASSISTANT_AUDIT_LOG")),
		EnableTranscripts:         envBool("ASSISTANT_TRANSCRIPTS", true),
		TranscriptLogPath:         strings.TrimSpace(os.Getenv("ASSISTANT_TRANSCRIPT_LOG")),
		GatewayAddr:               firstNonEmpty(os.Getenv("ASSISTANT_GATEWAY_ADDR"), ":8080"),
		GatewayToken:              strings.TrimSpace(os.Getenv("ASSISTANT_GATEWAY_TOKEN")),
		UserID:                    strings.TrimSpace(os.Getenv("ASSISTANT_USER_ID")),
		TokenLimit:                envInt64("ASSISTANT_TOKEN_LIMIT", 0),
		UsagePath:                 strings.TrimSpace(os.Getenv("ASSISTANT_USAGE_PATH")),
		LangfuseEnabled:           envBool("ASSISTANT_LANGFUSE", false),
		LangfuseHost:              firstNonEmpty(os.Getenv("ASSISTANT_LANGFUSE_HOST"), "https://cloud.langfuse.com"),
		LangfusePublicKey:         strings.TrimSpace(os.Getenv("ASSISTANT_LANGFUSE_PUBLIC_KEY")),
		LangfuseSecretKey:         strings.TrimSpace(os.Getenv("ASSISTANT_LANGFUSE_SECRET_KEY")),
		SentryEnabled:             envBool("ASSISTANT_SENTRY", false),
		SentryDSN:                 strings.TrimSpace(os.Getenv("ASSISTANT_SENTRY_DSN")),
		SentryEnvironment:         strings.TrimSpace(os.Getenv("ASSISTANT_SENTRY_ENVIRONMENT")),
		TelegramBotToken:          strings.TrimSpace(os.Getenv("ASSISTANT_TELEGRAM_BOT_TOKEN")),
		TelegramAPIBase:           strings.TrimSpace(os.Getenv("ASSISTANT_TELEGRAM_API_BASE")),
		TelegramAllowedUsers:      splitListEnv(os.Getenv("ASSISTANT_TELEGRAM_ALLOWED_USERS")),
		TelegramAllowedChats:      splitListEnv(os.Getenv("ASSISTANT_TELEGRAM_ALLOWED_CHATS")),
		TelegramPollTimeout:       envInt("ASSISTANT_TELEGRAM_POLL_TIMEOUT", 50),
		TelegramErrorDetails:      envBool("ASSISTANT_TELEGRAM_ERROR_DETAILS", false),
		GmailToken:                firstNonEmpty(os.Getenv("ASSISTANT_GMAIL_ACCESS_TOKEN"), os.Getenv("ASSISTANT_GMAIL_TOKEN")),
		GmailUser:                 firstNonEmpty(os.Getenv("ASSISTANT_GMAIL_USER"), "me"),
		GmailQuery:                firstNonEmpty(os.Getenv("ASSISTANT_GMAIL_QUERY"), "is:unread"),
		GmailPollInterval:         envInt("ASSISTANT_GMAIL_POLL_INTERVAL", 60),
		GmailMaxResults:           envInt("ASSISTANT_GMAIL_MAX_RESULTS", 10),
		GmailMarkRead:             envBool("ASSISTANT_GMAIL_MARK_READ", false),
		GmailSendReplies:          envBool("ASSISTANT_GMAIL_SEND_REPLIES", false),
		GoogleAuthPath:            strings.TrimSpace(os.Getenv("ASSISTANT_GOOGLE_AUTH_PATH")),
		GoogleConnectURL:          strings.TrimSpace(os.Getenv("ASSISTANT_GOOGLE_CONNECT_URL")),
		GoogleScopes:              splitListEnv(os.Getenv("ASSISTANT_GOOGLE_SCOPES")),
	}
}

func (c *appConfig) validate() error {
	c.Provider = normalizeProvider(c.Provider)
	if c.Provider == "" {
		return fmt.Errorf("unsupported provider\n\n%s", usage())
	}
	if strings.TrimSpace(c.Model) == "" {
		c.Model = defaultModel(c.Provider)
	}
	if strings.TrimSpace(c.WorkDir) == "" {
		c.WorkDir = "."
	}
	if abs, err := filepath.Abs(expandUserPath(c.WorkDir)); err == nil {
		c.WorkDir = abs
	}
	if strings.TrimSpace(c.StateDir) != "" {
		if abs, err := filepath.Abs(expandUserPath(c.StateDir)); err == nil {
			c.StateDir = abs
		}
	}
	c.ConfigPath = expandUserPath(c.ConfigPath)
	c.AuditLogPath = expandUserPath(c.AuditLogPath)
	c.TranscriptLogPath = expandUserPath(c.TranscriptLogPath)
	c.SkillCatalogPath = expandUserPath(c.SkillCatalogPath)
	c.OpenAIOAuthPath = expandUserPath(c.OpenAIOAuthPath)
	c.OpenAIAccountIDPath = expandUserPath(c.OpenAIAccountIDPath)
	c.MCPConfigPaths = expandPathList(c.MCPConfigPaths)
	c.Permission = normalizePermission(c.Permission)
	c.ApprovalsReviewer = normalizeApprovalsReviewer(c.ApprovalsReviewer)
	if c.ApprovalsReviewer == "" {
		return errors.New("--approvals-reviewer must be user or auto-review")
	}
	if c.ApprovalsReviewerTimeout <= 0 {
		c.ApprovalsReviewerTimeout = 90
	}
	if c.MemoryReviewerTimeout <= 0 {
		c.MemoryReviewerTimeout = 90
	}
	c.MemoryReviewMode = normalizeMemoryReviewMode(c.MemoryReviewMode)
	if c.MemoryReviewMode == "" {
		return errors.New("--memory-review must be off, preview, or apply")
	}
	if c.MemoryReviewLimit <= 0 {
		c.MemoryReviewLimit = 8
	}
	if c.MemoryReviewLimit > 50 {
		c.MemoryReviewLimit = 50
	}
	c.AuditLevel = normalizeAuditLevel(c.AuditLevel)
	if c.AuditLevel == "" {
		return errors.New("--audit-level must be low or full")
	}
	if c.TelegramPollTimeout <= 0 {
		c.TelegramPollTimeout = 50
	}
	c.TelegramAllowedUsers = normalizeTelegramAllowList(c.TelegramAllowedUsers)
	c.TelegramAllowedChats = normalizeTelegramAllowList(c.TelegramAllowedChats)
	if c.GmailPollInterval <= 0 {
		c.GmailPollInterval = 60
	}
	if c.GmailMaxResults <= 0 {
		c.GmailMaxResults = 10
	}
	if strings.TrimSpace(c.GmailUser) == "" {
		c.GmailUser = "me"
	}
	if strings.TrimSpace(c.GmailQuery) == "" {
		c.GmailQuery = "is:unread"
	}
	if c.Audit && strings.TrimSpace(c.AuditLogPath) == "" {
		c.AuditLogPath = stateFilePath(*c, "audit.ndjson")
	}
	if c.EnableTranscripts && strings.TrimSpace(c.TranscriptLogPath) == "" {
		c.TranscriptLogPath = stateFilePath(*c, transcriptStateFileName)
	}

	if err := c.loadFileConfig(); err != nil {
		return err
	}
	c.applyFileConfig()
	if err := c.resolveInstructions(); err != nil {
		return err
	}
	c.ApprovalsReviewer = normalizeApprovalsReviewer(c.ApprovalsReviewer)
	if c.ApprovalsReviewer == "" {
		return errors.New("--approvals-reviewer must be user or auto-review")
	}
	if c.ApprovalsReviewerTimeout <= 0 {
		c.ApprovalsReviewerTimeout = 90
	}
	if c.MemoryReviewerTimeout <= 0 {
		c.MemoryReviewerTimeout = 90
	}
	c.MemoryReviewMode = normalizeMemoryReviewMode(c.MemoryReviewMode)
	if c.MemoryReviewMode == "" {
		return errors.New("--memory-review must be off, preview, or apply")
	}
	if c.MemoryReviewLimit <= 0 {
		c.MemoryReviewLimit = 8
	}
	if c.MemoryReviewLimit > 50 {
		c.MemoryReviewLimit = 50
	}

	switch c.Provider {
	case providerOpenAIAPI:
		if strings.TrimSpace(c.APIKey) == "" {
			return errors.New("--provider openai-api requires --api-key or OPENAI_API_KEY")
		}
		if strings.TrimSpace(c.BaseURL) == "" {
			c.BaseURL = "https://api.openai.com/v1"
		}
		if strings.TrimSpace(c.APIMode) == "" {
			c.APIMode = "responses"
		}
	case providerOpenRouter:
		if strings.TrimSpace(c.APIKey) == "" {
			c.APIKey = firstNonEmpty(os.Getenv("ASSISTANT_OPENROUTER_API_KEY"), os.Getenv("OPENROUTER_API_KEY"))
		}
		if strings.TrimSpace(c.APIKey) == "" {
			return errors.New("--provider openrouter requires --api-key or OPENROUTER_API_KEY")
		}
		if strings.TrimSpace(c.BaseURL) == "" {
			c.BaseURL = defaultOpenRouterBaseURL
		}
		if strings.TrimSpace(c.APIMode) == "" {
			c.APIMode = defaultOpenRouterAPIMode
		}
	case providerOpenAIOAuth:
		if strings.TrimSpace(c.OpenAIOAuthPath) == "" {
			return errors.New("--provider openai-oauth requires --openai-oauth-path")
		}
	}
	return nil
}

func (c *appConfig) validateOAuthRefreshCommand() error {
	c.OpenAIOAuthPath = expandUserPath(c.OpenAIOAuthPath)
	c.OpenAIAccountIDPath = expandUserPath(c.OpenAIAccountIDPath)
	if strings.TrimSpace(c.OpenAIOAuthPath) == "" {
		return errors.New("oauth-refresh requires --openai-oauth-path")
	}
	if strings.TrimSpace(c.Prompt) != "" {
		return errors.New("oauth-refresh does not accept a prompt")
	}
	if c.OAuthRefreshInterval < 0 {
		return errors.New("--oauth-refresh-interval must be non-negative")
	}
	return nil
}

func isConnectCommand(arg string) bool {
	switch strings.TrimSpace(strings.ToLower(arg)) {
	case "google-connect", "google-refresh", "google-disconnect":
		return true
	default:
		return false
	}
}

// validateConnectCommand validates the Google Connect client commands. These
// commands do not talk to the model provider, so they skip the standard
// provider/model validation.
func (c *appConfig) validateConnectCommand(command string) error {
	if strings.TrimSpace(c.WorkDir) == "" {
		c.WorkDir = "."
	}
	if strings.TrimSpace(c.StateDir) != "" {
		if abs, err := filepath.Abs(expandUserPath(c.StateDir)); err == nil {
			c.StateDir = abs
		}
	}
	c.GoogleAuthPath = expandUserPath(c.GoogleAuthPath)
	c.GoogleScopes = normalizeGoogleScopes(c.GoogleScopes)
	if len(c.GoogleScopes) == 0 {
		c.GoogleScopes = defaultGoogleScopes()
	}
	if c.OAuthRefreshInterval < 0 {
		return errors.New("--oauth-refresh-interval must be non-negative")
	}

	switch strings.TrimSpace(strings.ToLower(command)) {
	case "google-connect":
		if strings.TrimSpace(c.GoogleConnectURL) == "" {
			return errors.New("google-connect requires --google-connect-url (or ASSISTANT_GOOGLE_CONNECT_URL)")
		}
	case "google-refresh", "google-disconnect":
		// Resolved from the saved google-auth.json at runtime.
	}
	return nil
}

// resolveInstructions resolves the configurable system prompt. An inline value
// (--instructions / ASSISTANT_INSTRUCTIONS / config "instructions") always wins;
// otherwise a file path (--instructions-file / ASSISTANT_INSTRUCTIONS_FILE /
// config "instructionsPath") is read from disk. When neither is set the runtime
// falls back to the built-in default instructions.
func (c *appConfig) resolveInstructions() error {
	c.Instructions = strings.TrimSpace(c.Instructions)
	if c.Instructions != "" {
		return nil
	}
	path := expandUserPath(strings.TrimSpace(c.InstructionsPath))
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read instructions file %s: %w", path, err)
	}
	c.Instructions = strings.TrimSpace(string(data))
	return nil
}

func (c *appConfig) loadFileConfig() error {
	path := strings.TrimSpace(c.ConfigPath)
	if path == "" {
		return nil
	}
	cfg, exists, err := loadAssistantConfigFile(path)
	if err != nil {
		return err
	}
	if exists {
		c.FileConfig = cfg
	}
	return nil
}

func (c *appConfig) applyFileConfig() {
	fc := c.FileConfig
	if strings.TrimSpace(c.Instructions) == "" && strings.TrimSpace(c.InstructionsPath) == "" {
		c.Instructions = strings.TrimSpace(fc.Instructions)
		c.InstructionsPath = strings.TrimSpace(fc.InstructionsPath)
	}
	c.MCPConfigPaths = append(c.MCPConfigPaths, expandPathList(fc.MCPConfigPaths)...)
	if strings.TrimSpace(fc.Approvals.Reviewer) != "" && !c.ApprovalsReviewerFlagSet {
		c.ApprovalsReviewer = fc.Approvals.Reviewer
	}
	if strings.TrimSpace(c.ApprovalsReviewerModel) == "" && !c.ApprovalsReviewerModelSet {
		c.ApprovalsReviewerModel = strings.TrimSpace(fc.Approvals.ReviewerModel)
	}
	if fc.Approvals.ReviewerTimeout > 0 && !c.ApprovalsReviewerTimeoutSet {
		c.ApprovalsReviewerTimeout = fc.Approvals.ReviewerTimeout
	}
	if strings.TrimSpace(c.SkillCatalogPath) == "" {
		c.SkillCatalogPath = expandUserPath(fc.Skills.CatalogPath)
	}
	if fc.Skills.Enabled != nil {
		c.EnableSkills = *fc.Skills.Enabled
	}
	c.FeatureOverrides = fc.Features
	for _, extension := range fc.Extensions {
		if !extension.enabled() {
			continue
		}
		c.MCPConfigPaths = append(c.MCPConfigPaths, expandPathList(extension.MCPConfigPaths)...)
		if strings.TrimSpace(extension.ConfigPath) != "" {
			c.MCPConfigPaths = append(c.MCPConfigPaths, expandUserPath(extension.ConfigPath))
		}
	}
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, strings.TrimSpace(value))
	return nil
}

func normalizeApprovalsReviewer(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "user", "human":
		return approvalReviewerUser
	case "auto", "auto-review", "auto_review", "guardian", "guardian_subagent":
		return approvalReviewerAutoReview
	default:
		return ""
	}
}

func usage() string {
	return strings.TrimSpace(`usage: assistant [flags] [prompt]

providers:
  --provider openai-oauth   use OpenAI OAuth credentials
  --provider openai-api     use OPENAI_API_KEY or --api-key
  --provider openrouter     use OPENROUTER_API_KEY or --api-key
  --model-fallback MODEL    fallback model tried when --model is unavailable
                            (OpenRouter "models" routing); repeat for more

extension config:
  --config PATH             assistant JSON config; defaults to ~/.gratefulagents/assistant/config.json
  --instructions TEXT       override the system prompt inline (also ASSISTANT_INSTRUCTIONS)
  --instructions-file PATH  read the system prompt from a file (also ASSISTANT_INSTRUCTIONS_FILE)
  --mcp-config PATH         add an MCP config; repeat for any number of servers/bundles
  --skills                  expose SDK skill search/install/list tools
  --scheduling              expose schedule tools and run the background scheduler
  --embedding-model MODEL   enable embeddings-backed hybrid memory recall (empty = lexical only)
  --audit                   emit structured audit events to stdout and logs
  --audit-level LEVEL       audit verbosity: low or full
  --audit-log PATH          append audit JSONL to PATH
  --transcripts             persist redacted transcripts for session_search
  --memory-review MODE      after-turn memory review: off, preview, or apply
  --memory-reviewer-model MODEL  model override for memory_review

examples:
  assistant version
  assistant update
  assistant oauth-refresh
  assistant --provider openai-oauth
  assistant schedule --provider openai-oauth
  assistant telegram --provider openai-oauth
  assistant gmail --provider openai-oauth --gmail-query "is:unread"
  assistant google-connect --google-connect-url https://connect.gratefulagents.dev
  assistant google-refresh
  assistant poll --provider openai-oauth
  assistant serve --provider openai-oauth --addr :8080
  assistant family-deploy
  assistant --provider openai-api "what changed in this repo?"
  OPENROUTER_API_KEY=sk-or-... assistant --provider openrouter --model openai/gpt-4o-mini
  OPENROUTER_API_KEY=sk-or-... assistant --provider openrouter --model deepseek/deepseek-v4-pro --model-fallback deepseek/deepseek-chat --model-fallback openrouter/auto
`)
}
