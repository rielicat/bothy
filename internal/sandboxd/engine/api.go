package engine

import (
	"context"
	"io"
)

// API is the slice of the Engine API that sandboxd uses. *Client implements
// it; enginetest.Fake implements it for tests.
type API interface {
	Socket() string
	Negotiate(ctx context.Context) (string, error)
	APIVersion() string
	Version(ctx context.Context) (Version, error)
	Info(ctx context.Context) (Info, error)
	ImageInspect(ctx context.Context, ref string) error
	ImagePull(ctx context.Context, ref string) error
	ContainerCreate(ctx context.Context, name string, cfg *ContainerConfig) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerList(ctx context.Context, labels []string) ([]Container, error)
	ContainerInspect(ctx context.Context, id string) (*ContainerJSON, error)
	ExecCreate(ctx context.Context, id string, cfg ExecConfig) (string, error)
	ExecStart(ctx context.Context, id string) (ExecStream, error)
	ExecInspect(ctx context.Context, id string) (ExecInspect, error)
}

// ExecStream is a hijacked exec connection: stdin in, multiplexed frames out
// (see Demux).
type ExecStream interface {
	io.ReadWriteCloser
	CloseWrite() error
}

var _ API = (*Client)(nil)
