package github

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var secretRefRE = regexp.MustCompile(`^secret:[a-z0-9_.-]{1,64}$`)

// Content from repositories, issues, pull requests and workflow runs may be
// written by anyone, and private repositories hold company data: tools that
// return it taint the session with both legs, so the trifecta floor guards
// whatever could carry it out. repo_checkout carries both too: exec output
// from the workspace is never tainted private, so the checkout is the only
// point where the session learns that private code is now within reach.
const repoTaint = core.TaintPrivate | core.TaintUntrusted

func builtinSpec(def core.ToolDef, m core.Manifest) core.ToolSpec {
	b, err := core.CanonicalJSON(def)
	if err != nil {
		panic(fmt.Sprintf("github: tool definition %s is not JSON: %v", def.Name, err))
	}
	sum := sha256.Sum256(b)
	return core.ToolSpec{Def: def, Source: "builtin", Manifest: m, Hash: hex.EncodeToString(sum[:])}
}

func objectSchema(props string, required ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `}` + req + `,"additionalProperties":false}`)
}

const repoProp = `"repo":{"type":"string","description":"owner/name of a connected repository (repo_list)"}`

// decodeArgs decodes a JSON object strictly into v.
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

// gate makes the tools appear only while some repository is allowlisted.
type gate struct{ s *Service }

func (g gate) Enabled(*core.ToolEnv) bool { return g.s.enabled.Load() }

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return fmtTime(*t)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
