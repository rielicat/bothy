// Package sandboxtest provides fake core.Sandbox and core.Egress.
package sandboxtest

import (
	"context"
	"net/http"

	"github.com/bothyhq/bothy/internal/core"
)

type Sandbox struct {
	core.Sandbox
	StatusV core.SandboxStatus
	// Exec handles every exec; default echoes argv to stdout with exit 0.
	ExecFunc func(ctx context.Context, key string, req core.ExecRequest) (*core.ExecResult, error)
	Killed   []string
	Ensured  []core.BoxSpec
}

type Egress struct {
	core.Egress
	Client   *http.Client // returned by PublicClient and SystemClient (point it at an httptest server)
	Activity map[string]bool
	Bound    []core.EgressBinding
}
