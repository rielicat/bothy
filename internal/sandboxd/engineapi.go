package sandboxd

import "github.com/rielicat/bothy/internal/sandboxd/engine"

// Engine is what sandboxd needs from the container engine.
type Engine = engine.API

// ExecStream is a hijacked exec connection.
type ExecStream = engine.ExecStream
