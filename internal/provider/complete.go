package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
)

const (
	baseBackoff   = 250 * time.Millisecond
	maxRetryAfter = 20 * time.Second
)

// Complete streams one model turn with fallback (SPEC §5.14): per candidate,
// before the first streamed event, one retry of a retryable error after
// backoff (or Retry-After up to 20s), then the next candidate; auth errors
// move on; context_length, invalid_request and content_filter return. After
// the first event nothing is retried or switched.
func (f *Facade) Complete(ctx context.Context, slot core.Slot, override *core.ModelRef, build core.BuildFunc, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	ci := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Slot: slot}
	chain := f.Chain(slot, override)
	if len(chain) == 0 {
		err := fmt.Errorf("provider: no model configured for the %s slot: %w", slot, core.ErrInvalid)
		ci.Error = err.Error()
		return nil, ci, err
	}
	return f.run(ctx, chain, build, on, ci)
}

// Passthrough calls exactly ref, with the same-candidate retry but no
// fallback.
func (f *Facade) Passthrough(ctx context.Context, ref core.ModelRef, req *core.Request, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	ci := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Ref: ref, Purpose: "passthrough"}
	m, ok := f.Model(ref)
	if !ok {
		err := fmt.Errorf("provider: unknown model %s: %w", ref, core.ErrNotFound)
		ci.Error = err.Error()
		return nil, ci, err
	}
	if req == nil {
		err := fmt.Errorf("provider: nil request: %w", core.ErrInvalid)
		ci.Error = err.Error()
		return nil, ci, err
	}
	return f.run(ctx, []core.ModelInfo{m}, func(core.ModelInfo) (*core.Request, error) { return req, nil }, on, ci)
}

