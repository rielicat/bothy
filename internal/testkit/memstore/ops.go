package memstore

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func normAudit(e core.AuditEntry) core.AuditEntry {
	e.At = ms(e.At)
	e.Detail = bytes.Clone(e.Detail)
	return e
}

type auditRepo struct{ s *store }

// Append always assigns the next id, ignoring e.ID, so existing rows can never
// be overwritten. An empty Detail is stored as the column default "{}".
func (r auditRepo) Append(_ context.Context, e *core.AuditEntry, prevHash, hash []byte) (int64, error) {
	st, unlock := r.s.begin()
	defer unlock()
	row := normAudit(*e)
	if len(row.Detail) == 0 {
		row.Detail = json.RawMessage("{}")
	}
	switch {
	case !oneOf(row.Outcome, "ok", "denied", "error"):
		return 0, invalid("audit_log.outcome")
	case !json.Valid(row.Detail):
		return 0, invalid("audit_log.detail")
	}
	st.auditID++
	row.ID = st.auditID
	st.audit = append(st.audit, auditRow{e: row, prev: bytes.Clone(prevHash), hash: bytes.Clone(hash)})
	e.ID = row.ID
	return row.ID, nil
}

func (r auditRepo) Head(context.Context) (int64, []byte, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if len(st.audit) == 0 {
		return 0, nil, nil
	}
	last := st.audit[len(st.audit)-1]
	return last.e.ID, bytes.Clone(last.hash), nil
}

// Range visits ids in [fromID, toID] (toID <= 0: to the end) in order. fn runs
// after the lock is released so it may call back into the store.
func (r auditRepo) Range(_ context.Context, fromID, toID int64, fn func(e *core.AuditEntry, prevHash, hash []byte) error) error {
	st, unlock := r.s.begin()
	var rows []auditRow
	for _, row := range st.audit {
		if row.e.ID >= fromID && (toID <= 0 || row.e.ID <= toID) {
			rows = append(rows, row)
		}
	}
	unlock()
	for _, row := range rows {
		e := normAudit(row.e)
		if err := fn(&e, bytes.Clone(row.prev), bytes.Clone(row.hash)); err != nil {
			return err
		}
	}
	return nil
}

func (r auditRepo) Query(_ context.Context, f core.AuditFilter) ([]*core.AuditEntry, error) {
	st, unlock := r.s.begin()
	defer unlock()
	limit := pageLimit(f.Page.Limit)
	var out []core.AuditEntry
	for i := len(st.audit) - 1; i >= 0 && len(out) < limit; i-- {
		e := st.audit[i].e
		switch {
		case f.Page.Before > 0 && e.ID >= f.Page.Before,
			f.Actor != "" && e.Actor != f.Actor,
			!strings.HasPrefix(e.Action, f.ActionPrefix),
			!f.From.IsZero() && e.At.Before(f.From),
			!f.To.IsZero() && !e.At.Before(f.To):
			continue
		}
		out = append(out, normAudit(e))
	}
	return ptrs(out), nil
}

func normEgress(e core.EgressRecord) core.EgressRecord {
	e.At = ms(e.At)
	e.Duration = msDur(e.Duration)
	return e
}

// visibleEgress applies Viewer.Admin's egress exception: admins see rows
// without a principal (the system dialer).
func visibleEgress(v core.Viewer, e core.EgressRecord) bool {
	if e.Principal == "" {
		return v.System || v.Admin
	}
	return v.Can(e.Principal)
}

type egressRepo struct{ s *store }

// Add inserts the whole batch or nothing.
func (r egressRepo) Add(_ context.Context, rs []core.EgressRecord) error {
	st, unlock := r.s.begin()
	defer unlock()
	for _, e := range rs {
		switch {
		case !oneOf(e.Dialer, "box", "public", "system"):
			return invalid("egress_log.dialer")
		case !oneOf(e.Decision, "allow", "deny", "held_allow", "held_deny"):
			return invalid("egress_log.decision")
		}
	}
	for _, e := range rs {
		st.egressID++
		e = normEgress(e)
		e.ID = st.egressID
		st.egress = append(st.egress, e)
	}
	return nil
}

