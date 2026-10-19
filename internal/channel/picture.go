package channel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// pictureTimeout bounds one chat app's picture check or upload.
	pictureTimeout = 30 * time.Second
	// pictureReadTimeout bounds reading back what the bot shows after an upload.
	pictureReadTimeout = 10 * time.Second
)

// pictureKey is the meta key recording what Bothy knows of the bot's picture
// on that chat app: "<bot id>" once it looked after a bot whose picture is
// its own (BotFather, the developer portal), or "<bot id> <version>
// <picture id>" when Bothy gave the bot core.Picture <version>, which the
// chat app shows as PictureSetter.PictureID <picture id>. A restart then
// asks nothing, and a picture Bothy gave can follow Bothy's.
func pictureKey(app string) string { return "picture:" + app }

// firstPicture looks after a bot's picture when it connects. A bot without
// one gets Bothy's. A bot still showing the picture Bothy gave it gets
// Bothy's current one when that changed since (a new built-in default, or
// an admin's change while the chat app was off). A picture someone set
// themselves stays, even over one Bothy gave: only an admin's change in
// Bothy (ApplyPicture) replaces it.
func (h *Hub) firstPicture(ctx context.Context, a *adapter) {
	ps, ok := a.ch.(core.PictureSetter)
	if !ok || h.d.Picture == nil {
		return
	}
	a.pictureMu.Lock()
	defer a.pictureMu.Unlock()
	id := cmp.Or(ps.BotID(), "?")
	rec, err := h.d.Store.Meta().Get(ctx, pictureKey(a.name))
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		h.log.Warn("channel: reading what Bothy knows of the bot's picture failed", "channel", a.name, "err", err)
		return
	}
	f := strings.Fields(rec)
	known := len(f) > 0 && f[0] == id
	gave := known && len(f) == 3
	if known && !gave {
		return
	}
	p, err := h.d.Picture.Current(ctx)
	if err != nil {
		h.log.Warn("channel: reading Bothy's picture failed", "channel", a.name, "err", err)
		return
	}
	if gave && f[1] == p.Version {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pictureTimeout)
	defer cancel()
	shown, err := ps.PictureID(pctx)
	if err != nil {
		h.log.Warn("channel: can't tell what picture the bot has", "channel", a.name, "err", err)
		return
	}
	if gave && shown != f[2] || !gave && shown != "" {
		h.rememberPicture(ctx, a, id)
		return
	}
	if err := ps.SetPicture(pctx, p); err != nil {
		h.log.Warn("channel: giving the bot Bothy's picture failed", "channel", a.name, "err", err)
		return
	}
	h.log.Info("channel: gave the bot Bothy's picture", "channel", a.name, "identity", a.ch.Status().Identity, "custom", p.Custom, "replaced", gave)
	h.rememberGiven(ctx, a, ps, id, p.Version)
}

func (h *Hub) rememberPicture(ctx context.Context, a *adapter, v string) {
	if err := h.d.Store.Meta().Set(ctx, pictureKey(a.name), v); err != nil && ctx.Err() == nil {
		h.log.Warn("channel: remembering the bot's picture failed", "channel", a.name, "err", err)
	}
}

// rememberGiven records that the bot now shows Bothy's picture version.
// When the chat app doesn't say what it shows ("?" is a picture it couldn't
// name), the picture counts as the bot's own from then on: Bothy never
// replaces what it can't recognise. The read-back has its own time, so a
// slow upload that went through still gets recorded.
func (h *Hub) rememberGiven(ctx context.Context, a *adapter, ps core.PictureSetter, id, version string) {
	rctx, cancel := context.WithTimeout(ctx, pictureReadTimeout)
	defer cancel()
	shown, err := ps.PictureID(rctx)
	if err != nil || shown == "" || shown == "?" || strings.ContainsAny(shown, " \t\n") {
		h.log.Warn("channel: can't tell what picture the bot shows now", "channel", a.name, "err", err)
		h.rememberPicture(ctx, a, id)
		return
	}
	h.rememberPicture(ctx, a, id+" "+version+" "+shown)
}

// ApplyPicture gives every turned-on chat app Bothy's picture, replacing the
// bot's (core.ChannelHub). It is an admin's explicit choice, so unlike
// firstPicture it overwrites; apps run in parallel, each bounded by
// pictureTimeout.
func (h *Hub) ApplyPicture(ctx context.Context, p core.Picture) []core.PictureResult {
	cur := h.cfg()
	var live []*adapter
	for _, a := range h.sortedAdapters() {
		if enabled, _, _ := wantState(cur, a); enabled {
			live = append(live, a)
		}
	}
	out := make([]core.PictureResult, len(live))
	var wg sync.WaitGroup
	for i, a := range live {
		out[i] = core.PictureResult{App: a.name, Outcome: core.PictureManual}
		ps, ok := a.ch.(core.PictureSetter)
		if !ok {
			continue
		}
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					h.log.Error("channel: recovered from a panic", "in", "picture", "channel", a.name, "panic", r)
					out[i] = core.PictureResult{App: a.name, Outcome: core.PictureFailed, Err: fmt.Errorf("channel %s: panic: %v", a.name, r)}
				}
			}()
			out[i] = h.applyPicture(ctx, a, ps, p)
		})
	}
	wg.Wait()
	return out
}

func (h *Hub) applyPicture(ctx context.Context, a *adapter, ps core.PictureSetter, p core.Picture) core.PictureResult {
	r := core.PictureResult{App: a.name, Outcome: core.PictureFailed}
	a.pictureMu.Lock()
	defer a.pictureMu.Unlock()
	// Two changes at once can take the lock in either order; sending what
	// Bothy holds now rather than the caller's p leaves the later one.
	if h.d.Picture != nil {
		if cur, err := h.d.Picture.Current(ctx); err == nil {
			p = cur
		}
	}
	st := a.ch.Status()
	if st.State != "connected" {
		r.Err = fmt.Errorf("channel %s is %s: %w", a.name, cmp.Or(st.State, "starting"), core.ErrNotConnected)
		return r
	}
	pctx, cancel := context.WithTimeout(ctx, pictureTimeout)
	defer cancel()
	if err := ps.SetPicture(pctx, p); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("channel %s: no answer in %v: %w", a.name, pictureTimeout, err)
		}
		h.log.Warn("channel: changing the bot's picture failed", "channel", a.name, "err", err)
		r.Err = err
		return r
	}
	h.rememberGiven(ctx, a, ps, cmp.Or(ps.BotID(), "?"), p.Version)
	r.Outcome = core.PictureUpdated
	return r
}
