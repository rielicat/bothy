package httpapi

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const (
	skillMaxFiles   = 200
	installTimeout  = 15 * time.Minute
	composeHint     = "use the compose deployment, which adds the sandboxd sidecar (README Quickstart: sh install.sh --compose ~/bothy, then docker compose up -d in ~/bothy)"
	maxSecretValue  = 64 << 10
	defaultUsageAge = 30 * 24 * time.Hour
)

var (
	secretNameRE = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
	// wellKnownEnv are the only variables POST /api/vault/import copies (SPEC §7.2).
	wellKnownEnv = []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY",
		"XAI_API_KEY", "DEEPSEEK_API_KEY", "TELEGRAM_BOT_TOKEN"}
)

// ---- skills ----

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if isAdmin(a) {
		all, err := s.d.Skills.List(r.Context())
		if err != nil {
			return err
		}
		return ok(w, nonNil(all))
	}
	return ok(w, nonNil(s.d.Skills.Index(r.Context())))
}

func (s *Server) skillMeta(ctx context.Context, a core.Actor, name string) (*core.SkillMeta, error) {
	if isAdmin(a) {
		all, err := s.d.Skills.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range all {
			if m.Name == name {
				return m, nil
			}
		}
		return nil, errNotFound
	}
	for _, m := range s.d.Skills.Index(ctx) {
		if m.Name == name {
			return &m, nil
		}
	}
	return nil, errNotFound
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	m, err := s.skillMeta(ctx, a, r.PathValue("name"))
	if err != nil {
		return err
	}
	body, files, err := s.d.Skills.Load(ctx, m.Name)
	if err != nil {
		return err
	}
	return ok(w, dto.SkillDetail{Meta: m, Body: body, Files: nonNil(files)})
}

func (s *Server) installSkill(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("expected multipart/form-data with a zip file")
	}
	var data []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return badRequest("malformed multipart body: %v", err)
		}
		if part.FormName() != "file" {
			continue
		}
		data, err = io.ReadAll(io.LimitReader(part, skillZipMax+1))
		if err != nil {
			return err
		}
		break
	}
	switch {
	case data == nil:
		return badRequest("missing file part")
	case len(data) > skillZipMax:
		return newError(http.StatusRequestEntityTooLarge, "bad_request", "skill archives are limited to 5 MB")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return badRequest("not a zip archive")
	}
	if len(zr.File) > skillMaxFiles {
		return badRequest("skill archives are limited to %d files", skillMaxFiles)
	}
	m, err := s.d.Skills.Install(r.Context(), bytes.NewReader(data), int64(len(data)), a.User)
	if err != nil {
		return err
	}
	return ok(w, m)
}

func (s *Server) patchSkill(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SkillPatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	name := r.PathValue("name")
	if err := s.d.Skills.SetEnabled(ctx, name, req.Enabled, req.Hash, a.User); err != nil {
		return err
	}
	m, err := s.skillMeta(ctx, a, name)
	if err != nil {
		return err
	}
	return ok(w, m)
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if err := s.d.Skills.Remove(r.Context(), r.PathValue("name"), a.User); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) rescanSkills(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	ctx := r.Context()
	if err := s.d.Skills.Rescan(ctx); err != nil {
		return err
	}
	all, err := s.d.Skills.List(ctx)
	if err != nil {
		return err
	}
	return ok(w, nonNil(all))
}

// ---- schedules ----

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ss, err := s.d.Store.Schedules().List(r.Context(), a.Viewer)
	if err != nil {
		return err
	}
	return ok(w, nonNil(ss))
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	sc, err := s.d.Store.Schedules().Get(r.Context(), a.Viewer, core.ScheduleID(r.PathValue("id")))
	if err != nil {
		return err
	}
	return ok(w, sc)
}

// scheduleGrants validates pre-authorized grants and binds them to the
// schedule: scope schedule, narrow only, never admin or financial.
// errGrantsCookieOnly: pre-authorized grants answer approvals in advance, so
// like approvals they need a browser session; token runs follow on_approval.
var errGrantsCookieOnly = newError(http.StatusForbidden, "forbidden", "pre-authorized grants need a signed-in browser session, not an API token")

