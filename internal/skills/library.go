package skills

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/skills/treehash"
)

const (
	maxReadBytes = 256 << 10 // SKILL.md, Load and ReadFile cap
	maxListFiles = 200
)

var (
	errNotEnabled = errors.New("skill is not enabled")
	errChanged    = errors.New("skill files changed since approval; an admin must re-enable it")
)

// estimateTokens matches the provider estimator's bytes/3.6 heuristic.
func estimateTokens(s string) int { return (len(s)*10 + 35) / 36 }

func (l *Library) skillDir(name string) string { return filepath.Join(l.dir, name) }

// Index returns the enabled skills whose files still hash to the approved
// tree, sorted by name and fitted into skills.index_budget_tokens: with every
// description when that fits, otherwise names only (Description emptied),
// keeping as many names as the budget holds. A skill found drifted here is
// marked changed and left out.
func (l *Library) Index(ctx context.Context) []core.SkillMeta {
	rows, err := l.store.Skills().List(ctx)
	if err != nil {
		l.log.Error("list skills", "err", err)
		return nil
	}
	var out []core.SkillMeta
	for _, s := range rows {
		if s.Status != core.SkillEnabled || s.Hash != s.ApprovedHash {
			continue
		}
		if err := l.verify(ctx, s); err != nil {
			l.log.Warn("skill excluded from index", "skill", s.Name, "err", err)
			continue
		}
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b core.SkillMeta) int { return cmp.Compare(a.Name, b.Name) })
	return fitBudget(out, l.cfg.Current().Skills.IndexBudgetTokens)
}

func fitBudget(skills []core.SkillMeta, budget int) []core.SkillMeta {
	full := 0
	for _, s := range skills {
		full += estimateTokens("- " + s.Name + ": " + s.Description + "\n")
	}
	if full <= budget {
		return skills
	}
	used := 0
	out := skills[:0]
	for _, s := range skills {
		t := estimateTokens("- " + s.Name + "\n")
		if used+t > budget {
			break
		}
		used += t
		s.Description = ""
		out = append(out, s)
	}
	return out
}

// verify re-hashes an enabled skill's directory; drift marks it changed.
func (l *Library) verify(ctx context.Context, s *core.SkillMeta) error {
	sum, err := treehash.Sum(l.skillDir(s.Name))
	if err != nil {
		return fmt.Errorf("hash %s: %w", s.Name, err)
	}
	if sum != s.ApprovedHash {
		l.markChanged(ctx, s.Name, sum)
		return errChanged
	}
	return nil
}

// Load returns SKILL.md without its frontmatter plus the other files in the
// skill, for any installed skill: admins review pending and changed skills
// through it. Model-facing tools go through loadEnabled instead.
func (l *Library) Load(ctx context.Context, name string) (string, []string, error) {
	root, err := l.openSkill(name)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	data, err := readCapped(root, skillFile)
	if err != nil {
		return "", nil, err
	}
	_, body, err := parseSkill(data)
	if err != nil {
		return "", nil, fmt.Errorf("skill %s: %w: %w", name, core.ErrInvalid, err)
	}
	files, err := listFiles(root)
	if err != nil {
		return "", nil, fmt.Errorf("skill %s: %w", name, err)
	}
	return body, files, nil
}

// ReadFile reads one file of an installed skill. rel must be a clean relative
// path; ".." segments, absolute paths and any symlinked component are refused,
// and os.Root confines the read to the skill directory regardless.
func (l *Library) ReadFile(ctx context.Context, name, rel string) ([]byte, error) {
	root, err := l.openSkill(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readCapped(root, rel)
}

// loadEnabled is Load restricted to enabled skills whose files still match the
// approved hash.
func (l *Library) loadEnabled(ctx context.Context, name string) (string, []string, error) {
	if err := l.checkEnabled(ctx, name); err != nil {
		return "", nil, err
	}
	return l.Load(ctx, name)
}

func (l *Library) readEnabled(ctx context.Context, name, rel string) ([]byte, error) {
	if err := l.checkEnabled(ctx, name); err != nil {
		return nil, err
	}
	return l.ReadFile(ctx, name, rel)
}

func (l *Library) checkEnabled(ctx context.Context, name string) error {
	if !validName(name) {
		return fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
	}
	s, err := l.store.Skills().Get(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case s.Status == core.SkillChanged:
		return fmt.Errorf("skill %s: %w", name, errChanged)
	case s.Status != core.SkillEnabled || s.Hash != s.ApprovedHash:
		return fmt.Errorf("skill %s: %w", name, errNotEnabled)
	}
	return l.verify(ctx, s)
}

func (l *Library) openSkill(name string) (*os.Root, error) {
	if !validName(name) {
		return nil, fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
	}
	dir := l.skillDir(name)
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("skill %s: %w", name, core.ErrNotFound)
	case err != nil:
		return nil, err
	case !fi.IsDir():
		return nil, fmt.Errorf("skill %s is not a directory: %w", name, core.ErrNotFound)
	}
	return os.OpenRoot(dir)
}

