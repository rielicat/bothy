package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
)

var (
	loadSpec = newSpec(core.ToolDef{
		Name:        "skill_load",
		Description: "Load the instructions (SKILL.md) of a skill listed in the skills index, plus the names of its other files.",
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name from the skills index"}},` +
			`"required":["name"],"additionalProperties":false}`),
	}, core.Manifest{Title: "Load skill", SideEffect: core.SERead, SpaceOK: true, Core: true})

	readSpec = newSpec(core.ToolDef{
		Name:        "skill_read",
		Description: "Read one file of an enabled skill, by its path relative to the skill folder (as listed by skill_load).",
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name"},` +
			`"path":{"type":"string","description":"File path relative to the skill folder"}},"required":["name","path"],"additionalProperties":false}`),
	}, core.Manifest{Title: "Read skill file", SideEffect: core.SERead, SpaceOK: true})
)

func newSpec(def core.ToolDef, m core.Manifest) core.ToolSpec {
	canon, err := core.CanonicalJSON(def)
	if err != nil {
		panic(fmt.Sprintf("skills: tool %s: %v", def.Name, err))
	}
	def.Schema, err = core.CanonicalJSON(def.Schema)
	if err != nil {
		panic(fmt.Sprintf("skills: tool %s schema: %v", def.Name, err))
	}
	sum := sha256.Sum256(canon)
	return core.ToolSpec{Def: def, Source: "builtin", Manifest: m, Hash: hex.EncodeToString(sum[:])}
}

// decodeArgs strictly decodes a tool's JSON object arguments.
func decodeArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: arguments: %w", core.ErrInvalid, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: arguments: trailing data", core.ErrInvalid)
	}
	return nil
}

func newAction(spec core.ToolSpec, payload any) (*core.Action, error) {
	canon, err := core.CanonicalJSON(payload)
	if err != nil {
		return nil, err
	}
	return &core.Action{Tool: spec.Def.Name, Kind: core.ActSkill, SideEffect: core.SERead,
		Payload: canon, Fingerprint: core.ActionFingerprint(spec.Def.Name, canon)}, nil
}

func errorResult(name string, err error) *core.ToolResult {
	msg := err.Error()
	switch {
	case errors.Is(err, errChanged):
		msg = "This skill's files changed since it was approved, so it is unavailable until an admin re-enables it."
	case errors.Is(err, errNotEnabled), errors.Is(err, core.ErrNotFound):
		msg = "No such enabled skill or file. Check the skills index and the file list from skill_load."
	}
	return &core.ToolResult{Name: name, Content: msg, IsError: true}
}

// expected reports errors that are answers for the model rather than
// failures of the tool.
func expected(err error) bool {
	return errors.Is(err, errChanged) || errors.Is(err, errNotEnabled) || errors.Is(err, core.ErrNotFound) ||
		errors.Is(err, core.ErrInvalid) || errors.Is(err, core.ErrForbidden)
}

type loadArgs struct {
	Name string `json:"name"`
}

type loadTool struct{ l *Library }

func (loadTool) Spec() core.ToolSpec { return loadSpec }

func (t loadTool) Enabled(*core.ToolEnv) bool { return t.l.enabled.Load() > 0 }

func (loadTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a loadArgs
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if !validName(a.Name) {
		return nil, fmt.Errorf("%w: %q is not a skill name", core.ErrInvalid, a.Name)
	}
	return newAction(loadSpec, a)
}

func (t loadTool) Exec(ctx context.Context, _ *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var args loadArgs
	if err := decodeArgs(a.Payload, &args); err != nil {
		return nil, err
	}
	body, files, err := t.l.loadEnabled(ctx, args.Name)
	if expected(err) {
		return errorResult(loadSpec.Def.Name, err), nil
	}
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(body)
	if len(files) > 0 {
		fmt.Fprintf(&b, "\n\nOther files in skill %q (read them with skill_read):\n", args.Name)
		for _, f := range files {
			b.WriteString("- " + f + "\n")
		}
	}
	return &core.ToolResult{Name: loadSpec.Def.Name, Content: b.String()}, nil
}

type readArgs struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type readTool struct{ l *Library }

func (readTool) Spec() core.ToolSpec { return readSpec }

func (t readTool) Enabled(*core.ToolEnv) bool { return t.l.enabled.Load() > 0 }

func (readTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a readArgs
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if !validName(a.Name) {
		return nil, fmt.Errorf("%w: %q is not a skill name", core.ErrInvalid, a.Name)
	}
	rel, err := cleanRel(a.Path)
	if err != nil {
		return nil, err
	}
	return newAction(readSpec, readArgs{Name: a.Name, Path: rel})
}

func (t readTool) Exec(ctx context.Context, _ *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var args readArgs
	if err := decodeArgs(a.Payload, &args); err != nil {
		return nil, err
	}
	data, err := t.l.readEnabled(ctx, args.Name, args.Path)
	if expected(err) {
		return errorResult(readSpec.Def.Name, err), nil
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return &core.ToolResult{Name: readSpec.Def.Name, IsError: true,
			Content: fmt.Sprintf("%s is a binary file (%d bytes) and cannot be shown as text.", args.Path, len(data))}, nil
	}
	return &core.ToolResult{Name: readSpec.Def.Name, Content: string(data)}, nil
}
