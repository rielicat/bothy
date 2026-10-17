package channel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// pictureTimeout bounds one chat app's picture check or upload.
const pictureTimeout = 30 * time.Second

// pictureKey is the meta key naming the bot (PictureSetter.BotID) whose
// picture was already looked after on that chat app, so a restart doesn't
// ask again.
func pictureKey(app string) string { return "picture:" + app }

// firstPicture gives a newly connected bot Bothy's picture when it has none.
// A picture someone set in BotFather or the developer portal stays: only an
// admin's change in Bothy (ApplyPicture) replaces one.
func (h *Hub) firstPicture(ctx context.Context, a *adapter) {
	ps, ok := a.ch.(core.PictureSetter)
	if !ok || h.d.Picture == nil {
		return
	}
	a.pictureMu.Lock()
	defer a.pictureMu.Unlock()
	id := cmp.Or(ps.BotID(), "?")
	if v, err := h.d.Store.Meta().Get(ctx, pictureKey(a.name)); err == nil && v == id {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pictureTimeout)
	defer cancel()
	has, err := ps.HasPicture(pctx)
	if err != nil {
		h.log.Warn("channel: can't tell whether the bot has a picture", "channel", a.name, "err", err)
		return
	}
	if !has {
		p, err := h.d.Picture.Current(ctx)
		if err == nil {
			err = ps.SetPicture(pctx, p)
		}
		if err != nil {
			h.log.Warn("channel: giving the bot Bothy's picture failed", "channel", a.name, "err", err)
			return
		}
		h.log.Info("channel: gave the bot Bothy's picture", "channel", a.name, "identity", a.ch.Status().Identity, "custom", p.Custom)
	}
	h.rememberPicture(ctx, a, id)
}

func (h *Hub) rememberPicture(ctx context.Context, a *adapter, id string) {
	if err := h.d.Store.Meta().Set(ctx, pictureKey(a.name), id); err != nil && ctx.Err() == nil {
		h.log.Warn("channel: remembering the bot's picture failed", "channel", a.name, "err", err)
	}
}

// ApplyPicture gives every turned-on chat app p, replacing the bot's picture
// (core.ChannelHub). It is an admin's explicit choice, so unlike
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
	h.rememberPicture(ctx, a, cmp.Or(ps.BotID(), "?"))
	r.Outcome = core.PictureUpdated
	return r
}
