package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/engine"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

type daemon struct {
	cfg Config
	eng Engine
	log *slog.Logger
	now func() time.Time

	stdioGrace time.Duration // how long a stdio process may outlive its closed stream

	testMu sync.Mutex // serializes self-tests

	hmu            sync.RWMutex
	health         proto.Health
	runtime        string // engine runtime name passed to boxes
	seccompBuiltin bool

	mu      sync.Mutex
	boxes   map[string]*box
	pending map[string]chan struct{} // keys being created
}

type box struct {
	key, id, profile, runtime string
	created, lastUsed         time.Time
	active                    int    // running execs and stdio streams
	stdio                     bool   // one stdio stream per box
	killGen                   uint64 // bumped by Kill so running execs report killed
	stderr                    *ring
	// removing is non-nil while the container is being removed and closed
	// when that ends; no exec or stdio starts on the box meanwhile.
	removing chan struct{}
}

func newDaemon(cfg Config, eng Engine, log *slog.Logger, now func() time.Time) *daemon {
	return &daemon{
		cfg: cfg, eng: eng, log: log, now: now, stdioGrace: 5 * time.Second,
		health: proto.Health{Version: proto.Version, MaxBoxes: cfg.MaxBoxes, SelfTest: []proto.Check{},
			Reason: "the sandbox self-test has not finished yet"},
		boxes:   map[string]*box{},
		pending: map[string]chan struct{}{},
	}
}

// apiError carries a proto.Error code and HTTP status to the handler.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func errNotFound(key string) error {
	return &apiError{http.StatusNotFound, "not_found", "no box " + key}
}

func errUnavailable(reason string) error {
	return &apiError{http.StatusServiceUnavailable, "unavailable", "sandbox unavailable: " + reason}
}

func errEngine(err error) error {
	return &apiError{http.StatusBadGateway, "engine", err.Error()}
}

// containerName scopes the engine-wide container name to this install's run
// volume: the canary and MCP box keys are derived, not random, so two installs
// (or test runs) on one engine would otherwise share names, and a name clash
// makes createAndStart remove the other install's container.
func (d *daemon) containerName(key string) string { return d.cfg.RunVolume + "-" + key }

func (d *daemon) labels() []string {
	return []string{labelBox + "=1", labelVolume + "=" + d.cfg.RunVolume}
}

func (d *daemon) healthy() (bool, string) {
	d.hmu.RLock()
	defer d.hmu.RUnlock()
	return d.health.OK, d.health.Reason
}

func (d *daemon) healthSnapshot() proto.Health {
	d.hmu.RLock()
	h := d.health
	h.SelfTest = slices.Clone(d.health.SelfTest)
	d.hmu.RUnlock()
	d.mu.Lock()
	h.Boxes = len(d.boxes)
	d.mu.Unlock()
	h.MaxBoxes = d.cfg.MaxBoxes
	return h
}

// create makes the box for req.Key or returns the running one (idempotent).
func (d *daemon) create(ctx context.Context, req proto.CreateReq) (proto.CreateResp, error) {
	if err := proto.ValidateCreate(&req, d.cfg.Images); err != nil {
		return proto.CreateResp{}, err
	}
	if req.Key == canaryKey {
		// Its egress directory is sandboxd's own world-accessible canary socket.
		return proto.CreateResp{}, fmt.Errorf("%w: the canary key is reserved", proto.ErrInvalid)
	}
	if ok, reason := d.healthy(); !ok {
		return proto.CreateResp{}, errUnavailable(reason)
	}
	for {
		d.mu.Lock()
		if b := d.boxes[req.Key]; b != nil {
			if ch := b.removing; ch != nil {
				d.mu.Unlock()
				select {
				case <-ch:
					continue
				case <-ctx.Done():
					return proto.CreateResp{}, ctx.Err()
				}
			}
			id, rt := b.id, b.runtime
			b.lastUsed = d.now()
			d.mu.Unlock()
			if d.stillRunning(ctx, id) {
				return proto.CreateResp{ID: id, Created: false, Runtime: rt}, nil
			}
			d.forget(req.Key, id)
			continue
		}
		if ch := d.pending[req.Key]; ch != nil {
			d.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return proto.CreateResp{}, ctx.Err()
			}
		}
		if len(d.boxes)+len(d.pending) >= d.cfg.MaxBoxes {
			victim := d.lruIdleLocked()
			d.mu.Unlock()
			if victim == nil {
				return proto.CreateResp{}, &apiError{http.StatusServiceUnavailable, "busy",
					fmt.Sprintf("all %d boxes are busy", d.cfg.MaxBoxes)}
			}
			removed, err := d.removeIf(ctx, victim, func(b *box) bool { return b.active == 0 })
			if err != nil {
				return proto.CreateResp{}, &apiError{http.StatusServiceUnavailable, "busy",
					"evicting the least recently used box failed: " + err.Error()}
			}
			if removed {
				d.log.Info("evicted idle box", "key", victim.key)
			}
			continue
		}
		ch := make(chan struct{})
		d.pending[req.Key] = ch
		d.mu.Unlock()
		resp, err := d.start(ctx, req)
		d.mu.Lock()
		delete(d.pending, req.Key)
		close(ch)
		d.mu.Unlock()
		return resp, err
	}
}

