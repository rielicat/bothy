package core

import (
	"errors"
	"testing"
)

func TestParseModelRef(t *testing.T) {
	tests := []struct {
		in      string
		want    ModelRef
		wantErr bool
	}{
		{"anthropic/claude-sonnet-4-6", ModelRef{"anthropic", "claude-sonnet-4-6"}, false},
		{"ollama/qwen3:14b", ModelRef{"ollama", "qwen3:14b"}, false},
		{"openrouter/meta-llama/llama-4", ModelRef{"openrouter", "meta-llama/llama-4"}, false},
		{"anthropic", ModelRef{}, true},
		{"/model", ModelRef{}, true},
		{"provider/", ModelRef{}, true},
		{"", ModelRef{}, true},
	}
	for _, tt := range tests {
		got, err := ParseModelRef(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseModelRef(%q) = %+v, %v; want %+v, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
		if err == nil && got.String() != tt.in {
			t.Errorf("ModelRef.String() = %q, want %q", got.String(), tt.in)
		}
	}
}

func TestPriceCost(t *testing.T) {
	sonnet := Price{InPerM: 3_000_000, OutPerM: 15_000_000, CacheReadPerM: 300_000, CacheWritePerM: 3_750_000, Known: true}
	tiered := Price{InPerM: 3_000_000, OutPerM: 15_000_000, Tiers: []PriceTier{{AboveInput: 200_000, Permille: 2000}}}
	gemini25Pro := Price{InPerM: 1_250_000, OutPerM: 10_000_000, CacheReadPerM: 125_000, Known: true,
		Tiers: []PriceTier{{AboveInput: 200_000, Permille: 2000, OutPermille: 1500}}}
	tests := []struct {
		name string
		p    Price
		u    Usage
		want Micros
	}{
		{"zero usage", sonnet, Usage{}, 0},
		{"free model", Price{}, Usage{Input: 1_000_000, Output: 1_000_000}, 0},
		{"one million input", sonnet, Usage{Input: 1_000_000}, 3_000_000},
		{"input and output", sonnet, Usage{Input: 1000, Output: 500}, 3000 + 7500},
		{"cache read and write", sonnet, Usage{CacheRead: 10_000, CacheWrite: 2_000}, 3_000 + 7_500},
		{"reasoning not billed separately", sonnet, Usage{Reasoning: 1_000_000}, 0},
		{"half micro rounds up", Price{InPerM: 500_000}, Usage{Input: 1}, 1},
		{"below half micro rounds down", Price{InPerM: 499_999}, Usage{Input: 1}, 0},
		{"each component rounds", Price{InPerM: 500_000, OutPerM: 500_000}, Usage{Input: 1, Output: 1}, 2},
		{"tier not reached", tiered, Usage{Input: 200_000}, 600_000},
		{"tier reprices whole request", tiered, Usage{Input: 300_000, Output: 1000}, 1_800_000 + 30_000},
		{"tier counts cache tokens", tiered, Usage{Input: 100_000, CacheRead: 150_000}, 600_000},
		{"highest applicable tier wins", Price{InPerM: 1_000_000, Tiers: []PriceTier{
			{AboveInput: 10, Permille: 1500}, {AboveInput: 100, Permille: 3000}, {AboveInput: 1_000_000, Permille: 9000}}},
			Usage{Input: 1000}, 3_000},
		{"discount tiers ignored", Price{InPerM: 1_000_000, Tiers: []PriceTier{{AboveInput: 0, Permille: 500}}},
			Usage{Input: 1000}, 1_000},
		// Google bills Gemini 2.5 Pro prompts over 200K at $2.50 in, $15 out,
		// $0.25 cache read (from $1.25, $10, $0.125).
		{"gemini pro long context", gemini25Pro, Usage{Input: 400_000, Output: 4_000}, 1_000_000 + 60_000},
		{"gemini pro below the tier", gemini25Pro, Usage{Input: 100_000, Output: 4_000}, 125_000 + 40_000},
		{"gemini pro cache read doubles", gemini25Pro, Usage{Input: 1_000, CacheRead: 300_000}, 2_500 + 75_000},
		{"own multiplier per component", Price{InPerM: 1_000_000, OutPerM: 1_000_000, CacheReadPerM: 1_000_000, CacheWritePerM: 1_000_000,
			Tiers: []PriceTier{{AboveInput: 10, Permille: 2000, OutPermille: 1500, CacheReadPermille: 3000, CacheWritePermille: 1000}}},
			Usage{Input: 100, Output: 100, CacheRead: 100, CacheWrite: 100}, 200 + 150 + 300 + 100},
		{"component discount ignored", Price{OutPerM: 1_000_000, Tiers: []PriceTier{{AboveInput: 0, Permille: 2000, OutPermille: 500}}},
			Usage{Input: 1, Output: 1000}, 1_000},
	}
	for _, tt := range tests {
		if got := tt.p.Cost(tt.u); got != tt.want {
			t.Errorf("%s: Cost = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestUsageAdd(t *testing.T) {
	a := Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Reasoning: 5}
	b := Usage{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, Reasoning: 50}
	want := Usage{Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44, Reasoning: 55}
	if got := a.Add(b); got != want {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
	if got := a.Add(Usage{}); got != a {
		t.Fatalf("Add(zero) = %+v, want %+v", got, a)
	}
	if got := want.TotalInput(); got != 11+33+44 {
		t.Fatalf("TotalInput = %d", got)
	}
}

func TestProviderError(t *testing.T) {
	inner := errors.New("dial tcp: refused")
	retry := map[ErrorKind]bool{
		ErrKindAuth: false, ErrKindRateLimit: true, ErrKindOverloaded: true, ErrKindContextLength: false,
		ErrKindInvalidRequest: false, ErrKindContentFilter: false, ErrKindNetwork: true, ErrKindServer: true,
	}
	for k, want := range retry {
		e := &ProviderError{Provider: "openai", Kind: k, Msg: "m", Err: inner}
		if e.Retryable() != want {
			t.Errorf("%s: Retryable = %v, want %v", k, !want, want)
		}
		if !errors.Is(e, inner) {
			t.Errorf("%s: Unwrap lost the cause", k)
		}
		if e.Error() != "openai: "+string(k)+": m" {
			t.Errorf("Error() = %q", e.Error())
		}
	}
}
