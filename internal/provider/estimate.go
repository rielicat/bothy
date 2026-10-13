package provider

import (
	"encoding/json"
	"math"

	"github.com/rielicat/bothy/internal/core"
)

const (
	bytesPerToken = 3.6
	ewmaAlpha     = 0.2
	minCorrection = 0.25
	maxCorrection = 4.0
	// maxCorrections bounds the per-model table: passthrough clients name
	// models freely, so the set of refs is not limited by config.
	maxCorrections = 1024
)

func (f *Facade) correction(ref core.ModelRef) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.corr[ref.String()]; ok {
		return c
	}
	return 1
}

// Estimate is bytes/3.6 scaled by the model's learned correction.
func (f *Facade) Estimate(text string, ref core.ModelRef) int {
	if text == "" {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / bytesPerToken * f.correction(ref)))
}

// Calibrate moves the model's correction towards actual/estimated. The
// estimate passed in already includes the current correction, so the target
// is the current correction times that ratio. Outliers are clamped so one
// odd response (a cached prompt, an image) cannot swing later estimates.
func (f *Facade) Calibrate(ref core.ModelRef, estimated, actual int) {
	if estimated <= 0 || actual <= 0 {
		return
	}
	ratio := clamp(float64(actual)/float64(estimated), minCorrection, maxCorrection)
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.corr[ref.String()]
	if !ok {
		if len(f.corr) >= maxCorrections {
			return
		}
		cur = 1
	}
	f.corr[ref.String()] = clamp(cur+ewmaAlpha*(cur*ratio-cur), minCorrection, maxCorrection)
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// requestText is what the estimator counts for a request's input.
func requestText(r *core.Request) string {
	var b []byte
	for _, s := range r.System {
		b = append(b, s.Text...)
	}
	if len(r.Tools) > 0 {
		tb, _ := json.Marshal(r.Tools)
		b = append(b, tb...)
	}
	for _, m := range r.Messages {
		b = append(b, messageText(m)...)
	}
	return string(b)
}

func messageText(m core.Message) string {
	var b []byte
	for _, p := range m.Parts {
		b = append(b, p.Text...)
		if p.ToolCall != nil {
			b = append(b, p.ToolCall.Name...)
			b = append(b, p.ToolCall.Args...)
		}
		if p.ToolResult != nil {
			b = append(b, p.ToolResult.Content...)
		}
	}
	return string(b)
}
