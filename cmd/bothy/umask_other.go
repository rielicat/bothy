//go:build !unix

package main

import "github.com/bothyhq/bothy/internal/core"

func setUmask() {}

func reexec() error { return core.ErrNotImplemented }
