package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestHealthSPAAndUnknownRoutes(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("GET", "/healthz", nil, nil)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
	rec = h.do("GET", "/readyz", nil, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("readyz = %d %q", rec.Code, rec.Body.String())
	}
	expect(t, h.do("POST", "/healthz", nil, nil), http.StatusMethodNotAllowed)
	if rec := h.do("GET", "/chat/ses_1", nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Bothy") {
		t.Fatalf("SPA = %d", rec.Code)
	}
	for _, p := range []string{"/api/nope", "/v1/nope"} {
		rec := h.do("GET", p, nil, h.owner)
		expect(t, rec, http.StatusNotFound)
		if !strings.Contains(rec.Header().Get("Content-Type"), "json") {
			t.Fatalf("%s content type %q", p, rec.Header().Get("Content-Type"))
		}
	}
	rec = h.do("DELETE", "/api/sessions", nil, h.owner)
	expect(t, rec, http.StatusNotFound)
	if errCode(t, rec) != "not_found" {
		t.Fatalf("envelope = %s", rec.Body.String())
	}
	if rec := h.do("GET", "/api/auth/me", nil, h.owner); rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-Id") == "" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestMeAndBanners(t *testing.T) {
	h := newHarness(t).setup()
	h.bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "warn", Code: "restart_required", Text: "restart"}})
	h.runner.PausedV = true
	me := decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, h.owner))
	if me.User.ID != h.owner.user || me.CSRF != h.owner.csrf || me.Mode != "multi" || !me.Paused || !me.Sandbox || len(me.Banners) != 1 ||
		me.Access != (dto.Access{Peer: "local"}) {
		t.Fatalf("me = %+v", me)
	}
	if me := decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, h.owner, overTLS())); me.Access != (dto.Access{Peer: "local", TLS: true}) {
		t.Fatalf("access over TLS = %+v", me.Access)
	}
	if strings.Contains(h.do("GET", "/api/auth/me", nil, h.owner).Body.String(), "pw_hash") {
		t.Fatal("password hash serialised")
	}
	me = decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, h.member))
	if len(me.Banners) != 0 {
		t.Fatalf("member sees admin banners: %+v", me.Banners)
	}
	me = decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, h.tokenFor(h.member, "read")))
	if me.CSRF != "" {
		t.Fatal("tokens get no CSRF token")
	}
}

func sessionCookie(t *testing.T, rec interface{ Result() *http.Response }) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookiePlain && c.MaxAge > 0 {
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestPasswordRotationAndLogout(t *testing.T) {
	h := newHarness(t).setup()
	expect(t, h.do("POST", "/api/auth/password", dto.PasswordReq{Old: "nope nope nope", New: "fresh passphrase here"}, h.member), http.StatusBadRequest)
	rec := h.do("POST", "/api/auth/password", dto.PasswordReq{Old: testPassword, New: "fresh passphrase here"}, h.member)
	expect(t, rec, http.StatusNoContent)
	fresh := sessionCookie(t, rec)
	expect(t, h.do("GET", "/api/auth/me", nil, h.member), http.StatusUnauthorized)
	me := decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, &client{cookie: fresh}))
	c := &client{cookie: fresh, csrf: me.CSRF}
	rec = h.do("POST", "/api/auth/logout", nil, c)
	expect(t, rec, http.StatusNoContent)
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge >= 0 {
		t.Fatalf("logout cookie = %+v", cs)
	}
	expect(t, h.do("GET", "/api/auth/me", nil, c), http.StatusUnauthorized)
}

func TestTokenEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/tokens", core.TokenSpec{Name: "cli", Scopes: []string{"chat"}, TTL: 24 * time.Hour}, h.member)
	expect(t, rec, http.StatusOK)
	created := decodeAs[dto.TokenCreateResp](t, rec)
	if !strings.HasPrefix(created.Token, "bothy_pat_") || created.Meta.ID == "" {
		t.Fatalf("created = %+v", created)
	}
	rec = h.do("GET", "/api/tokens", nil, h.member)
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), created.Token) {
		t.Fatalf("listing leaks secrets: %s", rec.Body.String())
	}
	if l := decodeAs[[]core.TokenRecord](t, rec); len(l) != 1 {
		t.Fatalf("tokens = %+v", l)
	}
	expect(t, h.do("DELETE", "/api/tokens/"+created.Meta.ID, nil, h.owner), http.StatusNotFound)
	expect(t, h.do("DELETE", "/api/tokens/"+created.Meta.ID, nil, h.member), http.StatusNoContent)
	expect(t, h.do("GET", "/api/auth/me", nil, &client{token: created.Token, scopes: []string{"chat"}}), http.StatusUnauthorized)
	expect(t, h.do("POST", "/api/tokens", core.TokenSpec{Name: "adm", Scopes: []string{"admin"}, TTL: time.Hour}, h.member), http.StatusForbidden)
}

func TestUserEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/users", dto.UserCreateReq{Username: "bob", Password: testPassword, Role: core.RoleMember}, h.admin)
	expect(t, rec, http.StatusOK)
	bob := decodeAs[core.User](t, rec)
	expect(t, h.do("POST", "/api/users", dto.UserCreateReq{Username: "bob", Password: testPassword, Role: core.RoleMember}, h.admin), http.StatusConflict)
	expect(t, h.do("POST", "/api/users", dto.UserCreateReq{Username: "carl", Password: "password12345", Role: core.RoleMember}, h.admin), http.StatusBadRequest)
	role := core.RoleAdmin
	name := "Bob B."
	rec = h.do("PATCH", "/api/users/"+string(bob.ID), dto.UserPatchReq{Role: &role, DisplayName: &name}, h.owner)
	expect(t, rec, http.StatusOK)
	if u := decodeAs[core.User](t, rec); u.Role != core.RoleAdmin || u.DisplayName != "Bob B." {
		t.Fatalf("patched = %+v", u)
	}
	list := decodeAs[[]core.User](t, h.do("GET", "/api/users", nil, h.admin))
	if len(list) != 4 {
		t.Fatalf("users = %d", len(list))
	}
	h.cfg.update(func(c *configT) { c.Mode = "single" })
	rec = h.do("POST", "/api/users", dto.UserCreateReq{Username: "dora", Password: testPassword, Role: core.RoleMember}, h.admin)
	expect(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "mode: multi") {
		t.Fatalf("fix line = %s", rec.Body.String())
	}
}

