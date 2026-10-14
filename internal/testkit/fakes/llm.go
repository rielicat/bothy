package fakes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.LLM = (*LLM)(nil)

// LLM replays scripted turns: each Step streams Text and/or ToolCalls and
// returns Usage; Err fails the call. Requests records every BuildFunc result.
//
// A zero Info means DefaultModel; an Info without Ref gets DefaultModel's Ref.
// Each Complete or Passthrough call consumes one Step and fails when none is
// left. A step streams its Text deltas, then fails with Err if set (so Err
// without Text fails before any event), then streams each tool call as start
// and done events (ids "call_<n>" unique per fake when empty, args "{}" when
// empty) and finally one usage event. Chain, Model and Models report the one
// model; Estimate is bytes/3.6.
type LLM struct {
	core.LLM
	Steps    []LLMStep
	Requests []*core.Request
	Info     core.ModelInfo
	Probes   []core.ProbeConfig // every Probe call's config

	mu      sync.Mutex
	next    int
	callSeq int
}

type LLMStep struct {
	Text      []string // streamed as deltas
	ToolCalls []core.ToolCall
	Usage     core.Usage
	Err       error
	Block     chan struct{} // if set, blocks after the first delta until closed or ctx done
}

// DefaultModel is the model a zero LLM.Info stands for.
var DefaultModel = core.ModelInfo{
	Ref: core.ModelRef{Provider: "fake", Model: "fake-model"}, Display: "Fake model",
	Context: 128_000, MaxOutput: 8192, Tools: true, Source: "config",
}

func (l *LLM) model(override *core.ModelRef) core.ModelInfo {
	l.mu.Lock()
	info := l.Info
	l.mu.Unlock()
	switch {
	case info.Ref == (core.ModelRef{}) && info.Context == 0:
		info = DefaultModel
	case info.Ref == (core.ModelRef{}):
		info.Ref = DefaultModel.Ref
	}
	if override != nil {
		info.Ref = *override
	}
	return info
}

func (l *LLM) Complete(ctx context.Context, slot core.Slot, override *core.ModelRef, build core.BuildFunc, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	info := l.model(override)
	req, err := build(info)
	if err != nil {
		return nil, core.CallInfo{Ref: info.Ref, Slot: slot}, err
	}
	return l.play(ctx, slot, info, req, on)
}

func (l *LLM) Passthrough(ctx context.Context, ref core.ModelRef, req *core.Request, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	return l.play(ctx, "", l.model(&ref), req, on)
}

func (l *LLM) play(ctx context.Context, slot core.Slot, info core.ModelInfo, req *core.Request, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	start := time.Now()
	ci := core.CallInfo{
		ID: core.NewID(core.PrefixLLMCall), Ref: info.Ref, Slot: slot,
		Attempts: []core.Attempt{{Ref: info.Ref}}, PriceKnown: info.Price.Known,
	}
	l.mu.Lock()
	l.Requests = append(l.Requests, req)
	if l.next >= len(l.Steps) {
		n := len(l.Requests)
		l.mu.Unlock()
		err := fmt.Errorf("fakes.LLM: no scripted step for call %d", n)
		ci.Error = err.Error()
		return nil, ci, err
	}
	step := l.Steps[l.next]
	l.next++
	calls := make([]core.ToolCall, len(step.ToolCalls))
	for i, tc := range step.ToolCalls {
		if tc.ID == "" {
			l.callSeq++
			tc.ID = fmt.Sprintf("call_%d", l.callSeq)
		}
		if len(tc.Args) == 0 {
			tc.Args = json.RawMessage("{}")
		}
		calls[i] = tc
	}
	l.mu.Unlock()

	fail := func(err error) (*core.Response, core.CallInfo, error) {
		ci.Error, ci.Latency = err.Error(), time.Since(start)
		return nil, ci, err
	}
	emit := func(ev core.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ci.TTFT == 0 {
			ci.TTFT = time.Since(start)
		}
		if on == nil {
			return nil
		}
		return on(ev)
	}
	block := func() error {
		if step.Block == nil {
			return nil
		}
		select {
		case <-step.Block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	var text strings.Builder
	for i, chunk := range step.Text {
		if err := emit(core.StreamEvent{Type: core.StreamTextDelta, Text: chunk}); err != nil {
			return fail(err)
		}
		text.WriteString(chunk)
		if i == 0 {
			if err := block(); err != nil {
				return fail(err)
			}
		}
	}
	if len(step.Text) == 0 {
		if err := block(); err != nil {
			return fail(err)
		}
	}
	if step.Err != nil {
		return fail(step.Err)
	}

	msg := core.Message{Role: core.MsgAssistant, Kind: core.KindNormal, Tokens: step.Usage.Output, CreatedAt: time.Now()}
	if text.Len() > 0 {
		msg.Parts = append(msg.Parts, core.Part{Type: core.PartText, Text: text.String()})
	}
	for _, tc := range calls {
		if err := emit(core.StreamEvent{Type: core.StreamToolCallStart, Call: &core.ToolCall{ID: tc.ID, Name: tc.Name}}); err != nil {
			return fail(err)
		}
		done := tc
		if err := emit(core.StreamEvent{Type: core.StreamToolCallDone, Call: &done}); err != nil {
			return fail(err)
		}
		part := tc
		msg.Parts = append(msg.Parts, core.Part{Type: core.PartToolCall, ToolCall: &part})
	}
	usage := step.Usage
	if err := emit(core.StreamEvent{Type: core.StreamUsage, Usage: &usage}); err != nil {
		return fail(err)
	}

	stop := core.StopEnd
	if len(calls) > 0 {
		stop = core.StopTool
	}
	ci.Usage, ci.Cost, ci.Stop, ci.Latency = usage, info.Price.Cost(usage), stop, time.Since(start)
	return &core.Response{Message: msg, Usage: usage, Stop: stop, Model: info.Ref.Model}, ci, nil
}

func (l *LLM) Chain(_ core.Slot, override *core.ModelRef) []core.ModelInfo {
	return []core.ModelInfo{l.model(override)}
}

func (l *LLM) Model(ref core.ModelRef) (core.ModelInfo, bool) {
	info := l.model(nil)
	return info, ref == info.Ref
}

func (l *LLM) Models(context.Context) []core.ModelInfo { return []core.ModelInfo{l.model(nil)} }

func (l *LLM) Estimate(text string, _ core.ModelRef) int { return (len(text)*10 + 35) / 36 }

func (l *LLM) Calibrate(core.ModelRef, int, int) {}

// Probe records cfg in Probes and reports the one model as working.
func (l *LLM) Probe(_ context.Context, cfg core.ProbeConfig) (core.ProbeResult, error) {
	l.mu.Lock()
	l.Probes = append(l.Probes, cfg)
	l.mu.Unlock()
	info := l.model(nil)
	return core.ProbeResult{OK: true, ToolCalling: info.Tools, UsageReported: true, Context: info.Context, Models: []string{info.Ref.Model}}, nil
}

func (l *LLM) Detect(context.Context) []core.Detection { return nil }

func (l *LLM) Health() []core.ProviderHealth { return nil }

// Recorded returns a copy of Requests.
func (l *LLM) Recorded() []*core.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*core.Request(nil), l.Requests...)
}

// Calls returns how many Complete and Passthrough calls reached the script.
func (l *LLM) Calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Requests)
}
