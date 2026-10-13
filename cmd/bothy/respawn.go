package main

import "os"

// respawn starts os.Executable() with this process's arguments, environment
// and standard streams, and does not wait for it.
func respawn() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p, err := os.StartProcess(exe, os.Args, &os.ProcAttr{Env: os.Environ(), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	if err != nil {
		return err
	}
	return p.Release()
}
