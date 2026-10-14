package config

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var (
	sideEffects = []string{"read", "local_write", "external_send", "irreversible", "financial", "admin"}
	verdicts    = []string{"auto", "ask", "deny"}
)

// enums maps schema patterns ("*" for item ids and map keys) to their allowed
// values; validation and Schema share it.
var enums = map[string][]string{
	"mode":                                   {"single", "multi"},
	"log.level":                              {"debug", "info", "warn", "error"},
	"log.format":                             {"text", "json"},
	"providers.*.type":                       {"anthropic", "openai", "gemini", "ollama", "openai_compat", "bedrock"},
	"providers.*.api":                        {"", "auto", "responses", "chat"},
	"agent.busy_mode":                        {"steer", "queue"},
	"agent.refine.critic_slot":               {"main", "fast"},
	"policy.preset":                          {"careful", "balanced", "hands_off"},
	"policy.defaults.*":                      verdicts,
	"policy.rules.*.verdict":                 verdicts,
	"policy.rules.*.side_effect":             append([]string{""}, sideEffects...),
	"policy.rules.*.triggers":                {"message", "schedule", "heartbeat", "api"},
	"policy.approval.interactive_on_timeout": {"deny", "park"},
	"policy.approval.scheduled_on_timeout":   {"deny", "park"},
	"sandbox.backend":                        {"auto", "sandboxd", "bwrap", "none"},
	"sandbox.profile":                        {"base", "node"},
	"search.provider":                        {"searxng", "brave", "tavily", "none"},
	"channels.unknown_senders":               {"pair", "ignore"},
	"mcp.servers.*.transport":                {"stdio", "http"},
	"mcp.servers.*.profile":                  {"", "base", "node"},
	"mcp.servers.*.workspace":                {"", "none", "principal"},
	"mcp.servers.*.package.ecosystem":        {"npm", "pypi"},
	"mcp.servers.*.side_effects.*":           sideEffects,
	"memory.tainted_writes":                  {"ask", "deny"},
	"scheduler.missed_run":                   {"skip", "once"},
	"scheduler.heartbeat.slot":               {"main", "fast"},
}

var (
	secretRefExact = regexp.MustCompile(`^secret:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	secretRefAny   = regexp.MustCompile(`secret:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`)
	providerIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	ruleIDRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	mcpNameRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	channelNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	headerNameRe   = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]+$`)
	hostnameRe     = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,62})(\.[A-Za-z0-9_]([A-Za-z0-9_-]{0,62}))*\.?$`)
	digitsRe       = regexp.MustCompile(`^[0-9]*$`)
	npmVersionRe   = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	pypiVersionRe  = regexp.MustCompile(`^\d+(?:\.\d+)*(?:(?:a|b|rc)\d+)?(?:\.post\d+)?(?:\.dev\d+)?$`)
	// awsRegionRe accepts the region shapes of every AWS partition (us-west-2,
	// us-gov-west-1, eu-isoe-west-1, cn-north-1).
	awsRegionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
	// awsProfileRe is narrower than the SDK allows: the name is interpolated
	// into an INI section header, so whitespace and brackets are refused.
	awsProfileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+@=,-]{0,127}$`)
)

// ValidAWSRegion reports whether s has the shape of an AWS region name.
func ValidAWSRegion(s string) bool { return awsRegionRe.MatchString(s) }

// ValidAWSProfile reports whether s may name a shared-config profile.
func ValidAWSProfile(s string) bool { return awsProfileRe.MatchString(s) }

type verr struct {
	path string
	idx  int  // element of a scalar list, -1 for the whole value
	item bool // drop the whole list item or map entry at path
	msg  string
}

type vctx struct {
	errs    []verr
	layerOf func(path string) int
}

func (v *vctx) add(p, format string, a ...any) {
	v.errs = append(v.errs, verr{path: p, idx: -1, msg: fmt.Sprintf(format, a...)})
}

func (v *vctx) addAt(p string, i int, format string, a ...any) {
	v.errs = append(v.errs, verr{path: p, idx: i, msg: fmt.Sprintf(format, a...)})
}

func (v *vctx) item(p, format string, a ...any) {
	v.errs = append(v.errs, verr{path: p, idx: -1, item: true, msg: fmt.Sprintf(format, a...)})
}

// blame picks, among keys that conflict with each other, the one set by the
// highest layer: that is the change that broke the combination.
func (v *vctx) blame(paths ...string) string {
	best, bestL := paths[0], -2
	for _, p := range paths {
		if l := v.layerOf(p); l > bestL {
			best, bestL = p, l
		}
	}
	return best
}

