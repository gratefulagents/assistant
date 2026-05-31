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

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

type appConfig struct {
	Provider             string
	Model                string
	BaseURL              string
	APIMode              string
	APIKey               string
	OpenAIOAuthPath      string
	OpenAIOAuthAccountID string
	OpenAIAccountIDPath  string
	WorkDir              string
	StateDir             string
	ConfigPath           string
	MCPConfigPaths       stringListFlag
	SkillCatalogPath     string
	Permission           string
	Reasoning            string
	Verbosity            string
	MaxTurns             int
	MaxTokens            int
	ToolTimeout          int
	EnableTools          bool
	EnableMCP            bool
	EnableSkills         bool
	EnableScheduling     bool
	EnableProjectState   bool
	EnableApproval       bool
	EnableGuardrails     bool
	EnableCompaction     bool
	AllowPrivateNetwork  bool
	Audit                bool
	AuditLevel           string
	AuditLogPath         string
	Debug                bool
	Command              string
	SessionMode          agentsdk.SessionMode
	ActiveMode           string
	ActivePhase          string
	ModeDirectiveText    string
	Serve                bool
	GatewayAddr          string
	GatewayToken         string
	TelegramBotToken     string
	TelegramPollTimeout  int
	GmailToken           string
	GmailUser            string
	GmailQuery           string
	GmailPollInterval    int
	GmailMaxResults      int
	GmailMarkRead        bool
	GmailSendReplies     bool
	Prompt               string
	FileConfig           assistantConfigFile
}

func parseConfig(args []string) (appConfig, error) {
	cfg := defaultConfig()
	fs := flag.NewFlagSet("assistant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "assistant extension config JSON")
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "provider: openai-oauth or openai-api")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model name")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "OpenAI base URL override")
	fs.StringVar(&cfg.APIMode, "api-mode", cfg.APIMode, "OpenAI API mode override")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "OpenAI API key for --provider openai-api")
	fs.StringVar(&cfg.OpenAIOAuthPath, "openai-oauth-path", cfg.OpenAIOAuthPath, "OpenAI OAuth auth JSON path")
	fs.StringVar(&cfg.OpenAIOAuthAccountID, "openai-oauth-account-id", cfg.OpenAIOAuthAccountID, "OpenAI OAuth account ID override")
	fs.StringVar(&cfg.OpenAIAccountIDPath, "openai-oauth-account-id-path", cfg.OpenAIAccountIDPath, "OpenAI OAuth account ID file")
	fs.StringVar(&cfg.WorkDir, "workdir", cfg.WorkDir, "workspace for assistant tools")
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
	fs.BoolVar(&cfg.EnableApproval, "approval", cfg.EnableApproval, "ask before tool execution")
	fs.BoolVar(&cfg.EnableGuardrails, "guardrails", cfg.EnableGuardrails, "enable SDK guardrails")
	fs.BoolVar(&cfg.EnableCompaction, "compaction", cfg.EnableCompaction, "enable SDK context compaction")
	fs.BoolVar(&cfg.AllowPrivateNetwork, "private-network", cfg.AllowPrivateNetwork, "allow web tools to reach private network URLs")
	fs.BoolVar(&cfg.Audit, "audit", cfg.Audit, "emit structured audit events to stdout and logs")
	fs.StringVar(&cfg.AuditLevel, "audit-level", cfg.AuditLevel, "audit verbosity: low or full")
	fs.StringVar(&cfg.AuditLogPath, "audit-log", cfg.AuditLogPath, "append-only audit log path; defaults to state-dir/audit.ndjson")
	fs.BoolVar(&cfg.Debug, "debug", cfg.Debug, "enable SDK debug logging")
	fs.StringVar(&cfg.GatewayAddr, "addr", cfg.GatewayAddr, "gateway listen address for serve mode")
	fs.StringVar(&cfg.GatewayToken, "gateway-token", cfg.GatewayToken, "bearer token for generic gateway endpoint")
	fs.StringVar(&cfg.TelegramBotToken, "telegram-bot-token", cfg.TelegramBotToken, "Telegram bot token for long polling")
	fs.IntVar(&cfg.TelegramPollTimeout, "telegram-poll-timeout", cfg.TelegramPollTimeout, "Telegram long-poll timeout in seconds")
	fs.StringVar(&cfg.GmailToken, "gmail-token", cfg.GmailToken, "Gmail OAuth access token for polling")
	fs.StringVar(&cfg.GmailUser, "gmail-user", cfg.GmailUser, "Gmail user id; usually me")
	fs.StringVar(&cfg.GmailQuery, "gmail-query", cfg.GmailQuery, "Gmail search query for polling")
	fs.IntVar(&cfg.GmailPollInterval, "gmail-poll-interval", cfg.GmailPollInterval, "Gmail polling interval in seconds")
	fs.IntVar(&cfg.GmailMaxResults, "gmail-max-results", cfg.GmailMaxResults, "maximum Gmail messages to fetch per poll")
	fs.BoolVar(&cfg.GmailMarkRead, "gmail-mark-read", cfg.GmailMarkRead, "mark Gmail messages read after processing")
	fs.BoolVar(&cfg.GmailSendReplies, "gmail-send-replies", cfg.GmailSendReplies, "send assistant replies through Gmail")

	if err := fs.Parse(args); err != nil {
		return appConfig{}, fmt.Errorf("%w\n\n%s", err, usage())
	}
	cfg.Prompt = strings.Join(fs.Args(), " ")
	return cfg, nil
}

