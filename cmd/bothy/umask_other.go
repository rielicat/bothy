//go:build !unix

package main

import "github.com/rielicat/bothy/internal/core"

func setUmask() {}

func reexec() error { return core.ErrNotImplemented }