// TestSpaces: only admins list and change group chats, which carry their
// tool profile; turning one on or off records the review and turning it off
// stops its tasks. A teammate's own space isn't a group chat.
func TestSpaces(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, Title: "#eng",
		Team: "T1", TeamName: "Acme", Members: 12, External: true, CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{ToolProfile: "restricted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	person := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: "slack", ChatID: "U1", Kind: core.ChatDM}, Title: "Ana",
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, person, &core.Principal{CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	expect(t, h.do("GET", "/api/spaces", nil, h.member), http.StatusForbidden)
	list := decodeAs[[]dto.SpaceView](t, h.do("GET", "/api/spaces", nil, h.admin))
	if len(list) != 1 || list[0].Principal != sp.Principal || list[0].ToolProfile != "restricted" || list[0].TeamName != "Acme" ||
		list[0].Members != 12 || !list[0].External || !list[0].ReviewedAt.IsZero() {
		t.Fatalf("spaces = %+v", list)
	}
	on, off, profile := true, false, "standard"
	expect(t, h.do("PATCH", "/api/spaces/"+string(sp.Principal), dto.SpacePatchReq{Enabled: &on}, h.member), http.StatusForbidden)
	expect(t, h.do("PATCH", "/api/spaces/"+string(person.Principal), dto.SpacePatchReq{Enabled: &off}, h.admin), http.StatusNotFound)
	rec := h.do("PATCH", "/api/spaces/"+string(sp.Principal), dto.SpacePatchReq{Enabled: &on, ToolProfile: &profile}, h.admin)
	expect(t, rec, http.StatusOK)
	if v := decodeAs[dto.SpaceView](t, rec); !v.Enabled || v.ToolProfile != "standard" || v.ReviewedAt.IsZero() {
		t.Fatalf("turned on = %+v", v)
	}
	if len(h.runner.StoppedPrincipals()) != 0 {
		t.Fatal("turning a channel on stopped its tasks")
	}
	expect(t, h.do("PATCH", "/api/spaces/"+string(sp.Principal), dto.SpacePatchReq{Enabled: &off}, h.admin), http.StatusOK)
	if got := h.runner.StoppedPrincipals(); !slices.Equal(got, []core.PrincipalID{sp.Principal}) {
		t.Fatalf("stopped principals = %v", got)
	}
	bad := "admin"
	expect(t, h.do("PATCH", "/api/spaces/"+string(sp.Principal), dto.SpacePatchReq{ToolProfile: &bad}, h.admin), http.StatusBadRequest)
	if !slices.Contains(h.audit.Actions(), "space.update") {
		t.Fatalf("audit = %v", h.audit.Actions())
	}
}

// Turning a channel on, or giving it standard tools, lets in whoever Bothy
// answers there, so an admin token can't; it can still rename a channel,
// change when Bothy answers or turn it off. Deleting one is cookie-only.
func TestSpaceRoutesRefuseTokensWhereTheyLetPeopleIn(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "discord", ChatID: "300", Kind: core.ChatGroup}, Title: "general",
		CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{ToolProfile: "restricted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := "/api/spaces/" + string(sp.Principal)
	tok := h.tokenFor(h.owner, "chat", "read", "llm", "admin")
	on, off, standard, restricted, mention, title := true, false, "standard", "restricted", false, "#general"
	for _, req := range []dto.SpacePatchReq{{Enabled: &on}, {ToolProfile: &standard}, {Enabled: &on, ToolProfile: &restricted}} {
		rec := h.do("PATCH", path, req, tok)
		expect(t, rec, http.StatusForbidden)
		if msg := errMessage(t, rec); !strings.Contains(msg, "signed-in browser session") {
			t.Fatalf("refusal = %q", msg)
		}
	}
	for _, req := range []dto.SpacePatchReq{{Title: &title}, {MentionOnly: &mention}, {Enabled: &off}, {ToolProfile: &restricted}} {
		expect(t, h.do("PATCH", path, req, tok), http.StatusOK)
	}
	if got, _ := h.store.Spaces().Get(ctx, core.SystemViewer(), sp.Principal); got.Enabled {
		t.Fatal("a token turned the channel on")
	}
	expect(t, h.do("PATCH", path, dto.SpacePatchReq{Enabled: &on, ToolProfile: &standard}, h.admin), http.StatusOK)
	expect(t, h.do("DELETE", path, nil, tok), http.StatusForbidden)
	expect(t, h.do("DELETE", path, nil, h.member), http.StatusForbidden)
}

// TestDeleteSpace: an admin deletes a channel that is off with its data;
// a channel that is on, or a teammate's own space, can't be deleted here.
func TestDeleteSpace(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	now := time.Now()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, Title: "#eng",
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{ToolProfile: "restricted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	person := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: "slack", ChatID: "U1", Kind: core.ChatDM}, Title: "Ana",
		CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, person, &core.Principal{CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sess := &core.Session{Principal: sp.Principal, Origin: "slack", ChannelKey: "slack:C1:", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Sessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	k.add(sp.Principal, "acme/web")
	path := "/api/spaces/" + string(sp.Principal)

	rec := h.do("DELETE", path, nil, h.owner)
	expect(t, rec, http.StatusBadRequest)
	if msg := errMessage(t, rec); msg != "Turn it off first." {
		t.Fatalf("refusal = %q", msg)
	}
	expect(t, h.do("DELETE", "/api/spaces/"+string(person.Principal), nil, h.owner), http.StatusNotFound)
	off := false
	expect(t, h.do("PATCH", path, dto.SpacePatchReq{Enabled: &off}, h.owner), http.StatusOK)
	expect(t, h.do("DELETE", path, nil, h.admin), http.StatusNoContent)

	if _, err := h.store.Principals().Get(ctx, core.SystemViewer(), sp.Principal); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the channel's principal is still there: %v", err)
	}
	if _, err := h.store.Spaces().ByChat(ctx, "slack", "C1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the channel is still there: %v", err)
	}
	if _, err := h.store.Sessions().Get(ctx, core.SystemViewer(), sess.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the channel's chat is still there: %v", err)
	}
	if !slices.Equal(k.purged, []core.PrincipalID{sp.Principal}) {
		t.Fatalf("knowledge purged = %v", k.purged)
	}
	rows := h.actions("space.delete")
	if len(rows) != 1 || rows[0].Target != "space:"+string(sp.Principal) || rows[0].Outcome != "ok" {
		t.Fatalf("space.delete audit = %+v", rows)
	}
	var detail map[string]any
	if err := json.Unmarshal(rows[0].Detail, &detail); err != nil || detail["title"] != "#eng" || detail["channel"] != "slack" {
		t.Fatalf("space.delete detail = %s", rows[0].Detail)
	}
	expect(t, h.do("DELETE", path, nil, h.owner), http.StatusNotFound)
}

func TestSessionFlow(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/sessions", dto.SessionCreateReq{Title: "Trip"}, h.member)
	expect(t, rec, http.StatusOK)
	sess := decodeAs[core.Session](t, rec)
	if sess.Title != "Trip" || sess.Origin != "web" || sess.Principal != core.UserPrincipal(h.member.user) {
		t.Fatalf("session = %+v", sess)
	}
	sid := "/api/sessions/" + string(sess.ID)
	bad := "nonsense"
	expect(t, h.do("PATCH", sid, dto.SessionPatchReq{ModelOverride: &bad}, h.member), http.StatusBadRequest)
	model, title := "anthropic/claude-sonnet-4-6", "Trip to Oslo"
	rec = h.do("PATCH", sid, dto.SessionPatchReq{ModelOverride: &model, Title: &title}, h.member)
	expect(t, rec, http.StatusOK)
	if s := decodeAs[core.Session](t, rec); s.ModelOverride != model || s.Title != title {
		t.Fatalf("patched = %+v", s)
	}
	expect(t, h.do("POST", sid+"/messages", dto.SendReq{}, h.member), http.StatusBadRequest)
	rec = h.do("POST", sid+"/messages", dto.SendReq{Text: "plan it"}, h.member)
	expect(t, rec, http.StatusAccepted)
	rr := h.runner.Submitted()[0]
	if rr.Trigger != core.TrigMessage || rr.Provenance != core.ProvUser || rr.Chat.Channel != "web" || rr.Sender != "web:"+string(h.member.user) ||
		rr.Input[0].Text != "plan it" || rr.User != h.member.user {
		t.Fatalf("run request = %+v", rr)
	}
	expect(t, h.do("POST", sid+"/messages", dto.SendReq{Text: "via api"}, h.tokenFor(h.member, "chat")), http.StatusAccepted)
	if rr := h.runner.Submitted()[1]; rr.Trigger != core.TrigAPI || rr.OnApproval != "deny" || !strings.HasPrefix(rr.Sender, "api:tok_") {
		t.Fatalf("bearer run request = %+v", rr)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = h.store.Messages().Append(ctx, &core.Message{Session: sess.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser,
			Parts: []core.Part{{Type: core.PartText, Text: "m" + string(rune('0'+i))}}, CreatedAt: time.Now()})
	}
	page := decodeAs[dto.MessagePage](t, h.do("GET", sid+"/messages?limit=2", nil, h.member))
	if len(page.Messages) != 2 || !page.HasMore || page.Messages[0].Seq > page.Messages[1].Seq || page.Messages[1].PlainText() != "m2" {
		t.Fatalf("page = %+v", page)
	}
	pin := decodeAs[core.Pin](t, h.do("POST", sid+"/pins", dto.PinReq{MsgSeq: page.Messages[0].Seq}, h.member))
	if pin.Text != "m1" || pin.Source != "user" {
		t.Fatalf("pin = %+v", pin)
	}
	expect(t, h.do("POST", sid+"/pins", dto.PinReq{Text: strings.Repeat("x", 2001)}, h.member), http.StatusBadRequest)
	detail := decodeAs[dto.SessionDetail](t, h.do("GET", sid, nil, h.member))
	if len(detail.Pins) != 1 || detail.Session.ID != sess.ID {
		t.Fatalf("detail = %+v", detail)
	}
	expect(t, h.do("DELETE", sid+"/pins/"+itoa(pin.ID), nil, h.member), http.StatusNoContent)
	if r := decodeAs[dto.StopResp](t, h.do("POST", sid+"/stop", nil, h.member)); !r.Stopped {
		t.Fatal("stop")
	}
	expect(t, h.do("POST", sid+"/compact", nil, h.member), http.StatusAccepted)
	fork := decodeAs[core.Session](t, h.do("POST", sid+"/fork", nil, h.member))
	if fork.ID == sess.ID {
		t.Fatal("fork returned the same session")
	}
	expect(t, h.do("DELETE", sid, nil, h.member), http.StatusNoContent)
	expect(t, h.do("POST", sid+"/messages", dto.SendReq{Text: "x"}, h.member), http.StatusConflict)
	list := decodeAs[dto.List[core.Session]](t, h.do("GET", "/api/sessions?archived=true", nil, h.member))
	if len(list.Items) != 1 || list.Items[0].ID != sess.ID {
		t.Fatalf("archived list = %+v", list)
	}
	h.runner.Err = core.ErrBusy
	rec = h.do("POST", "/api/sessions/"+string(fork.ID)+"/messages", dto.SendReq{Text: "x"}, h.member)
	if rec.Code != http.StatusTooManyRequests || errCode(t, rec) != "busy" {
		t.Fatalf("busy = %d %s", rec.Code, rec.Body.String())
	}
	h.runner.Err = &core.BudgetError{Kind: core.BudgetDailyCost}
	if rec := h.do("POST", "/api/sessions/"+string(fork.ID)+"/messages", dto.SendReq{Text: "x"}, h.member); errCode(t, rec) != "budget_exceeded" {
		t.Fatalf("budget = %s", rec.Body.String())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestUploadAndDownload(t *testing.T) {
	h := newHarness(t).setup()
	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	ct, body := multipartUpload(t, sess.ID, "../../evil/<photo>.png", png)
	rec := h.do("POST", "/api/uploads", body, h.member, withHeader("Content-Type", ct))
	expect(t, rec, http.StatusOK)
	up := decodeAs[dto.Upload](t, rec)
	if up.Name != "_photo_.png" || up.MIME != "image/png" || up.Size != int64(len(png)) {
		t.Fatalf("upload = %+v", up)
	}
	stored, err := h.store.Uploads().Get(context.Background(), core.SystemViewer(), up.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := h.store.Principals().Get(context.Background(), core.SystemViewer(), core.UserPrincipal(h.member.user))
	path := filepath.Join(h.workDir, "ws", string(p.Workspace), filepath.FromSlash(stored.Ref))
	fi, err := os.Stat(path)
	if err != nil || fi.Size() != int64(len(png)) {
		t.Fatalf("file %s: %v", path, err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o007 != 0 {
		t.Fatalf("upload readable by others: %v", fi.Mode())
	}
	// Attach to a message: the run receives an image part.
	expect(t, h.do("POST", "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Uploads: []string{up.ID}}, h.member), http.StatusAccepted)
	if part := h.runner.Submitted()[0].Input[0]; part.Type != core.PartImage || part.Blob.Ref != stored.Ref {
		t.Fatalf("part = %+v", part)
	}
	// Oversized uploads are refused and leave nothing behind.
	ct, body = multipartUpload(t, sess.ID, "big.bin", bytes.Repeat([]byte("x"), 1<<20+10))
	rec = h.do("POST", "/api/uploads", body, h.member, withHeader("Content-Type", ct))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d %s", rec.Code, rec.Body.String())
	}
	// session_id after the file part cannot be streamed.
	rec = h.do("POST", "/api/uploads", fileFirstBody(), h.member, withHeader("Content-Type", "multipart/form-data; boundary=XX"))
	expect(t, rec, http.StatusBadRequest)

	rec = h.do("GET", "/api/workspace/files?path="+stored.Ref, nil, h.member)
	expect(t, rec, http.StatusOK)
	hd := rec.Header()
	if hd.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(hd.Get("Content-Disposition"), "attachment") ||
		hd.Get("Content-Security-Policy") != "sandbox" || hd.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("download = %v", hd)
	}
	list := decodeAs[[]fileEntry](t, h.do("GET", "/api/workspace/files?path=.bothy/in", nil, h.member))
	if len(list) != 1 || !list[0].Dir {
		t.Fatalf("listing = %+v", list)
	}
	outside := filepath.Join(h.workDir, "secret.txt")
	_ = os.WriteFile(outside, []byte("top secret"), 0o600)
	_ = os.Symlink(outside, filepath.Join(h.workDir, "ws", string(p.Workspace), "link"))
	for _, q := range []string{"link", "../secret.txt", "../../etc/passwd", "/etc/passwd"} {
		rec := h.do("GET", "/api/workspace/files?path="+q, nil, h.member)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "top secret") {
			t.Errorf("path %q = %d %q", q, rec.Code, rec.Body.String())
		}
	}
	if l := decodeAs[[]fileEntry](t, h.do("GET", "/api/workspace/files", nil, h.owner)); len(l) != 0 {
		t.Fatalf("owner workspace = %+v", l)
	}
}

func fileFirstBody() string {
	return "--XX\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\nhello\r\n" +
		"--XX\r\nContent-Disposition: form-data; name=\"session_id\"\r\n\r\nses_x\r\n--XX--\r\n"
}

func TestMemoryEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	mp := core.UserPrincipal(h.member.user)
	h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{mp: {core.FileMemory: "likes tea"}}
	rec := h.do("GET", "/api/memory/MEMORY", nil, h.member)
	expect(t, rec, http.StatusOK)
	doc := decodeAs[dto.MemoryDoc](t, rec)
	if doc.Content != "likes tea" || doc.CapBytes != 4096 || doc.ETag == "" || rec.Header().Get("ETag") != `"`+doc.ETag+`"` {
		t.Fatalf("doc = %+v", doc)
	}
	expect(t, h.do("PUT", "/api/memory/MEMORY", dto.MemoryPutReq{Content: "x"}, h.member), http.StatusPreconditionRequired)
	expect(t, h.do("PUT", "/api/memory/MEMORY", dto.MemoryPutReq{Content: "x"}, h.member, withHeader("If-Match", `"stale"`)), http.StatusConflict)
	expect(t, h.do("PUT", "/api/memory/USER", dto.MemoryPutReq{Content: strings.Repeat("x", 2049)}, h.member, withHeader("If-Match", "x")),
		http.StatusBadRequest)
	rec = h.do("PUT", "/api/memory/memory.md", dto.MemoryPutReq{Content: "likes coffee"}, h.member, withHeader("If-Match", `W/"`+doc.ETag+`"`))
	expect(t, rec, http.StatusOK)
	if d := decodeAs[dto.MemoryDoc](t, rec); d.Content != "likes coffee" || d.ETag == doc.ETag {
		t.Fatalf("put = %+v", d)
	}
	hist := decodeAs[dto.List[dto.MemoryChange]](t, h.do("GET", "/api/memory/MEMORY/history", nil, h.member))
	if len(hist.Items) != 1 || hist.Items[0].Op != "edit" || hist.Items[0].ActorName != "member" || hist.Items[0].Chat != nil {
		t.Fatalf("history = %+v", hist)
	}
	expect(t, h.do("GET", "/api/memory/NOTES", nil, h.member), http.StatusNotFound)

	rec = h.do("GET", "/api/memory/export", nil, h.member)
	expect(t, rec, http.StatusOK)
	if rec.Header().Get("Content-Type") != "application/x-tar" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") ||
		rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("export headers = %v", rec.Header())
	}
	files := map[string]string{}
	tr := tar.NewReader(rec.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[hdr.Name] = string(b)
		if hdr.Mode != 0o600 {
			t.Fatalf("%s mode %o", hdr.Name, hdr.Mode)
		}
	}
	if files["MEMORY.md"] != "likes coffee" || files["USER.md"] != "" || len(files) != 4 {
		t.Fatalf("tar = %v", files)
	}
	var ev core.MemoryEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(files["events.jsonl"])), &ev); err != nil || ev.After != "likes coffee" {
		t.Fatalf("events.jsonl = %q (%v)", files["events.jsonl"], err)
	}

	// Nobody reads a channel's memory through the web.
	ctx := context.Background()
	now := time.Now()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "telegram", ChatID: "-5", Kind: core.ChatGroup},
		CreatedAt: now, UpdatedAt: now}
	pr := &core.Principal{Kind: core.PrincipalSpace, ToolProfile: "restricted", CreatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, pr); err != nil {
		t.Fatal(err)
	}
	m2, _, _ := h.auth.Login(ctx, "member", testPassword, "127.0.0.1")
	ma, _ := h.auth.FromCookie(ctx, m2)
	member2 := &client{name: "member", user: h.member.user, cookie: m2, csrf: ma.CSRF}
	expect(t, h.do("GET", "/api/memory/MEMORY?principal="+string(pr.ID), nil, member2), http.StatusNotFound)
}

// Memory history names who changed what: an admin's edit by name, Bothy's
// changes with the chat they came from, but only a chat the caller can open.
func TestMemoryHistoryNamesPeopleAndChats(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{mp: {core.FileMemory: "likes tea"}}
	f := newTranscript(t, h, core.RunDone)
	f.sess.Title = "Fix login redirect"
	if err := h.store.Sessions().Update(ctx, f.sess); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	hidden := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: core.UserPrincipal(h.owner.user), Origin: "web",
		Title: "Owner's private plans", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Sessions().Create(ctx, hidden); err != nil {
		t.Fatal(err)
	}
	other := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: hidden.ID, Principal: hidden.Principal, User: h.owner.user,
		Trigger: core.TrigMessage, Status: core.RunDone, CreatedAt: now}
	if err := h.store.Runs().Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := h.memory.Apply(ctx, mp, f.run.ID, core.MemoryOp{File: core.FileMemory, Op: "add", New: "staging resets on Mondays"}, core.ProvUser); err != nil {
		t.Fatal(err)
	}
	if _, err := h.memory.Apply(ctx, mp, other.ID, core.MemoryOp{File: core.FileMemory, Op: "remove", Old: "likes tea"}, core.ProvUser); err != nil {
		t.Fatal(err)
	}
	if _, err := h.memory.Write(ctx, core.SystemViewer(), mp, core.FileMemory, "staging resets on Mondays at 9", "", h.admin.user); err != nil {
		t.Fatal(err)
	}

	rec := h.do("GET", "/api/memory/MEMORY/history", nil, h.member)
	expect(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "Owner's private plans") || strings.Contains(rec.Body.String(), string(other.ID)) {
		t.Fatalf("history names a chat the member cannot open: %s", rec.Body.String())
	}
	hist := decodeAs[dto.List[dto.MemoryChange]](t, rec)
	if len(hist.Items) != 3 {
		t.Fatalf("history = %+v", hist.Items)
	}
	if c := hist.Items[0]; c.Op != "edit" || c.ActorName != "admin" || c.Chat != nil {
		t.Errorf("admin edit = %+v", c)
	}
	if c := hist.Items[1]; c.Op != "remove" || c.ActorName != "" || c.Chat != nil || c.Run != "" || c.Actor != "agent:" {
		t.Errorf("change from a chat the member cannot open = %+v", c)
	}
	if c := hist.Items[2]; c.Op != "add" || c.Chat == nil || c.Chat.ID != f.sess.ID || c.Chat.Title != "Fix login redirect" || c.Run != f.run.ID ||
		c.Actor != "agent:"+string(f.run.ID) {
		t.Errorf("Bothy's change = %+v", c)
	}
}

