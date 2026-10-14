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
func SumRoot(root *os.Root) (string, error) { return SumRootFiles(root, 0, nil) }

// SumRootFiles is SumRoot that also hands each regular file to visit, in
// walk order, with its contents' sha256, its size and the very bytes it
// hashed, so a caller can keep a copy of exactly the tree the sum stands
// for. A file over keep bytes is hashed in full but handed on with nil
// data; keep <= 0 hands every file on with nil data.
func SumRootFiles(root *os.Root, keep int64, visit func(p string, sum []byte, size int64, data []byte)) (string, error) {
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
			var data []byte
			var size int64
			if sum, data, size, err = fileSum(root, p, keep); err != nil {
				return err
			}
			if visit != nil {
				visit(p, sum, size, data)
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

// fileSum hashes p and, when keep > 0, also returns its contents if they
// are at most keep bytes.
func fileSum(root *os.Root, p string, keep int64) (sum, data []byte, size int64, err error) {
	f, err := root.Open(p)
	if err != nil {
		return nil, nil, 0, err
	}
	defer f.Close()
	h := sha256.New()
	var w io.Writer = h
	var buf *capped
	if keep > 0 {
		buf = &capped{max: keep + 1}
		w = io.MultiWriter(h, buf)
	}
	if size, err = io.Copy(w, f); err != nil {
		return nil, nil, 0, err
	}
	if buf != nil && size <= keep {
		data = buf.b
	}
	return h.Sum(nil), data, size, nil
}

// capped keeps the first max bytes written and accepts the rest unseen.
type capped struct {
	b   []byte
	max int64
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - int64(len(c.b)); room > 0 {
		c.b = append(c.b, p[:min(int64(len(p)), room)]...)
	}
	return len(p), nil
}
