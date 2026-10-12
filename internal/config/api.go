// Package config loads the layered configuration: defaults < bothy.yaml < env <
// bothy.ui.yaml. It is fully implemented in the skeleton and frozen.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"go.yaml.in/yaml/v3"
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

func (e FieldError) Error() string { return e.Message }

// FieldErrors is the error Set and Reset return when a change is rejected;
// the HTTP layer answers 400 with the list. It matches core.ErrInvalid.
type FieldErrors []FieldError

func (e FieldErrors) Error() string {
	msgs := make([]string, len(e))
	for i, fe := range e {
		msgs[i] = fe.Message
	}
	return strings.Join(msgs, "; ")
}

func (e FieldErrors) Is(target error) bool { return target == core.ErrInvalid }

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
type Manager struct {
	file     string
	required bool // $BOTHY_CONFIG names a file that must exist
	uiPath   string
	env      map[string]string
	locks    []string
	def      *layer
	schema   json.RawMessage
	safeMode bool
	boot     *Config
	now      func() time.Time
	poll     time.Duration

	// writeMu serialises Set, Reset and Reload, watcher calls included, so
	// watchers observe changes in order.
	writeMu sync.Mutex
	st      atomic.Pointer[state]
	onSet   atomic.Pointer[func(ChangeRecord)]

	wmu      sync.Mutex
	watchers []watcher
	nextID   int
}

type watcher struct {
	id int
	fn func(old, cur *Config)
}

// state is an immutable snapshot swapped atomically on every change.
type state struct {
	cfg       *Config
	eff       Effective
	issues    []issue
	errIDs    map[string]bool
	file      *layer
	fileErr   *issue
	ui        *layer
	uiErr     *issue
	fileStamp stamp
	uiStamp   stamp
}

// Load never fails on a bad file: YAML parse errors put the manager in safe
// mode (defaults + env + ui) with Errors set. It fails only on unreadable data dir.
//
// Load also polls both config files every 5s and reloads on SIGHUP until ctx
// is done.
func Load(ctx context.Context, o Options) (*Manager, error) {
	if o.DataDir == "" {
		o.DataDir = "/data"
	}
	if err := checkDataDir(o.DataDir); err != nil {
		return nil, fmt.Errorf("config: data dir %s: %w", o.DataDir, err)
	}
	var env []string
	if o.Env != nil {
		env = o.Env()
	}
	m := &Manager{
		file:     o.File,
		required: o.File != "",
		uiPath:   filepath.Join(o.DataDir, "bothy.ui.yaml"),
		env:      envMap(env),
		now:      time.Now,
		poll:     pollInterval,
	}
	if m.file == "" {
		m.file = filepath.Join(o.DataDir, "bothy.yaml")
	}
	m.locks = parseLocks(m.env["BOTHY_CONFIG_LOCK"])
	if o.OnSet != nil {
		m.SetOnSet(o.OnSet)
	}
	def := Default()
	def.Storage.DataDir = o.DataDir
	if m.env["BOTHY_DEPLOYMENT"] == "container" {
		def.Server.Listen = "0.0.0.0:8080"
	}
	if o.Defaults != nil {
		o.Defaults(def)
	}
	m.def = configLayer(def)
	m.schema = buildSchema(def)

	fileStamp, uiStamp := statStamp(m.file), statStamp(m.uiPath)
	file, fileErr := readLayerFile(m.file, LayerFile, m.required)
	m.safeMode = fileErr != nil
	ui, uiErr := readLayerFile(m.uiPath, LayerUI, false)
	st := m.build(file, fileErr, ui, uiErr)
	st.fileStamp, st.uiStamp = fileStamp, uiStamp
	m.boot = st.cfg
	m.st.Store(st)
	go m.watchFiles(ctx)
	return m, nil
}

func checkDataDir(dir string) error {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("not a directory")
	}
	return nil
}

func (m *Manager) isLocked(segs []string, f *field) bool {
	return f.locked || globLocked(m.locks, segs)
}