func TestPersona(t *testing.T) {
	h := newHarness(t).setup()
	if got := decodeAs[map[string]string](t, h.do("GET", "/api/persona", nil, h.member)); got["content"] != "" {
		t.Fatalf("empty persona = %v", got)
	}
	expect(t, h.do("PUT", "/api/persona", map[string]string{"content": strings.Repeat("x", 16<<10+1)}, h.admin), http.StatusBadRequest)
	expect(t, h.do("PUT", "/api/persona", map[string]string{"content": "# Moss\nFriendly and brief."}, h.admin), http.StatusNoContent)
	fi, err := os.Stat(filepath.Join(h.dataDir, "SOUL.md"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("SOUL.md = %v %v", fi, err)
	}
	if got := decodeAs[map[string]string](t, h.do("GET", "/api/persona", nil, h.member)); got["content"] != "# Moss\nFriendly and brief." {
		t.Fatalf("persona = %v", got)
	}
	entries, _ := os.ReadDir(h.dataDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
	var found bool
	for _, e := range h.audit.All() {
		if e.Action == "persona.update" {
			found = true
			if strings.Contains(string(e.Detail), "Friendly") {
				t.Fatal("persona content in audit detail")
			}
		}
	}
	if !found {
		t.Fatal("persona.update not audited")
	}
	h.cfg.update(func(c *configT) { c.Agent.PersonaFile = "../escape.md" })
	expect(t, h.do("GET", "/api/persona", nil, h.member), http.StatusServiceUnavailable)
	c := decodeAs[map[string]string](t, h.do("GET", "/api/constitution", nil, h.member))
	if c["version"] != "fake-1" || c["content"] == "" {
		t.Fatalf("constitution = %v", c)
	}
}

func TestSlackManifestGolden(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("GET", "/api/channels/slack/manifest", nil, h.admin)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `name: "Bothy"`) || rec.Header().Get("Content-Type") != "text/yaml; charset=utf-8" {
		t.Fatalf("default manifest = %s", rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(h.dataDir, "SOUL.md"), []byte("# Moss\n\nYou are Moss.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = h.do("GET", "/api/channels/slack/manifest", nil, h.admin)
	golden, err := os.ReadFile("testdata/slack_manifest.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != string(golden) {
		t.Fatalf("manifest differs from golden:\n%s", rec.Body.String())
	}
	for in, want := range map[string]string{
		"# Evil\" \n  injected: true":           "Evil",
		"name: Ada {{name}}":                    "Ada name",
		"# " + strings.Repeat("Long", 20):       strings.Repeat("Long", 8) + "Lon",
		"no heading here\nName: Fern the Agent": "Fern the Agent",
		"":                                      "Bothy",
	} {
		if got := agentName(strings.NewReader(in)); got != want {
			t.Errorf("agentName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscordInvite(t *testing.T) {
	h := newHarness(t).setup()
	expect(t, h.do("GET", "/api/channels/discord/invite", nil, h.admin), http.StatusConflict)
	h.cfg.update(func(c *configT) { c.Channels.Discord.ApplicationID = "12ab" })
	expect(t, h.do("GET", "/api/channels/discord/invite", nil, h.admin), http.StatusBadRequest)
	h.cfg.update(func(c *configT) { c.Channels.Discord.ApplicationID = "1234567890123" })
	inv := decodeAs[dto.DiscordInvite](t, h.do("GET", "/api/channels/discord/invite", nil, h.admin))
	want := "https://discord.com/oauth2/authorize?client_id=1234567890123&scope=bot%20applications.commands&permissions=274877975552"
	if inv.URL != want {
		t.Fatalf("invite = %s", inv.URL)
	}
	if 1024+2048+65536+274877906944 != discordPermissions {
		t.Fatal("permission arithmetic")
	}
}

func TestSchedulesWithGrants(t *testing.T) {
	h := newHarness(t).setup()
	ss := h.useStoreScheduler()
	sp := h.useStorePolicy()
	ctx := context.Background()
	narrow := []core.Grant{{Kind: core.GrantDomain, Value: "api.weather.example:443", MaxSE: core.SERead}}
	for name, g := range map[string]core.Grant{
		"whole tool": {Kind: core.GrantTool, Tool: "shell", MaxSE: core.SELocalWrite},
		"match all":  {Kind: core.GrantTool, Tool: "shell", Pattern: map[string]string{"/argv/2": "*"}, MaxSE: core.SELocalWrite},
		"admin":      {Kind: core.GrantDomain, Value: "x.example:443", MaxSE: core.SEAdmin},
		"financial":  {Kind: core.GrantRecipient, Value: "telegram:1:", MaxSE: core.SEFinancial},
		"wildcard":   {Kind: core.GrantDomain, Value: "*.example.com:443", MaxSE: core.SERead},
	} {
		rec := h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: name}, Grants: []core.Grant{g}}, h.member)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s grant = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if l := decodeAs[[]core.Schedule](t, h.do("GET", "/api/schedules", nil, h.member)); len(l) != 0 {
		t.Fatalf("rejected grants still created schedules: %+v", l)
	}
	rec := h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather", ID: "sch_forged"}, Grants: narrow}, h.member)
	expect(t, rec, http.StatusOK)
	sc := decodeAs[core.Schedule](t, rec)
	if sc.ID == "sch_forged" || sc.Principal != core.UserPrincipal(h.member.user) || sc.CreatedBy != "user:"+string(h.member.user) {
		t.Fatalf("schedule = %+v", sc)
	}
	v := core.Viewer{Principals: []core.PrincipalID{sc.Principal}}
	gs, _ := h.store.Grants().List(ctx, v, core.GrantSchedule, string(sc.ID))
	if len(gs) != 1 || gs[0].Principal != sc.Principal || gs[0].CreatedBy != h.member.user {
		t.Fatalf("grants = %+v", gs)
	}
	repl := []core.Grant{{Kind: core.GrantRecipient, Value: "telegram:42:", MaxSE: core.SEExternalSend}}
	expect(t, h.do("PATCH", "/api/schedules/"+string(sc.ID), dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather v2"}, Grants: repl}, h.member), http.StatusOK)
	gs, _ = h.store.Grants().List(ctx, v, core.GrantSchedule, string(sc.ID))
	if len(gs) != 1 || gs[0].Kind != core.GrantRecipient || len(sp.revoked) != 1 {
		t.Fatalf("replaced grants = %+v revoked %v", gs, sp.revoked)
	}
	expect(t, h.do("POST", "/api/schedules/"+string(sc.ID)+"/run", nil, h.member), http.StatusAccepted)
	if len(ss.runs) != 1 {
		t.Fatal("run now not forwarded")
	}
	expect(t, h.do("DELETE", "/api/schedules/"+string(sc.ID), nil, h.member), http.StatusNoContent)
	if gs, _ := h.store.Grants().List(ctx, v, core.GrantSchedule, string(sc.ID)); len(gs) != 0 {
		t.Fatalf("grants after delete = %+v", gs)
	}
	// The scheduler writes each change's one audit row, with the request's origin.
	if acts := strings.Join(h.audit.Actions(), " "); strings.Contains(acts, "schedule.") {
		t.Errorf("the handler audited schedule changes itself: %s", acts)
	}
	who := "user:" + string(h.member.user)
	if n := len(ss.origins); n < 3 || ss.origins[n-3].Actor != who || ss.origins[n-3].Detail["grants"] != 1 || ss.origins[n-1].Actor != who || ss.origins[n-1].IP == "" {
		t.Errorf("origins = %+v", ss.origins)
	}
	prev := decodeAs[dto.SchedulePreviewResp](t, h.do("POST", "/api/schedules/preview",
		dto.SchedulePreviewReq{Kind: core.SchedHeartbeat, Spec: "30m", TZ: "Europe/London"}, h.member))
	if len(prev.Next) != 5 || prev.Human != "heartbeat 30m (Europe/London)" {
		t.Fatalf("preview = %+v", prev)
	}
}

func TestConfigEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	view := decodeAs[map[string]json.RawMessage](t, h.do("GET", "/api/config?schema=1", nil, h.admin))
	if _, ok := view["schema"]; !ok {
		t.Fatalf("schema missing: %v", view)
	}
	rec := h.do("PATCH", "/api/config", dto.ConfigPatchReq{Set: map[string]json.RawMessage{"budgets.daily_usd": json.RawMessage("3")}}, h.admin)
	expect(t, rec, http.StatusOK)
	if len(h.cfg.sets) != 1 {
		t.Fatal("Set not called")
	}
	rec = h.do("PATCH", "/api/config", dto.ConfigPatchReq{Set: map[string]json.RawMessage{"bad.path": json.RawMessage("1")}}, h.admin)
	expect(t, rec, http.StatusBadRequest)
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Fields) != 1 || env.Fields[0].Path != "bad.path" {
		t.Fatalf("field errors = %s", rec.Body.String())
	}
	expect(t, h.do("DELETE", "/api/config/budgets/daily_usd", nil, h.admin), http.StatusNoContent)
}

func TestVaultEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	own := "/api/vault/user:" + string(h.member.user) + "/github"
	expect(t, h.do("PUT", own, dto.VaultPutReq{Value: "ghp_xyzxyzxyz"}, h.member), http.StatusOK)
	if l := decodeAs[[]core.SecretMeta](t, h.do("GET", "/api/vault", nil, h.member)); len(l) != 1 || l[0].Name != "github" {
		t.Fatalf("own list = %+v", l)
	}
	if strings.Contains(h.do("GET", "/api/vault", nil, h.member).Body.String(), "ghp_xyzxyzxyz") {
		t.Fatal("value returned")
	}
	expect(t, h.do("PUT", "/api/vault/global/shared", dto.VaultPutReq{Value: "x"}, h.member), http.StatusNotFound)
	expect(t, h.do("PUT", "/api/vault/global/Bad%20Name", dto.VaultPutReq{Value: "x"}, h.admin), http.StatusBadRequest)
	expect(t, h.do("PUT", "/api/vault/global/shared", dto.VaultPutReq{Value: "global-secret"}, h.admin), http.StatusOK)
	expect(t, h.do("GET", "/api/vault?scope=nonsense", nil, h.admin), http.StatusBadRequest)
	if l := decodeAs[[]core.SecretMeta](t, h.do("GET", "/api/vault?scope=user:"+string(h.member.user), nil, h.admin)); len(l) != 1 {
		t.Fatalf("admin view of member scope = %+v", l)
	}
	expect(t, h.do("DELETE", own, nil, h.member), http.StatusNoContent)
	h.env["ANTHROPIC_API_KEY"] = "sk-ant-test-value"
	expect(t, h.do("POST", "/api/vault/import", dto.VaultImportReq{EnvVar: "HOME", Name: "home"}, h.admin), http.StatusBadRequest)
	expect(t, h.do("POST", "/api/vault/import", dto.VaultImportReq{EnvVar: "OPENAI_API_KEY", Name: "openai_api_key"}, h.admin), http.StatusNotFound)
	rec := h.do("POST", "/api/vault/import", dto.VaultImportReq{EnvVar: "ANTHROPIC_API_KEY", Name: "anthropic_api_key"}, h.admin)
	expect(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "sk-ant-test-value") {
		t.Fatal("imported value echoed")
	}
	if s, err := h.vault.Get(context.Background(), core.GlobalScope, "anthropic_api_key", "test"); err != nil || s.Reveal() != "sk-ant-test-value" {
		t.Fatalf("imported = %v", err)
	}
	expect(t, h.do("POST", "/api/vault/unseal", dto.UnsealReq{Passphrase: "pw"}, h.admin), http.StatusOK)
	st := decodeAs[core.VaultStatus](t, h.do("GET", "/api/vault/status", nil, h.admin))
	if st.Sealed || st.Secrets != 2 {
		t.Fatalf("status = %+v", st)
	}
	// The vault audits its own writes; a handler row on top listed each twice.
	acts := strings.Join(h.audit.Actions(), " ")
	if strings.Contains(acts, "vault.put") || strings.Contains(acts, "vault.delete") || !strings.Contains(acts, "vault.unseal") {
		t.Fatalf("audit = %s", acts)
	}
	for _, e := range h.audit.All() {
		if strings.Contains(string(e.Detail), "sk-ant") || strings.Contains(string(e.Detail), "ghp_") {
			t.Fatalf("secret in audit: %s", e.Detail)
		}
	}
}

func TestUsageEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("GET", "/api/usage?from=2026-10-01&to=2026-10-12T00:00:00Z&group_by=model", nil, h.member)
	expect(t, rec, http.StatusOK)
	if r := decodeAs[dto.UsageResp](t, rec); r.Totals.Key != "total" || !r.Totals.PriceKnown {
		t.Fatalf("usage = %+v", r)
	}
	expect(t, h.do("GET", "/api/usage?group_by=planet", nil, h.member), http.StatusBadRequest)
	expect(t, h.do("GET", "/api/usage?from=yesterday", nil, h.member), http.StatusBadRequest)
	today := decodeAs[dto.UsageToday](t, h.do("GET", "/api/usage/today", nil, h.admin))
	if today.GlobalLimit != core.MicrosFromUSD(5) {
		t.Fatalf("today = %+v", today)
	}
	if m := decodeAs[dto.UsageToday](t, h.do("GET", "/api/usage/today", nil, h.member)); m.GlobalLimit != 0 || m.ChatLimit != 0 {
		t.Fatalf("member sees global numbers: %+v", m)
	}
}

// TestUsageTodayChatShare: admins see what chat apps spent today against
// their share of the daily limit, which the Spending page shows instead of
// a banner.
func TestUsageTodayChatShare(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	share := func(pct int) dto.UsageToday {
		t.Helper()
		h.cfg.mu.Lock()
		h.cfg.cfg.Budgets.ChatSharePct = pct
		h.cfg.mu.Unlock()
		return decodeAs[dto.UsageToday](t, h.do("GET", "/api/usage/today", nil, h.admin))
	}
	let := core.IdentityTeammate
	h.touch("telegram", "1", "Ana", core.MemberUnknown)
	expect(t, h.do("PATCH", "/api/identities/telegram/1", dto.IdentityPatchReq{Status: &let}, h.owner), http.StatusOK)
	ana, _ := h.store.Identities().Get(ctx, "telegram", "1")
	day := time.Now().In(h.srv.location()).Format(time.DateOnly)
	if err := h.store.Usage().AddSpend(ctx, ana.Principal, day, core.CallInfo{Cost: core.MicrosFromUSD(2.5)}); err != nil {
		t.Fatal(err)
	}
	if got := share(60); got.ChatLimit != core.MicrosFromUSD(3) || got.ChatSpent != core.MicrosFromUSD(2.5) {
		t.Fatalf("with a 60%% share: %+v", got)
	}
	// "All of it" is no separate share.
	if got := share(100); got.ChatLimit != 0 || got.ChatSpent != 0 {
		t.Fatalf("without a share: %+v", got)
	}
}

func TestAdminEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	// The runner's persisted flag is the kill switch; the scheduler reads it
	// (Runner.Paused), so a /resume from chat releases the scheduler too. A
	// copy in the scheduler's own flag would outlive that /resume.
	expect(t, h.do("POST", "/api/admin/pause", dto.PauseReq{Paused: true}, h.admin), http.StatusNoContent)
	if !h.runner.Paused() || h.scheduler.PausedV || h.runner.StopAlls != 1 {
		t.Fatalf("pause: runner %v scheduler's own flag %v stopall %d", h.runner.Paused(), h.scheduler.PausedV, h.runner.StopAlls)
	}
	if bs := h.bus.Banners(core.Filter{Admin: true}); len(bs) != 1 || bs[0].Data.(core.BannerData).Code != "paused" {
		t.Fatalf("banners = %+v", bs)
	}
	expect(t, h.do("POST", "/api/admin/pause", dto.PauseReq{Paused: false}, h.admin), http.StatusNoContent)
	if h.runner.Paused() || len(h.bus.Banners(core.Filter{Admin: true})) != 0 {
		t.Fatal("resume")
	}
	expect(t, h.do("POST", "/api/admin/restart", nil, h.admin), http.StatusAccepted)
	if h.restarts.Load() != 1 {
		t.Fatal("restart not requested")
	}
	expect(t, h.do("POST", "/api/admin/backup", nil, h.admin), http.StatusServiceUnavailable)
	acts := strings.Join(h.audit.Actions(), " ")
	for _, a := range []string{"admin.pause", "admin.resume", "admin.restart", "backup.create"} {
		if !strings.Contains(acts, a) {
			t.Errorf("%s not audited (%s)", a, acts)
		}
	}
	ctx := context.Background()
	old := &core.TokenRecord{Name: "old", User: h.owner.user, SecretHash: []byte("0123456789abcdef0123456789abcdef"), Scopes: []string{"read"},
		OnApproval: "deny", RatePerMin: 30, CreatedAt: time.Now().Add(-2 * time.Hour), Expires: time.Now().Add(-time.Hour)}
	if err := h.store.Tokens().Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	rep := decodeAs[dto.DoctorReport](t, h.do("POST", "/api/security-audit", dto.SecurityAuditReq{Fix: true}, h.admin))
	if !slices.Contains(rep.Fixed, "tokens.expired_unrevoked") {
		t.Fatalf("security audit = %+v", rep)
	}
	for _, f := range rep.Findings {
		if f.ID == "tokens.expired_unrevoked" {
			t.Fatal("finding survived the fix")
		}
	}
	if rep := decodeAs[dto.DoctorReport](t, h.do("GET", "/api/doctor", nil, h.admin)); rep.Findings == nil {
		t.Fatal("findings must be a list")
	}
}

type planTool struct{ se core.SideEffect }

func (p planTool) Spec() core.ToolSpec {
	return core.ToolSpec{Def: core.ToolDef{Name: "shell", Schema: json.RawMessage(`{"type":"object"}`)}, Source: "builtin",
		Manifest: core.Manifest{Title: "Run shell command", SideEffect: p.se}}
}
func (p planTool) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	return &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: p.se, Argv: []string{"bash", "-lc", "ls"}, Payload: args}, nil
}
func (p planTool) Exec(context.Context, *core.ToolEnv, *core.Action) (*core.ToolResult, error) {
	panic("preview must never execute")
}