func (r egressRepo) Query(_ context.Context, v core.Viewer, s core.SessionID, p core.Page) ([]*core.EgressRecord, error) {
	st, unlock := r.s.begin()
	defer unlock()
	limit := pageLimit(p.Limit)
	var out []core.EgressRecord
	for i := len(st.egress) - 1; i >= 0 && len(out) < limit; i-- {
		e := st.egress[i]
		if visibleEgress(v, e) && (s == "" || e.Session == s) && (p.Before <= 0 || e.ID < p.Before) {
			out = append(out, e)
		}
	}
	return ptrs(out), nil
}

func (r egressRepo) Prune(_ context.Context, before time.Time) (int64, error) {
	st, unlock := r.s.begin()
	defer unlock()
	n := len(st.egress)
	st.egress = slices.DeleteFunc(st.egress, func(e core.EgressRecord) bool { return e.At.Before(before) })
	return int64(n - len(st.egress)), nil
}

func normMCPPin(p core.MCPPin) (core.MCPPin, error) {
	fm, err := jsonCol("mcp_tool_pins.field_map", p.FieldMap)
	if err != nil {
		return p, err
	}
	p.FieldMap = fm
	p.Definition = bytes.Clone(p.Definition)
	p.FirstSeen = ms(p.FirstSeen)
	p.ApprovedAt = ms(p.ApprovedAt)
	return p, nil
}

type mcpRepo struct{ s *store }

// UpsertPin records an unseen (server, tool, hash) as pending and otherwise
// reports the stored status. A hash that was superseded must be approved
// again, so seeing it again makes it pending. side_effect falls back to the
// column default external_send.
func (r mcpRepo) UpsertPin(_ context.Context, p *core.MCPPin) (core.MCPPinStatus, error) {
	st, unlock := r.s.begin()
	defer unlock()
	k := pinKey{p.Server, p.Tool, p.Hash}
	if row, ok := st.mcpPins[k]; ok {
		if row.p.Status == core.PinSuperseded {
			row.p.Status = core.PinPending
			st.mcpPins[k] = row
		}
		return row.p.Status, nil
	}
	if len(p.Definition) == 0 || !json.Valid(p.Definition) {
		return "", invalid("mcp_tool_pins.definition")
	}
	row, err := normMCPPin(*p)
	if err != nil {
		return "", err
	}
	row.Status = core.PinPending
	row.ApprovedBy, row.ApprovedAt = "", time.Time{}
	if !row.SideEffect.Valid() {
		row.SideEffect = core.SEExternalSend
	}
	st.mcpPins[k] = mcpPinRow{p: row, n: st.next()}
	return core.PinPending, nil
}

// ApprovePins approves approve[tool].Hash for each tool and supersedes the
// tool's previously approved definition. Every hash must exist, or nothing
// changes.
func (r mcpRepo) ApprovePins(_ context.Context, server string, approve map[string]core.MCPPin, by core.UserID, at time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	for tool, a := range approve {
		if _, ok := st.mcpPins[pinKey{server, tool, a.Hash}]; !ok {
			return notFound("mcp pin " + server + "/" + tool)
		}
		if _, err := jsonCol("mcp_tool_pins.field_map", a.FieldMap); err != nil {
			return err
		}
	}
	for tool, a := range approve {
		for k, row := range st.mcpPins {
			if k.server == server && k.tool == tool && k.hash != a.Hash && row.p.Status == core.PinApproved {
				row.p.Status = core.PinSuperseded
				st.mcpPins[k] = row
			}
		}
		k := pinKey{server, tool, a.Hash}
		row := st.mcpPins[k]
		row.p.Status = core.PinApproved
		row.p.SideEffect = a.SideEffect
		if !row.p.SideEffect.Valid() {
			row.p.SideEffect = core.SEExternalSend
		}
		row.p.FieldMap = must(jsonCol("mcp_tool_pins.field_map", a.FieldMap))
		row.p.Private = a.Private
		row.p.ApprovedBy = by
		row.p.ApprovedAt = ms(at)
		st.mcpPins[k] = row
	}
	return nil
}

func (r mcpRepo) Pins(_ context.Context, server string) ([]*core.MCPPin, error) {
	st, unlock := r.s.begin()
	defer unlock()
	latest := map[string]mcpPinRow{}
	for k, row := range st.mcpPins {
		if k.server != server {
			continue
		}
		cur, ok := latest[k.tool]
		if !ok || cmp.Or(row.p.FirstSeen.Compare(cur.p.FirstSeen), cmp.Compare(row.n, cur.n)) > 0 {
			latest[k.tool] = row
		}
	}
	out := make([]core.MCPPin, 0, len(latest))
	for _, row := range latest {
		out = append(out, must(normMCPPin(row.p)))
	}
	slices.SortFunc(out, func(a, b core.MCPPin) int { return cmp.Compare(a.Tool, b.Tool) })
	return ptrs(out), nil
}

