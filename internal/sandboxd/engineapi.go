package sandboxd

import (
	"context"
	"io"

	"github.com/bothyhq/bothy/internal/sandboxd/engine"
)

// Engine is the slice of the Docker Engine API that sandboxd uses;
// *engine.Client implements it through engineClient, tests use a fake.
type Engine interface {
	Socket() string
	Negotiate(ctx context.Context) (string, error)
	APIVersion() string
	Version(ctx context.Context) (engine.Version, error)
	Info(ctx context.Context) (engine.Info, error)
	ImageInspect(ctx context.Context, ref string) error
	ImagePull(ctx context.Context, ref string) error
	ContainerCreate(ctx context.Context, name string, cfg *engine.ContainerConfig) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerList(ctx context.Context, labels []string) ([]engine.Container, error)
	ContainerInspect(ctx context.Context, id string) (*engine.ContainerJSON, error)
	ExecCreate(ctx context.Context, id string, cfg engine.ExecConfig) (string, error)
	ExecStart(ctx context.Context, id string) (ExecStream, error)
	ExecInspect(ctx context.Context, id string) (engine.ExecInspect, error)
}

// ExecStream is a hijacked exec connection: stdin in, multiplexed frames out.
type ExecStream interface {
	io.ReadWriteCloser
	CloseWrite() error
}

type engineClient struct{ *engine.Client }

func (c engineClient) ExecStart(ctx context.Context, id string) (ExecStream, error) {
	s, err := c.Client.ExecStart(ctx, id)
	if err != nil {
		return nil, err
	}
	return s, nil
}
