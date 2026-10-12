// Package config loads the layered configuration: defaults < bothy.yaml < env <
// bothy.ui.yaml. It is fully implemented in the skeleton and frozen.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type Layer string

const (
	LayerDefault Layer = "default"
	LayerFile    Layer = "file" // <data>/bothy.yaml or $BOTHY_CONFIG
	LayerEnv     Layer = "env"  // BOTHY_<PATH> with "__" between segments
	LayerUI      Layer = "ui"   // <data>/bothy.ui.yaml, written by the web UI
)

type Source struct {
	Layer  Layer     `json:"layer"`
	File   string    `json:"file,omitempty"`
	Line   int       `json:"line,omitempty"`
	EnvVar string    `json:"env_var,omitempty"`
	By     string    `json:"by,omitempty"` // UI layer: "user:<id>"
	At     time.Time `json:"at,omitempty"` // UI layer write time
}

type Shadow struct {
	Source Source          `json:"source"`
	Value  json.RawMessage `json:"value"` // secret refs shown as refs, never resolved
}

type Entry struct {
	Path     string          `json:"path"` // dotted; list items by id/name: "providers.ollama.base_url"
	Value    json.RawMessage `json:"value"`
	Source   Source          `json:"source"`
	Shadowed []Shadow        `json:"shadowed"`
	Locked   bool            `json:"locked"` // locked:"true" tag or BOTHY_CONFIG_LOCK glob
	Hot      bool            `json:"hot"`    // applies without restart
	Secret   bool            `json:"secret"`
}

type FieldError struct {
	Path    string `json:"path"`
	Source  Source `json:"source"`
	Message string `json:"message"` // includes "did you mean …" suggestions
}

type Effective struct {
	Entries  []Entry      `json:"entries"`
	Errors   []FieldError `json:"errors"` // a key failing validation falls back to the next lower valid layer
	SafeMode bool         `json:"safe_mode"`
	Restart  []string     `json:"restart_required"` // restart-only paths changed since boot
}

type Options struct {
	DataDir  string             // default /data; config file <DataDir>/bothy.yaml unless File set
	File     string             // $BOTHY_CONFIG
	Env      func() []string    // os.Environ in production
	Defaults func(*Config)      // optional extra defaulting (binary vs image listen address)
	OnSet    func(ChangeRecord) // audit hook (config.set / config.reset)
}

type ChangeRecord struct {
	Path   string          `json:"path"`
	Old    json.RawMessage `json:"old"`
	New    json.RawMessage `json:"new"` // null for reset
	By     string          `json:"by"`
	Action string          `json:"action"` // set | reset
}

// View is what modules depend on (fakeable with config.Static).
type View interface {
	Current() *Config // immutable snapshot; never mutate
	Watch(fn func(old, cur *Config)) (cancel func())
}

// Admin is the config surface used by the HTTP API (implemented by *Manager).
type Admin interface {
	View
	Effective() Effective
	Set(ctx context.Context, changes map[string]json.RawMessage, by string) (restart []string, err error)
	Reset(ctx context.Context, path, by string) error
	Schema() json.RawMessage
}

// Manager implements Admin.
//
// TEMPORARY skeleton-base implementation: defaults + scalar env overrides, no
// file or UI layers, no provenance. Replaced by the full loader.
type Manager struct {
	mu       sync.Mutex
	cur      *Config
	watchers map[int]func(old, cur *Config)
	nextID   int
}

var errNotImplemented = errors.New("config: not implemented in the skeleton base")

// Load never fails on a bad file: YAML parse errors put the manager in safe
// mode (defaults + env + ui) with Errors set. It fails only on unreadable data dir.
func Load(ctx context.Context, o Options) (*Manager, error) {
	if o.DataDir == "" {
		o.DataDir = "/data"
	}
	if o.Env == nil {
		o.Env = func() []string { return nil }
	}
	env := envMap(o.Env())
	c := Default()
	c.Storage.DataDir = o.DataDir
	if env["BOTHY_DEPLOYMENT"] == "container" {
		c.Server.Listen = "0.0.0.0:8080"
	}
	if o.Defaults != nil {
		o.Defaults(c)
	}
	applyEnv(c, env)
	return &Manager{cur: c, watchers: map[int]func(old, cur *Config){}}, nil
}

func (m *Manager) Current() *Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur
}

func (m *Manager) Watch(fn func(old, cur *Config)) (cancel func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	m.watchers[id] = fn
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.watchers, id)
	}
}

func (m *Manager) Effective() Effective { return Effective{} }

// Set validates every change against the fully merged result, rejects locked and
// secret-literal values, writes bothy.ui.yaml atomically (temp, fsync, rename,
// 0600), calls OnSet per path, then notifies watchers.
func (m *Manager) Set(ctx context.Context, changes map[string]json.RawMessage, by string) (restart []string, err error) {
	return nil, errNotImplemented
}

func (m *Manager) Reset(ctx context.Context, path, by string) error { return errNotImplemented }

// Schema returns a JSON Schema (draft 2020-12) generated from Config tags, for
// UI forms.
func (m *Manager) Schema() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`)
}

// SetOnSet installs the audit hook after the auditor exists. The skeleton base
// never writes the UI layer, so there is nothing to audit yet.
func (m *Manager) SetOnSet(fn func(ChangeRecord)) {}

// Reload re-reads bothy.yaml (polled every 5s by mtime and on SIGHUP).
func (m *Manager) Reload(ctx context.Context) error { return nil }

// Default returns the built-in defaults (the values in SPEC §7.1).
func Default() *Config { return defaults() }

// Diff returns the dotted leaf paths whose values differ (for config.changed).
func Diff(old, cur *Config) []string { return nil }

// Dirs are the resolved storage paths. storage.work_dir "" and vault.keyfile ""
// mean auto (SPEC §2.1: /work or /keys/kek when that directory is a mount point
// per /proc/self/mountinfo, else under data_dir). cmd/bothy and the vault use
// ResolveDirs; nothing else interprets the raw values.
type Dirs struct {
	Data, Work, Run, Keyfile string
	KeyfileColocated         bool // the KEK file lives on the same volume as the DB
}

func ResolveDirs(c *Config) Dirs { return resolveDirs(c, isMountPoint) }

// Static wraps a fixed Config as a View for tests.
func Static(c *Config) View { return staticView{c} }

type staticView struct{ c *Config }

func (s staticView) Current() *Config                             { return s.c }
func (s staticView) Watch(func(old, cur *Config)) (cancel func()) { return func() {} }