func (v *vctx) enum(p, pattern, val string) bool {
	allowed := enums[pattern]
	if slices.Contains(allowed, val) {
		return true
	}
	v.add(p, "%s", enumMessage(val, allowed))
	return false
}

func enumMessage(val string, allowed []string) string {
	var shown []string
	for _, a := range allowed {
		if a != "" {
			shown = append(shown, a)
		}
	}
	return fmt.Sprintf("invalid value %q; want one of %s%s", val, strings.Join(shown, " | "), didYouMean(val, shown))
}

func (v *vctx) intMin(p string, n, lo int) {
	if n < lo {
		v.add(p, "must be at least %d, got %d", lo, n)
	}
}

func (v *vctx) intRange(p string, n, lo, hi int) {
	if n < lo || n > hi {
		v.add(p, "must be between %d and %d, got %d", lo, hi, n)
	}
}

func (v *vctx) nonNegF(p string, x float64) {
	if x < 0 {
		v.add(p, "must not be negative, got %g", x)
	}
}

func (v *vctx) ratio(p string, x float64) {
	if x <= 0 || x > 1 {
		v.add(p, "must be greater than 0 and at most 1, got %g", x)
	}
}

func (v *vctx) positive(p string, d Duration) {
	if d <= 0 {
		v.add(p, "must be a positive duration, got %s", d.D())
	}
}

func (v *vctx) nonNeg(p string, d Duration) {
	if d < 0 {
		v.add(p, "must not be negative, got %s", d.D())
	}
}

func (v *vctx) url(p, s string, required bool, schemes ...string) {
	if s == "" {
		if required {
			v.add(p, "is required")
		}
		return
	}
	if msg := urlProblem(s, schemes...); msg != "" {
		v.add(p, "%s", msg)
	}
}

func urlProblem(s string, schemes ...string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "invalid URL"
	}
	ok := false
	for _, sc := range schemes {
		ok = ok || u.Scheme == sc
	}
	switch {
	case !ok:
		return fmt.Sprintf("URL scheme must be %s", strings.Join(schemes, " or "))
	case u.Host == "":
		return "URL has no host"
	case u.User != nil:
		return "credentials in URLs are not allowed; use a secret reference"
	}
	return ""
}

func (v *vctx) hostList(p string, list []string, wildcard, port bool) {
	for i, h := range list {
		if msg := hostProblem(h, wildcard, port); msg != "" {
			v.addAt(p, i, "%s", msg)
		}
	}
}

// hostProblem checks "host", "host:port", "[v6]:port" and, with wildcard,
// "*.suffix".
func hostProblem(s string, wildcard, port bool) string {
	if s == "" {
		return "empty host"
	}
	host := s
	if h, pt, err := net.SplitHostPort(s); err == nil {
		if !port {
			return fmt.Sprintf("%q: a port is not allowed here", s)
		}
		n, err := strconv.Atoi(pt)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Sprintf("%q: invalid port", s)
		}
		host = h
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return ""
	}
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		if !wildcard {
			return fmt.Sprintf("%q: wildcards are not allowed here", s)
		}
		host = rest
	}
	if !hostnameRe.MatchString(host) {
		return fmt.Sprintf("%q: invalid host", s)
	}
	return ""
}

func validGlob(g string) bool {
	_, err := path.Match(g, "")
	return err == nil
}

func typeMessage(f *field, n *yaml.Node) string {
	want := "a value"
	switch f.kind {
	case kScalarList:
		want = "a list"
		if f.typ.Elem().Kind() == reflect.Int {
			want = "a list of integers"
		}
	case kScalar:
		switch f.typ {
		case durationType:
			want = `a duration such as "90s", "15m" or "24h"`
		default:
			switch f.scalarKind() {
			case reflect.Bool:
				want = "true or false"
			case reflect.Int, reflect.Uint8, reflect.Uint32:
				want = "an integer"
			case reflect.Float64:
				want = "a number"
			case reflect.String:
				want = "a string"
			}
		}
	}
	if f.secret || n == nil || deref(n).Kind != yaml.ScalarNode {
		return "expected " + want
	}
	return fmt.Sprintf("expected %s, got %q", want, deref(n).Value)
}

// secretMessage checks a secret field: only "secret:<name>" (or "" for none);
// header values may embed the ref in a longer string. The value never appears
// in the message.
func secretMessage(segs []string, f *field, n *yaml.Node) string {
	val := deref(n).Value
	if isNull(n) || val == "" {
		if f.embed {
			return "a header value must contain a \"secret:<name>\" reference"
		}
		return ""
	}
	if f.embed && secretRefAny.MatchString(val) || !f.embed && secretRefExact.MatchString(val) {
		return ""
	}
	name := suggestedSecret(segs)
	return fmt.Sprintf("plaintext secrets are not allowed; reference one as \"secret:%s\". Move this secret to the vault: Settings → Vault, or `BOTHY_SECRET_%s`.", name, envNorm(name))
}