func (m *Manager) build(file *layer, fileErr *issue, ui *layer, uiErr *issue) *state {
	env := envLayer(m.env, m.def, file)
	mg := newMerger([]*layer{m.def, file, env, ui}, m.isLocked)
	cfg := mg.run()
	st := &state{cfg: cfg, file: file, fileErr: fileErr, ui: ui, uiErr: uiErr, errIDs: map[string]bool{}}
	var issues []issue
	for _, is := range []*issue{fileErr, uiErr} {
		if is != nil {
			issues = append(issues, *is)
		}
	}
	for _, l := range []*layer{file, env, ui} {
		issues = append(issues, l.errs...)
	}
	issues = append(issues, mg.errs...)
	errs := make([]FieldError, 0, len(issues))
	for _, is := range issues {
		if !st.errIDs[is.id] {
			st.errIDs[is.id] = true
			st.issues = append(st.issues, is)
			errs = append(errs, is.FieldError)
		}
	}
	st.eff = Effective{Entries: mg.entries(cfg), Errors: errs, SafeMode: m.safeMode, Restart: []string{}}
	if m.boot != nil {
		st.eff.Restart = restartOnly(Diff(m.boot, cfg))
	}
	return st
}

func (m *Manager) Current() *Config { return m.st.Load().cfg }

func (m *Manager) Watch(fn func(old, cur *Config)) (cancel func()) {
	m.wmu.Lock()
	id := m.nextID
	m.nextID++
	m.watchers = append(m.watchers, watcher{id: id, fn: fn})
	m.wmu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.wmu.Lock()
			m.watchers = slices.DeleteFunc(m.watchers, func(w watcher) bool { return w.id == id })
			m.wmu.Unlock()
		})
	}
}

func (m *Manager) notify(old, cur *Config) {
	m.wmu.Lock()
	ws := slices.Clone(m.watchers)
	m.wmu.Unlock()
	for _, w := range ws {
		w.fn(old, cur)
	}
}

func (m *Manager) Effective() Effective {
	e := m.st.Load().eff
	e.Entries = slices.Clone(e.Entries)
	e.Errors = slices.Clone(e.Errors)
	e.Restart = slices.Clone(e.Restart)
	return e
}

// Set validates every change against the fully merged result, rejects locked and
// secret-literal values, writes bothy.ui.yaml atomically (temp, fsync, rename,
// 0600), calls OnSet per path, then notifies watchers.
//
// Setting a path replaces the UI layer's value there (an item path such as
// "providers.<id>" creates or replaces the UI item); JSON null removes the UI
// override and is audited as a reset. A rejected change returns FieldErrors
// and writes nothing.
func (m *Manager) Set(ctx context.Context, changes map[string]json.RawMessage, by string) (restart []string, err error) {
	if len(changes) == 0 {
		return nil, nil
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	st := m.st.Load()
	if st.uiErr != nil {
		return nil, fmt.Errorf("config: fix or remove %s first: %s", m.uiPath, st.uiErr.Message)
	}
	src := Source{Layer: LayerUI, File: m.uiPath, By: by, At: m.now().UTC().Truncate(time.Second)}
	ui := st.ui.clone()
	ui.errs = nil
	var bad FieldErrors
	type change struct {
		segs   []string
		remove bool
	}
	var applied []change
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		t, err := resolvePath(p)
		if err != nil {
			bad = append(bad, newIssue(p, "", src, err.Error()).FieldError)
			continue
		}
		n, err := jsonNode(changes[p])
		if err != nil {
			bad = append(bad, newIssue(p, "", src, "invalid JSON: "+err.Error()).FieldError)
			continue
		}
		ui.removeUnder(t.segs)
		if isNull(n) {
			applied = append(applied, change{segs: t.segs, remove: true})
			continue
		}
		if m.isLocked(t.segs, t.f) {
			bad = append(bad, newIssue(p, "", src, "locked: set it in bothy.yaml or the environment").FieldError)
			continue
		}
		sub := newLayer(LayerUI)
		w := walker{l: sub, src: constSource(src)}
		w.target(t, n)
		for _, is := range sub.errs {
			bad = append(bad, is.FieldError)
		}
		for _, k := range sub.keys {
			lf := sub.leaves[k]
			if msg := m.checkUILeaf(lf); msg != "" {
				bad = append(bad, newIssue(k, "", src, msg).FieldError)
				continue
			}
			ui.put(lf)
		}
		for _, c := range sub.items {
			for _, k := range c.keys {
				ui.addChild(c.segs, k)
			}
		}
		applied = append(applied, change{segs: t.segs})
	}
	if len(bad) > 0 {
		return nil, bad
	}
	if bad = newErrors(st, m.build(st.file, st.fileErr, ui, nil)); len(bad) > 0 {
		return nil, bad
	}
	next, err := m.commitUI(st, ui)
	if err != nil {
		return nil, err
	}
	for i, p := range paths {
		rec := ChangeRecord{Path: p, Old: valueAt(st.cfg, applied[i].segs), By: by, Action: "set"}
		if applied[i].remove {
			rec.New, rec.Action = json.RawMessage("null"), "reset"
		} else {
			rec.New = valueAt(next.cfg, applied[i].segs)
		}
		m.audit(rec)
	}
	m.notify(st.cfg, next.cfg)
	return restartOnly(Diff(st.cfg, next.cfg)), nil
}

