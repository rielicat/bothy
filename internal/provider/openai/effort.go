package openai

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

// EffortMode is what a Chat Completions endpoint showed about function tools
// and reasoning for one model.
type EffortMode uint8

const (
	// EffortOff: tools work only with reasoning_effort "none" (OpenAI from
	// GPT-5.4 on, and compatible servers that forward the restriction).
	EffortOff EffortMode = iota + 1
	// EffortNoTools: tools fail with reasoning on and with "none" too (GPT-6
	// Astra rejects "none"); only the Responses API can call them.
	EffortNoTools
)

// EffortEntry is one remembered (provider, model).
type EffortEntry struct {
	Provider string
	Model    string
	Mode     EffortMode
}

// EffortMemory remembers, per provider and model, that Chat Completions
// rejected function tools with reasoning on, so later requests send
// reasoning_effort "none" at once instead of paying a rejected request every
// turn. It is bounded (oldest entries go first) and lives in memory only: a
// restart or a provider config change costs one more rejected request.
type EffortMemory struct {
	mu     sync.Mutex
	max    int
	order  []string
	m      map[string]EffortEntry
	notify func(EffortEntry)
}

// NewEffortMemory keeps at most limit entries; notify (optional) is called,
// outside the lock, when an entry is added or changes.
func NewEffortMemory(limit int, notify func(EffortEntry)) *EffortMemory {
	return &EffortMemory{max: max(1, limit), m: map[string]EffortEntry{}, notify: notify}
}

func effortKey(provider, model string) string { return provider + "\x00" + model }

// Mode returns what was learned for the model, or 0.
func (e *EffortMemory) Mode(provider, model string) EffortMode {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.m[effortKey(provider, model)].Mode
}

func (e *EffortMemory) record(provider, model string, mode EffortMode) {
	if e == nil {
		return
	}
	k := effortKey(provider, model)
	ent := EffortEntry{Provider: provider, Model: model, Mode: mode}
	e.mu.Lock()
	old, ok := e.m[k]
	if !ok {
		if len(e.order) >= e.max {
			delete(e.m, e.order[0])
			e.order = e.order[1:]
		}
		e.order = append(e.order, k)
	}
	e.m[k] = ent
	notify := e.notify
	e.mu.Unlock()
	if notify != nil && (!ok || old.Mode != mode) {
		notify(ent)
	}
}

// Forget drops a provider's entries.
func (e *EffortMemory) Forget(provider string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.order = slices.DeleteFunc(e.order, func(k string) bool {
		if e.m[k].Provider == provider {
			delete(e.m, k)
			return true
		}
		return false
	})
}

// Entries lists what is remembered, sorted by provider and model.
func (e *EffortMemory) Entries() []EffortEntry {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	out := make([]EffortEntry, 0, len(e.m))
	for _, ent := range e.m {
		out = append(out, ent)
	}
	e.mu.Unlock()
	slices.SortFunc(out, func(a, b EffortEntry) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model))
	})
	return out
}

// toolsNeedNoReasoning reports whether err is the rejection OpenAI sends
// (HTTP 400) for function tools on Chat Completions when the effective
// reasoning effort is not "none", which is also the case when Bothy sends
// none and the model defaults to medium:
//
//	Function tools with reasoning_effort are not supported for gpt-6-astra in
//	/v1/chat/completions. To use function tools, use /v1/responses or set
//	reasoning_effort to 'none'.
//
// The body's type, code and param are undocumented, so the message is
// matched by its key terms rather than as a sentence.
func toolsNeedNoReasoning(err error) bool {
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest {
		return false
	}
	m := strings.ToLower(pe.Msg)
	effort := strings.Contains(m, "reasoning_effort") || strings.Contains(m, "reasoning effort")
	tools := strings.Contains(m, "tool") || strings.Contains(m, "function")
	refused := strings.Contains(m, "not supported") || strings.Contains(m, "unsupported") ||
		strings.Contains(m, "'none'") || strings.Contains(m, `"none"`)
	return effort && tools && refused
}