func suggestedSecret(segs []string) string {
	p := pathKey(segs)
	switch {
	case len(segs) >= 3 && segs[0] == "providers" && segs[len(segs)-1] == "api_key":
		return segs[1] + "_api_key"
	case len(segs) >= 3 && segs[0] == "providers":
		return segs[1] + "_token"
	case len(segs) >= 4 && segs[0] == "mcp":
		return segs[2] + "_mcp_token"
	case p == "channels.telegram.token":
		return "telegram_bot_token"
	case p == "channels.discord.token":
		return "discord_bot_token"
	case p == "channels.slack.app_token":
		return "slack_app_token"
	case p == "channels.slack.bot_token":
		return "slack_bot_token"
	}
	return segs[len(segs)-1]
}

func validate(c *Config, v *vctx) {
	if c.Version != 1 {
		v.add("version", "unsupported version %d (this release reads version 1)", c.Version)
	}
	v.enum("mode", "mode", c.Mode)
	if c.Timezone == "" {
		v.add("timezone", "is required")
	} else if _, err := time.LoadLocation(c.Timezone); err != nil {
		v.add("timezone", "unknown time zone %q", c.Timezone)
	}
	validateServer(c, v)
	validateStorage(c, v)
	v.enum("log.level", "log.level", c.Log.Level)
	v.enum("log.format", "log.format", c.Log.Format)
	validateAuth(c, v)
	validateVault(c, v)
	ids := validateProviders(c, v)
	validateModels(c, v, ids)
	validateAgent(c, v)
	validateContext(c, v)
	validateBudgets(c, v)
	validatePolicy(c, v)
	validateSandbox(c, v)
	validateEgress(c, v)
	validateSearch(c, v)
	validateChannels(c, v)
	validateGitHub(c, v)
	validateMCP(c, v)
	validateMisc(c, v)
}

func validateServer(c *Config, v *vctx) {
	s := c.Server
	if host, port, err := net.SplitHostPort(s.Listen); err != nil {
		v.add("server.listen", "expected host:port, got %q", s.Listen)
	} else if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		v.add("server.listen", "invalid port in %q", s.Listen)
	} else if host != "" && !hostnameRe.MatchString(host) {
		if _, err := netip.ParseAddr(host); err != nil {
			v.add("server.listen", "invalid host in %q", s.Listen)
		}
	}
	if s.PublicURL != "" {
		if msg := urlProblem(s.PublicURL, "http", "https"); msg != "" {
			v.add("server.public_url", "%s", msg)
		} else if u, _ := url.Parse(s.PublicURL); u.RawQuery != "" || u.Fragment != "" {
			v.add("server.public_url", "must not have a query or fragment")
		}
	}
	v.hostList("server.allowed_hosts", s.AllowedHosts, false, true)
	for _, l := range []struct {
		path string
		list []string
	}{{"server.trusted_proxies", s.TrustedProxies}, {"server.local_cidrs", s.LocalCIDRs}} {
		for i, cidr := range l.list {
			if _, err := netip.ParsePrefix(cidr); err != nil {
				if _, err := netip.ParseAddr(cidr); err != nil {
					v.addAt(l.path, i, "%q is not a CIDR or IP address", cidr)
				}
			}
		}
	}
	switch {
	case s.TLS.CertFile != "" && s.TLS.KeyFile == "":
		v.add("server.tls.key_file", "is required when cert_file is set")
	case s.TLS.CertFile == "" && s.TLS.KeyFile != "":
		v.add("server.tls.cert_file", "is required when key_file is set")
	}
	v.intRange("server.max_body_mb", s.MaxBodyMB, 1, 1024)
	v.intRange("server.upload_max_mb", s.UploadMaxMB, 1, 10240)
}

func validateStorage(c *Config, v *vctx) {
	s := c.Storage
	if s.DataDir == "" {
		v.add("storage.data_dir", "is required")
	}
	if s.RunDir == "" {
		v.add("storage.run_dir", "is required")
	}
	v.intRange("storage.backups_keep", s.BackupsKeep, 0, 1000)
	v.nonNeg("storage.retention.egress_log", s.Retention.EgressLog)
	v.nonNeg("storage.retention.runs", s.Retention.Runs)
	v.nonNeg("storage.retention.inbound_dedup", s.Retention.InboundDedup)
}