func (m *Manager) checkUILeaf(lf *leaf) string {
	if m.isLocked(lf.segs, lf.f) {
		return "locked: set it in bothy.yaml or the environment"
	}
	if lf.container {
		return ""
	}
	if _, err := leafValue(lf.f, lf.node); err != nil {
		return typeMessage(lf.f, lf.node)
	}
	if lf.f.secret {
		return secretMessage(lf.segs, lf.f, lf.node)
	}
	return ""
}

// newErrors lists validation errors in next that st did not already have.
func newErrors(st, next *state) FieldErrors {
	var out FieldErrors
	for _, is := range next.issues {
		if !st.errIDs[is.id] {
			out = append(out, is.FieldError)
		}
	}
	return out
}

// Reset deletes path (and everything below it) from bothy.ui.yaml.
func (m *Manager) Reset(ctx context.Context, path, by string) error {
	t, err := resolvePath(path)
	if err != nil {
		return FieldErrors{newIssue(path, "", Source{Layer: LayerUI, File: m.uiPath}, err.Error()).FieldError}
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	st := m.st.Load()
	if st.uiErr != nil {
		return fmt.Errorf("config: fix or remove %s first: %s", m.uiPath, st.uiErr.Message)
	}
	ui := st.ui.clone()
	ui.errs = nil
	if !ui.removeUnder(t.segs) {
		return nil
	}
	next, err := m.commitUI(st, ui)
	if err != nil {
		return err
	}
	m.audit(ChangeRecord{Path: path, Old: valueAt(st.cfg, t.segs), New: json.RawMessage("null"), By: by, Action: "reset"})
	m.notify(st.cfg, next.cfg)
	return nil
}

// commitUI writes ui to bothy.ui.yaml and installs the state built from what
// was written, so provenance lines match the file a later Reload reads.
func (m *Manager) commitUI(st *state, ui *layer) (*state, error) {
	data, err := renderUI(ui)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(m.uiPath, data); err != nil {
		return nil, fmt.Errorf("config: write %s: %w", m.uiPath, err)
	}
	written, uiErr := parseFile(m.uiPath, LayerUI, data)
	next := m.build(st.file, st.fileErr, written, uiErr)
	next.fileStamp, next.uiStamp = st.fileStamp, statStamp(m.uiPath)
	m.st.Store(next)
	return next, nil
}

func (m *Manager) audit(r ChangeRecord) {
	if fn := m.onSet.Load(); fn != nil && *fn != nil {
		(*fn)(r)
	}
}

// Schema returns a JSON Schema (draft 2020-12) generated from Config tags, for
// UI forms.
func (m *Manager) Schema() json.RawMessage { return slices.Clone(m.schema) }

// SetOnSet installs the audit hook after the auditor exists.
func (m *Manager) SetOnSet(fn func(ChangeRecord)) { m.onSet.Store(&fn) }

// Reload re-reads bothy.yaml (polled every 5s by mtime and on SIGHUP).
//
// bothy.ui.yaml is re-read too. A file that no longer parses keeps its last
// good contents, adds an error and makes Reload return it; safe mode, once
// entered at boot, lasts until a restart. Watchers run when the merged config
// or its errors changed.
func (m *Manager) Reload(ctx context.Context) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	st := m.st.Load()
	fileStamp, uiStamp := statStamp(m.file), statStamp(m.uiPath)
	file, fileErr := readLayerFile(m.file, LayerFile, m.required)
	if fileErr != nil {
		file = st.file
	}
	ui, uiErr := readLayerFile(m.uiPath, LayerUI, false)
	if uiErr != nil {
		ui = st.ui
	}
	next := m.build(file, fileErr, ui, uiErr)
	next.fileStamp, next.uiStamp = fileStamp, uiStamp
	m.st.Store(next)
	if !reflect.DeepEqual(st.cfg, next.cfg) || !maps.Equal(st.errIDs, next.errIDs) {
		m.notify(st.cfg, next.cfg)
	}
	for _, is := range []*issue{fileErr, uiErr} {
		if is != nil {
			return errors.New("config: " + is.Message)
		}
	}
	return nil
}

