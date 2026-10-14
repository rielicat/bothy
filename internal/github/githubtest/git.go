package githubtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // git object names are SHA-1; this is a test fake
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// Repo is a repository of the fake: git objects, refs, issues, pull
// requests and workflow runs. Methods lock the server.
type Repo struct {
	srv           *Server
	ID            int64
	Owner, Name   string
	DefaultBranch string
	Description   string
	Private       bool
	Archived      bool

	blobs    map[string][]byte
	trees    map[string]map[string]entry // tree sha -> path -> entry
	commits  map[string]*commit
	refs     map[string]string // "heads/main" -> commit sha
	issues   []*Issue
	comments map[int][]Comment
	pulls    []*Pull
	runs     []*Run
	tarballs map[string][]byte // commit sha -> archive served instead of a generated one
	symlinks map[string]string // extra symlinks (path -> target) put into generated archives
	seq      int
}

type entry struct {
	sha  string
	mode string // 100644 | 100755
}

type commit struct {
	sha, tree, message string
	parents            []string
}

// Issue is a seeded issue.
type Issue struct {
	Number int
	Title  string
	Body   string
	State  string // open | closed
	Labels []string
	User   string
}

// Comment is an issue or pull request comment.
type Comment struct {
	ID   int64
	User string
	Body string
}

// Pull is a pull request created through the API or seeded.
type Pull struct {
	Number  int
	Title   string
	Body    string
	Head    string
	Base    string
	HeadSHA string
	BaseSHA string
	Draft   bool
	State   string
}

// Run is a seeded workflow run.
type Run struct {
	ID         int64
	Name       string
	Workflow   string // file name, e.g. ci.yml
	Branch     string
	Event      string
	Status     string
	Conclusion string
	Title      string
	HeadSHA    string    // "" keeps the fixture's
	CreatedAt  time.Time // zero keeps the fixture's
}

func (r *Repo) FullName() string { return r.Owner + "/" + r.Name }

func gitSHA(kind string, b []byte) string {
	h := sha1.New() //nolint:gosec // git object names
	fmt.Fprintf(h, "%s %d\x00", kind, len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Repo) putBlob(b []byte) string {
	sha := gitSHA("blob", b)
	r.blobs[sha] = bytes.Clone(b)
	return sha
}

func (r *Repo) putTree(t map[string]entry) string {
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%s %s %s\n", t[p].mode, p, t[p].sha)
	}
	sha := gitSHA("tree", []byte(b.String()))
	cp := make(map[string]entry, len(t))
	for k, v := range t {
		cp[k] = v
	}
	r.trees[sha] = cp
	return sha
}

func (r *Repo) putCommit(tree, message string, parents []string) string {
	r.seq++
	body := fmt.Sprintf("tree %s\nparents %s\nseq %d\n\n%s", tree, strings.Join(parents, " "), r.seq, message)
	sha := gitSHA("commit", []byte(body))
	r.commits[sha] = &commit{sha: sha, tree: tree, message: message, parents: slices.Clone(parents)}
	return sha
}

// treeOf builds a tree from file contents; a path ending in "*" is
// executable (the star is dropped).
func (r *Repo) treeOf(files map[string]string) string {
	t := map[string]entry{}
	for p, content := range files {
		mode := "100644"
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), "100755"
		}
		t[p] = entry{sha: r.putBlob([]byte(content)), mode: mode}
	}
	return r.putTree(t)
}

// Commit records files (path -> content; a trailing * marks an executable)
// as a new commit on the default branch and returns its SHA.
func (r *Repo) Commit(files map[string]string, message string) string {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	var parents []string
	if head, ok := r.refs["heads/"+r.DefaultBranch]; ok {
		parents = []string{head}
	}
	sha := r.putCommit(r.treeOf(files), message, parents)
	r.refs["heads/"+r.DefaultBranch] = sha
	return sha
}

// Branch points a branch (or "tags/<name>" for a tag) at a commit.
func (r *Repo) Branch(name, sha string) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	if !strings.HasPrefix(name, "tags/") {
		name = "heads/" + name
	}
	r.refs[name] = sha
}

// Head returns the commit a branch points at.
func (r *Repo) Head(branch string) string {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	return r.refs["heads/"+branch]
}

// File returns a file's content at a branch or commit.
func (r *Repo) File(ref, p string) (string, bool) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	sha := r.resolve(ref)
	c := r.commits[sha]
	if c == nil {
		return "", false
	}
	e, ok := r.trees[c.tree][p]
	if !ok {
		return "", false
	}
	return string(r.blobs[e.sha]), true
}

// Files lists the paths and modes at a branch or commit.
func (r *Repo) Files(ref string) map[string]string {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	out := map[string]string{}
	if c := r.commits[r.resolve(ref)]; c != nil {
		for p, e := range r.trees[c.tree] {
			out[p] = e.mode
		}
	}
	return out
}