func validateAuth(c *Config, v *vctx) {
	a := c.Auth
	v.positive("auth.session_idle", a.SessionIdle)
	v.positive("auth.session_max", a.SessionMax)
	if a.SessionIdle > a.SessionMax && a.SessionMax > 0 {
		v.add(v.blame("auth.session_idle", "auth.session_max"), "session_idle (%s) must not exceed session_max (%s)", a.SessionIdle.D(), a.SessionMax.D())
	}
	v.intRange("auth.password_min_length", a.PasswordMinLength, 8, 1024)
	v.intMin("auth.lockout.max_failures", a.Lockout.MaxFailures, 1)
	v.positive("auth.lockout.base", a.Lockout.Base)
	if a.Lockout.Max < a.Lockout.Base {
		v.add(v.blame("auth.lockout.max", "auth.lockout.base"), "lockout.max (%s) must be at least lockout.base (%s)", a.Lockout.Max.D(), a.Lockout.Base.D())
	}
	v.intMin("auth.argon2.parallelism", int(a.Argon2.Parallelism), 1)
	v.intMin("auth.argon2.iterations", int(a.Argon2.Iterations), 1)
	if a.Argon2.MemoryKiB < 1024 || a.Argon2.MemoryKiB < 8*uint32(a.Argon2.Parallelism) {
		v.add("auth.argon2.memory_kib", "must be at least 1024 and 8 × parallelism, got %d", a.Argon2.MemoryKiB)
	}
	v.positive("auth.pairing_ttl", a.PairingTTL)
	v.positive("auth.setup_token_ttl", a.SetupTokenTTL)
}

func validateVault(c *Config, v *vctx) {
	k := c.Vault.KEK
	switch {
	case k == "auto", k == "passphrase", k == "env":
	case strings.HasPrefix(k, "file:"):
		if p := strings.TrimPrefix(k, "file:"); !strings.HasPrefix(p, "/") && !filepath.IsAbs(p) {
			v.add("vault.kek", "file: needs an absolute path, got %q", k)
		}
	case strings.HasPrefix(k, "systemd:"):
		if n := strings.TrimPrefix(k, "systemd:"); n == "" || strings.ContainsAny(n, "/\\") {
			v.add("vault.kek", "systemd: needs a credential name, got %q", k)
		}
	default:
		v.add("vault.kek", "invalid value %q; want auto | file:/path | systemd:<name> | passphrase | env", k)
	}
}

func validateProviders(c *Config, v *vctx) []string {
	var ids []string
	for _, p := range c.Providers {
		base := "providers." + p.ID
		if !providerIDRe.MatchString(p.ID) {
			v.item(base, "invalid id %q: use lower-case letters, digits, '_' or '-'", p.ID)
			continue
		}
		switch {
		case p.Type == "":
			v.item(base, "type is required")
			continue
		case !v.enum(base+".type", "providers.*.type", p.Type):
			continue
		case p.Type == "openai_compat" && p.BaseURL == "":
			v.item(base, "base_url is required for openai_compat")
			continue
		case p.Type == "bedrock" && p.Region == "":
			v.item(base, "region is required for bedrock (for example us-west-2)")
			continue
		case p.Type == "bedrock" && !ValidAWSRegion(p.Region):
			v.item(base, "region %q is not an AWS region name (for example us-west-2)", p.Region)
			continue
		}
		if p.Type == "bedrock" {
			validateBedrock(p, base, v)
		} else {
			v.url(base+".base_url", p.BaseURL, false, "http", "https")
			if p.Region != "" {
				v.add(base+".region", "region applies to type bedrock only")
			}
			if p.Profile != "" {
				v.add(base+".profile", "profile applies to type bedrock only")
			}
		}
		if v.enum(base+".api", "providers.*.api", p.API) && p.API != "" && p.Type != "openai" && p.Type != "openai_compat" {
			v.add(base+".api", "only providers of type openai or openai_compat choose an api")
		}
		for _, name := range keysOf(p.Headers) {
			if !headerNameRe.MatchString(name) {
				v.add(base+".headers."+name, "invalid header name")
			}
		}
		for _, m := range p.Models {
			mp := base + ".models." + m.ID
			if m.Context < 0 {
				v.add(mp+".context", "must not be negative, got %d", m.Context)
			}
			if m.MaxOutput < 0 {
				v.add(mp+".max_output", "must not be negative, got %d", m.MaxOutput)
			}
		}
		ids = append(ids, p.ID)
	}
	return ids
}

