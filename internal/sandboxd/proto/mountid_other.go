//go:build !unix

package proto

import "io/fs"

func MountID(fs.FileInfo) (string, bool) { return "", false }
