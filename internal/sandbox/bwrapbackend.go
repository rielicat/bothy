package sandbox

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/sandbox/bwrap"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

// bwrapBackend: a "box" is the per-key mount set; every exec is its own
// jail sharing the session workspace (SPEC §2.12).
type bwrapBackend struct {
	jailer *bwrap.Backend // nil when the probe failed
	st     core.SandboxStatus

	mu    sync.Mutex
	jails map[string]bwrap.Jail
}

func newBwrapBackend(ctx context.Context, work, run string) *bwrapBackend {
	b := &bwrapBackend{jails: map[string]bwrap.Jail{}}
	o, version, checks, err := bwrap.Probe(ctx)
	b.st = core.SandboxStatus{Backend: "bwrap", Runtime: "bwrap", Engine: version, SelfTest: checks}
	if err != nil {
		b.st.Reason = err.Error()
		b.st.Fix = "install bubblewrap 0.8 or newer (apt install bubblewrap), or use the compose deployment"
		if strings.Contains(err.Error(), "user namespace") {
			b.st.Fix = bwrap.UsernsFix
		}
		return b
	}
	o.WorkDir, o.RunDir = work, run
	jailer, err := bwrap.New(o, version)
	if err != nil {
		b.st.SelfTest = append(b.st.SelfTest, core.SelfTestCheck{Name: "seccomp", Detail: err.Error()})
		b.st.Reason = "cannot build the seccomp filter: " + err.Error()
		return b
	}
	b.st.SelfTest = append(b.st.SelfTest, core.SelfTestCheck{Name: "seccomp", OK: true, Detail: "filter compiled"})
	b.jailer, b.st.Available = jailer, true
	return b
}

func (b *bwrapBackend) name() string                             { return "bwrap" }
func (b *bwrapBackend) gid() int                                 { return noGroup }
func (b *bwrapBackend) probe(context.Context) core.SandboxStatus { return b.st }

func (b *bwrapBackend) create(_ context.Context, spec core.BoxSpec) error {
	if b.jailer == nil {
		return unavailable(b.st)
	}
	b.mu.Lock()
	b.jails[spec.Key] = bwrap.Jail{Key: spec.Key, Workspace: string(spec.Workspace), MCPTree: spec.MCPTree, MCPTreeRW: spec.MCPTreeRW}
	b.mu.Unlock()
	return nil
}

func (b *bwrapBackend) jail(key string) (bwrap.Jail, error) {
	if b.jailer == nil {
		return bwrap.Jail{}, unavailable(b.st)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	j, ok := b.jails[key]
	if !ok {
		return j, errGone
	}
	return j, nil
}

func (b *bwrapBackend) exec(ctx context.Context, key string, req core.ExecRequest, onOut func(int, []byte)) (*core.ExecResult, error) {
	j, err := b.jail(key)
	if err != nil {
		return nil, err
	}
	return b.jailer.Exec(ctx, j, req, onOut)
}

func (b *bwrapBackend) stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error) {
	j, err := b.jail(key)
	if err != nil {
		return nil, err
	}
	return b.jailer.Stdio(ctx, j, argv, env)
}

func (b *bwrapBackend) kill(_ context.Context, key string) error {
	if b.jailer != nil {
		b.jailer.Kill(key)
	}
	return nil
}

func (b *bwrapBackend) destroy(_ context.Context, key string) error {
	if b.jailer != nil {
		b.jailer.Kill(key)
	}
	b.mu.Lock()
	delete(b.jails, key)
	b.mu.Unlock()
	return nil
}

func (b *bwrapBackend) list(context.Context) ([]proto.BoxInfo, error) { return nil, nil }
