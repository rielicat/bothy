package config

import "time"

// Tags: yaml = key name; reload:"hot" fields apply without restart (default
// "restart"); secret:"true" fields accept only "secret:<name>" references;
// locked:"true" fields are never settable from the UI layer.

type Config struct {
	Version   int    `yaml:"version"`
	Mode      string `yaml:"mode" reload:"hot"` // single | multi
	Timezone  string `yaml:"timezone" reload:"hot"`
	Telemetry bool   `yaml:"telemetry"` // reserved; no phone-home code exists

	Server    Server            `yaml:"server"`
	Storage   Storage           `yaml:"storage"`
	Log       Log               `yaml:"log" reload:"hot"`
	Auth      Auth              `yaml:"auth"`
	Vault     VaultCfg          `yaml:"vault"`
	Providers []Provider        `yaml:"providers" reload:"hot"`
	Models    Models            `yaml:"models" reload:"hot"`
	Pricing   map[string]PriceO `yaml:"pricing" reload:"hot"`
	Agent     Agent             `yaml:"agent" reload:"hot"`
	Context   ContextCfg        `yaml:"context" reload:"hot"`
	Budgets   Budgets           `yaml:"budgets" reload:"hot"`
	Policy    PolicyCfg         `yaml:"policy" reload:"hot"`
	Sandbox   SandboxCfg        `yaml:"sandbox"`
	Egress    EgressCfg         `yaml:"egress" reload:"hot"`
	Search    Search            `yaml:"search" reload:"hot"`
	WebFetch  WebFetch          `yaml:"web_fetch" reload:"hot"`
	Channels  Channels          `yaml:"channels"`
	MCP       MCPCfg            `yaml:"mcp" reload:"hot"`
	Memory    MemoryCfg         `yaml:"memory" reload:"hot"`
	Skills    SkillsCfg         `yaml:"skills" reload:"hot"`
	Scheduler SchedulerCfg      `yaml:"scheduler" reload:"hot"`
	API       APICfg            `yaml:"api" reload:"hot"`
}

type Server struct {
	Listen         string   `yaml:"listen" locked:"true"`
	PublicURL      string   `yaml:"public_url"`
	AllowedHosts   []string `yaml:"allowed_hosts" reload:"hot"`
	TrustedProxies []string `yaml:"trusted_proxies" locked:"true"`
	LocalCIDRs     []string `yaml:"local_cidrs" locked:"true"`
	InsecureLAN    bool     `yaml:"insecure_lan" locked:"true"`
	TLS            TLS      `yaml:"tls" locked:"true"`
	MaxBodyMB      int      `yaml:"max_body_mb"`
	UploadMaxMB    int      `yaml:"upload_max_mb"`
}

type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type Storage struct {
	DataDir     string    `yaml:"data_dir" locked:"true"`
	WorkDir     string    `yaml:"work_dir" locked:"true"`
	RunDir      string    `yaml:"run_dir" locked:"true"`
	BackupsKeep int       `yaml:"backups_keep" reload:"hot"`
	Retention   Retention `yaml:"retention" reload:"hot"`
}

type Retention struct {
	EgressLog    Duration `yaml:"egress_log"`
	Runs         Duration `yaml:"runs"` // 0 = keep forever
	InboundDedup Duration `yaml:"inbound_dedup"`
}

type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

type Auth struct {
	SessionIdle       Duration `yaml:"session_idle" reload:"hot"`
	SessionMax        Duration `yaml:"session_max" reload:"hot"`
	PasswordMinLength int      `yaml:"password_min_length" reload:"hot"`
	Lockout           Lockout  `yaml:"lockout" reload:"hot"`
	Argon2            Argon2   `yaml:"argon2"`
	PairingTTL        Duration `yaml:"pairing_ttl" reload:"hot"`
	SetupTokenTTL     Duration `yaml:"setup_token_ttl"`
	AdminCanReadChats bool     `yaml:"admin_can_read_chats" reload:"hot" locked:"true"`
}

type Lockout struct {
	MaxFailures int      `yaml:"max_failures"`
	Base        Duration `yaml:"base"`
	Max         Duration `yaml:"max"`
}

type Argon2 struct {
	MemoryKiB   uint32 `yaml:"memory_kib"`
	Iterations  uint32 `yaml:"iterations"`
	Parallelism uint8  `yaml:"parallelism"`
}

type VaultCfg struct {
	KEK     string `yaml:"kek" locked:"true"`     // auto | file:/path | systemd:<name> | passphrase | env
	Keyfile string `yaml:"keyfile" locked:"true"` // autogen target
}

