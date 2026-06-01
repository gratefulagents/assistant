// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	familyRoleFamily     = "family"
	familyRoleFreeloader = "freeloader"

	defaultFamilyImage      = "ghcr.io/gratefulagents/assistant:latest"
	defaultFamilyConfigPath = "assistant.yaml"
	defaultFamilyRestart    = "unless-stopped"
	// defaultFamilyUser runs each container as root so the assistant can write
	// to its named state volume (a distroless nonroot user cannot), which is
	// what keeps a member's durable data intact across container restarts.
	defaultFamilyUser = "0:0"

	familyStateMount = "/state"
	familyOAuthMount = "/codex/auth.json"
)

// familyConfig is the on-disk assistant.yaml that holds the deployment config
// and is the single source of truth for managing the per-member containers.
type familyConfig struct {
	Image         string            `yaml:"image"`
	Provider      string            `yaml:"provider"`
	CodexAuthPath string            `yaml:"codexAuthPath"`
	Restart       string            `yaml:"restart"`
	User          string            `yaml:"user,omitempty"`
	Audit         *bool             `yaml:"audit,omitempty"`
	AuditLevel    string            `yaml:"auditLevel,omitempty"`
	Defaults      assistantSettings `yaml:"defaults,omitempty"`
	Members       []familyMember    `yaml:"members"`
}

// familyMember is a single deployed assistant: one family member or freeloader,
// each backed by its own container and persistent volume, individually
// configurable through assistant.yaml.
type familyMember struct {
	Name                 string            `yaml:"name"`
	Role                 string            `yaml:"role"`
	Container            string            `yaml:"container"`
	Volume               string            `yaml:"volume"`
	TelegramBotToken     string            `yaml:"telegramBotToken"`
	TelegramAllowedUsers []string          `yaml:"telegramAllowedUsers,omitempty"`
	TelegramAllowedChats []string          `yaml:"telegramAllowedChats,omitempty"`
	Env                  map[string]string `yaml:"env,omitempty"`
	// Settings holds any assistant flag; inlined so members read like a flat
	// config. Values set here override familyConfig.Defaults for this member.
	Settings assistantSettings `yaml:",inline"`
}

// assistantSettings mirrors the tunable `assistant` flags so every flag is
// configurable from assistant.yaml, both deployment-wide (familyConfig.Defaults)
// and per member. Unset fields fall back to the assistant binary's own defaults.
type assistantSettings struct {
	Model               string   `yaml:"model,omitempty"`
	BaseURL             string   `yaml:"baseUrl,omitempty"`
	APIMode             string   `yaml:"apiMode,omitempty"`
	APIKey              string   `yaml:"apiKey,omitempty"`
	OAuthAccountID      string   `yaml:"openaiOauthAccountId,omitempty"`
	OAuthAccountIDPath  string   `yaml:"openaiOauthAccountIdPath,omitempty"`
	Permission          string   `yaml:"permission,omitempty"`
	Reasoning           string   `yaml:"reasoning,omitempty"`
	Verbosity           string   `yaml:"verbosity,omitempty"`
	SkillCatalog        string   `yaml:"skillCatalog,omitempty"`
	MCPConfigPaths      []string `yaml:"mcpConfig,omitempty"`
	MaxTurns            *int     `yaml:"maxTurns,omitempty"`
	MaxTokens           *int     `yaml:"maxTokens,omitempty"`
	ToolTimeout         *int     `yaml:"toolTimeout,omitempty"`
	Tools               *bool    `yaml:"tools,omitempty"`
	MCP                 *bool    `yaml:"mcp,omitempty"`
	Skills              *bool    `yaml:"skills,omitempty"`
	Scheduling          *bool    `yaml:"scheduling,omitempty"`
	ProjectState        *bool    `yaml:"projectState,omitempty"`
	Approval            *bool    `yaml:"approval,omitempty"`
	Guardrails          *bool    `yaml:"guardrails,omitempty"`
	Compaction          *bool    `yaml:"compaction,omitempty"`
	PrivateNetwork      *bool    `yaml:"privateNetwork,omitempty"`
	Debug               *bool    `yaml:"debug,omitempty"`
	EmbeddingModel      string   `yaml:"embeddingModel,omitempty"`
	EmbeddingBaseURL    string   `yaml:"embeddingBaseUrl,omitempty"`
	EmbeddingDimensions *int     `yaml:"embeddingDimensions,omitempty"`
	TelegramPollTimeout *int     `yaml:"telegramPollTimeout,omitempty"`
}