func scheduleGrants(req []core.Grant, sc *core.Schedule, by core.UserID, now time.Time) ([]core.Grant, error) {
	out := make([]core.Grant, 0, len(req))
	for _, g := range req {
		g.ID, g.Principal, g.Scope, g.ScopeID = "", sc.Principal, core.GrantSchedule, string(sc.ID)
		g.CreatedBy, g.CreatedAt, g.Uses = by, now, 0
		if err := validGrant(g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.ScheduleUpsertReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if a.Via == "token" && len(req.Grants) > 0 {
		return errGrantsCookieOnly
	}
	ctx := r.Context()
	sc := req.Schedule
	sc.ID, sc.CreatedBy, sc.Failures = "", "user:"+string(a.User), 0
	if sc.Principal == "" {
		sc.Principal = core.UserPrincipal(a.User)
	}
	if !a.Viewer.Can(sc.Principal) {
		return errNotFound
	}
	if _, err := scheduleGrants(req.Grants, &sc, a.User, s.now()); err != nil {
		return err
	}
	// The scheduler writes the one audit row, with this request's origin.
	if err := s.d.Scheduler.Upsert(withOrigin(r, a, map[string]any{"grants": len(req.Grants)}), a.Viewer, &sc, byString(a)); err != nil {
		return err
	}
	grants, err := scheduleGrants(req.Grants, &sc, a.User, s.now())
	if err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := s.d.Policy.Grant(ctx, g); err != nil {
			// The client sees a failure, so leave no half-made schedule for
			// a retry to duplicate.
			if uerr := s.undoSchedule(context.WithoutCancel(ctx), a, sc.ID); uerr != nil {
				s.log.Error("undo schedule after a failed grant", "schedule", sc.ID, "err", uerr)
			}
			return err
		}
	}
	return s.respondSchedule(w, ctx, a, sc.ID)
}

func (s *Server) respondSchedule(w http.ResponseWriter, ctx context.Context, a core.Actor, id core.ScheduleID) error {
	sc, err := s.d.Store.Schedules().Get(ctx, a.Viewer, id)
	if err != nil {
		return err
	}
	return ok(w, sc)
}

func (s *Server) undoSchedule(ctx context.Context, a core.Actor, id core.ScheduleID) error {
	return errors.Join(s.revokeScheduleGrants(ctx, a, id), s.d.Scheduler.Delete(ctx, a.Viewer, id, a.User))
}

func (s *Server) revokeScheduleGrants(ctx context.Context, a core.Actor, id core.ScheduleID) error {
	gs, err := s.d.Store.Grants().List(ctx, a.Viewer, core.GrantSchedule, string(id))
	if err != nil {
		return err
	}
	for _, g := range gs {
		if err := s.d.Policy.Revoke(ctx, a.Viewer, g.ID, a.User); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (s *Server) patchSchedule(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req dto.ScheduleUpsertReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if a.Via == "token" && len(req.Grants) > 0 {
		return errGrantsCookieOnly
	}
	ctx := r.Context()
	id := core.ScheduleID(r.PathValue("id"))
	cur, err := s.d.Store.Schedules().Get(ctx, a.Viewer, id)
	if errors.Is(err, core.ErrNotFound) {
		if team, ok := s.teamSchedule(ctx, a, id); ok {
			return s.patchTeamSchedule(w, r, a, team, body)
		}
	}
	if err != nil {
		return err
	}
	sc := req.Schedule
	sc.ID, sc.Principal, sc.CreatedBy, sc.CreatedAt = cur.ID, cur.Principal, cur.CreatedBy, cur.CreatedAt
	grants, err := scheduleGrants(req.Grants, &sc, a.User, s.now())
	if err != nil {
		return err
	}
	if err := s.d.Scheduler.Upsert(withOrigin(r, a, map[string]any{"grants": len(grants)}), a.Viewer, &sc, byString(a)); err != nil {
		return err
	}
	if err := s.revokeScheduleGrants(ctx, a, sc.ID); err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := s.d.Policy.Grant(ctx, g); err != nil {
			return err
		}
	}
	return s.respondSchedule(w, ctx, a, sc.ID)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	id := core.ScheduleID(r.PathValue("id"))
	v := a.Viewer
	if _, err := s.d.Store.Schedules().Get(ctx, v, id); err != nil {
		team, ok := s.teamSchedule(ctx, a, id)
		if !errors.Is(err, core.ErrNotFound) || !ok {
			return err
		}
		v = teamScheduleViewer(a, team)
	}
	if err := s.d.Scheduler.Delete(withOrigin(r, a, nil), v, id, a.User); err != nil {
		return err
	}
	if err := s.revokeScheduleGrants(ctx, core.Actor{User: a.User, Viewer: v}, id); err != nil {
		return err
	}
	return noContent(w)
}

// teamScheduleViewer lets an admin act on one team schedule, nothing more.
func teamScheduleViewer(a core.Actor, sc *core.Schedule) core.Viewer {
	return core.Viewer{User: a.User, Admin: true, Principals: []core.PrincipalID{sc.Principal}}
}

// patchTeamSchedule turns a teammate's or a channel's schedule on or off;
// anything else about it is changed by asking Bothy in their chat.
func (s *Server) patchTeamSchedule(w http.ResponseWriter, r *http.Request, a core.Actor, cur *core.Schedule, body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return badRequest("invalid JSON: %v", err)
	}
	var enabled *bool
	for k, raw := range fields {
		if k != "enabled" {
			return badRequest("Only turning it on or off can be changed here.")
		}
		if err := json.Unmarshal(raw, &enabled); err != nil || enabled == nil {
			return badRequest("enabled must be true or false")
		}
	}
	if enabled == nil {
		return badRequest("Only turning it on or off can be changed here.")
	}
	ctx := r.Context()
	v := teamScheduleViewer(a, cur)
	sc := *cur
	sc.Enabled = *enabled
	if err := s.d.Scheduler.Upsert(withOrigin(r, a, nil), v, &sc, byString(a)); err != nil {
		return err
	}
	got, err := s.d.Store.Schedules().Get(ctx, v, sc.ID)
	if err != nil {
		return err
	}
	return ok(w, got)
}

// runNowIn is a scheduler that says which chat a Run now writes in.
type runNowIn interface {
	RunNowIn(ctx context.Context, v core.Viewer, id core.ScheduleID, by core.UserID) (core.RunID, core.SessionID, error)
}

// runSchedule answers with the run and its chat, so the toast can open the
// result. The runner makes the run's row after Submit returns, so the
// chat comes from the scheduler; a run row that already exists (a skipped
// tick) is the fallback.
func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	id := core.ScheduleID(r.PathValue("id"))
	if _, err := s.d.Store.Schedules().Get(ctx, a.Viewer, id); err != nil {
		return err
	}
	var resp dto.RunScheduleResp
	var err error
	if sch, has := s.d.Scheduler.(runNowIn); has {
		resp.RunID, resp.SessionID, err = sch.RunNowIn(ctx, a.Viewer, id, a.User)
	} else {
		resp.RunID, err = s.d.Scheduler.RunNow(ctx, a.Viewer, id, a.User)
	}
	if err != nil {
		return err
	}
	if resp.SessionID == "" && resp.RunID != "" {
		if run, err := s.d.Store.Runs().Get(ctx, a.Viewer, resp.RunID); err == nil {
			resp.SessionID = run.Session
		}
	}
	return accepted(w, resp)
}

