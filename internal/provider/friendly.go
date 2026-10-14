package provider

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/bedrock"
)

// Plain model names and suggestions for the UI (docs/design/ui.md §5.6.3,
// §14.2): "Claude Sonnet 4.5" instead of an id, the provider's name, and
// which model to use for chats and which for quick tasks.

// endpoint is a provider as far as naming goes.
type endpoint struct {
	kind   core.ProviderKind
	base   string
	region string // bedrock: picks the geography of cross-region profiles
}

// compatNames are the OpenAI-compatible services known by host.
var compatNames = map[string]string{
	"openrouter.ai":    "OpenRouter",
	"api.groq.com":     "Groq",
	"api.x.ai":         "xAI",
	"api.deepseek.com": "DeepSeek",
}

// providerName is the provider's plain name: "Anthropic", "Amazon Bedrock",
// "LM Studio", or the server's host name for other servers.
func (e endpoint) providerName() string {
	switch e.kind {
	case core.KindAnthropic:
		return "Anthropic"
	case core.KindOpenAI:
		return "OpenAI"
	case core.KindGemini:
		return "Google Gemini"
	case core.KindBedrock:
		return "Amazon Bedrock"
	case core.KindOllama:
		return "Ollama"
	}
	u, err := url.Parse(e.base)
	if err != nil || u.Hostname() == "" {
		return "Model server"
	}
	host := strings.ToLower(u.Hostname())
	if n, ok := compatNames[host]; ok {
		return n
	}
	switch u.Port() {
	case "1234":
		return "LM Studio"
	case "8000":
		return "vLLM"
	}
	if localHost(host) {
		return "Model server"
	}
	return host
}

func (e endpoint) local() bool {
	if e.kind == core.KindOllama {
		return true
	}
	u, err := url.Parse(e.base)
	return e.kind == core.KindOpenAICompat && err == nil && localHost(u.Hostname())
}

var (
	// vendorPrefixRe is OpenRouter's "Anthropic: " in front of the name.
	vendorPrefixRe = regexp.MustCompile(`^[A-Za-z0-9 .&'-]{1,40}:\s+`)
	// geoSuffixRe is the geography Bedrock discovery appends ("(US)",
	// "(Global, legacy)").
	geoSuffixRe     = regexp.MustCompile(`\s*\(([^()]*)\)$`)
	profileGeoRe    = regexp.MustCompile(`(?i)^(us|eu|apac|jp|au|ca|us-gov|global)\s+`)
	profileVendorRe = regexp.MustCompile(`^(Anthropic|Amazon)\s+`)
	sizeRe          = regexp.MustCompile(`^(\d+(?:\.\d+)?)([bmk])$`)
	letterNumRe     = regexp.MustCompile(`^([a-z])(\d+(?:\.\d+)?)$`)
	quantRe         = regexp.MustCompile(`^(q\d.*|iq\d.*|fp\d+|bf16|f16|int\d+|gguf|mlx|latest)$`)
	versionRe       = regexp.MustCompile(`\d+(?:\.\d+)*`)
)

// brands keep their own capitalisation in names built from ids.
var brands = map[string]string{
	"gpt": "GPT", "oss": "OSS", "llama": "Llama", "qwen": "Qwen", "mistral": "Mistral", "mixtral": "Mixtral",
	"gemma": "Gemma", "phi": "Phi", "deepseek": "DeepSeek", "glm": "GLM", "kimi": "Kimi", "granite": "Granite",
	"codestral": "Codestral", "devstral": "Devstral", "nemotron": "Nemotron", "smollm": "SmolLM", "o1": "o1", "o3": "o3", "o4": "o4",
}