func (d *daemon) lruIdleLocked() *box {
	var victim *box
	for _, b := range d.boxes {
		if b.active == 0 && b.removing == nil && (victim == nil || b.lastUsed.Before(victim.lastUsed)) {
			victim = b
		}
	}
	return victim
}

func (d *daemon) start(ctx context.Context, req proto.CreateReq) (proto.CreateResp, error) {
	d.hmu.RLock()
	rt, label, builtin := d.runtime, d.health.Runtime, d.seccompBuiltin
	d.hmu.RUnlock()
	cfg := boxSpec(specInput{
		Key: req.Key, Profile: req.Profile, Workspace: req.Workspace, MCPTree: req.MCPTree, MCPTreeRW: req.MCPTreeRW,
		Limits: req.Limits, Image: d.cfg.Images[req.Profile], Runtime: rt,
		WorkVolume: d.cfg.WorkVolume, RunVolume: d.cfg.RunVolume, SeccompBuiltin: builtin,
	})
	id, err := d.createAndStart(ctx, d.containerName(req.Key), cfg)
	if err != nil {
		return proto.CreateResp{}, errEngine(err)
	}
	now := d.now()
	d.mu.Lock()
	d.boxes[req.Key] = &box{key: req.Key, id: id, profile: req.Profile, runtime: label,
		created: now, lastUsed: now, stderr: newRing()}
	d.mu.Unlock()
	d.log.Info("box created", "key", req.Key, "profile", req.Profile, "runtime", label)
	return proto.CreateResp{ID: id, Created: true, Runtime: label}, nil
}

// createAndStart creates and starts a container, replacing a leftover one of
// the same name (a box whose sandboxd died before it was registered).
func (d *daemon) createAndStart(ctx context.Context, name string, cfg *engine.ContainerConfig) (string, error) {
	id, err := d.eng.ContainerCreate(ctx, name, cfg)
	if errors.Is(err, engine.ErrConflict) {
		if rerr := d.eng.ContainerRemove(ctx, name); rerr != nil {
			return "", rerr
		}
		id, err = d.eng.ContainerCreate(ctx, name, cfg)
	}
	if err != nil {
		return "", err
	}
	if err := d.eng.ContainerStart(ctx, id); err != nil {
		_ = d.eng.ContainerRemove(context.WithoutCancel(ctx), id)
		return "", err
	}
	return id, nil
}

func (d *daemon) stillRunning(ctx context.Context, id string) bool {
	c, err := d.eng.ContainerInspect(ctx, id)
	if err != nil {
		// Only a definite "gone" drops the box; engine hiccups keep it.
		return !errors.Is(err, engine.ErrNotFound)
	}
	return c.State.Running
}

// forget drops key from the map if it still refers to container id.
func (d *daemon) forget(key, id string) {
	d.mu.Lock()
	if b := d.boxes[key]; b != nil && b.id == id {
		delete(d.boxes, key)
	}
	d.mu.Unlock()
}