func (s *Server) scheduleRuns(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	id := core.ScheduleID(r.PathValue("id"))
	if _, err := s.d.Store.Schedules().Get(ctx, a.Viewer, id); err != nil {
		return err
	}
	runs, err := s.d.Store.Runs().List(ctx, a.Viewer, core.RunFilter{Schedule: id, Page: p})
	if err != nil {
		return err
	}
	return ok(w, listOf(s.namer(ctx, a.Viewer).runViews(runs), p, func(r dto.RunView) int64 { return ms(r.CreatedAt) }))
}

func (s *Server) previewSchedule(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SchedulePreviewReq
	if err := decode(r, &req); err != nil {
		return err
	}
	tz := req.TZ
	if tz == "" {
		tz = s.userZone(r.Context(), a.User)
	}
	next, err := s.d.Scheduler.Preview(req.Kind, req.Spec, tz, 5)
	if err != nil {
		return err
	}
	human, err := s.d.Scheduler.Describe(req.Kind, req.Spec, tz)
	if err != nil {
		return err
	}
	return ok(w, dto.SchedulePreviewResp{Next: nonNil(next), Human: human})
}

// userZone is u's own time zone, which a schedule saved without one gets
// (scheduler.Upsert), so its preview reads the same.
func (s *Server) userZone(ctx context.Context, u core.UserID) string {
	if usr, err := s.d.Store.Users().Get(ctx, u); err == nil && usr.Timezone != "" {
		if _, err := time.LoadLocation(usr.Timezone); err == nil {
			return usr.Timezone
		}
	}
	return s.cfg().Timezone
}

// ---- MCP ----

func (s *Server) mcpStatus(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	return ok(w, nonNil(s.d.MCP.Status()))
}

func (s *Server) mcpPins(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	pins, err := s.d.MCP.Pins(r.Context(), r.PathValue("server"))
	if err != nil {
		return err
	}
	return ok(w, nonNil(pins))
}

func (s *Server) mcpApprove(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.MCPApproveReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if len(req.Tools) == 0 {
		return badRequest("tools is empty")
	}
	for name, ap := range req.Tools {
		if !ap.Disable && !ap.SideEffect.Valid() {
			return badRequest("tool %q needs a side_effect class", name)
		}
	}
	if err := s.d.MCP.Approve(r.Context(), r.PathValue("server"), req.Tools, a.User); err != nil {
		return err
	}
	return noContent(w)
}

