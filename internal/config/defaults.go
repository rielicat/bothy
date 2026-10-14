package config

import "time"

// defaults holds the built-in values of SPEC §7.1. Entries marked "example"
// there (providers, model chains, pricing, MCP servers) are not defaults.
func defaults() *Config {
	d := func(v time.Duration) Duration { return Duration(v) }
	return &Config{
		Version:  1,
		Mode:     "single",
		Timezone: "UTC",
		Server: Server{
			Listen:      "127.0.0.1:8080",
			MaxBodyMB:   1,
			UploadMaxMB: 20,
		},
		Storage: Storage{
			DataDir:     "/data",
			RunDir:      defaultRunDir,
			BackupsKeep: 3,
			Retention:   Retention{EgressLog: d(720 * time.Hour), InboundDedup: d(24 * time.Hour)},
		},
		Log: Log{Level: "info", Format: "text"},
		Auth: Auth{
			SessionIdle:       d(168 * time.Hour),
			SessionMax:        d(720 * time.Hour),
			PasswordMinLength: 12,
			Lockout:           Lockout{MaxFailures: 5, Base: d(time.Minute), Max: d(time.Hour)},
			Argon2:            Argon2{MemoryKiB: 19456, Iterations: 2, Parallelism: 1},
			PairingTTL:        d(15 * time.Minute),
			SetupTokenTTL:     d(24 * time.Hour),
		},
		Vault: VaultCfg{KEK: "auto"},
		Agent: Agent{
			PersonaFile:       "SOUL.md",
			BusyMode:          "steer",
			CoalesceWindow:    d(1200 * time.Millisecond),
			QueueDepth:        8,
			MaxConcurrentRuns: 4,
			MaxRunsPerUser:    2,
			ToolOutputTokens:  4000,
			ParallelReadTools: 4,
			Refine:            Refine{Drafts: 3, Depth: 2, EarlyStop: true, CriticSlot: "main"},
		},
		Context: ContextCfg{
			ReserveOutput: "auto",
			CompactBelow:  32768,
			DegradedBelow: 16384,
			MinContext:    8192,
			Compaction:    Compaction{Trigger: 0.70, CompactTrigger: 0.60, KeepRecentTurns: 4, SummaryMaxTokens: 1200, AutoPin: true},
		},
		Budgets: Budgets{
			DailyUSD:          5.00,
			MemberDailyUSD:    2.00,
			WarnRatio:         0.8,
			Run:               RunBudget{MaxSteps: 25, MaxTokens: 400000, MaxTime: d(10 * time.Minute)},
			Scheduled:         RunBudget{MaxSteps: 15, MaxTokens: 150000, MaxTime: d(15 * time.Minute), MaxUSD: 0.25},
			HeartbeatDailyUSD: 0.25,
			SenderRate:        Rate{PerMinute: 10, Burst: 5},
			Loop:              Loop{WarnAfter: 3, AbortAfter: 5},
		},
		Policy: PolicyCfg{
			Preset: "careful",
			Rules: []Rule{
				{ID: "readonly-shell", Tool: "shell", Argv0: []string{"ls", "cat", "head", "tail", "grep", "rg", "wc", "pwd", "find"}, Verdict: "auto"},
				{ID: "wiki", Tool: "web_fetch", Domains: []string{"*.wikipedia.org"}, Verdict: "auto"},
				{ID: "no-pipe-to-shell", Tool: "shell", Match: map[string]string{"/argv/2": "*curl*|*sh*"}, Verdict: "ask", Reason: "pipes a download into a shell"},
			},
			Approval: Approval{
				InteractiveTimeout:   d(15 * time.Minute),
				InteractiveOnTimeout: "deny",
				ScheduledTimeout:     d(2 * time.Minute),
				ScheduledOnTimeout:   "park",
				ParkTTL:              d(24 * time.Hour),
				APIWait:              d(120 * time.Second),
			},
			Trifecta: Trifecta{Enforce: true},
		},
		Sandbox: SandboxCfg{
			Backend:          "auto",
			Socket:           "/run/bothy/sandboxd.sock",
			Profile:          "base",
			Limits:           BoxLimits{MemoryMB: 512, CPUs: 1.0, Pids: 256},
			ExecTimeout:      d(120 * time.Second),
			OutputKB:         64,
			WorkspaceQuotaMB: 2048,
			IdleTimeout:      d(10 * time.Minute),
		},
		Egress: EgressCfg{
			Ports:           []int{443},
			HoldForApproval: d(45 * time.Second),
			Caps:            EgressCaps{SessionUpMB: 10, SessionDownMB: 200},
			SharedTenantSuffixes: []string{".amazonaws.com", ".cloudfront.net", ".googleusercontent.com", ".blob.core.windows.net",
				".workers.dev", ".pages.dev", ".ngrok-free.app", ".github.io"},
			DNS: DNS{MaxTTL: d(60 * time.Second)},
		},
		Search: Search{
			Provider:            "none",
			SearxngURL:          "http://searxng:8080",
			SearxngAllowPrivate: true,
			BraveAPIKey:         "secret:brave_api_key",
			TavilyAPIKey:        "secret:tavily_api_key",
			MaxResults:          8,
		},
		WebFetch: WebFetch{MaxBytes: 2097152, Timeout: d(20 * time.Second), MaxRedirects: 5},
		Channels: Channels{
			UnknownSenders: "pair",
			Telegram:       Telegram{Token: "secret:telegram_bot_token", Groups: Groups{MentionOnly: true}, APIBase: "https://api.telegram.org"},
			Discord:        Discord{Token: "secret:discord_bot_token", Groups: Groups{MentionOnly: true}, APIBase: "https://discord.com/api/v10"},
			Slack: Slack{AppToken: "secret:slack_app_token", BotToken: "secret:slack_bot_token", Groups: Groups{MentionOnly: true},
				APIBase: "https://slack.com/api/"},
		},
		GitHub:    GitHubCfg{APIBase: "https://api.github.com", Checkout: GitHubCheckout{MaxMB: 256, MaxFiles: 20000}},
		Memory:    MemoryCfg{MemoryMaxBytes: 4096, UserMaxBytes: 2048, HeartbeatMaxBytes: 2048, TaintedWrites: "ask"},
		Skills:    SkillsCfg{IndexBudgetTokens: 600},
		Scheduler: SchedulerCfg{Enabled: true, MissedRun: "skip", Heartbeat: Heartbeat{Every: d(30 * time.Minute), ActiveHours: "08:00-22:00", Slot: "fast", Deliver: "preferred"}},
		API:       APICfg{OpenAICompat: true, Passthrough: true},
		Knowledge: defaultKnowledge(),
	}
}