// CommitMessage returns a commit's message and parents.
func (r *Repo) CommitMessage(sha string) (string, []string) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	if c := r.commits[sha]; c != nil {
		return c.message, slices.Clone(c.parents)
	}
	return "", nil
}

// SetTarball serves b as the archive of a commit instead of generating one.
func (r *Repo) SetTarball(sha string, b []byte) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	r.tarballs[sha] = b
}

// AddSymlink puts a symbolic link into every generated archive.
func (r *Repo) AddSymlink(p, target string) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	r.symlinks[p] = target
}

// AddIssue seeds an open issue and returns its number.
func (r *Repo) AddIssue(title, body string, labels ...string) int {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	n := r.nextNumber()
	r.issues = append(r.issues, &Issue{Number: n, Title: title, Body: body, State: "open", Labels: labels, User: "octocat"})
	return n
}

// AddComment seeds a comment on an issue or pull request.
func (r *Repo) AddComment(number int, user, body string) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	r.srv.nextID++
	r.comments[number] = append(r.comments[number], Comment{ID: r.srv.nextID, User: user, Body: body})
}

// Comments returns the comments of an issue or pull request.
func (r *Repo) Comments(number int) []Comment {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	return slices.Clone(r.comments[number])
}

// AddRun seeds a workflow run and returns its id.
func (r *Repo) AddRun(run Run) int64 {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	r.srv.nextID++
	run.ID = r.srv.nextID
	r.runs = append(r.runs, &run)
	return run.ID
}

// SetRun moves a workflow run to status and conclusion ("" while running).
func (r *Repo) SetRun(id int64, status, conclusion string) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	for _, run := range r.runs {
		if run.ID == id {
			run.Status, run.Conclusion = status, conclusion
		}
	}
}

// Pulls returns the repository's pull requests.
func (r *Repo) Pulls() []Pull {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	out := make([]Pull, len(r.pulls))
	for i, p := range r.pulls {
		out[i] = *p
	}
	return out
}

func (r *Repo) nextNumber() int {
	n := 1
	for _, is := range r.issues {
		n = max(n, is.Number+1)
	}
	for _, p := range r.pulls {
		n = max(n, p.Number+1)
	}
	return n
}

// resolve turns a full SHA, branch or tag into a commit SHA ("" if none).
func (r *Repo) resolve(ref string) string {
	if _, ok := r.commits[ref]; ok {
		return ref
	}
	for _, k := range []string{"heads/" + ref, "tags/" + ref, ref} {
		if sha, ok := r.refs[k]; ok {
			return sha
		}
	}
	return ""
}

// archive builds the tarball git archive would: one <owner>-<repo>-<sha7>/
// folder and a pax global header carrying the commit SHA.
func (r *Repo) archive(sha string) ([]byte, error) {
	if b, ok := r.tarballs[sha]; ok {
		return b, nil
	}
	c := r.commits[sha]
	if c == nil {
		return nil, fmt.Errorf("no commit %s", sha)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	top := r.Owner + "-" + r.Name + "-" + sha[:7] + "/"
	mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": sha}}); err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top, Mode: 0o775, ModTime: mtime}); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(r.trees[c.tree]))
	for p := range r.trees[c.tree] {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	dirs := map[string]bool{}
	for _, p := range paths {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if dirs[d] {
				break
			}
			dirs[d] = true
		}
	}
	dlist := make([]string, 0, len(dirs))
	for d := range dirs {
		dlist = append(dlist, d)
	}
	slices.Sort(dlist)
	for _, d := range dlist {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top + d + "/", Mode: 0o775, ModTime: mtime}); err != nil {
			return nil, err
		}
	}
	for _, p := range paths {
		e := r.trees[c.tree][p]
		data := r.blobs[e.sha]
		mode := int64(0o664)
		if e.mode == "100755" {
			mode = 0o775
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: top + p, Mode: mode, Size: int64(len(data)), ModTime: mtime}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	links := make([]string, 0, len(r.symlinks))
	for p := range r.symlinks {
		links = append(links, p)
	}
	slices.Sort(links)
	for _, p := range links {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: top + p, Linkname: r.symlinks[p], Mode: 0o777, ModTime: mtime}); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// changed lists the paths that differ between two commits' trees.
func (r *Repo) changed(base, head string) []string {
	bt, ht := map[string]entry{}, map[string]entry{}
	if c := r.commits[base]; c != nil {
		bt = r.trees[c.tree]
	}
	if c := r.commits[head]; c != nil {
		ht = r.trees[c.tree]
	}
	var out []string
	for p, e := range ht {
		if b, ok := bt[p]; !ok || b != e {
			out = append(out, p)
		}
	}
	for p := range bt {
		if _, ok := ht[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}