// mcpInstall starts the installer in the background; progress is visible in
// GET /api/mcp (state installing, then ready or error).
func (s *Server) mcpInstall(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	server := r.PathValue("server")
	if !slices.ContainsFunc(s.cfg().MCP.Servers, func(m configMCP) bool { return m.Name == server }) {
		return errNotFound
	}
	s.jobs.start(r.Context(), "mcp-install:"+server, installTimeout, func(ctx context.Context) {
		if err := s.d.MCP.Install(ctx, server, a.User); err != nil {
			s.log.Warn("mcp install failed", "server", server, "err", err)
		}
	})
	return accepted(w, nil)
}

var (
	npmName    = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	npmVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
)

// mcpResolve looks an npm package up in the public registry and returns the
// exact version and integrity hash a command MCP server is pinned to. The
// admin sees both before adding the server; Bothy installs nothing here.
func (s *Server) mcpResolve(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	var req dto.MCPResolveReq
	if err := decode(r, &req); err != nil {
		return err
	}
	name, version := strings.TrimSpace(req.Name), strings.TrimSpace(req.Version)
	if len(name) > 214 || !npmName.MatchString(name) {
		return badRequest("That isn't an npm package name. Use one like @linear/mcp.")
	}
	if version != "" && !npmVersion.MatchString(version) {
		return badRequest("Use an exact version, like 1.4.0, or leave it empty for the latest.")
	}
	hc := s.d.Egress.SystemClient(core.SystemDest{Name: "mcp:resolve", Hosts: []string{"registry.npmjs.org:443"}})
	if hc == nil {
		return newError(http.StatusServiceUnavailable, "unavailable", "Bothy can't reach the npm registry right now.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	u := "https://registry.npmjs.org/" + url.PathEscape(name) + "/" + url.PathEscape(cmp.Or(version, "latest"))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	hreq.Header.Set("Accept", "application/json")
	resp, err := hc.Do(hreq)
	if err != nil {
		return newError(http.StatusBadGateway, "unreachable", "Bothy couldn't reach the npm registry. Try again in a moment.")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound && version != "":
		return newError(http.StatusNotFound, "not_found", "npm has no version "+version+" of "+name+".")
	case resp.StatusCode == http.StatusNotFound:
		return newError(http.StatusNotFound, "not_found", "npm has no package called "+name+".")
	case resp.StatusCode != http.StatusOK:
		return newError(http.StatusBadGateway, "provider_error", fmt.Sprintf("The npm registry answered %d. Try again in a moment.", resp.StatusCode))
	}
	var meta struct {
		Name    string          `json:"name"`
		Version string          `json:"version"`
		Bin     json.RawMessage `json:"bin"`
		Dist    struct {
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&meta); err != nil || meta.Name != name ||
		!npmVersion.MatchString(meta.Version) || !strings.HasPrefix(meta.Dist.Integrity, "sha512-") {
		return newError(http.StatusBadGateway, "provider_error", "The npm registry sent something Bothy can't pin. Try again in a moment.")
	}
	return ok(w, dto.MCPResolveResp{Name: meta.Name, Version: meta.Version, Integrity: meta.Dist.Integrity, Command: npmCommand(meta.Name, meta.Bin)})
}

// npmCommand runs a package's program as npm installs it under /opt/mcp:
// "bin" is a path or a map of names to paths; with several, the one named
// like the package (without its scope), else the first by name. nil when
// the package has none, or only paths that leave its folder.
func npmCommand(pkg string, bin json.RawMessage) []string {
	var one string
	bins := map[string]string{}
	if json.Unmarshal(bin, &one) == nil {
		bins[""] = one
	} else if json.Unmarshal(bin, &bins) != nil {
		return nil
	}
	short := pkg[strings.LastIndex(pkg, "/")+1:]
	names := slices.Sorted(maps.Keys(bins))
	if i := slices.Index(names, short); i > 0 {
		names[0], names[i] = names[i], names[0]
	}
	for _, n := range names {
		p := path.Clean(strings.TrimPrefix(bins[n], "./"))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
			continue
		}
		return []string{"node", "node_modules/" + pkg + "/" + p}
	}
	return nil
}

func (s *Server) mcpReload(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	if err := s.d.MCP.Reload(r.Context(), r.PathValue("server")); err != nil {
		return err
	}
	return accepted(w, nil)
}

// mcpUseMyKey lets the admin's own keys list a server's actions when it has
// no shared key; their key never makes anyone else's calls. Cookie only: a
// token must not be able to send its owner's personal keys to an address.
func (s *Server) mcpUseMyKey(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if a.User == "" {
		return errNotFound
	}
	if err := s.d.MCP.DiscoverWith(withOrigin(r, a, nil), r.PathValue("server"), a.User); err != nil {
		return err
	}
	return accepted(w, nil)
}

// ---- providers, models, channels ----

func (s *Server) providers(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	rows := nonNil(s.d.LLM.Health())
	s.credentialSources(r.Context(), rows)
	return ok(w, rows)
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	var req dto.ProviderTestReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if err := checkProbeInput(req.Kind, req.API, req.Region, req.Profile, req.APIKey != "" || req.SecretRef != ""); err != nil {
		return err
	}
	if req.SecretRef != "" && !strings.HasPrefix(req.SecretRef, "secret:") && !strings.HasPrefix(req.SecretRef, config.AWSSecretPrefix) {
		return badRequest("secret_ref must be secret:<name> or aws-sm:<secret>")
	}
	if req.APIKey != "" && s.d.Vault != nil {
		// The key is write-only: keep it out of logs and probe errors.
		id := "probe:" + requestID()
		s.d.Vault.Redactor().Add(id, []byte(req.APIKey))
		defer s.d.Vault.Redactor().Remove(id)
	}
	res, err := s.d.LLM.Probe(r.Context(), core.ProbeConfig{Kind: req.Kind, BaseURL: req.BaseURL, APIKey: core.NewSecret([]byte(req.APIKey)),
		SecretRef: req.SecretRef, Model: req.Model, AllowPrivate: req.AllowPrivate, API: req.API, Region: req.Region, Profile: req.Profile})
	if err != nil {
		return err
	}
	res.Detail, res.Error = s.redact(res.Detail), s.redact(res.Error)
	return ok(w, res)
}

func (s *Server) detectProviders(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	return ok(w, nonNil(s.d.LLM.Detect(r.Context())))
}

func (s *Server) models(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	return ok(w, nonNil(s.d.LLM.Models(r.Context())))
}

func (s *Server) channels(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	return ok(w, nonNil(s.d.Hub.Status()))
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	name := r.PathValue("name")
	if !slices.ContainsFunc(s.d.Hub.Status(), func(st core.ChannelStatus) bool { return st.Name == name }) {
		return errNotFound
	}
	id, err := s.d.Hub.Test(r.Context(), name)
	if err != nil {
		return s.channelTestError(err)
	}
	return ok(w, dto.ChannelTestResp{Identity: id})
}

// ---- config ----

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	v := dto.ConfigView{Effective: s.d.Config.Effective()}
	if r.URL.Query().Get("schema") == "1" {
		v.Schema = s.d.Config.Schema()
	}
	return ok(w, v)
}

func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.ConfigPatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if len(req.Set) == 0 {
		return badRequest("set is empty")
	}
	before := s.d.Config.Current().MCP.Servers
	restart, err := s.d.Config.Set(configCtx(r, a), req.Set, byString(a))
	if err != nil {
		return err
	}
	for path, v := range req.Set {
		// A whole list set anew leaves out the servers it doesn't name.
		if string(bytes.TrimSpace(v)) == "null" || path == "mcp" || path == "mcp.servers" {
			s.forgetMCP(r.Context(), path, before)
		}
	}
	return ok(w, dto.ConfigPatchResp{RestartRequired: nonNil(restart)})
}

