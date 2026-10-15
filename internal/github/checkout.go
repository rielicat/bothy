package github

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	manifestName = "manifest.json"
	archiveName  = "base.tar.gz"
	maxGitignore = 256 << 10
)

// manifest records what repo_checkout wrote. It is kept with the pristine
// archive outside the workspace, so nothing a box runs can rewrite the base
// that repo_changes and repo_open_pr diff against, nor what they count as
// already sent.
type manifest struct {
	Version int                     `json:"version"`
	Repo    string                  `json:"repo"` // owner/name
	Ref     string                  `json:"ref"`
	SHA     string                  `json:"sha"`
	Dir     string                  `json:"dir"` // workspace-relative
	At      time.Time               `json:"checked_out_at"`
	Files   map[string]manifestFile `json:"files"`
	Skipped skips                   `json:"skipped"`
	Sent    map[string]sentFile     `json:"sent,omitempty"` // by path: what the latest pull request carrying it sent
}

// sentFile is one file of a pull request opened from the checkout.
type sentFile struct {
	Op     string    `json:"op"`
	SHA256 string    `json:"sha256,omitempty"`
	Exec   bool      `json:"exec,omitempty"`
	PR     int       `json:"pr"`
	At     time.Time `json:"at"`
}

// carries reports whether the pull request sent exactly this change.
func (s sentFile) carries(c change) bool {
	return s.Op == c.Op && s.SHA256 == c.SHA256 && s.Exec == c.Exec
}