func defaultConfig() appConfig {
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		wd = "."
	}
	return appConfig{
		ConfigPath:           firstNonEmpty(os.Getenv("ASSISTANT_CONFIG"), defaultConfigPath()),
		Provider:             firstNonEmpty(os.Getenv("ASSISTANT_PROVIDER"), providerOpenAIOAuth),
		Model:                strings.TrimSpace(os.Getenv("ASSISTANT_MODEL")),
		BaseURL:              firstNonEmpty(os.Getenv("ASSISTANT_OPENAI_BASE_URL"), os.Getenv("OPENAI_BASE_URL")),
		APIMode:              strings.TrimSpace(os.Getenv("ASSISTANT_OPENAI_API_MODE")),
		APIKey:               firstNonEmpty(os.Getenv("ASSISTANT_OPENAI_API_KEY"), os.Getenv("OPENAI_API_KEY")),
		OpenAIOAuthPath:      firstNonEmpty(os.Getenv("ASSISTANT_OPENAI_OAUTH_PATH"), os.Getenv("OPENAI_OAUTH_AUTH_JSON_PATH"), defaultOAuthPath()),
		OpenAIOAuthAccountID: strings.TrimSpace(os.Getenv("ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID")),
		OpenAIAccountIDPath:  strings.TrimSpace(os.Getenv("ASSISTANT_OPENAI_OAUTH_ACCOUNT_ID_PATH")),
		WorkDir:              firstNonEmpty(os.Getenv("ASSISTANT_WORKDIR"), wd),
		StateDir:             firstNonEmpty(os.Getenv("ASSISTANT_STATE_DIR"), defaultStateDir()),
		MCPConfigPaths:       splitListEnv(os.Getenv("ASSISTANT_MCP_CONFIGS")),
		SkillCatalogPath:     strings.TrimSpace(os.Getenv("ASSISTANT_SKILL_CATALOG")),
		Permission:           firstNonEmpty(os.Getenv("ASSISTANT_PERMISSION"), string(sdkpolicy.PermissionModeWorkspaceWrite)),
		Reasoning:            firstNonEmpty(os.Getenv("ASSISTANT_REASONING"), string(agentsdk.ReasoningLow)),
		Verbosity:            firstNonEmpty(os.Getenv("ASSISTANT_VERBOSITY"), string(agentsdk.TextVerbosityMedium)),
		MaxTurns:             envInt("ASSISTANT_MAX_TURNS", 8),
		MaxTokens:            envInt("ASSISTANT_MAX_TOKENS", 1200),
		ToolTimeout:          envInt("ASSISTANT_TOOL_TIMEOUT", 0),
		EnableTools:          envBool("ASSISTANT_TOOLS", true),
		EnableMCP:            envBool("ASSISTANT_MCP", false),
		EnableSkills:         envBool("ASSISTANT_SKILLS", false),
		EnableScheduling:     envBool("ASSISTANT_SCHEDULING", true),
		EnableProjectState:   envBool("ASSISTANT_PROJECT_STATE", true),
		EnableApproval:       envBool("ASSISTANT_APPROVAL", true),
		EnableGuardrails:     envBool("ASSISTANT_GUARDRAILS", true),
		EnableCompaction:     envBool("ASSISTANT_COMPACTION", true),
		AllowPrivateNetwork:  envBool("ASSISTANT_PRIVATE_NETWORK", false),
		Audit:                envBool("ASSISTANT_AUDIT", false),
		AuditLevel:           firstNonEmpty(os.Getenv("ASSISTANT_AUDIT_LEVEL"), auditLevelFull),
		AuditLogPath:         strings.TrimSpace(os.Getenv("ASSISTANT_AUDIT_LOG")),
		GatewayAddr:          firstNonEmpty(os.Getenv("ASSISTANT_GATEWAY_ADDR"), ":8080"),
		GatewayToken:         strings.TrimSpace(os.Getenv("ASSISTANT_GATEWAY_TOKEN")),
		TelegramBotToken:     strings.TrimSpace(os.Getenv("ASSISTANT_TELEGRAM_BOT_TOKEN")),
		TelegramPollTimeout:  envInt("ASSISTANT_TELEGRAM_POLL_TIMEOUT", 50),
		GmailToken:           firstNonEmpty(os.Getenv("ASSISTANT_GMAIL_ACCESS_TOKEN"), os.Getenv("ASSISTANT_GMAIL_TOKEN")),
		GmailUser:            firstNonEmpty(os.Getenv("ASSISTANT_GMAIL_USER"), "me"),
		GmailQuery:           firstNonEmpty(os.Getenv("ASSISTANT_GMAIL_QUERY"), "is:unread"),
		GmailPollInterval:    envInt("ASSISTANT_GMAIL_POLL_INTERVAL", 60),
		GmailMaxResults:      envInt("ASSISTANT_GMAIL_MAX_RESULTS", 10),
		GmailMarkRead:        envBool("ASSISTANT_GMAIL_MARK_READ", false),
		GmailSendReplies:     envBool("ASSISTANT_GMAIL_SEND_REPLIES", false),
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
	c.SkillCatalogPath = expandUserPath(c.SkillCatalogPath)
	c.OpenAIOAuthPath = expandUserPath(c.OpenAIOAuthPath)
	c.OpenAIAccountIDPath = expandUserPath(c.OpenAIAccountIDPath)
	c.MCPConfigPaths = expandPathList(c.MCPConfigPaths)
	c.Permission = normalizePermission(c.Permission)
	c.AuditLevel = normalizeAuditLevel(c.AuditLevel)
	if c.AuditLevel == "" {
		return errors.New("--audit-level must be low or full")
	}
	if c.TelegramPollTimeout <= 0 {
		c.TelegramPollTimeout = 50
	}
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

	if err := c.loadFileConfig(); err != nil {
		return err
	}
	c.applyFileConfig()

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
	case providerOpenAIOAuth:
		if strings.TrimSpace(c.OpenAIOAuthPath) == "" {
			return errors.New("--provider openai-oauth requires --openai-oauth-path")
		}
	}
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
	c.MCPConfigPaths = append(c.MCPConfigPaths, expandPathList(fc.MCPConfigPaths)...)
	if strings.TrimSpace(c.SkillCatalogPath) == "" {
		c.SkillCatalogPath = expandUserPath(fc.Skills.CatalogPath)
	}
	if fc.Skills.Enabled != nil {
		c.EnableSkills = *fc.Skills.Enabled
	}
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

func usage() string {
	return strings.TrimSpace(`usage: assistant [flags] [prompt]

providers:
  --provider openai-oauth   use OpenAI OAuth credentials
  --provider openai-api     use OPENAI_API_KEY or --api-key

extension config:
  --config PATH             assistant JSON config; defaults to ~/.gratefulagents/assistant/config.json
  --mcp-config PATH         add an MCP config; repeat for any number of servers/bundles
  --skills                  expose SDK skill search/install/list tools
  --scheduling              expose schedule tools and run the background scheduler
  --audit                   emit structured audit events to stdout and logs
  --audit-level LEVEL       audit verbosity: low or full
  --audit-log PATH          append audit JSONL to PATH

examples:
  assistant --provider openai-oauth
  assistant schedule --provider openai-oauth
  assistant telegram --provider openai-oauth
  assistant gmail --provider openai-oauth --gmail-query "is:unread"
  assistant poll --provider openai-oauth
  assistant serve --provider openai-oauth --addr :8080
  assistant --provider openai-api "what changed in this repo?"
`)
}