func (s *Server) resetConfig(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	before := s.d.Config.Current().MCP.Servers
	if err := s.d.Config.Reset(configCtx(r, a), r.PathValue("path"), byString(a)); err != nil {
		return err
	}
	s.forgetMCP(r.Context(), r.PathValue("path"), before)
	return noContent(w)
}

// configCtx gives an API token's config change the check that refuses what
// could let people in. Like letting in one person (PATCH /api/identities),
// that needs an admin signed in on the web; everything else stays open to
// admin tokens.
func configCtx(r *http.Request, a core.Actor) context.Context {
	if a.Via != "token" {
		return r.Context()
	}
	return config.WithCheck(r.Context(), admitsMore)
}

var (
	errSignInByToken = newError(http.StatusForbidden, "forbidden",
		"sign-in settings decide who can get in, so changing them needs a signed-in browser session, not an API token")
	errTeamModeByToken = newError(http.StatusForbidden, "forbidden",
		"team mode lets the people on the sign-in list make their own accounts, so turning it on needs a signed-in browser session, not an API token")
)

// admitsMore refuses any sign-in setting, team mode while signing in makes
// accounts, and a chat app turned on or given another token: Slack lets in
// every member of its token's workspace, Discord every member of a server
// with a channel on. A token swapped while the app is off counts too: it is
// the one an admin's later switch turns on.
func admitsMore(old, next *config.Config) error {
	for _, p := range config.Diff(old, next) {
		if strings.HasPrefix(p, "auth.") {
			return errSignInByToken
		}
	}
	if makesAccounts(next) && !makesAccounts(old) {
		return errTeamModeByToken
	}
	was := chatApps(old)
	for i, app := range chatApps(next) {
		if (app.on && !was[i].on) || !slices.Equal(was[i].tokens, app.tokens) {
			return newError(http.StatusForbidden, "forbidden",
				"turning on "+app.name+" or changing its token lets people in, so it needs a signed-in browser session, not an API token")
		}
	}
	return nil
}