func (r mcpRepo) Approved(_ context.Context, server string) (map[string]*core.MCPPin, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := map[string]*core.MCPPin{}
	for k, row := range st.mcpPins {
		if k.server == server && row.p.Status == core.PinApproved {
			p := must(normMCPPin(row.p))
			out[k.tool] = &p
		}
	}
	return out, nil
}

func (r mcpRepo) DisableTool(_ context.Context, server, tool string) error {
	st, unlock := r.s.begin()
	defer unlock()
	found := false
	for k, row := range st.mcpPins {
		if k.server == server && k.tool == tool {
			row.p.Status = core.PinDisabled
			st.mcpPins[k] = row
			found = true
		}
	}
	if !found {
		return notFound("mcp tool " + server + "/" + tool)
	}
	return nil
}

func normInstall(in core.MCPInstall) core.MCPInstall {
	in.Lock = bytes.Clone(in.Lock)
	in.CreatedAt = ms(in.CreatedAt)
	return in
}

func (r mcpRepo) PutInstall(_ context.Context, in *core.MCPInstall) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := normInstall(*in)
	row.InstallID = idOr(row.InstallID, core.PrefixInstall)
	if !oneOf(row.Status, "installing", "ready", "failed", "drifted") {
		return invalid("mcp_installs.status")
	}
	st.installs[row.InstallID] = installRow{in: row, n: st.next()}
	in.InstallID = row.InstallID
	return nil
}

func (r mcpRepo) Install(_ context.Context, server string) (*core.MCPInstall, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var ready, latest *installRow
	newer := func(a installRow, b *installRow) bool {
		return b == nil || cmp.Or(a.in.CreatedAt.Compare(b.in.CreatedAt), cmp.Compare(a.n, b.n)) > 0
	}
	for _, row := range st.installs {
		if row.in.Server != server {
			continue
		}
		if newer(row, latest) {
			latest = &row
		}
		if row.in.Status == "ready" && newer(row, ready) {
			ready = &row
		}
	}
	best := cmp.Or(ready, latest)
	if best == nil {
		return nil, notFound("mcp install " + server)
	}
	out := normInstall(best.in)
	return &out, nil
}

func normSkill(s core.SkillMeta) core.SkillMeta {
	s.InstalledAt = ms(s.InstalledAt)
	s.UpdatedAt = ms(s.UpdatedAt)
	return s
}

type skillRepo struct{ s *store }

func (r skillRepo) Upsert(_ context.Context, s *core.SkillMeta) error {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case s.Name == "":
		return invalid("skills.name")
	case !oneOf(s.Status, core.SkillPending, core.SkillEnabled, core.SkillDisabled, core.SkillChanged):
		return invalid("skills.status")
	}
	st.skills[s.Name] = normSkill(*s)
	return nil
}

func (r skillRepo) Get(_ context.Context, name string) (*core.SkillMeta, error) {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.skills[name]
	if !ok {
		return nil, notFound("skill " + name)
	}
	return &s, nil
}

func (r skillRepo) List(context.Context) ([]*core.SkillMeta, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := make([]core.SkillMeta, 0, len(st.skills))
	for _, s := range st.skills {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b core.SkillMeta) int { return cmp.Compare(a.Name, b.Name) })
	return ptrs(out), nil
}

func (r skillRepo) Delete(_ context.Context, name string) error {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.skills[name]; !ok {
		return notFound("skill " + name)
	}
	delete(st.skills, name)
	delete(st.skillFiles, name)
	return nil
}

func (r skillRepo) SetApproved(_ context.Context, name string, files []core.SkillFile) error {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.skills[name]; !ok {
		return notFound("skill " + name)
	}
	out := make([]core.SkillFile, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Path] {
			return conflict("skill_approved_files primary key")
		}
		seen[f.Path] = true
		if !f.Text {
			f.Content = ""
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b core.SkillFile) int { return cmp.Compare(a.Path, b.Path) })
	st.skillFiles[name] = out
	return nil
}

func (r skillRepo) Approved(_ context.Context, name string) ([]core.SkillFile, error) {
	st, unlock := r.s.begin()
	defer unlock()
	return slices.Clone(st.skillFiles[name]), nil
}

