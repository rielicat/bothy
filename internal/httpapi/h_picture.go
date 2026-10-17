package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const (
	pictureMax = 5 << 20 // internal/picture.MaxUpload
	// pictureApplyTimeout bounds waiting for the chat apps; the hub gives
	// each 30 s and runs them side by side.
	pictureApplyTimeout = time.Minute
)

var errNoPicture = newError(http.StatusServiceUnavailable, "unavailable", "Bothy's picture isn't available on this server")

func (s *Server) pictureRoutes() []route {
	return []route{
		{pattern: "GET /api/agent/picture", access: accessUser, h: s.getPicture},
		{pattern: "GET /api/agent/picture/info", access: accessUser, h: s.pictureInfo},
		{pattern: "PUT /api/agent/picture", access: accessAdmin, h: s.putPicture, limit: pictureMax + 64<<10},
		{pattern: "DELETE /api/agent/picture", access: accessAdmin, h: s.resetPicture},
	}
}

func (s *Server) currentPicture(ctx context.Context) (core.Picture, error) {
	if s.d.Picture == nil {
		return core.Picture{}, errNoPicture
	}
	return s.d.Picture.Current(ctx)
}

// getPicture serves the picture itself. The address never changes, so the
// browser revalidates with the ETag instead of keeping a stale copy.
func (s *Server) getPicture(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	p, err := s.currentPicture(r.Context())
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", `"`+p.Version+`"`)
	if r.URL.Query().Get("download") == "1" {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": pictureFileName(s.cfg().Agent.Name)}))
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(p.PNG))
	return nil
}

func (s *Server) pictureInfo(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	p, err := s.currentPicture(r.Context())
	if err != nil {
		return err
	}
	return ok(w, pictureView(p))
}

func (s *Server) putPicture(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.d.Picture == nil {
		return errNoPicture
	}
	raw, err := readPictureUpload(r)
	if err != nil {
		return err
	}
	p, err := s.d.Picture.Set(r.Context(), raw)
	if err != nil {
		return err
	}
	apps := s.applyPicture(r, p)
	s.audit(r, a, "picture.update", "agent:picture", "ok", map[string]any{"bytes": len(raw), "version": p.Version, "apps": pictureOutcomes(apps)})
	return ok(w, dto.PictureChange{Picture: pictureView(p), Apps: apps})
}

func (s *Server) resetPicture(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.d.Picture == nil {
		return errNoPicture
	}
	p, err := s.d.Picture.Reset(r.Context())
	if err != nil {
		return err
	}
	apps := s.applyPicture(r, p)
	s.audit(r, a, "picture.reset", "agent:picture", "ok", map[string]any{"version": p.Version, "apps": pictureOutcomes(apps)})
	return ok(w, dto.PictureChange{Picture: pictureView(p), Apps: apps})
}

// readPictureUpload takes the "file" part of a multipart form, else the raw
// body. The declared type is never trusted: the picture store sniffs it.
func readPictureUpload(r *http.Request) ([]byte, error) {
	var src io.Reader = r.Body
	if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return nil, badRequest("the form has no file part")
			}
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, err
			}
			if err != nil {
				return nil, badRequest("unreadable form")
			}
			if part.FormName() == "file" {
				src = part
				break
			}
		}
	}
	raw, err := io.ReadAll(io.LimitReader(src, pictureMax+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, err
		}
		return nil, badRequest("unreadable upload")
	}
	switch {
	case len(raw) > pictureMax:
		return nil, newError(http.StatusRequestEntityTooLarge, "bad_request", "That picture is over 5 MB. Choose a smaller one.")
	case len(raw) == 0:
		return nil, badRequest("Choose a picture to upload.")
	}
	return raw, nil
}

// applyPicture hands the new picture to the chat apps. The change is made
// by now, so a browser that goes away doesn't stop them catching up.
func (s *Server) applyPicture(r *http.Request, p core.Picture) []dto.PictureAppResult {
	out := []dto.PictureAppResult{}
	if s.d.Hub == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pictureApplyTimeout)
	defer cancel()
	for _, res := range s.d.Hub.ApplyPicture(ctx, p) {
		out = append(out, dto.PictureAppResult{App: res.App, Result: res.Outcome, Detail: pictureDetail(res)})
	}
	return out
}

func pictureView(p core.Picture) dto.PictureInfo {
	return dto.PictureInfo{Custom: p.Custom, Version: p.Version}
}

func pictureOutcomes(apps []dto.PictureAppResult) map[string]string {
	out := make(map[string]string, len(apps))
	for _, a := range apps {
		out[a.App] = string(a.Result)
	}
	return out
}

// pictureDetail says in plain words what happened in one chat app.
func pictureDetail(r core.PictureResult) string {
	app := appTitle(r.App)
	switch r.Outcome {
	case core.PictureUpdated:
		return "Updated in " + app + "."
	case core.PictureManual:
		if r.App == "slack" {
			return "In Slack, upload it under App icon in Slack's app settings."
		}
		return "Change it in " + app + "'s own settings."
	}
	switch {
	case errors.Is(r.Err, core.ErrNotConnected):
		return app + " isn't connected right now, so its picture wasn't changed. Try again once it's back."
	case core.CodeOf(r.Err) == core.FailRateLimited && r.App == "discord":
		return "Discord only allows a few picture changes an hour. Try again later."
	case core.CodeOf(r.Err) == core.FailRateLimited:
		return app + " is limiting changes right now. Try again later."
	}
	return "Couldn't change the picture in " + app + ". Try again later."
}

// pictureFileName is "<name>-picture.png" with the assistant's name kept to
// letters, digits, dashes and underscores.
func pictureFileName(name string) string {
	var b strings.Builder
	for _, r := range strings.Join(strings.Fields(name), "-") {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	base := strings.Trim(b.String(), "-_")
	if base == "" {
		base = "Bothy"
	}
	return base + "-picture.png"
}
