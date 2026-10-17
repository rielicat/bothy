package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// fakePicture keeps whatever it is given, refusing anything that isn't
// marked "pic:".
type fakePicture struct {
	mu      sync.Mutex
	cur     core.Picture
	uploads [][]byte
}

var fakeDefault = core.Picture{PNG: []byte("\x89PNG default"), JPEG: []byte("jpg"), Version: "0123456789abcdef"}

type refusal string

func (r refusal) Error() string { return string(r) }
func (r refusal) Unwrap() error { return core.ErrInvalid }

func (f *fakePicture) Current(context.Context) (core.Picture, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur, nil
}

func (f *fakePicture) Set(_ context.Context, raw []byte) (core.Picture, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, raw)
	if !bytes.HasPrefix(raw, []byte("pic:")) {
		return core.Picture{}, refusal("That file isn't a picture Bothy can use. Choose another one.")
	}
	f.cur = core.Picture{PNG: append([]byte("\x89PNG "), raw...), JPEG: []byte("jpg"), Custom: true, Version: fmt.Sprintf("%016x", len(raw))}
	return f.cur, nil
}

func (f *fakePicture) Reset(context.Context) (core.Picture, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cur = fakeDefault
	return f.cur, nil
}

func (f *fakePicture) last() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.uploads) == 0 {
		return nil
	}
	return f.uploads[len(f.uploads)-1]
}

func withPicture(h *harness) *fakePicture {
	fp := &fakePicture{cur: fakeDefault}
	h.srv.d.Picture = fp
	return fp
}