// validateBedrock checks the keys a bedrock provider uses differently: AWS
// credentials come from the default chain, never from api_key or headers,
// and a base_url (a VPC endpoint) must be https since requests carry a
// signature over session credentials.
func validateBedrock(p Provider, base string, v *vctx) {
	v.url(base+".base_url", p.BaseURL, false, "https")
	if p.APIKey != "" {
		v.add(base+".api_key", "bedrock uses the AWS credential chain (instance role, SSO profile, environment); remove api_key")
	}
	for _, name := range keysOf(p.Headers) {
		v.add(base+".headers."+name, "bedrock requests are signed with AWS credentials; extra headers are not supported")
	}
	if p.Profile != "" && !ValidAWSProfile(p.Profile) {
		v.add(base+".profile", "invalid profile name %q", p.Profile)
	}
}

func validateModels(c *Config, v *vctx, ids []string) {
	for _, chain := range []struct {
		path string
		refs []string
	}{{"models.main", c.Models.Main}, {"models.fast", c.Models.Fast}} {
		for i, ref := range chain.refs {
			prov, model, ok := strings.Cut(ref, "/")
			switch {
			case !ok || prov == "" || model == "":
				v.addAt(chain.path, i, "%q: want <provider>/<model>", ref)
			case !slices.Contains(ids, prov):
				v.addAt(chain.path, i, "unknown provider %q%s", prov, didYouMean(prov, ids))
			}
		}
	}
	for _, k := range keysOf(c.Pricing) {
		p := c.Pricing[k]
		prov, model, ok := strings.Cut(k, "/")
		if !ok || prov == "" || model == "" {
			v.item("pricing."+k, "key must be <provider>/<model>")
			continue
		}
		v.nonNegF("pricing."+k+".in", p.In)
		v.nonNegF("pricing."+k+".out", p.Out)
		v.nonNegF("pricing."+k+".cache_read", p.CacheRead)
		v.nonNegF("pricing."+k+".cache_write", p.CacheWrite)
		for _, n := range keysOf(p.Tiers) {
			tp := "pricing." + k + ".tiers." + n
			if above, err := strconv.Atoi(n); err != nil || above < 0 {
				v.item(tp, "key must be the prompt size in tokens the tier applies above, e.g. \"200000\"")
				continue
			}
			// core.Price.Cost ignores multipliers below 1x, so reject them rather than drop them silently.
			t := p.Tiers[n]
			v.intRange(tp+".permille", t.Permille, 1000, 100_000)
			for _, o := range []struct {
				name string
				m    int
			}{{"out_permille", t.OutPermille}, {"cache_read_permille", t.CacheReadPermille}, {"cache_write_permille", t.CacheWritePermille}} {
				if o.m != 0 {
					v.intRange(tp+"."+o.name, o.m, 1000, 100_000)
				}
			}
		}
	}
}