func TestPolicyPreview(t *testing.T) {
	h := newHarness(t).setup()
	req := dto.PolicyPreviewReq{Tool: "shell", Args: json.RawMessage(`{"cmd":"ls"}`)}
	expect(t, h.do("POST", "/api/policy/preview", req, h.admin), http.StatusServiceUnavailable)
	h.srv.tools = &fakes.Registry{Tools: map[string]core.Tool{"shell": planTool{se: core.SELocalWrite}}}
	h.policy.Verdicts = map[string]core.Verdict{"shell": core.VerdictAsk}
	rec := h.do("POST", "/api/policy/preview", req, h.admin)
	expect(t, rec, http.StatusOK)
	resp := decodeAs[dto.PolicyPreviewResp](t, rec)
	if resp.Action == nil || resp.Decision.Verdict != core.VerdictAsk || resp.Card == nil || resp.Card.Title != "Run shell command" {
		t.Fatalf("preview = %+v", resp)
	}
	if in := h.policy.Inputs[0]; in.Trigger != core.TrigMessage || in.Principal.ID != core.UserPrincipal(h.admin.user) {
		t.Fatalf("policy input = %+v", in)
	}
	expect(t, h.do("POST", "/api/policy/preview", dto.PolicyPreviewReq{Tool: "nope"}, h.admin), http.StatusNotFound)
	if len(h.approvals.All()) != 0 {
		t.Fatal("preview persisted an approval")
	}
}

func TestGrantEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	sp := h.useStorePolicy()
	mp := core.UserPrincipal(h.member.user)
	rec := h.do("POST", "/api/grants", dto.GrantCreateReq{Principal: mp, Kind: core.GrantDomain, Value: "*.wikipedia.org:443", MaxSE: core.SERead}, h.admin)
	expect(t, rec, http.StatusOK)
	g := decodeAs[core.Grant](t, rec)
	if g.Scope != core.GrantPrincipal || g.CreatedBy != h.admin.user || g.ID == "" {
		t.Fatalf("grant = %+v", g)
	}
	expect(t, h.do("POST", "/api/grants", dto.GrantCreateReq{Principal: mp, Kind: core.GrantTool, Tool: "shell", MaxSE: core.SELocalWrite}, h.admin), http.StatusBadRequest)
	expect(t, h.do("POST", "/api/grants", dto.GrantCreateReq{Principal: "usr_nope", Kind: core.GrantDomain, Value: "a.b:443", MaxSE: core.SERead}, h.admin), http.StatusNotFound)
	if l := decodeAs[[]dto.GrantView](t, h.do("GET", "/api/grants", nil, h.member)); len(l) != 1 || l[0].CreatedByName != "admin" ||
		l[0].PrincipalName != "member" {
		t.Fatalf("member grants = %+v", l)
	}
	// Admins manage everyone's grants, teammates' and channels' included.
	if l := decodeAs[[]dto.GrantView](t, h.do("GET", "/api/grants", nil, h.owner)); len(l) != 1 {
		t.Fatalf("owner grants = %+v", l)
	}
	if l := decodeAs[[]dto.GrantView](t, h.do("GET", "/api/grants", nil, h.tokenFor(h.owner, "read"))); len(l) != 0 {
		t.Fatalf("an owner's read token sees everyone's grants: %+v", l)
	}
	expect(t, h.do("DELETE", "/api/grants/"+string(g.ID), nil, h.owner), http.StatusNoContent) // admin
	if len(sp.revoked) != 1 {
		t.Fatal("not revoked")
	}
}