type Provider struct {
	ID           string            `yaml:"id"`
	Type         string            `yaml:"type"` // anthropic | openai | gemini | ollama | openai_compat
	BaseURL      string            `yaml:"base_url"`
	APIKey       string            `yaml:"api_key" secret:"true"`
	AllowPrivate bool              `yaml:"allow_private"` // exact host:port of base_url only
	Discover     bool              `yaml:"discover"`
	Enabled      *bool             `yaml:"enabled"` // nil = true
	Headers      map[string]string `yaml:"headers" secret:"true"`
	Models       []ProviderModel   `yaml:"models"`
}

type ProviderModel struct {
	ID        string `yaml:"id"`
	Context   int    `yaml:"context"`
	MaxOutput int    `yaml:"max_output"`
	Tools     *bool  `yaml:"tools"`
	Vision    bool   `yaml:"vision"`
}

type Models struct {
	Main []string `yaml:"main"` // "provider/model" fallback chain
	Fast []string `yaml:"fast"`
}

type PriceO struct { // USD per 1M tokens
	In         float64               `yaml:"in"`
	Out        float64               `yaml:"out"`
	CacheRead  float64               `yaml:"cache_read"`
	CacheWrite float64               `yaml:"cache_write"`
	Tiers      map[string]PriceTierO `yaml:"tiers"` // keyed by the prompt size they apply above ("200000"); unset keeps the catalog's
}

// PriceTierO is core.PriceTier: multipliers in permille (2000 = 2x) of the
// whole request once its prompt exceeds the tier's key; 0 = permille.
type PriceTierO struct {
	Permille           int `yaml:"permille"`
	OutPermille        int `yaml:"out_permille"`
	CacheReadPermille  int `yaml:"cache_read_permille"`
	CacheWritePermille int `yaml:"cache_write_permille"`
}

type Agent struct {
	PersonaFile       string   `yaml:"persona_file"`
	StandingRules     []string `yaml:"standing_rules"`
	BusyMode          string   `yaml:"busy_mode"` // steer | queue
	CoalesceWindow    Duration `yaml:"coalesce_window"`
	QueueDepth        int      `yaml:"queue_depth"`
	MaxConcurrentRuns int      `yaml:"max_concurrent_runs"`
	MaxRunsPerUser    int      `yaml:"max_runs_per_user"`
	ToolOutputTokens  int      `yaml:"tool_output_tokens"`
	ParallelReadTools int      `yaml:"parallel_read_tools"`
	ShowThinking      bool     `yaml:"show_thinking"`
	SessionIdleReset  Duration `yaml:"session_idle_reset"` // 0 = never
	Refine            Refine   `yaml:"refine"`
}

// Refine is refine mode (v0.2 F3): off by default; a message or a session
// can turn it on or off.
type Refine struct {
	Enabled    bool   `yaml:"enabled"`
	Drafts     int    `yaml:"drafts"` // breadth: independent drafts of the final answer
	Depth      int    `yaml:"depth"`  // rounds of continue → critique → correct per draft
	EarlyStop  bool   `yaml:"early_stop"`
	CriticSlot string `yaml:"critic_slot"` // main | fast
}

type ContextCfg struct {
	ReserveOutput string     `yaml:"reserve_output"` // "auto" or an integer
	CompactBelow  int        `yaml:"compact_below"`
	DegradedBelow int        `yaml:"degraded_below"`
	MinContext    int        `yaml:"min_context"`
	Compaction    Compaction `yaml:"compaction"`
}

type Compaction struct {
	Trigger          float64 `yaml:"trigger"`
	CompactTrigger   float64 `yaml:"compact_trigger"`
	KeepRecentTurns  int     `yaml:"keep_recent_turns"`
	SummaryMaxTokens int     `yaml:"summary_max_tokens"`
	AutoPin          bool    `yaml:"auto_pin"`
}

type Budgets struct {
	DailyUSD          float64            `yaml:"daily_usd"`
	MemberDailyUSD    float64            `yaml:"member_daily_usd"`
	PerUserDailyUSD   map[string]float64 `yaml:"per_user_daily_usd"` // username -> cap
	WarnRatio         float64            `yaml:"warn_ratio"`
	Run               RunBudget          `yaml:"run"`
	Scheduled         RunBudget          `yaml:"scheduled"`
	HeartbeatDailyUSD float64            `yaml:"heartbeat_daily_usd"`
	SenderRate        Rate               `yaml:"sender_rate"`
	Loop              Loop               `yaml:"loop"`
}