func TestGetPicture(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Agent.Name = "Acme Helper" }).setup()
	withPicture(h)
	rec := h.do("GET", "/api/agent/picture", nil, h.member)
	expect(t, rec, http.StatusOK)
	hd := rec.Header()
	if hd.Get("Content-Type") != "image/png" || hd.Get("ETag") != `"0123456789abcdef"` || hd.Get("Cache-Control") != "private, no-cache" ||
		hd.Get("Content-Disposition") != "" || rec.Body.String() != string(fakeDefault.PNG) {
		t.Fatalf("headers %v body %q", hd, rec.Body.String())
	}
	expect(t, h.do("GET", "/api/agent/picture", nil, h.member, withHeader("If-None-Match", `"0123456789abcdef"`)), http.StatusNotModified)
	rec = h.do("GET", "/api/agent/picture?download=1", nil, h.member)
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename=Acme-Helper-picture.png` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	expect(t, h.do("GET", "/api/agent/picture", nil, h.tokenFor(h.member, "read")), http.StatusOK)
	expect(t, h.do("GET", "/api/agent/picture", nil, nil), http.StatusUnauthorized)

	info := decodeAs[dto.PictureInfo](t, h.do("GET", "/api/agent/picture/info", nil, h.member))
	if info != (dto.PictureInfo{Custom: false, Version: "0123456789abcdef"}) {
		t.Fatalf("info = %+v", info)
	}
}

func TestPutPicture(t *testing.T) {
	h := newHarness(t).setup()
	fp := withPicture(h)
	h.hub.PictureResults = []core.PictureResult{
		{App: "discord", Outcome: core.PictureFailed, Err: picRateLimited{}},
		{App: "slack", Outcome: core.PictureManual},
		{App: "telegram", Outcome: core.PictureUpdated},
	}
	expect(t, h.do("PUT", "/api/agent/picture", "pic:raw", h.member), http.StatusForbidden)
	expect(t, h.do("PUT", "/api/agent/picture", "pic:raw", h.admin, noCSRF()), http.StatusForbidden)
	if fp.last() != nil {
		t.Fatal("a refused request reached the picture store")
	}

	// The declared type is not what decides: the bytes go to the store.
	rec := h.do("PUT", "/api/agent/picture", "pic:raw", h.admin, withHeader("Content-Type", "text/plain"))
	expect(t, rec, http.StatusOK)
	got := decodeAs[dto.PictureChange](t, rec)
	want := dto.PictureChange{Picture: dto.PictureInfo{Custom: true, Version: "0000000000000007"}, Apps: []dto.PictureAppResult{
		{App: "discord", Result: core.PictureFailed, Detail: "Discord only allows a few picture changes an hour. Try again later."},
		{App: "slack", Result: core.PictureManual, Detail: "In Slack, upload it under App icon in Slack's app settings."},
		{App: "telegram", Result: core.PictureUpdated, Detail: "Updated in Telegram."},
	}}
	if b, w := mustJSON(t, got), mustJSON(t, want); b != w {
		t.Fatalf("change = %s\nwant %s", b, w)
	}
	if string(fp.last()) != "pic:raw" || len(h.hub.Pictures) != 1 || !h.hub.Pictures[0].Custom {
		t.Fatalf("upload %q, hub got %+v", fp.last(), h.hub.Pictures)
	}
	var audited bool
	for _, e := range h.audit.All() {
		if e.Action == "picture.update" {
			audited = true
			if e.Target != "agent:picture" || e.Outcome != "ok" ||
				!strings.Contains(string(e.Detail), `"apps":{"discord":"failed","slack":"manual","telegram":"updated"}`) {
				t.Fatalf("audit = %+v %s", e, e.Detail)
			}
		}
	}
	if !audited {
		t.Fatal("picture.update not audited")
	}

	// A browser upload: the form's file part.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "ignored")
	fw, _ := mw.CreateFormFile("file", "logo.png")
	_, _ = fw.Write([]byte("pic:form"))
	_ = mw.Close()
	expect(t, h.do("PUT", "/api/agent/picture", buf.Bytes(), h.owner, withHeader("Content-Type", mw.FormDataContentType())), http.StatusOK)
	if string(fp.last()) != "pic:form" {
		t.Fatalf("form upload = %q", fp.last())
	}
}

func TestPutPictureRefusals(t *testing.T) {
	h := newHarness(t).setup()
	fp := withPicture(h)
	rec := h.do("PUT", "/api/agent/picture", "GIF89a", h.admin)
	expect(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "That file isn't a picture Bothy can use. Choose another one.") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	rec = h.do("PUT", "/api/agent/picture", bytes.Repeat([]byte("p"), pictureMax+1), h.admin)
	expect(t, rec, http.StatusRequestEntityTooLarge)
	if !strings.Contains(rec.Body.String(), "over 5 MB") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	expect(t, h.do("PUT", "/api/agent/picture", bytes.Repeat([]byte("p"), pictureMax+1<<20), h.admin), http.StatusRequestEntityTooLarge)
	expect(t, h.do("PUT", "/api/agent/picture", "", h.admin), http.StatusBadRequest)
	if len(h.hub.Pictures) != 0 {
		t.Fatal("a refused picture reached the chat apps")
	}
	if cur, _ := fp.Current(context.Background()); cur.Custom {
		t.Fatal("a refused picture replaced the current one")
	}
}

func TestResetPicture(t *testing.T) {
	h := newHarness(t).setup()
	fp := withPicture(h)
	fp.cur = core.Picture{PNG: []byte("custom"), Custom: true, Version: "c"}
	h.hub.PictureResults = []core.PictureResult{{App: "telegram", Outcome: core.PictureFailed, Err: fmt.Errorf("telegram is backoff: %w", core.ErrNotConnected)}}
	expect(t, h.do("DELETE", "/api/agent/picture", nil, h.member), http.StatusForbidden)
	got := decodeAs[dto.PictureChange](t, h.do("DELETE", "/api/agent/picture", nil, h.admin))
	if got.Picture != (dto.PictureInfo{Version: fakeDefault.Version}) || len(got.Apps) != 1 ||
		got.Apps[0].Detail != "Telegram isn't connected right now, so its picture wasn't changed. Try again once it's back." {
		t.Fatalf("reset = %+v", got)
	}
	if len(h.hub.Pictures) != 1 || h.hub.Pictures[0].Custom {
		t.Fatalf("hub got %+v", h.hub.Pictures)
	}
	var audited bool
	for _, e := range h.audit.All() {
		audited = audited || e.Action == "picture.reset"
	}
	if !audited {
		t.Fatal("picture.reset not audited")
	}
}

func TestPictureUnavailable(t *testing.T) {
	h := newHarness(t).setup()
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		rec := h.do(m, "/api/agent/picture", nil, h.admin)
		if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "unavailable" {
			t.Fatalf("%s = %d %s", m, rec.Code, rec.Body.String())
		}
	}
}

func TestPictureDetail(t *testing.T) {
	for _, c := range []struct {
		r    core.PictureResult
		want string
	}{
		{core.PictureResult{App: "discord", Outcome: core.PictureUpdated}, "Updated in Discord."},
		{core.PictureResult{App: "telegram", Outcome: core.PictureFailed, Err: picRateLimited{}}, "Telegram is limiting changes right now. Try again later."},
		{core.PictureResult{App: "telegram", Outcome: core.PictureFailed, Err: errors.New("boom")}, "Couldn't change the picture in Telegram. Try again later."},
		{core.PictureResult{App: "matrix", Outcome: core.PictureManual}, "Change it in Matrix's own settings."},
	} {
		if got := pictureDetail(c.r); got != c.want {
			t.Errorf("%+v: %q, want %q", c.r, got, c.want)
		}
	}
	for name, want := range map[string]string{"Bothy": "Bothy-picture.png", "  Ada  Lovelace ": "Ada-Lovelace-picture.png",
		"Zoë's \"bot\"/..": "Zoës-bot-picture.png", "<>": "Bothy-picture.png"} {
		if got := pictureFileName(name); got != want {
			t.Errorf("pictureFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

type picRateLimited struct{}

func (picRateLimited) Error() string                 { return "429" }
func (picRateLimited) FailureCode() core.FailureCode { return core.FailRateLimited }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