// ValidPersonaFile reports whether name may be agent.persona_file: a bare
// Markdown file name. PUT /api/persona overwrites that file in the data dir,
// which also holds bothy.yaml, bothy.db, the setup token and the KEK.
func ValidPersonaFile(name string) bool {
	return len(name) > len(".md") && filepath.IsLocal(name) && !strings.ContainsAny(name, `/\`) &&
		strings.EqualFold(filepath.Ext(name), ".md")
}

func validateAgent(c *Config, v *vctx) {
	a := c.Agent
	if !ValidPersonaFile(a.PersonaFile) {
		v.add("agent.persona_file", "must be a Markdown file name in the data dir (like SOUL.md), got %q", a.PersonaFile)
	}
	v.enum("agent.busy_mode", "agent.busy_mode", a.BusyMode)
	v.nonNeg("agent.coalesce_window", a.CoalesceWindow)
	if a.CoalesceWindow.D() > time.Minute {
		v.add("agent.coalesce_window", "must be at most 1m, got %s", a.CoalesceWindow.D())
	}
	v.intRange("agent.queue_depth", a.QueueDepth, 1, 1000)
	v.intMin("agent.max_concurrent_runs", a.MaxConcurrentRuns, 1)
	v.intMin("agent.max_runs_per_user", a.MaxRunsPerUser, 1)
	v.intMin("agent.tool_output_tokens", a.ToolOutputTokens, 100)
	v.intMin("agent.parallel_read_tools", a.ParallelReadTools, 1)
	v.nonNeg("agent.session_idle_reset", a.SessionIdleReset)
	// The upper bounds are the paper's defaults (N=8, D=4): 104 calls for one answer.
	v.intRange("agent.refine.drafts", a.Refine.Drafts, 1, 8)
	v.intRange("agent.refine.depth", a.Refine.Depth, 1, 4)
	v.enum("agent.refine.critic_slot", "agent.refine.critic_slot", a.Refine.CriticSlot)
}

func validateContext(c *Config, v *vctx) {
	x := c.Context
	if x.ReserveOutput != "auto" {
		if n, err := strconv.Atoi(x.ReserveOutput); err != nil || n < 1 {
			v.add("context.reserve_output", "want auto or a positive integer, got %q", x.ReserveOutput)
		}
	}
	v.intMin("context.min_context", x.MinContext, 1)
	if x.DegradedBelow < x.MinContext {
		v.add(v.blame("context.degraded_below", "context.min_context"), "degraded_below (%d) must be at least min_context (%d)", x.DegradedBelow, x.MinContext)
	}
	if x.CompactBelow < x.DegradedBelow {
		v.add(v.blame("context.compact_below", "context.degraded_below"), "compact_below (%d) must be at least degraded_below (%d)", x.CompactBelow, x.DegradedBelow)
	}
	v.ratio("context.compaction.trigger", x.Compaction.Trigger)
	v.ratio("context.compaction.compact_trigger", x.Compaction.CompactTrigger)
	v.intMin("context.compaction.keep_recent_turns", x.Compaction.KeepRecentTurns, 0)
	v.intMin("context.compaction.summary_max_tokens", x.Compaction.SummaryMaxTokens, 1)
}

func validateBudgets(c *Config, v *vctx) {
	b := c.Budgets
	v.nonNegF("budgets.daily_usd", b.DailyUSD)
	v.nonNegF("budgets.member_daily_usd", b.MemberDailyUSD)
	for _, user := range keysOf(b.PerUserDailyUSD) {
		v.nonNegF("budgets.per_user_daily_usd."+user, b.PerUserDailyUSD[user])
	}
	v.ratio("budgets.warn_ratio", b.WarnRatio)
	for _, r := range []struct {
		path string
		rb   RunBudget
	}{{"budgets.run", b.Run}, {"budgets.scheduled", b.Scheduled}} {
		v.intMin(r.path+".max_steps", r.rb.MaxSteps, 1)
		v.intMin(r.path+".max_tokens", r.rb.MaxTokens, 1)
		v.positive(r.path+".max_time", r.rb.MaxTime)
		v.nonNegF(r.path+".max_usd", r.rb.MaxUSD)
	}
	v.nonNegF("budgets.heartbeat_daily_usd", b.HeartbeatDailyUSD)
	v.intMin("budgets.sender_rate.per_minute", b.SenderRate.PerMinute, 0)
	v.intMin("budgets.sender_rate.burst", b.SenderRate.Burst, 0)
	v.intMin("budgets.loop.warn_after", b.Loop.WarnAfter, 1)
	if b.Loop.AbortAfter < b.Loop.WarnAfter {
		v.add(v.blame("budgets.loop.abort_after", "budgets.loop.warn_after"), "abort_after (%d) must be at least warn_after (%d)", b.Loop.AbortAfter, b.Loop.WarnAfter)
	}
}

func validatePolicy(c *Config, v *vctx) {
	p := c.Policy
	v.enum("policy.preset", "policy.preset", p.Preset)
	for _, class := range keysOf(p.Defaults) {
		verdict, path := p.Defaults[class], "policy.defaults."+class
		switch {
		case !slices.Contains(sideEffects, class):
			v.add(path, "unknown side-effect class %q%s", class, didYouMean(class, sideEffects))
		case !v.enum(path, "policy.defaults.*", verdict):
		case class == "financial" && verdict == "auto":
			v.add(path, "financial actions are never auto")
		}
	}
	for _, r := range p.Rules {
		base := "policy.rules." + r.ID
		if !ruleIDRe.MatchString(r.ID) {
			v.item(base, "invalid id %q", r.ID)
			continue
		}
		if r.Verdict == "" {
			v.item(base, "verdict is required")
			continue
		}
		if v.enum(base+".verdict", "policy.rules.*.verdict", r.Verdict) && r.Verdict == "auto" && r.SideEffect == "financial" {
			v.add(base+".verdict", "financial actions are never auto")
		}
		v.enum(base+".side_effect", "policy.rules.*.side_effect", r.SideEffect)
		if !validGlob(r.Tool) {
			v.add(base+".tool", "invalid glob %q", r.Tool)
		}
		for i, t := range r.Triggers {
			if !slices.Contains(enums["policy.rules.*.triggers"], t) {
				v.addAt(base+".triggers", i, "%s", enumMessage(t, enums["policy.rules.*.triggers"]))
			}
		}
		for _, l := range []struct {
			path string
			list []string
		}{{base + ".argv0", r.Argv0}, {base + ".paths", r.Paths}} {
			for i, s := range l.list {
				if s == "" {
					v.addAt(l.path, i, "empty entry")
				}
			}
		}
		v.hostList(base+".domains", r.Domains, true, true)
		for _, ptr := range keysOf(r.Match) {
			if !strings.HasPrefix(ptr, "/") {
				v.add(base+".match."+ptr, "keys are JSON pointers starting with \"/\"")
			}
		}
	}
	a := p.Approval
	v.positive("policy.approval.interactive_timeout", a.InteractiveTimeout)
	v.enum("policy.approval.interactive_on_timeout", "policy.approval.interactive_on_timeout", a.InteractiveOnTimeout)
	v.positive("policy.approval.scheduled_timeout", a.ScheduledTimeout)
	v.enum("policy.approval.scheduled_on_timeout", "policy.approval.scheduled_on_timeout", a.ScheduledOnTimeout)
	v.positive("policy.approval.park_ttl", a.ParkTTL)
	v.nonNeg("policy.approval.api_wait", a.APIWait)
	for i, n := range a.Notify {
		if !channelNameRe.MatchString(n) {
			v.addAt("policy.approval.notify", i, "invalid channel name %q", n)
		}
	}
}

func validateSandbox(c *Config, v *vctx) {
	s := c.Sandbox
	v.enum("sandbox.backend", "sandbox.backend", s.Backend)
	if s.Socket == "" {
		v.add("sandbox.socket", "is required")
	}
	v.enum("sandbox.profile", "sandbox.profile", s.Profile)
	v.intMin("sandbox.limits.memory_mb", s.Limits.MemoryMB, 64)
	if s.Limits.CPUs <= 0 || s.Limits.CPUs > 256 {
		v.add("sandbox.limits.cpus", "must be greater than 0 and at most 256, got %g", s.Limits.CPUs)
	}
	v.intMin("sandbox.limits.pids", s.Limits.Pids, 16)
	v.positive("sandbox.exec_timeout", s.ExecTimeout)
	v.intMin("sandbox.output_kb", s.OutputKB, 1)
	v.intMin("sandbox.workspace_quota_mb", s.WorkspaceQuotaMB, 0)
	v.positive("sandbox.idle_timeout", s.IdleTimeout)
}

func validateEgress(c *Config, v *vctx) {
	e := c.Egress
	v.hostList("egress.allow", e.Allow, true, true)
	v.hostList("egress.deny", e.Deny, true, true)
	for i, p := range e.Ports {
		if p < 1 || p > 65535 {
			v.addAt("egress.ports", i, "invalid port %d", p)
		}
	}
	v.nonNeg("egress.hold_for_approval", e.HoldForApproval)
	v.intMin("egress.caps.session_up_mb", e.Caps.SessionUpMB, 0)
	v.intMin("egress.caps.session_down_mb", e.Caps.SessionDownMB, 0)
	for i, s := range e.SharedTenantSuffixes {
		if strings.Trim(s, ".") == "" {
			v.addAt("egress.shared_tenant_suffixes", i, "empty suffix")
		}
	}
	for i, s := range e.DNS.Servers {
		if _, err := netip.ParseAddr(s); err != nil {
			if _, err := netip.ParseAddrPort(s); err != nil {
				v.addAt("egress.dns.servers", i, "%q is not an IP address or IP:port", s)
			}
		}
	}
	v.nonNeg("egress.dns.max_ttl", e.DNS.MaxTTL)
}

func validateSearch(c *Config, v *vctx) {
	s := c.Search
	v.enum("search.provider", "search.provider", s.Provider)
	v.url("search.searxng_url", s.SearxngURL, s.Provider == "searxng", "http", "https")
	v.intRange("search.max_results", s.MaxResults, 1, 100)
	w := c.WebFetch
	v.intMin("web_fetch.max_bytes", w.MaxBytes, 1024)
	v.positive("web_fetch.timeout", w.Timeout)
	v.intRange("web_fetch.max_redirects", w.MaxRedirects, 0, 20)
}

func validateChannels(c *Config, v *vctx) {
	ch := c.Channels
	v.enum("channels.unknown_senders", "channels.unknown_senders", ch.UnknownSenders)
	v.url("channels.telegram.api_base", ch.Telegram.APIBase, true, "http", "https")
	v.url("channels.discord.api_base", ch.Discord.APIBase, true, "http", "https")
	v.url("channels.discord.gateway_url", ch.Discord.GatewayURL, false, "ws", "wss")
	if !digitsRe.MatchString(ch.Discord.ApplicationID) {
		v.add("channels.discord.application_id", "must be a numeric Discord id")
	}
	v.url("channels.slack.api_base", ch.Slack.APIBase, true, "http", "https")
}

func validateMCP(c *Config, v *vctx) {
	for _, s := range c.MCP.Servers {
		base := "mcp.servers." + s.Name
		if !mcpNameRe.MatchString(s.Name) {
			v.item(base, "invalid name %q: lower-case letters, digits and '-', at most 32", s.Name)
			continue
		}
		if s.Transport == "" {
			v.item(base, "transport is required")
			continue
		}
		if !v.enum(base+".transport", "mcp.servers.*.transport", s.Transport) {
			continue
		}
		switch s.Transport {
		case "http":
			if s.URL == "" {
				v.item(base, "url is required for transport http")
				continue
			}
			v.url(base+".url", s.URL, true, "http", "https")
			for _, name := range keysOf(s.Headers) {
				if !headerNameRe.MatchString(name) {
					v.add(base+".headers."+name, "invalid header name")
				}
			}
		case "stdio":
			if s.Package == nil || s.Package.Ecosystem == "" || s.Package.Name == "" || s.Package.Version == "" || s.Package.Integrity == "" {
				v.item(base, "stdio servers need package: {ecosystem, name, version, integrity}")
				continue
			}
			if len(s.Command) == 0 || s.Command[0] == "" {
				v.item(base, "command is required for transport stdio")
				continue
			}
			pk := base + ".package"
			if v.enum(pk+".ecosystem", "mcp.servers.*.package.ecosystem", s.Package.Ecosystem) {
				re := npmVersionRe
				if s.Package.Ecosystem == "pypi" {
					re = pypiVersionRe
				}
				if !re.MatchString(s.Package.Version) {
					v.add(pk+".version", "must be an exact version (no latest, ranges or git refs), got %q", s.Package.Version)
				}
			}
			switch exe := filepath.Base(s.Command[0]); {
			case exe == "npx", exe == "uvx", exe == "bunx", exe == "pipx" && len(s.Command) > 1 && s.Command[1] == "run":
				v.add(base+".command", "%s fetches packages at run time; install the package and run it from /opt/mcp", exe)
			}
		}
		v.enum(base+".profile", "mcp.servers.*.profile", s.Profile)
		v.enum(base+".workspace", "mcp.servers.*.workspace", s.Workspace)
		v.hostList(base+".egress", s.Egress, false, true)
		for _, g := range keysOf(s.SideEffects) {
			class, p := s.SideEffects[g], base+".side_effects."+g
			if g != "default" && !validGlob(g) {
				v.add(p, "invalid glob %q", g)
				continue
			}
			v.enum(p, "mcp.servers.*.side_effects.*", class)
		}
		for i, g := range s.Private {
			if g == "" || !validGlob(g) {
				v.addAt(base+".private", i, "invalid glob %q", g)
			}
		}
		for i, g := range s.Core {
			if g == "" || !validGlob(g) {
				v.addAt(base+".core", i, "invalid glob %q", g)
			}
		}
	}
}

func validateGitHub(c *Config, v *vctx) {
	g := c.GitHub
	v.url("github.api_base", g.APIBase, true, "http", "https")
	v.intRange("github.checkout.max_mb", g.Checkout.MaxMB, 1, 8192)
	v.intRange("github.checkout.max_files", g.Checkout.MaxFiles, 1, 1_000_000)
}

func validateMisc(c *Config, v *vctx) {
	m := c.Memory
	v.intMin("memory.memory_max_bytes", m.MemoryMaxBytes, 256)
	v.intMin("memory.user_max_bytes", m.UserMaxBytes, 256)
	v.intMin("memory.heartbeat_max_bytes", m.HeartbeatMaxBytes, 256)
	v.enum("memory.tainted_writes", "memory.tainted_writes", m.TaintedWrites)
	v.intMin("skills.index_budget_tokens", c.Skills.IndexBudgetTokens, 0)
	s := c.Scheduler
	v.enum("scheduler.missed_run", "scheduler.missed_run", s.MissedRun)
	h := s.Heartbeat
	if h.Every.D() < time.Minute {
		v.add("scheduler.heartbeat.every", "must be at least 1m, got %s", h.Every.D())
	}
	if h.ActiveHours != "" && !validActiveHours(h.ActiveHours) {
		v.add("scheduler.heartbeat.active_hours", "want HH:MM-HH:MM, got %q", h.ActiveHours)
	}
	v.enum("scheduler.heartbeat.slot", "scheduler.heartbeat.slot", h.Slot)
	if h.Deliver != "preferred" && h.Deliver != "web" && !channelNameRe.MatchString(h.Deliver) {
		v.add("scheduler.heartbeat.deliver", "want preferred, web or a channel name, got %q", h.Deliver)
	}
}

func validActiveHours(s string) bool {
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return false
	}
	if _, err := time.Parse("15:04", from); err != nil {
		return false
	}
	if to == "24:00" {
		return true
	}
	_, err := time.Parse("15:04", to)
	return err == nil
}

// keysOf returns map keys sorted so errors come out in a stable order.
func keysOf[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
