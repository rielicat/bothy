//go:build unix

package main

import (
	"os"
	"syscall"
)

func setUmask() { syscall.Umask(0o007) }

func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