func TestApprovalsAndRuns(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	act := &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Payload: []byte(`{}`)}
	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: mp, User: h.member.user, Trigger: core.TrigMessage,
		Status: core.RunDone, CreatedAt: time.Now()}
	if err := h.store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	a1, _ := h.approvals.Request(ctx, core.ApprovalRequest{Run: run.ID, Session: sess.ID, Principal: mp, Action: act,
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{User: h.member.user}}})
	a2, _ := h.approvals.Request(ctx, core.ApprovalRequest{Run: run.ID, Session: sess.ID, Principal: mp, Action: act,
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{AdminOnly: true}}})
	pend := decodeAs[dto.List[*core.Approval]](t, h.do("GET", "/api/approvals?status=pending", nil, h.member))
	if len(pend.Items) != 2 {
		t.Fatalf("pending = %d", len(pend.Items))
	}
	if l := decodeAs[dto.List[*core.Approval]](t, h.do("GET", "/api/approvals", nil, h.admin)); len(l.Items) != 1 || l.Items[0].ID != a2.ID {
		t.Fatalf("admin inbox = %+v", l.Items)
	}
	expect(t, h.do("POST", "/api/approvals/"+string(a1.ID), dto.ApprovalResolveReq{Choice: "yolo"}, h.member), http.StatusBadRequest)
	expect(t, h.do("POST", "/api/approvals/"+string(a1.ID), dto.ApprovalResolveReq{Choice: core.Deny}, h.member), http.StatusOK)
	expect(t, h.do("POST", "/api/approvals/"+string(a1.ID), dto.ApprovalResolveReq{Choice: core.Deny}, h.member), http.StatusConflict)
	recent := decodeAs[dto.List[*core.Approval]](t, h.do("GET", "/api/approvals?status=recent", nil, h.member))
	if len(recent.Items) != 1 || recent.Items[0].Status != core.ApprovalDenied {
		t.Fatalf("recent = %+v", recent)
	}
	expect(t, h.do("GET", "/api/approvals?status=weird", nil, h.member), http.StatusBadRequest)
	detail := decodeAs[dto.RunDetail](t, h.do("GET", "/api/runs/"+string(run.ID), nil, h.member))
	if detail.Run.ID != run.ID || len(detail.Approvals) != 0 && detail.Approvals[0].Run != run.ID || detail.LLMCalls == nil || detail.ToolCalls == nil {
		t.Fatalf("run detail = %+v", detail)
	}
	runs := decodeAs[dto.List[core.Run]](t, h.do("GET", "/api/runs?session="+string(sess.ID), nil, h.member))
	if len(runs.Items) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	expect(t, h.do("POST", "/api/runs/"+string(run.ID)+"/continue", nil, h.member), http.StatusAccepted)
	expect(t, h.do("GET", "/api/runs?limit=0", nil, h.member), http.StatusBadRequest)
}

