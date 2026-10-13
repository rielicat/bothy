//go:build !linux

package bwrap

import (
	"context"
	"errors"
	"io"

	"github.com/rielicat/bothy/internal/core"
)

var errLinuxOnly = errors.New("bwrap jails need Linux")

func (b *Backend) Exec(context.Context, Jail, core.ExecRequest, func(int, []byte)) (*core.ExecResult, error) {
	return nil, errLinuxOnly
}

func (b *Backend) Stdio(context.Context, Jail, []string, map[string]string) (io.ReadWriteCloser, error) {
	return nil, errLinuxOnly
}
