// Package treehash fingerprints a directory tree: sha256 over every entry's
// slash-separated relative path, file type and permission bits, and the
// sha256 of its contents (regular files) or its target (symlinks). Entries are
// visited in lexical order, so the result does not depend on the filesystem's
// directory order. Symlinks are recorded, never followed. Skills (pinned
// SKILL.md trees) and MCP install trees both use it.
package treehash

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
)

// ErrUnsupported is returned for devices, sockets and named pipes: a pinned
// tree never legitimately holds one.
var ErrUnsupported = errors.New("treehash: unsupported file type")

// Sum hashes the tree rooted at dir.
func Sum(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return SumRoot(root)
}

// SumRoot hashes the tree below root.
func SumRoot(root *os.Root) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode() & (fs.ModeType | fs.ModePerm)
		var sum []byte
		switch {
		case mode.IsDir():
		case mode.IsRegular():
			if sum, err = fileSum(root, p); err != nil {
				return err
			}
		case mode&fs.ModeSymlink != 0:
			target, err := root.Readlink(p)
			if err != nil {
				return err
			}
			sum = []byte(target)
		default:
			return fmt.Errorf("%w: %s (%s)", ErrUnsupported, p, mode.Type())
		}
		field(h, []byte(p))
		field(h, binary.BigEndian.AppendUint32(nil, uint32(mode)))
		field(h, sum)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// field writes a length-prefixed value so that no choice of names can make
// two different trees serialize to the same bytes.
func field(h hash.Hash, b []byte) {
	h.Write(binary.AppendUvarint(nil, uint64(len(b))))
	h.Write(b)
}

func fileSum(root *os.Root, p string) ([]byte, error) {
	f, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