func normMemoryEvent(e core.MemoryEvent) core.MemoryEvent {
	e.At = ms(e.At)
	return e
}

type memoryEventRepo struct{ s *store }

func (r memoryEventRepo) Add(_ context.Context, e *core.MemoryEvent) (int64, error) {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case !oneOf(e.File, core.FileMemory, core.FileUser, core.FileHeartbeat):
		return 0, invalid("memory_events.file")
	case !oneOf(e.Op, "add", "replace", "remove", "edit"):
		return 0, invalid("memory_events.op")
	}
	if _, ok := st.principals[e.Principal]; !ok {
		return 0, invalid("memory_events.principal_id: no such principal")
	}
	row := normMemoryEvent(*e)
	st.memID++
	row.ID = st.memID
	st.memEvents = append(st.memEvents, row)
	e.ID = row.ID
	return row.ID, nil
}

func (r memoryEventRepo) Since(_ context.Context, p core.PrincipalID, afterID int64) ([]*core.MemoryEvent, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.MemoryEvent
	for _, e := range st.memEvents {
		if e.Principal == p && e.ID > afterID {
			out = append(out, e)
		}
	}
	return ptrs(out), nil
}

func (r memoryEventRepo) List(_ context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile, page core.Page) ([]*core.MemoryEvent, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if !v.Can(p) {
		return nil, notFound("principal")
	}
	limit := pageLimit(page.Limit)
	var out []core.MemoryEvent
	for i := len(st.memEvents) - 1; i >= 0 && len(out) < limit; i-- {
		e := st.memEvents[i]
		if e.Principal == p && (f == "" || e.File == f) && (page.Before <= 0 || e.ID < page.Before) {
			out = append(out, e)
		}
	}
	return ptrs(out), nil
}

type teamMemoryEventRepo struct{ s *store }

func (r teamMemoryEventRepo) Add(_ context.Context, e *core.MemoryEvent) (int64, error) {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case e.File != core.FileTeam || e.Principal != "":
		return 0, invalid("team_memory_events: not a TEAM.md change")
	case !oneOf(e.Op, "add", "replace", "remove", "edit"):
		return 0, invalid("team_memory_events.op")
	}
	if e.Approval != "" && slices.ContainsFunc(st.teamEvents, func(x core.MemoryEvent) bool { return x.Approval == e.Approval }) {
		return 0, conflict("team_memory_events.approval_id")
	}
	row := normMemoryEvent(*e)
	st.teamMemID++
	row.ID = st.teamMemID
	st.teamEvents = append(st.teamEvents, row)
	e.ID = row.ID
	return row.ID, nil
}

func (r teamMemoryEventRepo) List(_ context.Context, page core.Page) ([]*core.MemoryEvent, error) {
	st, unlock := r.s.begin()
	defer unlock()
	limit := pageLimit(page.Limit)
	var out []core.MemoryEvent
	for i := len(st.teamEvents) - 1; i >= 0 && len(out) < limit; i-- {
		if e := st.teamEvents[i]; page.Before <= 0 || e.ID < page.Before {
			out = append(out, e)
		}
	}
	return ptrs(out), nil
}

// validSecretName mirrors length(name) BETWEEN 1 AND 64 AND
// name NOT GLOB '*[^a-z0-9_.-]*'.
func validSecretName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for _, c := range []byte(name) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

type vaultRepo struct{ s *store }

func (r vaultRepo) ActiveKey(context.Context) (int64, []byte, []byte, string, error) {
	st, unlock := r.s.begin()
	defer unlock()
	for _, k := range st.vaultKeys {
		if k.active {
			return k.id, bytes.Clone(k.wrapped), bytes.Clone(k.check), k.source, nil
		}
	}
	return 0, nil, nil, "", notFound("vault key")
}

func (r vaultRepo) PutKey(_ context.Context, wrapped, check []byte, source string) (int64, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if wrapped == nil || check == nil {
		return 0, invalid("vault_keys: wrapped_dek and kek_check are required")
	}
	for i := range st.vaultKeys {
		st.vaultKeys[i].active = false
	}
	st.keyID++
	st.vaultKeys = append(st.vaultKeys, vaultKeyRow{
		id: st.keyID, wrapped: bytes.Clone(wrapped), check: bytes.Clone(check),
		source: source, active: true,
	})
	return st.keyID, nil
}

func (st *state) hasKey(id int64) bool {
	return slices.ContainsFunc(st.vaultKeys, func(k vaultKeyRow) bool { return k.id == id })
}