func (f *Facade) run(ctx context.Context, chain []core.ModelInfo, build core.BuildFunc, on func(core.StreamEvent) error, ci core.CallInfo) (*core.Response, core.CallInfo, error) {
	start := f.clock.Now()
	fail := func(err error) (*core.Response, core.CallInfo, error) {
		ci.Error = err.Error()
		ci.Latency = f.clock.Now().Sub(start)
		return nil, ci, err
	}
	var last error
	for _, m := range chain {
		e, err := f.entry(m.Ref.Provider)
		if err != nil {
			continue
		}
		if f.open(e) {
			last = fmt.Errorf("provider %s skipped: circuit open after repeated failures", e.id)
			ci.Attempts = append(ci.Attempts, core.Attempt{Ref: m.Ref, Err: "circuit open"})
			continue
		}
		f.ensureContext(ctx, e, &m)
		req, err := build(m)
		if err != nil {
			return fail(err)
		}
		if req == nil {
			return fail(fmt.Errorf("provider: build returned no request: %w", core.ErrInvalid))
		}
		if len(req.Tools) > 0 && !m.Tools {
			last = fmt.Errorf("model %s does not support tools", m.Ref)
			ci.Attempts = append(ci.Attempts, core.Attempt{Ref: m.Ref, Err: "model has no tool support", Kind: core.ErrKindInvalidRequest})
			continue
		}
		req = prepare(req, m, e.kind)
		ci.Ref = m.Ref // a failed call is recorded against the last model tried
		for try := 0; try < 2; try++ {
			resp, started, cbErr, err := f.attempt(ctx, e, m, req, on, &ci, start)
			if err == nil {
				return f.finish(e, m, req, resp, on, ci, start)
			}
			if cbErr {
				return fail(err)
			}
			if ctx.Err() != nil {
				return fail(context.Cause(ctx))
			}
			if started {
				f.failed(e, err)
				return fail(fmt.Errorf("model error mid-reply: %w", err))
			}
			var pe *core.ProviderError
			if !errors.As(err, &pe) {
				return fail(err)
			}
			if pe.Kind == core.ErrKindAuth {
				f.log.Warn("provider rejected credentials", "provider", e.id, "status", pe.Status)
				f.authFailed(e, err)
				last = err
				break
			}
			if !pe.Retryable() {
				return fail(err)
			}
			if try == 0 {
				if wait := f.backoff(pe.RetryAfter); wait >= 0 {
					f.log.Debug("retrying provider", "provider", e.id, "model", m.Ref.Model, "kind", pe.Kind, "wait", wait)
					if err := f.sleep(ctx, wait); err != nil {
						return fail(err)
					}
					continue
				}
			}
			f.log.Warn("provider failed, trying next candidate", "provider", e.id, "model", m.Ref.Model, "kind", pe.Kind, "status", pe.Status)
			f.failed(e, err)
			last = err
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("provider: no usable model in the chain: %w", core.ErrInvalid)
	}
	return fail(last)
}

// backoff returns the wait before the same-candidate retry, or -1 when the
// server asks for longer than the facade waits (then the next candidate is
// tried instead).
func (f *Facade) backoff(retryAfter time.Duration) time.Duration {
	if retryAfter > maxRetryAfter {
		return -1
	}
	if retryAfter > 0 {
		return retryAfter
	}
	return time.Duration(float64(baseBackoff) * (0.8 + 0.4*f.jitter()))
}

// attempt makes one provider call and records it. started reports whether
// any event reached the caller; cbErr whether the error came from on.
func (f *Facade) attempt(ctx context.Context, e *entry, m core.ModelInfo, req *core.Request, on func(core.StreamEvent) error, ci *core.CallInfo, start time.Time) (resp *core.Response, started, cbErr bool, err error) {
	t0 := f.clock.Now()
	var onErr error
	wrapped := func(ev core.StreamEvent) error {
		if !started {
			started = true
			if ci.TTFT == 0 {
				ci.TTFT = f.clock.Now().Sub(start)
			}
		}
		if on == nil {
			return nil
		}
		if err := on(ev); err != nil {
			onErr = err
			return err
		}
		return nil
	}
	resp, err = e.cl.Stream(ctx, req, wrapped)
	a := core.Attempt{Ref: m.Ref, Latency: f.clock.Now().Sub(t0)}
	if err != nil {
		a.Err = err.Error()
		var pe *core.ProviderError
		if errors.As(err, &pe) {
			a.Kind = pe.Kind
		}
	}
	ci.Attempts = append(ci.Attempts, a)
	if onErr != nil {
		return nil, started, true, onErr
	}
	if err == nil && resp == nil {
		err = &core.ProviderError{Provider: e.id, Kind: core.ErrKindServer, Msg: "empty response"}
	}
	return resp, started, false, err
}

func (f *Facade) finish(e *entry, m core.ModelInfo, req *core.Request, resp *core.Response, on func(core.StreamEvent) error, ci core.CallInfo, start time.Time) (*core.Response, core.CallInfo, error) {
	f.succeeded(e)
	if resp.UsageEstimated {
		resp.Usage = core.Usage{
			Input:  f.Estimate(requestText(req), m.Ref),
			Output: f.Estimate(messageText(resp.Message), m.Ref),
		}
		resp.Message.Tokens = resp.Usage.Output
		if on != nil {
			u := resp.Usage
			if err := on(core.StreamEvent{Type: core.StreamUsage, Usage: &u}); err != nil {
				ci.Error = err.Error()
				return nil, ci, err
			}
		}
	}
	if resp.Model == "" {
		resp.Model = m.Ref.Model
	}
	ci.Ref = m.Ref
	ci.Usage = resp.Usage
	ci.UsageEstimated = resp.UsageEstimated
	ci.Cost = m.Price.Cost(resp.Usage)
	ci.PriceKnown = m.Price.Known
	ci.Stop = resp.Stop
	ci.PrefixHash = prefixHash(req)
	ci.Latency = f.clock.Now().Sub(start)
	return resp, ci, nil
}

// prefixHash is sha256(tools bytes || system bytes), hex[:16]: equal hashes
// across calls mean the cacheable prefix did not change.
func prefixHash(r *core.Request) string {
	h := sha256.New()
	tb, _ := json.Marshal(r.Tools)
	h.Write(tb)
	for _, s := range r.System {
		h.Write([]byte(s.Text))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// prepare applies the provider rules the wire format depends on, on a copy
// (the caller's history must not change): notices never reach a model,
// thinking parts and signatures from another kind are dropped, images become
// a note for models without vision, and Ollama gets the model's window as
// num_ctx.
func prepare(r *core.Request, m core.ModelInfo, kind core.ProviderKind) *core.Request {
	out := *r
	out.Messages = make([]core.Message, 0, len(r.Messages))
	for _, msg := range r.Messages {
		if msg.Kind == core.KindNotice {
			continue
		}
		parts := make([]core.Part, 0, len(msg.Parts))
		for _, p := range msg.Parts {
			if p.Type == core.PartThinking && p.SigKind != kind {
				continue
			}
			if p.Sig != "" && p.SigKind != kind {
				p.Sig, p.SigKind = "", ""
			}
			if p.Type == core.PartImage && !m.Vision {
				p = core.Part{Type: core.PartText, Text: "[image omitted: this model cannot read images] " + wire.FileNote(p.Blob)}
			}
			parts = append(parts, p)
		}
		msg.Parts = parts
		out.Messages = append(out.Messages, msg)
	}
	if out.Model == "" {
		out.Model = m.Ref.Model
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = m.MaxOutput
	}
	if kind == core.KindOllama && out.NumCtx <= 0 {
		out.NumCtx = m.Context
	}
	return &out
}
