package httpapi

import (
	"errors"
	"net/http"

	"github.com/rielicat/bothy/internal/core"
)

const (
	skillZipMax = 5 << 20
	imageMax    = 5 << 20
	// v1BodyMax fits three maximum-size images as base64 data URIs plus text;
	// /v1 bodies are read whole, so the cap also bounds memory per request.
	v1BodyMax = 3*(imageMax*4/3) + 1<<20
)

// routeTable is SPEC §8.3. Access classes: accessUser = cookie or bearer
// chat (bearer read also covers GET); accessAdmin = owner/admin cookie or
// bearer admin; "own" filtering happens in handlers through the actor's
// Viewer.
func (s *Server) routeTable() []route {
	return []route{
		{pattern: "GET /healthz", access: accessNone, safe: true, h: s.healthz},
		{pattern: "GET /readyz", access: accessNone, safe: true, h: s.readyz},

		{pattern: "GET /api/setup/state", access: accessLocal, setup: true, safe: true, h: s.setupState},
		{pattern: "POST /api/setup/claim", access: accessLocal, setup: true, safe: true, h: s.setupClaim},
		{pattern: "POST /api/setup/owner", access: accessSetup, setup: true, safe: true, h: s.setupOwner},
		{pattern: "POST /api/setup/complete", access: accessAdmin, setup: true, safe: true, h: s.setupComplete},

		{pattern: "POST /api/auth/login", access: accessNone, safe: true, h: s.login},
		{pattern: "POST /api/auth/logout", access: accessUser, safe: true, h: s.logout},
		{pattern: "GET /api/auth/me", access: accessUser, safe: true, h: s.authMe},
		{pattern: "POST /api/auth/password", access: accessUserCookie, safe: true, h: s.changePassword},

		{pattern: "GET /api/tokens", access: accessUserCookie, h: s.listTokens},
		{pattern: "POST /api/tokens", access: accessUserCookie, h: s.createToken},
		{pattern: "DELETE /api/tokens/{id}", access: accessUserCookie, h: s.revokeToken},

		{pattern: "GET /api/users", access: accessAdmin, h: s.listUsers},
		{pattern: "POST /api/users", access: accessAdmin, h: s.createUser},
		{pattern: "PATCH /api/users/{id}", access: accessAdmin, h: s.patchUser},
		{pattern: "DELETE /api/users/{id}", access: accessAdmin, h: s.deleteUser},

		// Letting someone in or making them an approver lets them decide
		// requests in chat, so it is cookie-only like approvals and tokens.
		{pattern: "GET /api/identities", access: accessAdmin, h: s.listIdentities},
		{pattern: "PATCH /api/identities/{channel}/{external}", access: accessAdminCookie, h: s.patchIdentity},
		{pattern: "GET /api/spaces", access: accessAdmin, h: s.listSpaces},
		// Turning a channel on or giving it standard tools is cookie-only
		// too (patchSpace); renames and the mention setting aren't.
		{pattern: "PATCH /api/spaces/{id}", access: accessAdmin, h: s.patchSpace},
		{pattern: "DELETE /api/spaces/{id}", access: accessAdminCookie, h: s.deleteSpace},

		{pattern: "GET /api/sessions", access: accessUser, h: s.listSessions},
		{pattern: "POST /api/sessions", access: accessUser, h: s.createSession},
		{pattern: "GET /api/sessions/{id}", access: accessUser, h: s.getSession},
		{pattern: "PATCH /api/sessions/{id}", access: accessUser, h: s.patchSession},
		{pattern: "DELETE /api/sessions/{id}", access: accessUser, h: s.archiveSession},
		{pattern: "GET /api/sessions/{id}/messages", access: accessUser, h: s.listMessages},
		{pattern: "POST /api/sessions/{id}/messages", access: accessUser, h: s.sendMessage},
		{pattern: "POST /api/sessions/{id}/stop", access: accessUser, h: s.stopSession},
		{pattern: "POST /api/sessions/{id}/compact", access: accessUser, h: s.compactSession},
		{pattern: "GET /api/sessions/{id}/context", access: accessUser, h: s.sessionContext},
		{pattern: "GET /api/sessions/{id}/refine-estimate", access: accessUser, h: s.refineEstimate},
		{pattern: "POST /api/sessions/{id}/fork", access: accessUser, h: s.forkSession},
		{pattern: "GET /api/sessions/{id}/pins", access: accessUser, h: s.listPins},
		{pattern: "POST /api/sessions/{id}/pins", access: accessUser, h: s.addPin},
		{pattern: "DELETE /api/sessions/{id}/pins/{pin}", access: accessUser, h: s.deletePin},
		{pattern: "POST /api/uploads", access: accessUser, h: s.upload, limit: -1},

		{pattern: "GET /api/runs", access: accessUser, h: s.listRuns},
		{pattern: "GET /api/runs/{id}", access: accessUser, h: s.getRun},
		// Cookie-only: a continued run is interactive (cards, 15 min waits,
		// allow_always), which bearer clients get only through postMessage's
		// trigger api and on_approval.
		{pattern: "POST /api/runs/{id}/continue", access: accessUserCookie, h: s.continueRun},
		{pattern: "GET /api/refine", access: accessUser, h: s.refineInfo},

		{pattern: "GET /api/approvals", access: accessUser, h: s.listApprovals},
		{pattern: "GET /api/approvals/{id}", access: accessUser, h: s.getApproval},
		{pattern: "POST /api/approvals/{id}", access: accessUserCookie, h: s.resolveApproval},
		{pattern: "GET /api/approvals/{id}/votes", access: accessUser, h: s.approvalVotes},
		{pattern: "GET /api/releases", access: accessUser, h: s.releases},
		{pattern: "GET /api/releases/deploys", access: accessUser, h: s.releaseDeploys},

		{pattern: "GET /api/grants", access: accessUser, h: s.listGrants},
		{pattern: "POST /api/grants", access: accessAdmin, h: s.createGrant},
		{pattern: "DELETE /api/grants/{id}", access: accessUser, h: s.revokeGrant},
		{pattern: "POST /api/policy/preview", access: accessAdmin, h: s.policyPreview},

		{pattern: "GET /api/events", access: accessRead, h: s.events},
		{pattern: "GET /api/search", access: accessUser, h: s.search},

		{pattern: "GET /api/memory/export", access: accessUser, h: s.memoryExport},
		{pattern: "GET /api/memory/{file}", access: accessUser, h: s.getMemory},
		{pattern: "PUT /api/memory/{file}", access: accessUser, h: s.putMemory},
		{pattern: "GET /api/memory/{file}/history", access: accessUser, h: s.memoryHistory},
		{pattern: "GET /api/persona", access: accessUser, h: s.getPersona},
		{pattern: "PUT /api/persona", access: accessAdmin, h: s.putPersona},
		{pattern: "GET /api/constitution", access: accessUser, h: s.constitution},

		{pattern: "GET /api/skills", access: accessUser, h: s.listSkills},
		{pattern: "GET /api/skills/{name}", access: accessUser, h: s.getSkill},
		{pattern: "POST /api/skills", access: accessAdmin, h: s.installSkill, limit: skillZipMax + 64<<10},
		{pattern: "PATCH /api/skills/{name}", access: accessAdmin, h: s.patchSkill},
		{pattern: "DELETE /api/skills/{name}", access: accessAdmin, h: s.deleteSkill},
		{pattern: "POST /api/skills/rescan", access: accessAdmin, h: s.rescanSkills},

		{pattern: "GET /api/schedules", access: accessUser, h: s.listSchedules},
		{pattern: "POST /api/schedules", access: accessUser, h: s.createSchedule},
		{pattern: "POST /api/schedules/preview", access: accessUser, h: s.previewSchedule},
		{pattern: "GET /api/schedules/{id}", access: accessUser, h: s.getSchedule},
		{pattern: "PATCH /api/schedules/{id}", access: accessUser, h: s.patchSchedule},
		{pattern: "DELETE /api/schedules/{id}", access: accessUser, h: s.deleteSchedule},
		{pattern: "POST /api/schedules/{id}/run", access: accessUser, h: s.runSchedule},
		{pattern: "GET /api/schedules/{id}/runs", access: accessUser, h: s.scheduleRuns},

		{pattern: "GET /api/mcp", access: accessAdmin, h: s.mcpStatus},
		{pattern: "GET /api/mcp/{server}/pins", access: accessAdmin, h: s.mcpPins},
		{pattern: "POST /api/mcp/resolve", access: accessAdmin, h: s.mcpResolve},
		{pattern: "POST /api/mcp/{server}/approve", access: accessAdmin, h: s.mcpApprove},
		{pattern: "POST /api/mcp/{server}/install", access: accessAdmin, h: s.mcpInstall},
		{pattern: "POST /api/mcp/{server}/reload", access: accessAdmin, h: s.mcpReload},

		{pattern: "GET /api/providers", access: accessAdmin, h: s.providers},
		{pattern: "POST /api/providers/test", access: accessAdmin, h: s.testProvider},
		{pattern: "POST /api/providers/detect", access: accessAdmin, h: s.detectProviders},
		{pattern: "GET /api/models", access: accessUser, h: s.models},

		{pattern: "GET /api/channels", access: accessAdmin, h: s.channels},
		{pattern: "POST /api/channels/{name}/test", access: accessAdmin, h: s.testChannel},
		{pattern: "GET /api/channels/slack/manifest", access: accessAdmin, h: s.slackManifest},
		{pattern: "GET /api/channels/discord/invite", access: accessAdmin, h: s.discordInvite},

		{pattern: "GET /api/config", access: accessAdmin, safe: true, h: s.getConfig},
		{pattern: "PATCH /api/config", access: accessAdmin, safe: true, h: s.patchConfig},
		{pattern: "DELETE /api/config/{path...}", access: accessAdmin, safe: true, h: s.resetConfig},

		{pattern: "GET /api/vault/status", access: accessAdmin, h: s.vaultStatus},
		{pattern: "POST /api/vault/import", access: accessAdmin, h: s.vaultImport},
		{pattern: "POST /api/vault/unseal", access: accessAdmin, h: s.vaultUnseal},
		{pattern: "GET /api/vault", access: accessUser, h: s.vaultList},
		{pattern: "PUT /api/vault/{scope}/{name}", access: accessUser, h: s.vaultPut},
		{pattern: "DELETE /api/vault/{scope}/{name}", access: accessUser, h: s.vaultDelete},

		{pattern: "GET /api/usage", access: accessUser, h: s.usage},
		{pattern: "GET /api/usage/today", access: accessUser, h: s.usageToday},
		{pattern: "GET /api/audit", access: accessAdmin, h: s.auditLog},
		{pattern: "GET /api/audit/verify", access: accessAdmin, h: s.auditVerify},
		{pattern: "GET /api/egress", access: accessUser, h: s.egressLog},
		{pattern: "GET /api/sandbox", access: accessUser, h: s.sandboxInfo},
		{pattern: "GET /api/workspace/files", access: accessUser, h: s.workspaceFiles},

		{pattern: "POST /api/admin/pause", access: accessAdmin, h: s.pause},
		{pattern: "POST /api/admin/restart", access: accessAdmin, h: s.restart},
		{pattern: "POST /api/admin/backup", access: accessAdmin, h: s.backup},
		{pattern: "GET /api/doctor", access: accessAdmin, safe: true, h: s.doctor},
		{pattern: "POST /api/security-audit", access: accessAdmin, safe: true, h: s.securityAudit},

		{pattern: "GET /v1/models", access: accessV1, h: s.v1Models},
		{pattern: "POST /v1/chat/completions", access: accessV1, h: s.v1ChatCompletions, limit: v1BodyMax},

		{pattern: "GET /{path...}", access: accessNone, safe: true, h: s.spa},
	}
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
	return nil
}

// readyz reports whether the store answers and config parsed; it never
// discloses versions.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	ready := s.d.Store != nil && !s.safeMode()
	if ready {
		if _, err := s.d.Store.Meta().Get(r.Context(), "install_id"); err != nil && !errors.Is(err, core.ErrNotFound) {
			ready = false
		}
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false})
		return nil
	}
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) spa(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	if s.d.WebUI == nil {
		http.NotFound(w, r)
		return nil
	}
	s.d.WebUI.ServeHTTP(w, r)
	return nil
}