// runFamilyDeploy is the entry point for `assistant family-deploy`. It owns its
// own flag parsing so it stays decoupled from the main appConfig flag set.
func runFamilyDeploy(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("family-deploy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("file", defaultFamilyConfigPath, "assistant.yaml deployment config path")
	fs.StringVar(configPath, "f", defaultFamilyConfigPath, "assistant.yaml deployment config path (shorthand)")
	image := fs.String("image", "", "container image override for generated config")
	codexAuth := fs.String("codex-auth", "", "host Codex OAuth auth.json path to mount into containers")
	assumeYes := fs.Bool("yes", false, "do not prompt; require an existing config")
	dryRun := fs.Bool("dry-run", false, "print docker commands without executing them")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, familyUsage())
		return 2
	}

	action := "up"
	if rest := fs.Args(); len(rest) > 0 {
		action = strings.ToLower(strings.TrimSpace(rest[0]))
	}

	path := expandUserPath(*configPath)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	switch action {
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, familyUsage())
		return 0
	case "init":
		if _, err := familyInit(path, *image, *codexAuth, stdin, stdout); err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		return 0
	case "up", "deploy":
		cfg, err := familyEnsureConfig(path, *image, *codexAuth, *assumeYes, stdin, stdout)
		if err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		if err := familyUp(cfg, *dryRun, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		return 0
	case "down", "stop":
		cfg, err := loadFamilyConfig(path)
		if err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		if err := familyDown(cfg, *dryRun, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		return 0
	case "status", "ps":
		cfg, err := loadFamilyConfig(path)
		if err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		if err := familyStatus(cfg, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "family-deploy:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "family-deploy: unknown action %q\n\n%s\n", action, familyUsage())
		return 2
	}
}

// familyEnsureConfig loads assistant.yaml when present, otherwise builds one
// interactively (unless --yes was given, where a config is required).
func familyEnsureConfig(path, image, codexAuth string, assumeYes bool, stdin io.Reader, stdout io.Writer) (familyConfig, error) {
	if _, err := os.Stat(path); err == nil {
		return loadFamilyConfig(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return familyConfig{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if assumeYes {
		return familyConfig{}, fmt.Errorf("config %s not found and --yes was set; run `assistant family-deploy init` first", path)
	}
	return familyInit(path, image, codexAuth, stdin, stdout)
}

// familyInit interactively collects family members and freeloaders, then writes
// assistant.yaml to disk.
func familyInit(path, image, codexAuth string, stdin io.Reader, stdout io.Writer) (familyConfig, error) {
	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout, "Configuring the family assistant deployment.")
	fmt.Fprintln(stdout, "Each member gets their own container, persistent volume, and Telegram bot.")
	fmt.Fprintln(stdout)

	cfg := familyConfig{
		Image:         firstNonEmpty(image, defaultFamilyImage),
		Provider:      providerOpenAIOAuth,
		CodexAuthPath: firstNonEmpty(codexAuth, defaultOAuthPath()),
		Restart:       defaultFamilyRestart,
		User:          defaultFamilyUser,
	}

	taken := map[string]bool{}
	family, err := promptMembers(reader, stdout, familyRoleFamily, "family member", taken)
	if err != nil {
		return familyConfig{}, err
	}
	freeloaders, err := promptMembers(reader, stdout, familyRoleFreeloader, "freeloader", taken)
	if err != nil {
		return familyConfig{}, err
	}
	cfg.Members = append(family, freeloaders...)
	if len(cfg.Members) == 0 {
		return familyConfig{}, errors.New("no members configured; nothing to deploy")
	}

	if err := saveFamilyConfig(path, cfg); err != nil {
		return familyConfig{}, err
	}
	fmt.Fprintf(stdout, "\nWrote %s with %d member(s).\n", path, len(cfg.Members))
	return cfg, nil
}

func promptMembers(reader *bufio.Reader, stdout io.Writer, role, label string, taken map[string]bool) ([]familyMember, error) {
	count, err := promptInt(reader, stdout, fmt.Sprintf("How many %ss?", label))
	if err != nil {
		return nil, err
	}
	members := make([]familyMember, 0, count)
	for i := 0; i < count; i++ {
		fmt.Fprintf(stdout, "\n%s #%d\n", capitalize(label), i+1)
		name, err := promptName(reader, stdout, label, taken)
		if err != nil {
			return nil, err
		}
		token, err := promptRequired(reader, stdout, "  Telegram bot token (from BotFather): ")
		if err != nil {
			return nil, err
		}
		allowed, err := promptAllowedUsers(reader, stdout)
		if err != nil {
			return nil, err
		}
		container := familyContainerName(role, name)
		members = append(members, familyMember{
			Name:                 name,
			Role:                 role,
			Container:            container,
			Volume:               container + "-state",
			TelegramBotToken:     token,
			TelegramAllowedUsers: allowed,
		})
	}
	return members, nil
}

func promptName(reader *bufio.Reader, stdout io.Writer, label string, taken map[string]bool) (string, error) {
	for {
		name, err := promptLine(reader, stdout, "  Name: ")
		if err != nil {
			return "", err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			fmt.Fprintln(stdout, "  Name cannot be empty.")
			continue
		}
		key := strings.ToLower(name)
		if taken[key] {
			fmt.Fprintf(stdout, "  %q is already used; pick another name.\n", name)
			continue
		}
		taken[key] = true
		return name, nil
	}
}

func promptRequired(reader *bufio.Reader, stdout io.Writer, prompt string) (string, error) {
	for {
		value, err := promptLine(reader, stdout, prompt)
		if err != nil {
			return "", err
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
		fmt.Fprintln(stdout, "  This value is required.")
	}
}

// promptAllowedUsers collects the required Telegram allow list. At least one
// user ID or username must be provided so the bot never responds to strangers.
func promptAllowedUsers(reader *bufio.Reader, stdout io.Writer) ([]string, error) {
	for {
		line, err := promptLine(reader, stdout, "  Allowed Telegram user IDs/usernames (comma-separated): ")
		if err != nil {
			return nil, err
		}
		allowed := normalizeTelegramAllowList(splitListEnv(line))
		if len(allowed) > 0 {
			return allowed, nil
		}
		fmt.Fprintln(stdout, "  At least one allowed user is required.")
	}
}

func promptInt(reader *bufio.Reader, stdout io.Writer, question string) (int, error) {
	for {
		line, err := promptLine(reader, stdout, question+" ")
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 0 {
			fmt.Fprintln(stdout, "Please enter a non-negative whole number.")
			continue
		}
		return n, nil
	}
}

func promptLine(reader *bufio.Reader, stdout io.Writer, prompt string) (string, error) {
	fmt.Fprint(stdout, prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// familyContainerName builds a stable, docker-safe container name.
func familyContainerName(role, name string) string {
	return "assistant-" + role + "-" + sanitizeDockerName(name)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func sanitizeDockerName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		out = "member"
	}
	return out
}

func loadFamilyConfig(path string) (familyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return familyConfig{}, fmt.Errorf("config %s not found; run `assistant family-deploy init`", path)
		}
		return familyConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg familyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return familyConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return familyConfig{}, err
	}
	return cfg, nil
}

func saveFamilyConfig(path string, cfg familyConfig) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	header := []byte("# assistant.yaml - family assistant deployment config\n# Managed by `assistant family-deploy`. Edit and re-run to apply changes.\n")
	if err := os.WriteFile(path, append(header, data...), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (c *familyConfig) applyDefaults() {
	c.Image = firstNonEmpty(c.Image, defaultFamilyImage)
	c.Provider = firstNonEmpty(normalizeProvider(c.Provider), providerOpenAIOAuth)
	c.CodexAuthPath = firstNonEmpty(c.CodexAuthPath, defaultOAuthPath())
	c.Restart = firstNonEmpty(c.Restart, defaultFamilyRestart)
	c.User = firstNonEmpty(c.User, defaultFamilyUser)
	if c.Audit == nil {
		// Family containers run unattended, so default to low-level auditing for
		// a lightweight per-member activity trail.
		enabled := true
		c.Audit = &enabled
	}
	c.AuditLevel = normalizeAuditLevel(firstNonEmpty(c.AuditLevel, auditLevelLow))
	if c.AuditLevel == "" {
		c.AuditLevel = auditLevelLow
	}
	for i := range c.Members {
		m := &c.Members[i]
		m.Role = firstNonEmpty(strings.ToLower(m.Role), familyRoleFamily)
		if strings.TrimSpace(m.Container) == "" {
			m.Container = familyContainerName(m.Role, m.Name)
		}
		if strings.TrimSpace(m.Volume) == "" {
			m.Volume = m.Container + "-state"
		}
	}
}

func (c familyConfig) validate() error {
	if len(c.Members) == 0 {
		return errors.New("no members configured")
	}
	seen := map[string]bool{}
	for _, m := range c.Members {
		if strings.TrimSpace(m.Name) == "" {
			return errors.New("a member is missing a name")
		}
		if strings.TrimSpace(m.TelegramBotToken) == "" {
			return fmt.Errorf("member %q is missing a telegramBotToken", m.Name)
		}
		if len(normalizeTelegramAllowList(m.TelegramAllowedUsers)) == 0 {
			return fmt.Errorf("member %q needs at least one telegramAllowedUsers entry", m.Name)
		}
		if seen[m.Container] {
			return fmt.Errorf("duplicate container name %q", m.Container)
		}
		seen[m.Container] = true
	}
	return nil
}

// familyUp creates the persistent volume and (re)starts the container for every
// member defined in assistant.yaml.
func familyUp(cfg familyConfig, dryRun bool, stdout, stderr io.Writer) error {
	if err := requireDocker(dryRun); err != nil {
		return err
	}
	authPath := expandUserPath(cfg.CodexAuthPath)
	if abs, err := filepath.Abs(authPath); err == nil {
		authPath = abs
	}
	if !dryRun {
		if _, err := os.Stat(authPath); err != nil {
			return fmt.Errorf("Codex auth file %s not readable: %w", authPath, err)
		}
	}

	for _, m := range cfg.Members {
		fmt.Fprintf(stdout, "==> %s (%s)\n", m.Container, m.Role)
		if err := runDocker(dryRun, stdout, stderr, "volume", "create", m.Volume); err != nil {
			return fmt.Errorf("create volume %s: %w", m.Volume, err)
		}
		// Remove any prior container so re-running applies config changes; the
		// named volume keeps the member's data intact across the replace.
		_ = runDockerQuiet(dryRun, "rm", "-f", m.Container)

		args := familyRunArgs(cfg, m, authPath)
		if err := runDocker(dryRun, stdout, stderr, args...); err != nil {
			return fmt.Errorf("run container %s: %w", m.Container, err)
		}
	}
	fmt.Fprintf(stdout, "\nDeployed %d member(s). Check status with `assistant family-deploy status`.\n", len(cfg.Members))
	return nil
}

// familyRunArgs builds the `docker run` argument list for a single member.
func familyRunArgs(cfg familyConfig, m familyMember, authPath string) []string {
	args := []string{
		"run", "-d",
		"--name", m.Container,
		"--restart", cfg.Restart,
		"--label", "com.gratefulagents.assistant.role=" + m.Role,
		"--label", "com.gratefulagents.assistant.member=" + m.Name,
		"-v", m.Volume + ":" + familyStateMount,
		"-v", authPath + ":" + familyOAuthMount + ":ro",
	}
	if strings.TrimSpace(cfg.User) != "" {
		args = append(args, "--user", cfg.User)
	}
	if strings.TrimSpace(m.TelegramBotToken) != "" {
		args = append(args, "-e", "ASSISTANT_TELEGRAM_BOT_TOKEN="+m.TelegramBotToken)
	}
	for k, v := range m.Env {
		if strings.TrimSpace(k) == "" {
			continue
		}
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, cfg.Image,
		"telegram",
		"--provider", firstNonEmpty(cfg.Provider, providerOpenAIOAuth),
		"--openai-oauth-path", familyOAuthMount,
		"--state-dir", familyStateMount,
	)
	if cfg.Audit == nil || *cfg.Audit {
		args = append(args, "--audit", "--audit-level", firstNonEmpty(cfg.AuditLevel, auditLevelLow))
	}
	args = append(args, cfg.Defaults.merge(m.Settings).renderArgs()...)
	for _, u := range m.TelegramAllowedUsers {
		if strings.TrimSpace(u) != "" {
			args = append(args, "--telegram-allowed-user", u)
		}
	}
	for _, ch := range m.TelegramAllowedChats {
		if strings.TrimSpace(ch) != "" {
			args = append(args, "--telegram-allowed-chat", ch)
		}
	}
	return args
}

// merge returns base settings with every field set on override taking
// precedence, so per-member values win over deployment-wide defaults.
func (base assistantSettings) merge(override assistantSettings) assistantSettings {
	out := base
	if override.Model != "" {
		out.Model = override.Model
	}
	if override.BaseURL != "" {
		out.BaseURL = override.BaseURL
	}
	if override.APIMode != "" {
		out.APIMode = override.APIMode
	}
	if override.APIKey != "" {
		out.APIKey = override.APIKey
	}
	if override.OAuthAccountID != "" {
		out.OAuthAccountID = override.OAuthAccountID
	}
	if override.OAuthAccountIDPath != "" {
		out.OAuthAccountIDPath = override.OAuthAccountIDPath
	}
	if override.Permission != "" {
		out.Permission = override.Permission
	}
	if override.Reasoning != "" {
		out.Reasoning = override.Reasoning
	}
	if override.Verbosity != "" {
		out.Verbosity = override.Verbosity
	}
	if override.SkillCatalog != "" {
		out.SkillCatalog = override.SkillCatalog
	}
	if len(override.MCPConfigPaths) > 0 {
		out.MCPConfigPaths = override.MCPConfigPaths
	}
	if override.MaxTurns != nil {
		out.MaxTurns = override.MaxTurns
	}
	if override.MaxTokens != nil {
		out.MaxTokens = override.MaxTokens
	}
	if override.ToolTimeout != nil {
		out.ToolTimeout = override.ToolTimeout
	}
	if override.Tools != nil {
		out.Tools = override.Tools
	}
	if override.MCP != nil {
		out.MCP = override.MCP
	}
	if override.Skills != nil {
		out.Skills = override.Skills
	}
	if override.Scheduling != nil {
		out.Scheduling = override.Scheduling
	}
	if override.ProjectState != nil {
		out.ProjectState = override.ProjectState
	}
	if override.Approval != nil {
		out.Approval = override.Approval
	}
	if override.Guardrails != nil {
		out.Guardrails = override.Guardrails
	}
	if override.Compaction != nil {
		out.Compaction = override.Compaction
	}
	if override.PrivateNetwork != nil {
		out.PrivateNetwork = override.PrivateNetwork
	}
	if override.Debug != nil {
		out.Debug = override.Debug
	}
	if override.EmbeddingModel != "" {
		out.EmbeddingModel = override.EmbeddingModel
	}
	if override.EmbeddingBaseURL != "" {
		out.EmbeddingBaseURL = override.EmbeddingBaseURL
	}
	if override.EmbeddingDimensions != nil {
		out.EmbeddingDimensions = override.EmbeddingDimensions
	}
	if override.TelegramPollTimeout != nil {
		out.TelegramPollTimeout = override.TelegramPollTimeout
	}
	return out
}

// renderArgs turns the settings into assistant CLI flags. Only fields that are
// explicitly set are emitted; everything else defers to the binary's defaults.
func (s assistantSettings) renderArgs() []string {
	var args []string
	addStr := func(flag, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, "--"+flag, value)
		}
	}
	addInt := func(flag string, value *int) {
		if value != nil {
			args = append(args, "--"+flag, strconv.Itoa(*value))
		}
	}
	addBool := func(flag string, value *bool) {
		if value != nil {
			args = append(args, fmt.Sprintf("--%s=%t", flag, *value))
		}
	}

	addStr("model", s.Model)
	addStr("base-url", s.BaseURL)
	addStr("api-mode", s.APIMode)
	addStr("api-key", s.APIKey)
	addStr("openai-oauth-account-id", s.OAuthAccountID)
	addStr("openai-oauth-account-id-path", s.OAuthAccountIDPath)
	addStr("permission", s.Permission)
	addStr("reasoning", s.Reasoning)
	addStr("verbosity", s.Verbosity)
	addStr("skill-catalog", s.SkillCatalog)
	for _, p := range s.MCPConfigPaths {
		addStr("mcp-config", p)
	}
	addInt("max-turns", s.MaxTurns)
	addInt("max-tokens", s.MaxTokens)
	addInt("tool-timeout", s.ToolTimeout)
	addBool("tools", s.Tools)
	addBool("mcp", s.MCP)
	addBool("skills", s.Skills)
	addBool("scheduling", s.Scheduling)
	addBool("project-state", s.ProjectState)
	addBool("approval", s.Approval)
	addBool("guardrails", s.Guardrails)
	addBool("compaction", s.Compaction)
	addBool("private-network", s.PrivateNetwork)
	addBool("debug", s.Debug)
	addStr("embedding-model", s.EmbeddingModel)
	addStr("embedding-base-url", s.EmbeddingBaseURL)
	addInt("embedding-dimensions", s.EmbeddingDimensions)
	addInt("telegram-poll-timeout", s.TelegramPollTimeout)
	return args
}

func familyDown(cfg familyConfig, dryRun bool, stdout, stderr io.Writer) error {
	if err := requireDocker(dryRun); err != nil {
		return err
	}
	for _, m := range cfg.Members {
		fmt.Fprintf(stdout, "==> removing %s\n", m.Container)
		if err := runDocker(dryRun, stdout, stderr, "rm", "-f", m.Container); err != nil {
			return fmt.Errorf("remove container %s: %w", m.Container, err)
		}
	}
	fmt.Fprintln(stdout, "\nContainers removed. Named volumes are kept so data survives; remove them manually with `docker volume rm` if desired.")
	return nil
}

func familyStatus(cfg familyConfig, stdout, stderr io.Writer) error {
	if err := requireDocker(false); err != nil {
		return err
	}
	args := []string{"ps", "-a", "--filter", "label=com.gratefulagents.assistant.role",
		"--format", "table {{.Names}}\t{{.Status}}\t{{.Image}}"}
	return runDocker(false, stdout, stderr, args...)
}

func requireDocker(dryRun bool) error {
	if dryRun {
		return nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker not found on PATH; install Docker or re-run with --dry-run")
	}
	return nil
}

func runDocker(dryRun bool, stdout, stderr io.Writer, args ...string) error {
	fmt.Fprintln(stdout, "+ docker "+strings.Join(args, " "))
	if dryRun {
		return nil
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func runDockerQuiet(dryRun bool, args ...string) error {
	if dryRun {
		return nil
	}
	return exec.Command("docker", args...).Run()
}

func familyUsage() string {
	return strings.TrimSpace(`usage: assistant family-deploy [action] [flags]

Interactively configure a family of containerized assistants and manage their
Docker containers. Each family member and freeloader gets their own container,
a persistent named volume (data survives restarts), the host Codex OAuth
auth.json mounted read-only, and an individually configurable Telegram bot.

actions:
  up        (default) create/refresh containers from assistant.yaml; prompts to
            generate the config interactively when it does not exist yet
  init      interactively (re)generate assistant.yaml only, without deploying
  down      stop and remove the member containers (named volumes are kept)
  status    show the status of all member containers

flags:
  -f, --file PATH   assistant.yaml path (default ./assistant.yaml)
  --image REF       container image for the generated config
  --codex-auth PATH host Codex auth.json to mount (default ~/.codex/auth.json)
  --yes             do not prompt; require an existing assistant.yaml
  --dry-run         print docker commands without executing them

examples:
  assistant family-deploy
  assistant family-deploy init
  assistant family-deploy up --file ./assistant.yaml
  assistant family-deploy status
  assistant family-deploy down`)
}
