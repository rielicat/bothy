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

// localServers are probed by Detect: Ollama from inside the container (the
// host), on the host itself and as a compose service; LM Studio and vLLM on
// their default ports.
var localServers = []struct {
	kind core.ProviderKind
	base string
	note string
}{
	{core.KindOllama, "http://host.docker.internal:11434", "Ollama on the Docker host"},
	{core.KindOllama, "http://localhost:11434", "Ollama on this machine"},
	{core.KindOllama, "http://ollama:11434", "Ollama compose service"},
	{core.KindOpenAICompat, "http://localhost:1234/v1", "LM Studio"},
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

// Detect finds local model servers and well-known key variables for the
// setup wizard.
func (f *Facade) Detect(ctx context.Context) []core.Detection {
	results := make([]*core.Detection, len(localServers))
	var wg sync.WaitGroup
	for i, s := range localServers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.detectServer(ctx, s.kind, s.base, s.note)
		}()
	}
	wg.Wait()
	var out []core.Detection
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
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
				Note: fmt.Sprintf("Found %s. Import it into the vault?", k.name)})
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
	var names []string
	if kind == core.KindOllama {
		names, err = ollama.New(o).Tags(ctx)
	} else {
		var ms []core.ModelInfo
		ms, err = openai.New(o).Discover(ctx)
		for _, m := range ms {
			names = append(names, m.Ref.Model)
		}
	}
	if err != nil {
		return nil
	}
	return &core.Detection{Kind: kind, BaseURL: base, Models: names, Note: fmt.Sprintf("%s (%d models)", note, len(names))}
}