// prettyID turns a model id without a known name into one: "qwen3:14b" →
// "Qwen3 14B", "deepseek-r1:8b-q4_K_M" → "DeepSeek R1 8B".
func prettyID(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 && i < len(id)-1 {
		id = id[i+1:]
	}
	name, tag, _ := strings.Cut(id, ":")
	var words []string
	for i, part := range []string{name, tag} {
		// Quantisation tags keep their underscores ("q4_K_M").
		sep := func(r rune) bool { return r == '-' || r == ' ' || i == 0 && r == '_' }
		for _, w := range strings.FieldsFunc(part, sep) {
			lw := strings.ToLower(w)
			switch {
			case quantRe.MatchString(lw):
				continue
			case brands[lw] != "":
				w = brands[lw]
			case sizeRe.MatchString(lw):
				w = strings.ToUpper(lw)
			case letterNumRe.MatchString(lw):
				w = strings.ToUpper(lw[:1]) + lw[1:]
			default:
				r := []rune(w)
				r[0] = unicode.ToUpper(r[0])
				w = string(r)
			}
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return id
	}
	return strings.Join(words, " ")
}

// displayName is a model's plain name without a geography: the catalog's or
// the provider's name when there is one, else one built from the id.
func (f *Facade) displayName(e endpoint, model, listed string) string {
	name := listed
	if cm, ok := f.cat.Find(e.kind, e.base).Model(model); ok && cm.Display != "" {
		name = cm.Display
	}
	legacy := false
	if e.kind == core.KindBedrock {
		if m := geoSuffixRe.FindStringSubmatch(listed); m != nil {
			legacy = strings.Contains(m[1], "legacy")
		}
		if m := geoSuffixRe.FindStringSubmatch(name); m != nil {
			name = strings.TrimSpace(name[:len(name)-len(m[0])])
		}
		// Profile names of models the listing does not describe ("US
		// Anthropic Claude 3 Haiku").
		name = profileVendorRe.ReplaceAllString(profileGeoRe.ReplaceAllString(name, ""), "")
	}
	name = vendorPrefixRe.ReplaceAllString(strings.TrimSpace(name), "")
	if name == "" || name == model || !strings.ContainsRune(name, ' ') && strings.ToLower(name) == name {
		name = prettyID(model)
	}
	if legacy {
		name += " (legacy)"
	}
	return name
}

// shortName drops "Claude " for narrow screens, but not from the old
// "Claude 3 Haiku" style, where "3 Haiku" would read as a count.
func shortName(display string) string {
	rest, ok := strings.CutPrefix(display, "Claude ")
	if !ok || rest == "" || rest[0] >= '0' && rest[0] <= '9' {
		return display
	}
	return rest
}

// name fills a model's plain names.
func (f *Facade) name(e endpoint, m *core.ModelInfo) {
	m.DisplayName = f.displayName(e, m.Ref.Model, m.Display)
	m.ShortName = shortName(m.DisplayName)
	m.ProviderName = e.providerName()
}

// suggestions are the id patterns, most wanted first, of a provider's
// suggested main and quick tasks models; the newest version of the first
// pattern any listed model matches wins.
type suggestions struct{ main, quick []*regexp.Regexp }

func res(ps ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(ps))
	for i, p := range ps {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

var (
	claudeSuggest = suggestions{main: res(`^claude-sonnet-`), quick: res(`^claude-haiku-`)}
	// Bedrock ids carry a geography prefix (us., global.) or none.
	bedrockSuggest = suggestions{
		main:  res(`anthropic\.claude-sonnet`, `anthropic\.claude-opus`, `amazon\.nova-pro`, `amazon\.nova-2-lite`, `amazon\.nova-premier`, `amazon\.nova-lite`),
		quick: res(`anthropic\.claude-haiku`, `amazon\.nova-2-lite`, `amazon\.nova-lite`, `amazon\.nova-micro`),
	}
	openaiSuggest     = suggestions{main: res(`^gpt-\d+(\.\d+)?$`), quick: res(`^gpt-\d+(\.\d+)?-mini$`)}
	geminiSuggest     = suggestions{main: res(`^gemini-\d+(\.\d+)?-pro(-preview)?$`), quick: res(`^gemini-\d+(\.\d+)?-flash-lite$`, `^gemini-\d+(\.\d+)?-flash$`)}
	openrouterSuggest = suggestions{main: res(`^anthropic/claude-sonnet`), quick: res(`^anthropic/claude-haiku`)}
	compatSuggest     = map[string]suggestions{
		"api.x.ai":         {main: res(`^grok-\d+(\.\d+)?$`)},
		"api.deepseek.com": {main: res(`-pro$`, `^deepseek-chat$`), quick: res(`-flash$`)},
		"openrouter.ai":    openrouterSuggest,
	}
)

func (e endpoint) suggestions() suggestions {
	switch e.kind {
	case core.KindAnthropic:
		return claudeSuggest
	case core.KindBedrock:
		return bedrockSuggest
	case core.KindOpenAI:
		return openaiSuggest
	case core.KindGemini:
		return geminiSuggest
	case core.KindOpenAICompat:
		if u, err := url.Parse(e.base); err == nil {
			return compatSuggest[strings.ToLower(u.Hostname())]
		}
	}
	return suggestions{}
}

// annotate names a list of one provider's models and marks the suggested
// main and quick tasks models. Bedrock models listed under several
// geographies get it in their name ("Claude Sonnet 4.6 (US)").
func (f *Facade) annotate(e endpoint, ms []core.ModelInfo) {
	if len(ms) == 0 {
		return
	}
	for i := range ms {
		f.name(e, &ms[i])
		ms[i].Recommended, ms[i].QuickTasksDefault = false, false
	}
	if e.kind == core.KindBedrock {
		count := map[string]int{}
		for _, m := range ms {
			count[m.DisplayName]++
		}
		for i, m := range ms {
			if count[m.DisplayName] > 1 {
				geo, _ := bedrock.SplitModel(m.Ref.Model)
				name, legacy := strings.CutSuffix(m.DisplayName, " (legacy)")
				label := geoName(geo)
				if legacy {
					label += ", legacy"
				}
				ms[i].DisplayName = name + " (" + label + ")"
			}
		}
	}
	main, quick := f.suggest(e, ms)
	if main >= 0 {
		ms[main].Recommended = true
	}
	if quick >= 0 {
		ms[quick].QuickTasksDefault = true
	}
}

func geoName(geo string) string {
	switch geo {
	case "global":
		return "Global"
	case "":
		return "this region"
	case "apac":
		return "Asia Pacific"
	case "us-gov":
		return "US GovCloud"
	}
	return strings.ToUpper(geo)
}

// suggest returns the indexes of the suggested main and quick tasks models
// (-1 when the list has none). Local servers use one model for both, so
// only one has to fit in memory; elsewhere quick tasks fall back to the
// cheapest priced model when it costs less than the main one.
func (f *Facade) suggest(e endpoint, ms []core.ModelInfo) (main, quick int) {
	sg := e.suggestions()
	newer := func(a, b core.ModelInfo) bool { return f.newer(e, a, b) }
	main = pickBy(ms, sg.main, newer)
	if main < 0 {
		if e.local() {
			main = largest(ms)
		} else {
			main = slices.IndexFunc(ms, func(m core.ModelInfo) bool { return m.Tools })
		}
	}
	if main < 0 {
		main = 0
	}
	if e.local() {
		return main, main
	}
	if quick = pickBy(ms, sg.quick, newer); quick >= 0 {
		return main, quick
	}
	quick = main
	for i, m := range ms {
		if m.Tools && priced(m) && (!priced(ms[quick]) || unitPrice(m) < unitPrice(ms[quick])) && (m.Context == 0 || m.Context >= 32_768) {
			quick = i
		}
	}
	if !priced(ms[main]) || unitPrice(ms[quick]) >= unitPrice(ms[main]) {
		quick = main
	}
	return main, quick
}

func priced(m core.ModelInfo) bool { return m.Price.Known && m.Price.InPerM+m.Price.OutPerM > 0 }

func unitPrice(m core.ModelInfo) core.Micros { return m.Price.InPerM + m.Price.OutPerM }

// pickBy returns the best model matching the first pattern any model
// matches, or -1.
func pickBy(ms []core.ModelInfo, pats []*regexp.Regexp, better func(a, b core.ModelInfo) bool) int {
	for _, re := range pats {
		best := -1
		for i, m := range ms {
			if !m.Tools || !re.MatchString(strings.ToLower(m.Ref.Model)) {
				continue
			}
			if best < 0 || better(m, ms[best]) {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
	}
	return -1
}

// newer orders two models of one family: the higher version in the name,
// then (Bedrock) the geography that keeps data nearest the region, then an
// alias over a dated snapshot, then the shorter id.
func (f *Facade) newer(e endpoint, a, b core.ModelInfo) bool {
	if c := slices.Compare(version(a.DisplayName), version(b.DisplayName)); c != 0 {
		return c > 0
	}
	if e.kind == core.KindBedrock {
		if ra, rb := geoRank(e.region, a.Ref.Model), geoRank(e.region, b.Ref.Model); ra != rb {
			return ra < rb
		}
	}
	da, db := dateSuffix(a.Ref.Model), dateSuffix(b.Ref.Model)
	if da != db {
		return !da
	}
	if len(a.Ref.Model) != len(b.Ref.Model) {
		return len(a.Ref.Model) < len(b.Ref.Model)
	}
	return a.Ref.Model < b.Ref.Model
}

func version(name string) []int {
	v := versionRe.FindString(name)
	if v == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

var datedRe = regexp.MustCompile(`-(20\d{6}|20\d{2}-\d{2}-\d{2})(-v\d+(:\d+)?)?$`)

func dateSuffix(id string) bool { return datedRe.MatchString(id) }

// geoRank prefers the cross-region profile of the region's own geography,
// then global, then in-region use.
func geoRank(region, id string) int {
	geo, _ := bedrock.SplitModel(id)
	home, _, _ := strings.Cut(region, "-")
	switch home {
	case "ap":
		home = "apac"
	case "us":
		if strings.HasPrefix(region, "us-gov-") {
			home = "us-gov"
		}
	}
	switch geo {
	case home:
		return 0
	case "global":
		return 1
	case "":
		return 2
	}
	return 3
}

var paramsRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9.])(\d+(?:\.\d+)?)b(?:$|[^a-z])`)

// bigLocal is the size above which a local model rarely fits the server it
// runs on; such models are suggested only when nothing smaller is there.
const bigLocal = 40

// largest is the local model with the most parameters by its name
// ("qwen3:14b") up to bigLocal billion, else the smallest above it; the
// first when no name says.
func largest(ms []core.ModelInfo) int {
	best, size := -1, 0.0
	for i, m := range ms {
		s := 0.0
		if mm := paramsRe.FindStringSubmatch(m.Ref.Model); mm != nil {
			s, _ = strconv.ParseFloat(mm[1], 64)
		}
		fits, bestFits := s <= bigLocal, size <= bigLocal
		var better bool
		switch {
		case best < 0:
			better = true
		case fits != bestFits:
			better = fits
		case fits:
			better = s > size
		default:
			better = s < size
		}
		if better {
			best, size = i, s
		}
	}
	return best
}

// choicesFor turns an annotated list into picker entries.
func choicesFor(ms []core.ModelInfo) []core.ModelChoice {
	out := make([]core.ModelChoice, 0, len(ms))
	for _, m := range ms {
		out = append(out, core.ModelChoice{ID: m.Ref.Model, Display: cmp.Or(m.Display, m.Ref.Model), DisplayName: m.DisplayName,
			ShortName: m.ShortName, ProviderName: m.ProviderName, Recommended: m.Recommended, QuickTasksDefault: m.QuickTasksDefault})
	}
	return out
}
