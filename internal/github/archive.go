package github

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	// Workspace files are shared with the box user through group 10001
	// (umask 007); directories inherit the setgid bit from the workspace.
	fileMode fs.FileMode = 0o660
	execMode fs.FileMode = 0o770
	dirMode  fs.FileMode = 0o770
)

// archiveSlack is what tar headers, padding and skipped entries may add to
// the file bytes of an archive before the walk gives up.
var archiveSlack int64 = 64 << 20

var (
	errTooLarge = errors.New("larger than github.checkout.max_mb or the room left by the workspace quota")
	errTooMany  = errors.New("more files than github.checkout.max_files")
	errOverRead = errors.New("archive expands past its size limit")
)

// archiveLimits bound one repository archive.
type archiveLimits struct {
	maxBytes int64 // sum of the regular files' sizes
	maxFiles int
}

// skips counts archive entries that were not written. Symbolic and hard
// links are dropped so nothing in a checkout points outside it.
type skips struct {
	Symlinks   int `json:"symlinks,omitempty"`
	Hardlinks  int `json:"hardlinks,omitempty"`
	Special    int `json:"special,omitempty"`    // devices, FIFOs
	Unsafe     int `json:"unsafe,omitempty"`     // absolute, escaping, .git or control-character names
	Collisions int `json:"collisions,omitempty"` // duplicates or file/folder clashes (case-insensitive file systems)
}

func (s skips) String() string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{s.Symlinks, "symbolic link"}, {s.Hardlinks, "hard link"}, {s.Special, "special file"},
		{s.Unsafe, "unsafe name"}, {s.Collisions, "name collision"}} {
		switch {
		case p.n == 1:
			parts = append(parts, "1 "+p.what)
		case p.n > 1:
			parts = append(parts, fmt.Sprintf("%d %ss", p.n, p.what))
		}
	}
	return strings.Join(parts, ", ")
}

type archiveFile struct {
	path string // relative to the repository root, slash-separated
	exec bool
	size int64
	r    io.Reader
}

// capReader fails once more than left bytes were read: a small gzip
// stream can expand without bound, and skipped entries are read too.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errOverRead
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// walkArchive reads a repository archive as GitHub serves it (gzip'd tar,
// every entry under one top-level folder such as octocat-Hello-World-7fd1a60)
// and calls fn for each regular file with a safe path, in archive order.
func walkArchive(r io.Reader, lim archiveLimits, fn func(f *archiveFile) error) (skips, error) {
	var sk skips
	zr, err := gzip.NewReader(r)
	if err != nil {
		return sk, fmt.Errorf("github: the archive is not gzip data: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(&capReader{r: zr, left: lim.maxBytes + lim.maxBytes/4 + archiveSlack})
	var total int64
	files := 0
	top := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return sk, nil
		}
		if errors.Is(err, errOverRead) {
			return sk, errTooLarge
		}
		if err != nil {
			return sk, fmt.Errorf("github: reading the archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, ok := stripTop(h.Name, &top)
		if !ok {
			if rel != "" || h.Typeflag != tar.TypeDir {
				sk.Unsafe++
			}
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeSymlink:
			sk.Symlinks++
			continue
		case tar.TypeLink:
			sk.Hardlinks++
			continue
		case tar.TypeReg, '\x00':
		default:
			sk.Special++
			continue
		}
		if !safeRepoPath(rel) {
			sk.Unsafe++
			continue
		}
		if h.Size < 0 {
			return sk, errors.New("github: the archive has an entry with a negative size")
		}
		if files++; files > lim.maxFiles {
			return sk, errTooMany
		}
		if total += h.Size; total > lim.maxBytes {
			return sk, errTooLarge
		}
		if err := fn(&archiveFile{path: rel, exec: h.Mode&0o111 != 0, size: h.Size, r: tr}); err != nil {
			if errors.Is(err, errOverRead) {
				return sk, errTooLarge
			}
			return sk, err
		}
	}
}

// stripTop removes the archive's top-level folder. ok is false for the
// folder itself (rel == "") and for entries outside it or with names that
// are not clean relative paths.
func stripTop(name string, top *string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	first, rest, _ := strings.Cut(name, "/")
	if first == "" || first == "." || first == ".." {
		return name, false
	}
	if *top == "" {
		*top = first
	} else if first != *top {
		return name, false
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "", false
	}
	if path.Clean(rest) != rest || path.IsAbs(rest) {
		return rest, false
	}
	return rest, true
}

type manifestFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec,omitempty"`
}

// extract writes an archive's files under dir (workspace-relative, already
// created) through root, so no path can leave the workspace, and returns
// what it wrote.
func extract(root *os.Root, dir string, r io.Reader, lim archiveLimits) (map[string]manifestFile, skips, error) {
	files := map[string]manifestFile{}
	var collisions int
	sk, err := walkArchive(r, lim, func(f *archiveFile) error {
		target := path.Join(dir, f.path)
		if parent := path.Dir(target); parent != dir {
			if err := root.MkdirAll(filepath.FromSlash(parent), dirMode); err != nil {
				if clash(err) {
					collisions++
					return nil
				}
				return fmt.Errorf("github: creating %s: %w", path.Dir(f.path), rootErr(err))
			}
		}
		mode := fileMode
		if f.exec {
			mode = execMode
		}
		fh, err := root.OpenFile(filepath.FromSlash(target), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			if clash(err) {
				collisions++
				return nil
			}
			return fmt.Errorf("github: writing %s: %w", f.path, rootErr(err))
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(fh, h), f.r)
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			if errors.Is(err, errOverRead) {
				return err
			}
			return fmt.Errorf("github: writing %s: %w", f.path, rootErr(err))
		}
		files[f.path] = manifestFile{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Exec: f.exec}
		return nil
	})
	sk.Collisions += collisions
	return files, sk, err
}

// clash reports a name that cannot be created because an entry already
// holds it: a duplicate, a case-insensitive twin or a file where a folder
// is needed.
func clash(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTDIR)
}

// readBase returns the pristine content of the wanted paths from a stored
// archive, walked with the same rules as the checkout.
func readBase(archive string, wanted map[string]bool, lim archiveLimits) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(wanted) == 0 {
		return out, nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return nil, fmt.Errorf("github: the checkout's base archive is missing: %w", rootErr(err))
	}
	defer f.Close()
	_, err = walkArchive(f, lim, func(af *archiveFile) error {
		if !wanted[af.path] {
			return nil
		}
		if _, dup := out[af.path]; dup {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(af.r, maxDiffFile+1))
		if err != nil {
			return err
		}
		out[af.path] = b
		if len(out) == len(wanted) {
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, err
	}
	return out, nil
}

var errStopWalk = errors.New("stop")

// rootErr rewrites os.Root errors without host paths.
func rootErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}