// Default returns the built-in defaults (the values in SPEC §7.1).
func Default() *Config { return defaults() }

// Diff returns the dotted leaf paths whose values differ (for config.changed).
func Diff(old, cur *Config) []string {
	if old == nil {
		old = &Config{}
	}
	if cur == nil {
		cur = &Config{}
	}
	a, b := configLayer(old), configLayer(cur)
	var out []string
	for _, k := range a.keys {
		lb, ok := b.leaves[k]
		la := a.leaves[k]
		if !ok || !bytes.Equal(leafJSON(la.f, la.node), leafJSON(lb.f, lb.node)) {
			out = append(out, k)
		}
	}
	for _, k := range b.keys {
		if _, ok := a.leaves[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func restartOnly(paths []string) []string {
	out := []string{}
	for _, p := range paths {
		if t, err := resolvePath(p); err == nil && !t.f.hot {
			out = append(out, p)
		}
	}
	return out
}

func jsonNode(raw json.RawMessage) (*yaml.Node, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data")
	}
	return anyNode(v), nil
}

func anyNode(v any) *yaml.Node {
	switch x := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(x)}
	case json.Number:
		tag := "!!float"
		if _, err := x.Int64(); err == nil {
			tag = "!!int"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: x.String()}
	case string:
		return strNode(x)
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range x {
			n.Content = append(n.Content, anyNode(e))
		}
		return n
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			n.Content = append(n.Content, strNode(k), anyNode(x[k]))
		}
		return n
	}
	return strNode(fmt.Sprint(v))
}

// Dirs are the resolved storage paths. storage.work_dir "" and vault.keyfile ""
// mean auto (SPEC §2.1: /work or /keys/kek when that directory is a mount point
// per /proc/self/mountinfo, else under data_dir). cmd/bothy and the vault use
// ResolveDirs; nothing else interprets the raw values.
type Dirs struct {
	Data, Work, Run, Keyfile string
	KeyfileColocated         bool // the KEK file lives on the same volume as the DB
}

func ResolveDirs(c *Config) Dirs { return resolveDirs(c, readMounts()) }

// Static wraps a fixed Config as a View for tests.
func Static(c *Config) View { return staticView{c} }

type staticView struct{ c *Config }

func (s staticView) Current() *Config                             { return s.c }
func (s staticView) Watch(func(old, cur *Config)) (cancel func()) { return func() {} }