// cleanRel validates a model- or user-supplied path inside a skill.
func cleanRel(rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, '\\') || strings.ContainsRune(rel, 0) || path.IsAbs(rel) {
		return "", fmt.Errorf("path %q: %w: use a relative path inside the skill", rel, core.ErrInvalid)
	}
	if c := path.Clean(rel); c != rel || !fs.ValidPath(c) || c == "." {
		return "", fmt.Errorf("path %q: %w: use a clean relative path without .. segments", rel, core.ErrInvalid)
	}
	return rel, nil
}

// readCapped refuses symlinks anywhere in rel (os.Root would follow ones that
// stay inside the skill; skills have no business containing any), non-regular
// files and files over maxReadBytes.
func readCapped(root *os.Root, rel string) ([]byte, error) {
	rel, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		fi, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", rel, core.ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s: %w: symlinks are not followed", rel, core.ErrForbidden)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return nil, fmt.Errorf("%s: %w", rel, core.ErrNotFound)
		}
		if i == len(parts)-1 {
			if !fi.Mode().IsRegular() {
				return nil, fmt.Errorf("%s: %w: not a regular file", rel, core.ErrInvalid)
			}
			if fi.Size() > maxReadBytes {
				return nil, fmt.Errorf("%s: %w: %d bytes is over the %d KB limit", rel, core.ErrInvalid, fi.Size(), maxReadBytes>>10)
			}
		}
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReadBytes {
		return nil, fmt.Errorf("%s: %w: over the %d KB limit", rel, core.ErrInvalid, maxReadBytes>>10)
	}
	return data, nil
}

// listFiles returns the regular files of a skill other than SKILL.md, sorted,
// at most maxListFiles of them. Symlinks are skipped, matching readCapped.
func listFiles(root *os.Root) ([]string, error) {
	var out []string
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && p != skillFile {
			out = append(out, p)
			if len(out) == maxListFiles {
				return fs.SkipAll
			}
		}
		return nil
	})
	return out, err
}

// List returns every known skill, any status, sorted by name.
func (l *Library) List(ctx context.Context) ([]*core.SkillMeta, error) {
	rows, err := l.store.Skills().List(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b *core.SkillMeta) int { return cmp.Compare(a.Name, b.Name) })
	return rows, nil
}

// SetEnabled approves the skill's current files (on) or disables it (off).
// Enabling re-hashes the tree and pins that hash as approved.
func (l *Library) SetEnabled(ctx context.Context, name string, on bool, by core.UserID) error {
	if !validName(name) {
		return fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s, err := l.store.Skills().Get(ctx, name)
	if err != nil {
		return err
	}
	now := l.clock.Now()
	action := "skill.disable"
	if on {
		root, err := l.openSkill(name)
		if err != nil {
			return err
		}
		defer root.Close()
		data, err := readCapped(root, skillFile)
		if err != nil {
			return err
		}
		fm, body, err := parseSkill(data)
		if err != nil {
			return fmt.Errorf("skill %s: %w: %w", name, core.ErrInvalid, err)
		}
		sum, err := treehash.SumRoot(root)
		if err != nil {
			return fmt.Errorf("skill %s: %w", name, err)
		}
		s.Status, s.Hash, s.ApprovedHash, s.ApprovedBy = core.SkillEnabled, sum, sum, by
		s.Description, s.Tokens = fm.Description, estimateTokens(body)
		action = "skill.enable"
	} else {
		s.Status = core.SkillDisabled
	}
	s.UpdatedAt = now
	if err := l.store.Skills().Upsert(ctx, s); err != nil {
		return err
	}
	l.record(ctx, "user:"+string(by), action, name, map[string]any{"hash": s.Hash})
	return l.refreshLocked(ctx)
}

// Remove deletes the skill's directory and row.
func (l *Library) Remove(ctx context.Context, name string, by core.UserID) error {
	if !validName(name) {
		return fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, getErr := l.store.Skills().Get(ctx, name)
	if getErr != nil && !errors.Is(getErr, core.ErrNotFound) {
		return getErr
	}
	dir := l.skillDir(name)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) && getErr != nil {
		return fmt.Errorf("skill %s: %w", name, core.ErrNotFound)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("skill %s: %w", name, err)
	}
	if getErr == nil {
		if err := l.store.Skills().Delete(ctx, name); err != nil {
			return err
		}
	}
	l.record(ctx, "user:"+string(by), "skill.disable", name, map[string]any{"removed": true})
	return l.refreshLocked(ctx)
}

// Rescan reconciles the skills table with <dir>/*/SKILL.md: new directories
// become pending skills, enabled skills whose tree no longer matches the
// approved hash become changed (audited, banner raised) and rows whose
// directory is gone are dropped. Hidden directories (in-progress installs)
// and symlinked directories are ignored.
func (l *Library) Rescan(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return fmt.Errorf("skills: read %s: %w", l.dir, err)
	}
	rows, err := l.store.Skills().List(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]*core.SkillMeta, len(rows))
	for _, s := range rows {
		known[s.Name] = s
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.IsDir() {
			continue
		}
		if !validName(name) {
			l.log.Warn("ignoring skill directory with an invalid name", "dir", name)
			continue
		}
		seen[name] = true
		if err := l.scanOne(ctx, name, known[name]); err != nil {
			l.log.Warn("skill scan", "skill", name, "err", err)
		}
	}
	for name := range known {
		if !seen[name] {
			if err := l.store.Skills().Delete(ctx, name); err != nil {
				return err
			}
			l.log.Info("skill directory removed; dropped its row", "skill", name)
		}
	}
	return l.refreshLocked(ctx)
}