func TestSandboxInfo(t *testing.T) {
	h := newHarness(t).setup()
	p, _ := h.store.Principals().Get(context.Background(), core.SystemViewer(), core.UserPrincipal(h.member.user))
	h.sandbox.UsedBytes = map[core.WorkspaceID]int64{p.Workspace: 5 << 20}
	h.sandbox.QuotaBytes = 100 << 20
	info := decodeAs[dto.SandboxInfo](t, h.do("GET", "/api/sandbox", nil, h.member))
	if !info.Status.Available || info.UsedMB != 5 || info.QuotaMB != 100 || info.Compose != "" {
		t.Fatalf("info = %+v", info)
	}
	h.sandbox.SetStatus(core.SandboxStatus{Backend: "none", Reason: "no sandboxd"})
	info = decodeAs[dto.SandboxInfo](t, h.do("GET", "/api/sandbox", nil, h.member))
	// deploy/compose.yaml keeps digest placeholders and install.sh deployments
	// have no deploy/ directory, so the hint follows the install.sh flow.
	if info.Status.Available || strings.Contains(info.Compose, "deploy/compose.yaml") || !strings.Contains(info.Compose, "install.sh --compose") {
		t.Fatalf("unavailable info = %+v", info)
	}
}

func TestMiscReads(t *testing.T) {
	h := newHarness(t).setup()
	h.skills.Metas = []core.SkillMeta{{Name: "pdf", Status: core.SkillEnabled, Hash: "h", ApprovedHash: "h"}, {Name: "new", Status: core.SkillPending}}
	h.skills.Bodies["pdf"] = "# PDF"
	if l := decodeAs[[]core.SkillMeta](t, h.do("GET", "/api/skills", nil, h.member)); len(l) != 1 {
		t.Fatalf("member skills = %+v", l)
	}
	if l := decodeAs[[]core.SkillMeta](t, h.do("GET", "/api/skills", nil, h.admin)); len(l) != 2 {
		t.Fatalf("admin skills = %+v", l)
	}
	expect(t, h.do("GET", "/api/skills/new", nil, h.member), http.StatusNotFound)
	if d := decodeAs[dto.SkillDetail](t, h.do("GET", "/api/skills/pdf", nil, h.member)); d.Body != "# PDF" || d.Files == nil {
		t.Fatalf("skill = %+v", d)
	}
	expect(t, h.do("PATCH", "/api/skills/new", dto.SkillPatchReq{Enabled: true}, h.admin), http.StatusOK)
	if l := decodeAs[[]core.ModelInfo](t, h.do("GET", "/api/models", nil, h.member)); len(l) != 1 {
		t.Fatal("models")
	}
	h.memory.Files = nil
	if res := decodeAs[dto.SearchResp](t, h.do("GET", "/api/search?q=", nil, h.member)); res.Chats == nil || len(res.Chats)+len(res.Notes)+len(res.Code) != 0 {
		t.Fatalf("empty search = %+v", res)
	}
	if l := decodeAs[[]core.ChannelStatus](t, h.do("GET", "/api/channels", nil, h.admin)); l == nil {
		t.Fatal("channels must be a list")
	}
	expect(t, h.do("POST", "/api/channels/telegram/test", nil, h.admin), http.StatusNotFound)
	expect(t, h.do("GET", "/api/audit/verify", nil, h.admin), http.StatusOK)
	for _, act := range []string{"user.create", "user.update", "token.create"} {
		if _, err := h.store.Audit().Append(context.Background(), &core.AuditEntry{At: time.Now(), Actor: "system", Action: act, Outcome: "ok",
			Detail: json.RawMessage("{}")}, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	audit := decodeAs[dto.List[*core.AuditEntry]](t, h.do("GET", "/api/audit?action=user.&limit=1", nil, h.admin))
	if len(audit.Items) != 1 || audit.Items[0].Action != "user.update" || audit.Next != audit.Items[0].ID {
		t.Fatalf("audit page = %+v", audit)
	}
	next := decodeAs[dto.List[*core.AuditEntry]](t, h.do("GET", "/api/audit?action=user.&limit=1&before="+itoa(audit.Next), nil, h.admin))
	if len(next.Items) != 1 || next.Items[0].Action != "user.create" {
		t.Fatalf("second page = %+v", next)
	}
}

func TestSanitizeNameAndSniff(t *testing.T) {
	long := strings.Repeat("é", 100) + ".txt"
	got := sanitizeName(long)
	if len(got) > maxFileName || !strings.HasSuffix(got, ".txt") || !utf8.ValidString(got) {
		t.Fatalf("sanitizeName(long) = %q", got)
	}
	for in, want := range map[string]string{"..\\..\\boot.ini": "boot.ini", ".hidden": "hidden", "a b/c?.pdf": "c_.pdf", "": "file", "..": "file"} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sniffType("x.html", []byte("plain words")); strings.Contains(got, "html") {
		t.Fatalf("html extension trusted: %s", got)
	}
	if got := sniffType("data.csv", []byte("a,b\n1,2\n")); got != "text/csv; charset=utf-8" {
		t.Fatalf("csv = %s", got)
	}
}