func makesAccounts(c *config.Config) bool { return c.Mode == "multi" && c.Auth.SignIn.AllowNew }

type chatApp struct {
	name   string
	on     bool
	tokens []string // secret references
}

func chatApps(c *config.Config) []chatApp {
	ch := c.Channels
	return []chatApp{
		{"Telegram", ch.Telegram.Enabled, []string{ch.Telegram.Token}},
		{"Discord", ch.Discord.Enabled, []string{ch.Discord.Token}},
		{"Slack", ch.Slack.Enabled, []string{ch.Slack.AppToken, ch.Slack.BotToken}},
	}
}

// chatTokenWrite refuses an API token's write of the global secret a chat
// app's token setting names: another workspace's Slack token lets its
// members in, as a changed reference would.
func (s *Server) chatTokenWrite(a core.Actor, sc core.Scope, name string) error {
	if a.Via != "token" || sc != core.GlobalScope {
		return nil
	}
	for _, app := range chatApps(s.cfg()) {
		if slices.Contains(app.tokens, "secret:"+name) {
			return newError(http.StatusForbidden, "forbidden",
				"this is "+app.name+"'s token, and another one can let people in, so replacing it needs a signed-in browser session, not an API token")
		}
	}
	return nil
}

// forgetMCP drops the approvals of the MCP servers an admin removed at path
// (one server, or all of them), of those configured before. Only an explicit
// removal does: an entry that a bothy.yaml edit made invalid also leaves the
// config, and its approvals must survive the fix. MCP.Forget keeps those of a
// name that is still configured.
func (s *Server) forgetMCP(ctx context.Context, path string, before []config.MCPServer) {
	if s.d.MCP == nil {
		return
	}
	var names []string
	if name, ok := strings.CutPrefix(path, "mcp.servers."); ok && name != "" && !strings.Contains(name, ".") {
		names = []string{name}
	} else if path == "mcp" || path == "mcp.servers" {
		for _, c := range before {
			names = append(names, c.Name)
		}
	}
	for _, name := range names {
		if err := s.d.MCP.Forget(ctx, name); err != nil {
			s.log.Error("forget a removed mcp server's approvals", "server", name, "err", err)
		}
	}
}

// ---- vault ----

// Vault.Put and Vault.Delete write their own vault.put and vault.delete
// audit rows, for every caller, so these handlers write none; withOrigin
// gives those rows the token, client address and source.

func (s *Server) vaultStatus(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	return ok(w, s.d.Vault.Status())
}

func (s *Server) vaultImport(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.VaultImportReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if !slices.Contains(wellKnownEnv, req.EnvVar) {
		return badRequest("env_var must be one of %s", strings.Join(wellKnownEnv, ", "))
	}
	if !secretNameRE.MatchString(req.Name) {
		return badRequest("name must be 1-64 of a-z, 0-9, '_', '.', '-'")
	}
	if err := s.chatTokenWrite(a, core.GlobalScope, req.Name); err != nil {
		return err
	}
	val := s.getenv(req.EnvVar)
	if val == "" {
		return newError(http.StatusNotFound, "not_found", req.EnvVar+" is not set in the gateway's environment")
	}
	meta, err := s.d.Vault.Put(withOrigin(r, a, map[string]any{"source": "env:" + req.EnvVar}), core.GlobalScope, req.Name, []byte(val), a.User)
	if err != nil {
		return err
	}
	return ok(w, meta)
}

func (s *Server) vaultUnseal(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.UnsealReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Passphrase == "" {
		return badRequest("passphrase is required")
	}
	// Vault.Unseal of an open vault checks nothing; recording it as an
	// unlock would log any passphrase as the right one.
	if st := s.d.Vault.Status(); !st.Sealed {
		return ok(w, st)
	}
	if err := s.d.Vault.Unseal(r.Context(), []byte(req.Passphrase)); err != nil {
		s.audit(r, a, "vault.unseal", "vault", "denied", nil)
		return err
	}
	s.audit(r, a, "vault.unseal", "vault", "ok", nil)
	return ok(w, s.d.Vault.Status())
}

