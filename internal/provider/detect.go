package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/ollama"
	"github.com/rielicat/bothy/internal/provider/openai"
	"github.com/rielicat/bothy/internal/provider/wire"
)

const detectTimeout = 1500 * time.Millisecond

// localServers are probed by Detect: Ollama, LM Studio and vLLM on their
// default ports, from inside the container (host.docker.internal is the
// Docker host; localhost is the container itself) and on this machine, and
// Ollama as a compose service.
var localServers = []struct {
	kind core.ProviderKind
	base string
	note string
}{
	{core.KindOllama, "http://host.docker.internal:11434", "Ollama on the Docker host"},
	{core.KindOllama, "http://localhost:11434", "Ollama on this machine"},
	{core.KindOllama, "http://ollama:11434", "Ollama compose service"},
	{core.KindOpenAICompat, "http://host.docker.internal:1234/v1", "LM Studio on the Docker host"},
	{core.KindOpenAICompat, "http://localhost:1234/v1", "LM Studio"},
	{core.KindOpenAICompat, "http://host.docker.internal:8000/v1", "vLLM on the Docker host"},
	{core.KindOpenAICompat, "http://localhost:8000/v1", "vLLM"},
}

// keyEnvVars are the well-known key variables the wizard offers to import.
// Only the variable name is ever reported; Bothy never uses the value
// unless the user imports it into the vault.
var keyEnvVars = []struct {
	name string
	kind core.ProviderKind
	base string
}{
	{"ANTHROPIC_API_KEY", core.KindAnthropic, "https://api.anthropic.com/v1"},
	{"OPENAI_API_KEY", core.KindOpenAI, "https://api.openai.com/v1"},
	{"GEMINI_API_KEY", core.KindGemini, "https://generativelanguage.googleapis.com/v1beta"},
	{"OPENROUTER_API_KEY", core.KindOpenAICompat, "https://openrouter.ai/api/v1"},
	{"GROQ_API_KEY", core.KindOpenAICompat, "https://api.groq.com/openai/v1"},
	{"XAI_API_KEY", core.KindOpenAICompat, "https://api.x.ai/v1"},
	{"DEEPSEEK_API_KEY", core.KindOpenAICompat, "https://api.deepseek.com/v1"},
}

// savedKeyPrefixes map the start of a global vault secret's name
// ("openrouter_api_key", "anthropic-prod") to the provider it is a key for,
// so setup can offer keys loaded with `bothy vault set`.
var savedKeyPrefixes = []struct {
	prefix string
	kind   core.ProviderKind
	base   string
}{
	{"anthropic", core.KindAnthropic, "https://api.anthropic.com/v1"},
	{"openai", core.KindOpenAI, "https://api.openai.com/v1"},
	{"gemini", core.KindGemini, "https://generativelanguage.googleapis.com/v1beta"},
	{"google", core.KindGemini, "https://generativelanguage.googleapis.com/v1beta"},
	{"openrouter", core.KindOpenAICompat, "https://openrouter.ai/api/v1"},
	{"groq", core.KindOpenAICompat, "https://api.groq.com/openai/v1"},
	{"xai", core.KindOpenAICompat, "https://api.x.ai/v1"},
	{"deepseek", core.KindOpenAICompat, "https://api.deepseek.com/v1"},
}

// savedKeys lists provider keys already in the global vault by name and
// last four characters, never their values.
func (f *Facade) savedKeys(ctx context.Context) []core.Detection {
	if f.d.Vault == nil {
		return nil
	}
	metas, err := f.d.Vault.List(ctx, core.GlobalScope)
	if err != nil {
		return nil
	}
	var out []core.Detection
	for _, m := range metas {
		for _, k := range savedKeyPrefixes {
			rest, ok := strings.CutPrefix(m.Name, k.prefix)
			if !ok || rest != "" && !strings.ContainsRune("_-.", rune(rest[0])) {
				continue
			}
			note := "Saved key " + m.Name
			if m.Hint != "" {
				note += ", ending " + m.Hint
			}
			out = append(out, core.Detection{Kind: k.kind, BaseURL: k.base, SecretRef: "secret:" + m.Name, SecretHint: m.Hint, Note: note + "."})
			break
		}
	}
	return out
}

// Detect finds this server's AWS access (offered first, as Bedrock), local
// model servers, provider keys already saved in the vault and well-known key
// variables for the setup wizard.
func (f *Facade) Detect(ctx context.Context) []core.Detection {
	results := make([]*core.Detection, len(localServers)+1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0] = f.detectAWS(ctx)
	}()
	for i, s := range localServers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i+1] = f.detectServer(ctx, s.kind, s.base, s.note)
		}()
	}
	wg.Wait()
	var out []core.Detection
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	out = append(out, f.savedKeys(ctx)...)
	set := map[string]bool{}
	for _, kv := range f.d.Env() {
		name, val, ok := strings.Cut(kv, "=")
		if ok && strings.TrimSpace(val) != "" {
			set[name] = true
		}
	}
	for _, k := range keyEnvVars {
		if set[k.name] {
			out = append(out, core.Detection{Kind: k.kind, BaseURL: k.base, EnvVar: k.name,
				Note: fmt.Sprintf("Found %s in the server's environment. Choosing it saves a copy in Bothy's saved keys.", k.name)})
		}
	}
	return out
}

func (f *Facade) detectServer(ctx context.Context, kind core.ProviderKind, base, note string) *core.Detection {
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	hc, err := f.httpClient("provider:detect", base, true)
	if err != nil {
		return nil
	}
	defer hc.CloseIdleConnections()
	o := wire.Options{ID: "detect", Kind: kind, BaseURL: base, HTTP: hc}
	var ms []core.ModelInfo
	if kind == core.KindOllama {
		var names []string
		names, err = ollama.New(o).Tags(ctx)
		for _, n := range names {
			ms = append(ms, core.ModelInfo{Ref: core.ModelRef{Provider: "detect", Model: n}, Tools: true})
		}
	} else {
		ms, err = openai.New(o).Discover(ctx)
	}
	if err != nil {
		return nil
	}
	var names []string
	for _, m := range ms {
		names = append(names, m.Ref.Model)
	}
	f.annotate(endpoint{kind: kind, base: base}, ms)
	return &core.Detection{Kind: kind, BaseURL: base, Models: names, Choices: choicesFor(ms),
		Note: fmt.Sprintf("%s (%d models)", note, len(names))}
}