type RunBudget struct {
	MaxSteps  int      `yaml:"max_steps"`
	MaxTokens int      `yaml:"max_tokens"`
	MaxTime   Duration `yaml:"max_time"`
	MaxUSD    float64  `yaml:"max_usd"` // 0 = no per-run $ cap
}

type Rate struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}

type Loop struct {
	WarnAfter  int `yaml:"warn_after"`
	AbortAfter int `yaml:"abort_after"`
}

type PolicyCfg struct {
	Preset   string            `yaml:"preset"`   // careful | balanced | hands_off
	Defaults map[string]string `yaml:"defaults"` // class -> auto|ask|deny; overrides the preset
	Rules    []Rule            `yaml:"rules"`
	Approval Approval          `yaml:"approval"`
	Trifecta Trifecta          `yaml:"trifecta" locked:"true"`
}

type Rule struct {
	ID         string            `yaml:"id"`
	Tool       string            `yaml:"tool"` // glob
	Argv0      []string          `yaml:"argv0"`
	Domains    []string          `yaml:"domains"`
	Paths      []string          `yaml:"paths"`
	Match      map[string]string `yaml:"match"` // JSON pointer -> glob over Action.Payload
	SideEffect string            `yaml:"side_effect"`
	Triggers   []string          `yaml:"triggers"`
	Verdict    string            `yaml:"verdict"` // auto | ask | deny
	Reason     string            `yaml:"reason"`
}

type Approval struct {
	InteractiveTimeout   Duration `yaml:"interactive_timeout"`
	InteractiveOnTimeout string   `yaml:"interactive_on_timeout"` // deny | park
	ScheduledTimeout     Duration `yaml:"scheduled_timeout"`
	ScheduledOnTimeout   string   `yaml:"scheduled_on_timeout"`
	ParkTTL              Duration `yaml:"park_ttl"`
	APIWait              Duration `yaml:"api_wait"`
	Notify               []string `yaml:"notify"` // extra channels for mirrored cards (web-origin and scheduled runs)
}

type Trifecta struct {
	Enforce bool `yaml:"enforce"`
}

type SandboxCfg struct {
	Backend          string    `yaml:"backend"` // auto | sandboxd | bwrap | none
	Socket           string    `yaml:"socket" locked:"true"`
	Profile          string    `yaml:"profile" reload:"hot"`
	Limits           BoxLimits `yaml:"limits" reload:"hot"`
	ExecTimeout      Duration  `yaml:"exec_timeout" reload:"hot"`
	OutputKB         int       `yaml:"output_kb" reload:"hot"`
	WorkspaceQuotaMB int       `yaml:"workspace_quota_mb" reload:"hot"`
	IdleTimeout      Duration  `yaml:"idle_timeout" reload:"hot"`
}

type BoxLimits struct {
	MemoryMB int     `yaml:"memory_mb"`
	CPUs     float64 `yaml:"cpus"`
	Pids     int     `yaml:"pids"`
}

type EgressCfg struct {
	Allow                []string      `yaml:"allow"` // host or host:port; "*.suffix" allowed here (admin)
	Deny                 []string      `yaml:"deny"`
	Presets              EgressPresets `yaml:"presets"`
	Ports                []int         `yaml:"ports"`
	AllowPlainHTTP       bool          `yaml:"allow_plain_http"`
	HoldForApproval      Duration      `yaml:"hold_for_approval"` // 0 = off
	Caps                 EgressCaps    `yaml:"caps"`
	SharedTenantSuffixes []string      `yaml:"shared_tenant_suffixes"`
	DNS                  DNS           `yaml:"dns"`
}

type EgressPresets struct {
	PythonPackages bool `yaml:"python_packages"` // pypi.org, files.pythonhosted.org
	NPMPackages    bool `yaml:"npm_packages"`    // registry.npmjs.org
}

type EgressCaps struct {
	SessionUpMB   int `yaml:"session_up_mb"`
	SessionDownMB int `yaml:"session_down_mb"`
}

type DNS struct {
	Servers []string `yaml:"servers"` // "" = system resolver
	MaxTTL  Duration `yaml:"max_ttl"`
}

type Search struct {
	Provider            string `yaml:"provider"` // searxng | brave | tavily | none
	SearxngURL          string `yaml:"searxng_url"`
	SearxngAllowPrivate bool   `yaml:"searxng_allow_private"` // SystemDest.AllowPrivate for searxng_url's exact host:port
	BraveAPIKey         string `yaml:"brave_api_key" secret:"true"`
	TavilyAPIKey        string `yaml:"tavily_api_key" secret:"true"`
	MaxResults          int    `yaml:"max_results"`
}