// loadManifest returns nil without an error when nothing was checked out.
func loadManifest(store string) (*manifest, error) {
	b, err := os.ReadFile(filepath.Join(store, manifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("github: reading the checkout manifest: %w", rootErr(err))
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil || m.Version != 1 || !shaRE.MatchString(m.SHA) {
		return nil, errors.New("github: the checkout manifest is damaged; check the repository out again")
	}
	if m.Files == nil {
		m.Files = map[string]manifestFile{}
	}
	return &m, nil
}

// saveCheckout stores the manifest and moves the downloaded archive next to
// it, replacing the previous checkout's.
func saveCheckout(store string, m *manifest, archive string) error {
	tmp, err := writeManifestTemp(store, m)
	if err != nil {
		return err
	}
	if err := os.Rename(archive, filepath.Join(store, archiveName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(store, manifestName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// saveManifest replaces the manifest of the checkout already stored.
func saveManifest(store string, m *manifest) error {
	tmp, err := writeManifestTemp(store, m)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(store, manifestName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeManifestTemp(store string, m *manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(store, "."+manifestName+"."+randHex(6))
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// change is one file that differs from the checkout.
type change struct {
	Path   string `json:"path"`
	Op     string `json:"op"`               // added | modified | deleted
	SHA256 string `json:"sha256,omitempty"` // of the workspace content (added, modified)
	Exec   bool   `json:"exec,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SentIn int    `json:"-"` // the pull request that last carried this path, with this content or (in changes) older content
}

type scanResult struct {
	changes  []change // not yet sent as they are now
	sent     []change // exactly as a pull request already sent them
	symlinks []string // untracked symbolic links: never committed
	unsafe   []string // untracked files whose names cannot be committed
}

// scanCheckout compares repos/<owner>/<name> with its manifest. Tracked
// files are always compared; untracked ones count unless a .gitignore
// covers them. .git folders (a local git init in the box) are skipped.
// Only content that could matter is hashed: a box can make a sparse file
// of any apparent size, so a file whose size already tells it apart is
// hashed only when small enough to be proposed.
func scanCheckout(ctx context.Context, root *os.Root, dir string, m *manifest, maxEntries int) (*scanResult, error) {
	res := &scanResult{}
	prefixes := map[string]bool{}
	for p := range m.Files {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if prefixes[d] {
				break
			}
			prefixes[d] = true
		}
	}
	seen := map[string]bool{}
	ig := &ignorer{}
	entries := 0
	var walk func(rel string) error
	walk = func(rel string) error {
		full := path.Join(dir, rel)
		list, err := readDir(root, full)
		if err != nil {
			return fmt.Errorf("github: reading %s: %w", path.Join(dir, rel), rootErr(err))
		}
		if b, ok := readSmall(root, path.Join(full, ".gitignore"), maxGitignore); ok {
			ig.add(rel, b)
		}
		for _, e := range list {
			name := e.Name()
			if strings.EqualFold(name, ".git") {
				continue
			}
			if entries++; entries > maxEntries {
				return fmt.Errorf("github: %s holds more than %d files", dir, maxEntries)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			p := path.Join(rel, name)
			switch t := e.Type(); {
			case t&fs.ModeSymlink != 0:
				if !ig.ignored(p, false) {
					res.symlinks = append(res.symlinks, p)
				}
			case t.IsDir():
				if !ig.ignored(p, true) || prefixes[p] {
					if err := walk(p); err != nil {
						return err
					}
				}
			case t.IsRegular():
				tracked, isTracked := m.Files[p]
				if !isTracked && ig.ignored(p, false) {
					continue
				}
				if !isTracked && !safeRepoPath(p) {
					res.unsafe = append(res.unsafe, p)
					continue
				}
				hashUpTo := int64(maxPRFile)
				if isTracked {
					hashUpTo = max(hashUpTo, tracked.Size)
				}
				sum, size, exec, err := hashFile(ctx, root, path.Join(dir, p), hashUpTo)
				if err != nil {
					return fmt.Errorf("github: reading %s: %w", p, err)
				}
				switch {
				case !isTracked:
					res.changes = append(res.changes, change{Path: p, Op: "added", SHA256: sum, Exec: exec, Size: size})
				case tracked.Size != size || tracked.SHA256 != sum || tracked.Exec != exec:
					res.changes = append(res.changes, change{Path: p, Op: "modified", SHA256: sum, Exec: exec, Size: size})
				}
				if isTracked {
					seen[p] = true
				}
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	for p := range m.Files {
		if !seen[p] {
			res.changes = append(res.changes, change{Path: p, Op: "deleted"})
		}
	}
	sort.Slice(res.changes, func(i, j int) bool { return res.changes[i].Path < res.changes[j].Path })
	pending := res.changes[:0]
	for _, c := range res.changes {
		s, ok := m.Sent[c.Path]
		if ok {
			c.SentIn = s.PR
		}
		if ok && s.carries(c) {
			res.sent = append(res.sent, c)
		} else {
			pending = append(pending, c)
		}
	}
	res.changes = pending
	return res, nil
}

// readDir lists a workspace folder in name order without blocking on a
// FIFO a box process may have put in its place.
func readDir(root *os.Root, rel string) ([]fs.DirEntry, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, syscall.ENOTDIR
	}
	list, err := f.ReadDir(-1)
	slices.SortFunc(list, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return list, err
}

// openRegular opens a workspace file for reading, refusing anything but a
// regular file (checked on the open descriptor).
func openRegular(root *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, rootErr(err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, errors.New("not a regular file")
	}
	return f, st, nil
}

// hashFile hashes a workspace file of at most limit bytes; a larger one
// comes back with its size and no hash.
func hashFile(ctx context.Context, root *os.Root, rel string, limit int64) (sum string, size int64, exec bool, err error) {
	f, st, err := openRegular(root, rel)
	if err != nil {
		return "", 0, false, err
	}
	defer f.Close()
	exec = st.Mode()&0o111 != 0
	if st.Size() > limit {
		return "", st.Size(), exec, nil
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(ctxReader{ctx, f}, limit+1))
	if err != nil {
		return "", 0, false, err
	}
	if n > limit {
		return "", n, exec, nil
	}
	return hex.EncodeToString(h.Sum(nil)), n, exec, nil
}

// ctxReader stops a long read once its context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// readFile reads a workspace file whole when it is at most limit bytes;
// otherwise its first limit+1 bytes. It also returns the hash of what it
// read and the executable bit, so callers can tell whether the bytes are
// the ones planned.
func readFile(root *os.Root, rel string, limit int64) (data []byte, sum string, exec bool, err error) {
	f, st, err := openRegular(root, rel)
	if err != nil {
		return nil, "", false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, "", false, err
	}
	s := sha256.Sum256(b)
	return b, hex.EncodeToString(s[:]), st.Mode()&0o111 != 0, nil
}

func readSmall(root *os.Root, rel string, limit int64) ([]byte, bool) {
	b, _, _, err := readFile(root, rel, limit)
	if err != nil || int64(len(b)) > limit {
		return nil, false
	}
	return b, true
}

// renderDiff renders the changes as one git-style diff: base content from
// the stored archive, current content from the workspace. complete is false
// when some file's content could not be shown in full. It fails when a file
// no longer matches the hash the scan recorded, so a diff always shows the
// bytes the changes name.
func renderDiff(root *os.Root, dir, archive string, m *manifest, changes []change, lim archiveLimits) (string, bool, error) {
	wanted := map[string]bool{}
	for _, c := range changes {
		if c.Op == "modified" {
			wanted[c.Path] = true
		}
	}
	base, err := readBase(archive, wanted, lim)
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	complete := true
	for _, c := range changes {
		var old, cur side
		if mf, ok := m.Files[c.Path]; ok {
			old = side{present: true, data: base[c.Path], size: mf.Size, exec: mf.Exec, sha256: mf.SHA256}
		}
		if c.Op != "deleted" {
			cur = side{present: true, size: c.Size, exec: c.Exec, sha256: c.SHA256}
			data, sum, _, err := readFile(root, path.Join(dir, c.Path), maxDiffFile)
			if err != nil {
				return "", false, fmt.Errorf("github: reading %s: %w", c.Path, err)
			}
			if int64(len(data)) <= maxDiffFile && sum != c.SHA256 {
				return "", false, fmt.Errorf("github: %s changed while it was being read; try again", c.Path)
			}
			cur.data = data
		}
		text, ok := fileDiff(c.Path, old, cur)
		b.WriteString(text)
		complete = complete && ok
	}
	return b.String(), complete, nil
}

// changeSummary lists changes as "M path", "A path", "D path" lines.
func changeSummary(changes []change, limit int) string {
	var b strings.Builder
	for i, c := range changes {
		if i == limit {
			fmt.Fprintf(&b, "… and %d more\n", len(changes)-limit)
			break
		}
		fmt.Fprintf(&b, "%s %s", strings.ToUpper(c.Op[:1]), c.Path)
		if c.SentIn > 0 {
			fmt.Fprintf(&b, " (changed again after pull request #%d)", c.SentIn)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sentSummary lists changes a pull request already carried.
func sentSummary(sent []change, limit int) string {
	var b strings.Builder
	for i, c := range sent {
		if i == limit {
			fmt.Fprintf(&b, "… and %d more\n", len(sent)-limit)
			break
		}
		fmt.Fprintf(&b, "%s %s (#%d)\n", strings.ToUpper(c.Op[:1]), c.Path, c.SentIn)
	}
	return b.String()
}

// pullRequests names the pull requests that carried the changes:
// "pull request #3" or "pull requests #1, #3".
func pullRequests(sent []change) string {
	var ns []int
	for _, c := range sent {
		if !slices.Contains(ns, c.SentIn) {
			ns = append(ns, c.SentIn)
		}
	}
	slices.Sort(ns)
	refs := make([]string, len(ns))
	for i, n := range ns {
		refs[i] = fmt.Sprintf("#%d", n)
	}
	if len(ns) == 1 {
		return "pull request " + refs[0]
	}
	return "pull requests " + strings.Join(refs, ", ")
}

// changesDigest identifies a set of changes exactly.
func changesDigest(changes []change) string {
	h := sha256.New()
	for _, c := range changes {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\n", c.Op, c.Path, c.SHA256, c.Exec)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// workspaceRoot opens the run's workspace.
func workspaceRoot(env *core.ToolEnv) (*os.Root, error) {
	if env == nil || env.Workspace == "" {
		return nil, errors.New("this run has no workspace")
	}
	r, err := os.OpenRoot(env.Workspace)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", rootErr(err))
	}
	return r, nil
}
