package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rielicat/bothy/internal/core"
)

func (s *Service) snapshotDir() string { return filepath.Join(s.dataDir, "github", "snapshots") }

// Snapshot writes the files an allowed repository holds at sha ("" = the
// head of its default branch now) into a new folder outside every
// workspace, for Knowledge to index exactly what the commit holds: the
// checkout in a workspace is edited by boxes, so its files can differ from
// the commit they would be cited at. The pristine archive kept with ws's
// checkout is reused when it holds that commit; otherwise the archive is
// downloaded. done removes the folder.
func (s *Service) Snapshot(ctx context.Context, ws core.WorkspaceID, repo, sha string) (dir, resolved string, done func(), err error) {
	if s.dataDir == "" {
		return "", "", nil, errors.New("github: no data directory for snapshots")
	}
	r, err := s.allowedRepo(ctx, repo)
	if err != nil {
		return "", "", nil, err
	}
	if sha != "" && !shaRE.MatchString(sha) {
		return "", "", nil, fmt.Errorf("%w: %q is not a commit id", core.ErrInvalid, sha)
	}
	if err := os.MkdirAll(s.snapshotDir(), 0o700); err != nil {
		return "", "", nil, fmt.Errorf("github: creating the snapshot folder: %w", rootErr(err))
	}
	tmp, err := os.MkdirTemp(s.snapshotDir(), "snap-")
	if err != nil {
		return "", "", nil, fmt.Errorf("github: creating the snapshot folder: %w", rootErr(err))
	}
	remove := func() { _ = os.RemoveAll(tmp) }
	defer func() {
		if err != nil {
			remove()
		}
	}()
	archive := s.storedArchive(ws, r, sha)
	lim := s.limits()
	if archive == nil {
		conn, err := s.connectionFor(ctx, r)
		if err != nil {
			return "", "", nil, err
		}
		cl, ep, err := s.client(conn)
		if err != nil {
			return "", "", nil, err
		}
		if sha == "" {
			if r.DefaultBranch == "" {
				return "", "", nil, fmt.Errorf("%w: %s has no known default branch", core.ErrInvalid, r.FullName())
			}
			if sha, err = resolveSHA(ctx, cl, r, r.DefaultBranch); err != nil {
				return "", "", nil, err
			}
		}
		dl := filepath.Join(tmp, archiveName)
		if err := s.download(ctx, cl, ep, r, sha, dl, lim.maxBytes); err != nil {
			return "", "", nil, err
		}
		if archive, err = os.Open(dl); err != nil {
			return "", "", nil, fmt.Errorf("github: opening the downloaded archive: %w", rootErr(err))
		}
	}
	defer archive.Close()
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return "", "", nil, err
	}
	defer root.Close()
	if err := root.Mkdir("tree", 0o700); err != nil {
		return "", "", nil, fmt.Errorf("github: creating the snapshot folder: %w", rootErr(err))
	}
	if _, _, err := extract(root, "tree", archive, lim); err != nil {
		if errors.Is(err, errTooLarge) || errors.Is(err, errTooMany) {
			return "", "", nil, fmt.Errorf("%w: %s is %w", core.ErrInvalid, r.FullName(), err)
		}
		return "", "", nil, err
	}
	return filepath.Join(tmp, "tree"), sha, remove, nil
}

// storedArchive opens the pristine archive of ws's checkout of r when it
// holds sha, else nil: then GitHub serves it. sha "" asks for the branch
// head, which only GitHub can say. The checkout lock keeps a new checkout
// from swapping the archive between reading the manifest and opening it.
func (s *Service) storedArchive(ws core.WorkspaceID, r *core.GitHubRepo, sha string) *os.File {
	if sha == "" {
		return nil
	}
	store, err := s.checkoutStore(ws, r.Owner, r.Name)
	if err != nil {
		return nil
	}
	defer s.lockRepo(ws, r)()
	if m, err := loadManifest(store); err != nil || m == nil || m.SHA != sha {
		return nil
	}
	f, err := os.Open(filepath.Join(store, archiveName))
	if err != nil {
		return nil
	}
	return f
}