// PutSecret keeps last_used_at of an existing secret; updated_at is the
// store's clock.
func (r vaultRepo) PutSecret(_ context.Context, sc core.Scope, name string, keyID int64, version int, nonce, ct []byte, hint string, _ core.UserID) error {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case !validSecretName(name):
		return invalid("vault_secrets.name")
	case nonce == nil || ct == nil:
		return invalid("vault_secrets: nonce and ciphertext are required")
	case !st.hasKey(keyID):
		return invalid("vault_secrets.key_id: no such key")
	}
	k := secretKey{sc, name}
	row := st.secrets[k]
	row.keyID, row.version, row.nonce, row.ct, row.hint, row.updatedAt = keyID, version, bytes.Clone(nonce), bytes.Clone(ct), hint, ms(time.Now())
	st.secrets[k] = row
	return nil
}

func (r vaultRepo) GetSecret(_ context.Context, sc core.Scope, name string) (int64, int, []byte, []byte, error) {
	st, unlock := r.s.begin()
	defer unlock()
	row, ok := st.secrets[secretKey{sc, name}]
	if !ok {
		return 0, 0, nil, nil, notFound("secret " + name)
	}
	return row.keyID, row.version, bytes.Clone(row.nonce), bytes.Clone(row.ct), nil
}

func (r vaultRepo) DeleteSecret(_ context.Context, sc core.Scope, name string) error {
	st, unlock := r.s.begin()
	defer unlock()
	k := secretKey{sc, name}
	if _, ok := st.secrets[k]; !ok {
		return notFound("secret " + name)
	}
	delete(st.secrets, k)
	return nil
}

func (r vaultRepo) ListSecrets(_ context.Context, sc core.Scope) ([]core.SecretMeta, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.SecretMeta
	for k, row := range st.secrets {
		if k.scope == sc {
			out = append(out, core.SecretMeta{
				Scope: k.scope, Name: k.name, Source: "vault", Hint: row.hint,
				UpdatedAt: row.updatedAt, LastUsed: row.used,
			})
		}
	}
	slices.SortFunc(out, func(a, b core.SecretMeta) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (r vaultRepo) TouchSecret(_ context.Context, sc core.Scope, name string, at time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	k := secretKey{sc, name}
	row, ok := st.secrets[k]
	if !ok {
		return notFound("secret " + name)
	}
	row.used = ms(at)
	st.secrets[k] = row
	return nil
}

// Rewrap holds the lock while fn runs, like the SQLite write transaction, so
// fn must not call back into the store. Any error leaves every secret as it
// was.
func (r vaultRepo) Rewrap(_ context.Context, fn func(sc core.Scope, name string, keyID int64, version int, nonce, ct []byte) (int64, []byte, []byte, error)) error {
	st, unlock := r.s.begin()
	defer unlock()
	keys := slices.Collect(maps.Keys(st.secrets))
	slices.SortFunc(keys, func(a, b secretKey) int { return cmp.Or(cmp.Compare(a.scope, b.scope), cmp.Compare(a.name, b.name)) })
	next := make(map[secretKey]secretRow, len(keys))
	for _, k := range keys {
		row := st.secrets[k]
		keyID, nonce, ct, err := fn(k.scope, k.name, row.keyID, row.version, bytes.Clone(row.nonce), bytes.Clone(row.ct))
		if err != nil {
			return err
		}
		switch {
		case !st.hasKey(keyID):
			return invalid("vault_secrets.key_id: no such key")
		case nonce == nil || ct == nil:
			return invalid("vault_secrets: nonce and ciphertext are required")
		}
		row.keyID, row.nonce, row.ct = keyID, bytes.Clone(nonce), bytes.Clone(ct)
		next[k] = row
	}
	for k, row := range next {
		st.secrets[k] = row
	}
	return nil
}

type dedupRepo struct{ s *store }

func (r dedupRepo) Seen(_ context.Context, channel, msgID string, at time.Time) (bool, error) {
	st, unlock := r.s.begin()
	defer unlock()
	k := dedupKey{channel, msgID}
	if _, ok := st.dedup[k]; ok {
		return true, nil
	}
	st.dedup[k] = ms(at)
	return false, nil
}

func (r dedupRepo) Prune(_ context.Context, before time.Time) (int64, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var n int64
	for k, at := range st.dedup {
		if at.Before(before) {
			delete(st.dedup, k)
			n++
		}
	}
	return n, nil
}