// removeIf force-removes b's container and forgets it, provided b is still
// registered and cond (nil = always) holds at that moment. The check and the
// removing mark are atomic, so a box that became busy after it was picked
// (eviction, idle reaping) survives. It waits for a removal already under way.
func (d *daemon) removeIf(ctx context.Context, b *box, cond func(*box) bool) (bool, error) {
	for {
		d.mu.Lock()
		if d.boxes[b.key] != b {
			d.mu.Unlock()
			return false, nil
		}
		if ch := b.removing; ch != nil {
			d.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		if cond != nil && !cond(b) {
			d.mu.Unlock()
			return false, nil
		}
		ch := make(chan struct{})
		b.removing = ch
		d.mu.Unlock()
		err := d.eng.ContainerRemove(ctx, b.id)
		d.mu.Lock()
		b.removing = nil
		if err == nil && d.boxes[b.key] == b {
			delete(d.boxes, b.key)
		}
		d.mu.Unlock()
		close(ch)
		return err == nil, err
	}
}

// acquire marks a box busy for an exec or stdio stream.
func (d *daemon) acquire(key string, stdio bool) (*box, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.boxes[key]
	if b == nil || b.removing != nil {
		// The gateway recreates the box; its create waits for the removal.
		return nil, errNotFound(key)
	}
	if stdio {
		if b.stdio {
			return nil, &apiError{http.StatusConflict, "busy", "box " + key + " already has a stdio stream"}
		}
		b.stdio = true
	}
	b.active++
	b.lastUsed = d.now()
	return b, nil
}

func (d *daemon) release(b *box, stdio bool) {
	d.mu.Lock()
	b.active--
	if stdio {
		b.stdio = false
	}
	b.lastUsed = d.now()
	d.mu.Unlock()
}

func (d *daemon) lookup(key string) (*box, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if b := d.boxes[key]; b != nil {
		return b, nil
	}
	return nil, errNotFound(key)
}

func (d *daemon) destroy(ctx context.Context, key string) error {
	b, err := d.lookup(key)
	if err != nil {
		// Also remove an unregistered container of that name.
		return d.eng.ContainerRemove(ctx, d.containerName(key))
	}
	_, err = d.removeIf(ctx, b, nil)
	return err
}

func (d *daemon) list() []proto.BoxInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]proto.BoxInfo, 0, len(d.boxes))
	for _, b := range d.boxes {
		out = append(out, proto.BoxInfo{Key: b.key, ID: b.id, Profile: b.profile,
			Created: b.created.UnixMilli(), LastUsed: b.lastUsed.UnixMilli(), Runtime: b.runtime})
	}
	slices.SortFunc(out, func(a, b proto.BoxInfo) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return out
}

// reap removes idle and over-age boxes and forgets boxes whose containers are
// gone (AutoRemove after their PID 1 died).
func (d *daemon) reap(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// A box registered after the listing began is missing from it without
	// being gone.
	listedAt := d.now()
	alive := map[string]bool{}
	cs, listErr := d.eng.ContainerList(ctx, d.labels())
	for _, c := range cs {
		alive[c.ID] = c.State == "running"
	}
	now := d.now()
	idle := func(b *box) bool { return b.active == 0 && d.now().Sub(b.lastUsed) > d.cfg.Idle }
	type victim struct {
		b    *box
		cond func(*box) bool
	}
	var victims []victim
	d.mu.Lock()
	for _, b := range d.boxes {
		switch {
		case b.removing != nil:
		case listErr == nil && !alive[b.id] && b.created.Before(listedAt):
			delete(d.boxes, b.key)
		case now.Sub(b.created) > d.cfg.MaxLifetime:
			victims = append(victims, victim{b, nil})
		case idle(b):
			victims = append(victims, victim{b, idle})
		}
	}
	d.mu.Unlock()
	for _, v := range victims {
		if removed, err := d.removeIf(ctx, v.b, v.cond); err != nil {
			d.log.Warn("reaping box failed", "key", v.b.key, "err", err)
		} else if removed {
			d.log.Info("reaped box", "key", v.b.key)
		}
	}
}

// removeOrphans deletes boxes of an earlier sandboxd process of this install.
func (d *daemon) removeOrphans(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cs, err := d.eng.ContainerList(ctx, d.labels())
	if err != nil {
		d.log.Warn("listing orphan boxes failed", "err", err)
		return
	}
	for _, c := range cs {
		if err := d.eng.ContainerRemove(ctx, c.ID); err != nil {
			d.log.Warn("removing orphan box failed", "id", c.ID, "err", err)
		}
	}
	if len(cs) > 0 {
		d.log.Info("removed orphan boxes", "count", len(cs))
	}
}

func (d *daemon) removeAll(ctx context.Context) {
	d.mu.Lock()
	all := make([]*box, 0, len(d.boxes))
	for _, b := range d.boxes {
		all = append(all, b)
	}
	d.mu.Unlock()
	for _, b := range all {
		if _, err := d.removeIf(ctx, b, nil); err != nil {
			d.log.Warn("removing box at shutdown failed", "key", b.key, "err", err)
		}
	}
}
