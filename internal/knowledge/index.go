package knowledge

import (
	"context"
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

	"github.com/rielicat/bothy/internal/core"
)

const (
	batchFiles = 128     // file changes per Apply transaction
	batchBytes = 4 << 20 // chunk bytes per Apply transaction
)

type caps struct {
	fileBytes  int64
	files      int
	totalBytes int64
}

type indexStats struct {
	files, chunks, skipped int
	bytes                  int64
	truncated              bool
}

// errUser is an indexing failure worded for the person who asked.
type errUser struct{ msg string }

func (e *errUser) Error() string { return e.msg }

// indexer walks one directory read-only (os.Root, symlinks never followed)
// and brings the stored index in line with it.
type indexer struct {
	repo  core.KnowledgeRepo
	id    int64
	sha   string
	caps  caps
	root  *os.Root
	ig    *ignorer
	known map[string]core.KnowledgeFile

	stats      indexStats
	batch      []core.KnowledgeFileChange
	batchBytes int
}

// indexDir indexes dir (slash path inside ws) for repo id at sha.
func indexDir(ctx context.Context, repo core.KnowledgeRepo, id int64, sha, ws, dir string, c caps) (indexStats, error) {
	if ws == "" {
		return indexStats{}, &errUser{"This principal has no workspace."}
	}
	wsRoot, err := os.OpenRoot(ws)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return indexStats{}, &errUser{"The workspace does not exist yet."}
		}
		return indexStats{}, err
	}
	defer wsRoot.Close()
	root := wsRoot
	if dir != "." {
		if fi, err := wsRoot.Stat(filepath.FromSlash(dir)); err != nil || !fi.IsDir() {
			return indexStats{}, &errUser{fmt.Sprintf("The directory %s is not in the workspace.", dir)}
		}
		if root, err = wsRoot.OpenRoot(filepath.FromSlash(dir)); err != nil {
			return indexStats{}, err
		}
		defer root.Close()
	}
	files, err := repo.Files(ctx, core.SystemViewer(), id)
	if err != nil {
		return indexStats{}, err
	}
	ix := &indexer{repo: repo, id: id, sha: sha, caps: c, root: root, ig: &ignorer{dirs: map[string]ruleSet{}, limit: ignoreWork},
		known: make(map[string]core.KnowledgeFile, len(files))}
	for _, f := range files {
		ix.known[f.Path] = f
	}
	if data, ok := ix.readSmall(".gitattributes"); ok {
		ix.ig.attrs = parseAttributes(data)
	}
	if err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return ix.visit(ctx, p, d, err)
	}); err != nil {
		return indexStats{}, err
	}
	for p := range ix.known {
		if err := ix.add(ctx, core.KnowledgeFileChange{File: core.KnowledgeFile{Path: p}, Delete: true}, 0); err != nil {
			return indexStats{}, err
		}
	}
	if err := ix.flush(ctx); err != nil {
		return indexStats{}, err
	}
	if err := repo.SetSHA(ctx, id, sha); err != nil {
		return indexStats{}, err
	}
	return ix.stats, nil
}

func (ix *indexer) visit(ctx context.Context, p string, d fs.DirEntry, err error) error {
	if err != nil {
		if p == "." {
			return err
		}
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if p == "." {
		ix.loadIgnore(".")
		return nil
	}
	if d.Type()&fs.ModeSymlink != 0 || len(p) > maxIndexedPath {
		return nil
	}
	if d.IsDir() {
		if skipDirs[d.Name()] || strings.Count(p, "/") >= maxWalkDepth || ix.ig.ignored(p, true) {
			return ix.skip(fs.SkipDir)
		}
		ix.loadIgnore(p)
		return nil
	}
	if !d.Type().IsRegular() || skipByName(d.Name()) || ix.ig.ignored(p, false) {
		return ix.skip(nil)
	}
	return ix.file(ctx, p, d)
}

// skip passes the walk's verdict through, unless the ignore rules spent
// their work cap: then the walk ends where it is, as with the size caps.
func (ix *indexer) skip(verdict error) error {
	if ix.ig.exhausted() {
		ix.stats.truncated = true
		return fs.SkipAll
	}
	return verdict
}

func (ix *indexer) loadIgnore(dir string) {
	if data, ok := ix.readSmall(path.Join(dir, ".gitignore")); ok {
		base := dir
		if base == "." {
			base = ""
		}
		ix.ig.add(parseIgnore(base, data))
	}
}

var errNotRegular = errors.New("not a regular file")

// openRegular opens a slash path under root read-only. O_NONBLOCK: boxes
// write the workspace, and a FIFO planted there (or swapped in after a
// check saw a regular file) would otherwise park the caller until something
// writes to it.
func openRegular(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(filepath.FromSlash(name), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

// readSmall reads a regular file (not a symlink) of at most ignoreFileMax
// bytes.
func (ix *indexer) readSmall(name string) ([]byte, bool) {
	fi, err := ix.root.Lstat(filepath.FromSlash(name))
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	f, err := openRegular(ix.root, name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ignoreFileMax))
	return data, err == nil
}

func (ix *indexer) file(ctx context.Context, p string, d fs.DirEntry) error {
	if ix.stats.files >= ix.caps.files {
		ix.stats.truncated = true
		return fs.SkipAll
	}
	size, ok := entrySize(d)
	if !ok {
		return nil // vanished since the directory was read
	}
	if size > ix.caps.fileBytes {
		ix.stats.skipped++
		return nil
	}
	if ix.stats.bytes+size > ix.caps.totalBytes {
		ix.stats.truncated = true
		return fs.SkipAll
	}
	data, ok := ix.read(p)
	if !ok || binary(data) || generated(data) {
		return nil
	}
	sum := sha256.Sum256(data)
	hash := chunkerVersion + ":" + hex.EncodeToString(sum[:])
	ix.stats.files++
	ix.stats.bytes += int64(len(data))
	old, seen := ix.known[p]
	delete(ix.known, p)
	if seen && old.Hash == hash {
		ix.stats.chunks += old.Chunks
		return nil
	}
	lang, chunks := chunkFile(p, data)
	ix.stats.chunks += len(chunks)
	n := 0
	for _, c := range chunks {
		n += len(c.Body) + len(c.Terms)
	}
	return ix.add(ctx, core.KnowledgeFileChange{File: core.KnowledgeFile{Path: p, Hash: hash, Size: int64(len(data)), Lang: lang},
		Chunks: chunks}, n)
}

func entrySize(d fs.DirEntry) (int64, bool) {
	info, err := d.Info()
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// read opens through the root (a symlink swapped in after the walk saw the
// file cannot leave it) and reads at most the per-file cap.
func (ix *indexer) read(p string) ([]byte, bool) {
	f, err := openRegular(ix.root, p)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ix.caps.fileBytes+1))
	if err != nil || int64(len(data)) > ix.caps.fileBytes {
		return nil, false
	}
	return data, true
}

func (ix *indexer) add(ctx context.Context, c core.KnowledgeFileChange, n int) error {
	ix.batch = append(ix.batch, c)
	ix.batchBytes += n
	if len(ix.batch) >= batchFiles || ix.batchBytes >= batchBytes {
		return ix.flush(ctx)
	}
	return nil
}

func (ix *indexer) flush(ctx context.Context) error {
	if len(ix.batch) == 0 {
		return nil
	}
	if err := ix.repo.Apply(ctx, ix.id, ix.sha, ix.batch); err != nil {
		return err
	}
	ix.batch, ix.batchBytes = ix.batch[:0], 0
	return nil
}
