//go:build !unix

package main

func setUmask() {}

// reexec has no exec(2) to use here, so it starts a fresh copy and lets this
// process exit; serve has already closed the listener and the database.
func reexec() error { return respawn() }