// vaultScope resolves a scope a caller may use: admins any, everyone else
// only their own user scope (anything else is 404, not 403).
func vaultScope(a core.Actor, raw string) (core.Scope, error) {
	own := core.UserScope(a.User)
	if raw == "" {
		if isAdmin(a) {
			return core.GlobalScope, nil
		}
		return own, nil
	}
	sc := core.Scope(raw)
	if sc == own {
		return sc, nil
	}
	if !isAdmin(a) {
		return "", errNotFound
	}
	if sc == core.GlobalScope {
		return sc, nil
	}
	if u, found := strings.CutPrefix(raw, "user:"); found && core.ValidID(core.PrefixUser, u) {
		return sc, nil
	}
	return "", badRequest("scope must be global or user:<id>")
}

func (s *Server) vaultList(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	sc, err := vaultScope(a, r.URL.Query().Get("scope"))
	if err != nil {
		return err
	}
	metas, err := s.d.Vault.List(r.Context(), sc)
	if err != nil {
		return err
	}
	return ok(w, nonNil(metas))
}

func (s *Server) vaultPut(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	sc, err := vaultScope(a, r.PathValue("scope"))
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	if !secretNameRE.MatchString(name) {
		return badRequest("name must be 1-64 of a-z, 0-9, '_', '.', '-'")
	}
	if err := s.chatTokenWrite(a, sc, name); err != nil {
		return err
	}
	var req dto.VaultPutReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Value == "" || len(req.Value) > maxSecretValue {
		return badRequest("value must be 1 byte to 64 KB")
	}
	meta, err := s.d.Vault.Put(withOrigin(r, a, nil), sc, name, []byte(req.Value), a.User)
	if err != nil {
		return err
	}
	return ok(w, meta)
}

func (s *Server) vaultDelete(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	sc, err := vaultScope(a, r.PathValue("scope"))
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	if err := s.d.Vault.Delete(withOrigin(r, a, nil), sc, name, a.User); err != nil {
		return err
	}
	return noContent(w)
}

// ---- usage, audit, egress, sandbox ----

func parseTimeParam(v string, loc *time.Location) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation(time.DateOnly, v, loc)
	if err != nil {
		return time.Time{}, badRequest("times are RFC 3339 or YYYY-MM-DD")
	}
	return t, nil
}

func (s *Server) location() *time.Location {
	if loc, err := time.LoadLocation(s.cfg().Timezone); err == nil {
		return loc
	}
	return time.UTC
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	q := r.URL.Query()
	loc := s.location()
	from, err := parseTimeParam(q.Get("from"), loc)
	if err != nil {
		return err
	}
	to, err := parseTimeParam(q.Get("to"), loc)
	if err != nil {
		return err
	}
	if to.IsZero() {
		to = s.now()
	}
	if from.IsZero() {
		from = to.Add(-defaultUsageAge)
	}
	group := q.Get("group_by")
	switch group {
	case "":
		group = "day"
	case "day", "model", "user", "schedule", "session":
	default:
		return badRequest("group_by must be day, model, user, schedule or session")
	}
	uq := core.UsageQuery{User: a.User, From: from, To: to, GroupBy: group}
	if isAdmin(a) {
		uq.User = core.UserID(q.Get("user"))
	} else if group == "user" {
		uq.GroupBy = "day"
	}
	rows, err := s.d.Store.Usage().Report(r.Context(), uq)
	if err != nil {
		return err
	}
	tot := core.UsageRow{Key: "total", PriceKnown: true}
	for _, row := range rows {
		tot.Cost += row.Cost
		tot.Input += row.Input
		tot.Output += row.Output
		tot.CacheRead += row.CacheRead
		tot.CacheWrite += row.CacheWrite
		tot.Calls += row.Calls
		tot.PriceKnown = tot.PriceKnown && row.PriceKnown
	}
	// Admins read everyone's spending, but a chat's title and a schedule's
	// name stay private to their owner (SPEC §5.9), so rows are named through
	// the caller's own view.
	return ok(w, dto.UsageResp{Rows: s.namer(r.Context(), a.Viewer).usageRows(uq.GroupBy, rows), Totals: tot})
}

