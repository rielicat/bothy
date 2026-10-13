package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/rielicat/bothy/internal/core"
)

// decodeArgs strictly decodes a tool's JSON arguments; empty means {}.
func decodeArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: arguments: %w", core.ErrInvalid, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: arguments: trailing data", core.ErrInvalid)
	}
	return nil
}

func newAction(tool string, kind core.ActionKind, se core.SideEffect, payload any) (*core.Action, error) {
	b, err := core.CanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s action: %w", tool, err)
	}
	return &core.Action{Tool: tool, Kind: kind, SideEffect: se, Payload: b, Fingerprint: core.ActionFingerprint(tool, b)}, nil
}

func payloadOf(a *core.Action, v any) error {
	if a == nil {
		return fmt.Errorf("%w: no action", core.ErrInvalid)
	}
	if err := json.Unmarshal(a.Payload, v); err != nil {
		return fmt.Errorf("%w: %s payload: %w", core.ErrInvalid, a.Tool, err)
	}
	return nil
}