func (l *Library) scanOne(ctx context.Context, name string, row *core.SkillMeta) error {
	root, err := os.OpenRoot(l.skillDir(name))
	if err != nil {
		return err
	}
	defer root.Close()
	sum, err := treehash.SumRoot(root)
	if err != nil {
		return err
	}
	data, readErr := readCapped(root, skillFile)
	var fm frontmatter
	var body string
	parseErr := readErr
	if readErr == nil {
		fm, body, parseErr = parseSkill(data)
	}
	if parseErr == nil && fm.Name != name {
		parseErr = fmt.Errorf("SKILL.md name %q does not match its directory %q", fm.Name, name)
	}
	now := l.clock.Now()
	if row == nil {
		if parseErr != nil {
			return parseErr
		}
		return l.store.Skills().Upsert(ctx, &core.SkillMeta{
			Name: name, Description: fm.Description, Path: "skills/" + name, Hash: sum,
			Status: core.SkillPending, Tokens: estimateTokens(body), InstalledAt: now, UpdatedAt: now,
		})
	}
	if row.Hash == sum && (row.Status != core.SkillEnabled || sum == row.ApprovedHash) {
		return nil
	}
	if parseErr == nil {
		row.Description, row.Tokens = fm.Description, estimateTokens(body)
	}
	row.Hash, row.UpdatedAt = sum, now
	if row.Status == core.SkillEnabled && sum != row.ApprovedHash {
		row.Status = core.SkillChanged
		l.record(ctx, "system", "skill.changed", name, map[string]any{"hash": sum, "approved_hash": row.ApprovedHash})
	}
	return l.store.Skills().Upsert(ctx, row)
}

// markChanged moves an enabled skill to changed after a drifted hash was seen
// outside a rescan.
func (l *Library) markChanged(ctx context.Context, name, sum string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, err := l.store.Skills().Get(ctx, name)
	if err != nil || s.Status != core.SkillEnabled {
		return
	}
	s.Status, s.Hash, s.UpdatedAt = core.SkillChanged, sum, l.clock.Now()
	if err := l.store.Skills().Upsert(ctx, s); err != nil {
		l.log.Error("mark skill changed", "skill", name, "err", err)
		return
	}
	l.record(ctx, "system", "skill.changed", name, map[string]any{"hash": sum, "approved_hash": s.ApprovedHash})
	if err := l.refreshLocked(ctx); err != nil {
		l.log.Error("refresh skills", "err", err)
	}
}

// refreshLocked recounts enabled skills and raises or clears the
// skills_changed banner. Callers hold l.mu.
func (l *Library) refreshLocked(ctx context.Context) error {
	rows, err := l.store.Skills().List(ctx)
	if err != nil {
		return err
	}
	var enabled int64
	var changed []string
	for _, s := range rows {
		switch {
		case s.Status == core.SkillEnabled && s.Hash == s.ApprovedHash:
			enabled++
		case s.Status == core.SkillChanged:
			changed = append(changed, s.Name)
		}
	}
	l.enabled.Store(enabled)
	slices.Sort(changed)
	text := ""
	if len(changed) > 0 {
		text = fmt.Sprintf("Skill files changed since approval: %s. They are excluded until an admin re-enables them.", strings.Join(changed, ", "))
	}
	if text == l.banner || l.bus == nil {
		l.banner = text
		return nil
	}
	l.banner = text
	l.bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
		Level: "warn", Code: "skills_changed", Text: text, Action: "/settings/skills", Clear: text == "",
	}})
	return nil
}

func (l *Library) record(ctx context.Context, actor, action, name string, detail map[string]any) {
	if l.audit == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		l.log.Error("audit detail", "err", err)
		return
	}
	if err := l.audit.Record(ctx, core.AuditEntry{At: l.clock.Now(), Actor: actor, Action: action,
		Target: "skill:" + name, Outcome: "ok", Detail: raw}); err != nil {
		l.log.Error("audit", "action", action, "err", err)
	}
}