func (s *Server) usageToday(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	spent, limit, err := s.d.Budget.Daily(ctx, core.UserPrincipal(a.User))
	if err != nil {
		return err
	}
	out := dto.UsageToday{Spent: spent, Limit: limit, Timezone: s.location().String()}
	if isAdmin(a) {
		day := s.now().In(s.location()).Format(time.DateOnly)
		if out.GlobalSpent, err = s.d.Store.Usage().Spend(ctx, "", day); err != nil {
			return err
		}
		b := s.cfg().Budgets
		out.GlobalLimit = core.MicrosFromUSD(b.DailyUSD)
		if usd, ok := b.ChatShareUSD(); ok {
			out.ChatLimit = core.MicrosFromUSD(usd)
			if out.ChatSpent, err = s.d.Store.Usage().TeamSpend(ctx, day); err != nil {
				return err
			}
		}
	}
	return ok(w, out)
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	from, err := parseTimeParam(q.Get("from"), s.location())
	if err != nil {
		return err
	}
	to, err := parseTimeParam(q.Get("to"), s.location())
	if err != nil {
		return err
	}
	es, err := s.d.Store.Audit().Query(r.Context(), core.AuditFilter{Actor: q.Get("actor"), ActionPrefix: q.Get("action"), From: from, To: to, Page: p})
	if err != nil {
		return err
	}
	av, err := s.adminViewer(r.Context(), a)
	if err != nil {
		return err
	}
	return ok(w, listOf(s.auditRows(r.Context(), a.Viewer, av, es), p, func(e dto.AuditRow) int64 { return e.ID }))
}

func (s *Server) auditVerify(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	bad, err := s.d.Audit.Verify(r.Context())
	if err != nil && bad == 0 {
		return err
	}
	return ok(w, dto.AuditVerify{OK: bad == 0, BadID: bad})
}

func (s *Server) egressLog(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	rs, err := s.d.Store.Egress().Query(r.Context(), a.Viewer, core.SessionID(r.URL.Query().Get("session")), p)
	if err != nil {
		return err
	}
	return ok(w, listOf(egressRows(rs), p, func(e dto.EgressRow) int64 { return e.ID }))
}

func (s *Server) sandboxInfo(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	info := dto.SandboxInfo{Status: s.d.Sandbox.Status(ctx), QuotaMB: s.cfg().Sandbox.WorkspaceQuotaMB}
	if !info.Status.Available {
		info.Compose = composeHint
	}
	if pr, err := s.d.Store.Principals().Get(ctx, a.Viewer, core.UserPrincipal(a.User)); err == nil {
		if used, quota, err := s.d.Sandbox.Usage(ctx, pr.Workspace); err == nil {
			info.UsedMB = int(used >> 20)
			if quota > 0 {
				info.QuotaMB = int(quota >> 20)
			}
		}
	}
	return ok(w, info)
}

// ---- admin ----

func (s *Server) pause(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.PauseReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	// The runner's persisted flag is the whole kill switch: the scheduler
	// reads it, so /resume from a chat releases what this pause engaged.
	if err := s.d.Runner.SetPaused(ctx, req.Paused, a.User); err != nil {
		return err
	}
	action := "admin.resume"
	if req.Paused {
		action = "admin.pause"
		if _, err := s.d.Runner.StopAll(ctx, a.User); err != nil {
			return err
		}
	}
	if s.d.Bus != nil {
		banner := core.BannerData{Code: "paused", Clear: true}
		if req.Paused {
			by, at := s.d.Runner.PausedBy()
			banner = core.PausedBanner(by, s.namer(ctx, a.Viewer).who(by), at)
		}
		s.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: banner})
	}
	s.audit(r, a, action, "runner", "ok", nil)
	return noContent(w)
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.d.Restart == nil {
		return newError(http.StatusServiceUnavailable, "unavailable", "restart is not available in this process")
	}
	s.audit(r, a, "admin.restart", "gateway", "ok", nil)
	s.d.Restart()
	return accepted(w, nil)
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.d.DataDir == "" {
		return newError(http.StatusServiceUnavailable, "unavailable", "no data directory")
	}
	p, err := s.d.Store.Backup(r.Context(), filepath.Join(s.d.DataDir, "backups"))
	if err != nil {
		s.audit(r, a, "backup.create", "", "error", nil)
		return err
	}
	s.audit(r, a, "backup.create", p, "ok", nil)
	return ok(w, map[string]string{"path": p})
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	if s.d.Doctor == nil {
		return core.ErrNotImplemented
	}
	return ok(w, dto.DoctorReport{Findings: nonNil(s.d.Doctor.Run(r.Context(), false))})
}

func (s *Server) securityAudit(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	if s.d.Doctor == nil {
		return core.ErrNotImplemented
	}
	var req dto.SecurityAuditReq
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil {
			return err
		}
	}
	ctx := r.Context()
	fs := s.d.Doctor.Run(ctx, true)
	rep := dto.DoctorReport{Findings: nonNil(fs)}
	if req.Fix {
		rep.Fixed = s.d.Doctor.Fix(ctx, fs)
		rep.Findings = nonNil(s.d.Doctor.Run(ctx, true))
	}
	return ok(w, rep)
}
