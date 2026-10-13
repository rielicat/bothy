package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// backend is one way of running boxes: sandboxd, bwrap or none. The service
// in front of it owns directories, egress sockets, validation and status.
type backend interface {
	name() string
	// probe returns the backend's current status; it may do I/O.
	probe(ctx context.Context) core.SandboxStatus
	// gid is the group box directories must carry, or noGroup.
	gid() int
	create(ctx context.Context, spec core.BoxSpec) error
	exec(ctx context.Context, key string, req core.ExecRequest, onOut func(int, []byte)) (*core.ExecResult, error)
	stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error)
	kill(ctx context.Context, key string) error
	destroy(ctx context.Context, key string) error
	// list reports boxes that outlived an earlier gateway process.
	list(ctx context.Context) ([]proto.BoxInfo, error)
}

// errGone means the backend no longer has the box (reaped, evicted, killed
// and removed); the service recreates it from the last spec and retries once.
var errGone = fmt.Errorf("box is gone: %w", core.ErrNotFound)

const (
	noSandboxText = "No sandbox: shell, python and MCP stdio are off. Use the compose file."
	composeFix    = "docker compose -f deploy/compose.yaml up -d"
)

type noneBackend struct {
	reason, fix string
}

func (n noneBackend) name() string { return "none" }
func (n noneBackend) gid() int     { return noGroup }

func (n noneBackend) probe(context.Context) core.SandboxStatus {
	return core.SandboxStatus{Backend: "none", Reason: n.reason, Fix: n.fix, SelfTest: []core.SelfTestCheck{}}
}

func (noneBackend) create(context.Context, core.BoxSpec) error { return core.ErrNoSandbox }
func (noneBackend) exec(context.Context, string, core.ExecRequest, func(int, []byte)) (*core.ExecResult, error) {
	return nil, core.ErrNoSandbox
}
func (noneBackend) stdio(context.Context, string, []string, map[string]string) (io.ReadWriteCloser, error) {
	return nil, core.ErrNoSandbox
}
func (noneBackend) kill(context.Context, string) error            { return core.ErrNoSandbox }
func (noneBackend) destroy(context.Context, string) error         { return core.ErrNoSandbox }
func (noneBackend) list(context.Context) ([]proto.BoxInfo, error) { return nil, nil }

// isolationFailure reports the failed isolation probes of a status.
func isolationFailure(st core.SandboxStatus) []string {
	var bad []string
	for _, c := range st.SelfTest {
		for _, name := range proto.IsolationChecks {
			if c.Name == name && !c.OK {
				bad = append(bad, name)
			}
		}
	}
	return bad
}

func unavailable(st core.SandboxStatus) error {
	if st.Reason == "" {
		return core.ErrNoSandbox
	}
	return fmt.Errorf("%w: %s", core.ErrNoSandbox, st.Reason)
}

func isGone(err error) bool { return errors.Is(err, errGone) }