type WebFetch struct {
	MaxBytes     int      `yaml:"max_bytes"`
	Timeout      Duration `yaml:"timeout"`
	MaxRedirects int      `yaml:"max_redirects"`
}

type Channels struct {
	UnknownSenders string   `yaml:"unknown_senders" reload:"hot"` // pair | ignore
	UsageFooter    bool     `yaml:"usage_footer" reload:"hot"`
	Telegram       Telegram `yaml:"telegram" reload:"hot"`
	Discord        Discord  `yaml:"discord" reload:"hot"`
	Slack          Slack    `yaml:"slack" reload:"hot"`
}

type Groups struct {
	MentionOnly bool `yaml:"mention_only"`
}

type Telegram struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token" secret:"true"`
	Groups  Groups `yaml:"groups"`
	APIBase string `yaml:"api_base" locked:"true"` // https://api.telegram.org (self-hosted Bot API servers, tests)
}

type Discord struct {
	Enabled       bool   `yaml:"enabled"`
	Token         string `yaml:"token" secret:"true"`
	ApplicationID string `yaml:"application_id"`
	Groups        Groups `yaml:"groups"`
	APIBase       string `yaml:"api_base" locked:"true"`    // https://discord.com/api/v10 (tests)
	GatewayURL    string `yaml:"gateway_url" locked:"true"` // "" = from GET /gateway/bot (tests)
}

type Slack struct {
	Enabled  bool   `yaml:"enabled"`
	AppToken string `yaml:"app_token" secret:"true"` // xapp-
	BotToken string `yaml:"bot_token" secret:"true"` // xoxb-
	Groups   Groups `yaml:"groups"`
	APIBase  string `yaml:"api_base" locked:"true"` // https://slack.com/api/ (tests)
}

type MCPCfg struct {
	Servers []MCPServer `yaml:"servers"`
}

type MCPServer struct {
	Name          string            `yaml:"name"` // ^[a-z0-9][a-z0-9-]{0,31}$
	Enabled       *bool             `yaml:"enabled"`
	Transport     string            `yaml:"transport"` // stdio | http
	URL           string            `yaml:"url"`
	Headers       map[string]string `yaml:"headers" secret:"true"`
	AllowPrivate  bool              `yaml:"allow_private"`
	Profile       string            `yaml:"profile"` // base | node
	Package       *MCPPackage       `yaml:"package"`
	Command       []string          `yaml:"command"`      // relative to /opt/mcp after install
	Egress        []string          `yaml:"egress"`       // runtime host[:port] allowlist for the box
	Workspace     string            `yaml:"workspace"`    // none | principal
	SideEffects   map[string]string `yaml:"side_effects"` // glob -> class; "default" key
	Private       []string          `yaml:"private"`      // tool globs whose output is private data
	TrustedOutput bool              `yaml:"trusted_output"`
	Core          []string          `yaml:"core"` // tool globs pinned into the core tools block (default: lazy)
}

type MCPPackage struct {
	Ecosystem string `yaml:"ecosystem"` // npm | pypi
	Name      string `yaml:"name"`
	Version   string `yaml:"version"` // exact
	Integrity string `yaml:"integrity"`
}

type MemoryCfg struct {
	MemoryMaxBytes    int    `yaml:"memory_max_bytes"`
	UserMaxBytes      int    `yaml:"user_max_bytes"`
	HeartbeatMaxBytes int    `yaml:"heartbeat_max_bytes"`
	TaintedWrites     string `yaml:"tainted_writes" locked:"true"` // ask | deny
}

type SkillsCfg struct {
	Dir               string `yaml:"dir" locked:"true"`
	IndexBudgetTokens int    `yaml:"index_budget_tokens"`
}

type SchedulerCfg struct {
	Enabled   bool      `yaml:"enabled"`
	MissedRun string    `yaml:"missed_run"` // skip | once
	Heartbeat Heartbeat `yaml:"heartbeat"`
}

type Heartbeat struct {
	Enabled     bool     `yaml:"enabled"`
	Every       Duration `yaml:"every"`
	ActiveHours string   `yaml:"active_hours"`
	Slot        string   `yaml:"slot"`
	Deliver     string   `yaml:"deliver"` // preferred | web | <channel name>
}

type APICfg struct {
	OpenAICompat bool `yaml:"openai_compat"`
	Passthrough  bool `yaml:"passthrough"`
}

// Duration unmarshals "15m", "720h", "1200ms"; marshals back the same way.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	*d = Duration(v)
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }
