package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/rielicat/bothy/internal/core"
)

const (
	historyFile = "history.jsonl"
	historyMax  = 50
	// historyMaxBytes bounds the file; the newest change is kept even when
	// it alone is larger (notes may be up to 1 MiB before and after).
	historyMaxBytes = 4 << 20
	historyReadMax  = 2 * historyMaxBytes
)

func (s *Service) historyPath(r *core.IndexedRepo) (string, error) {
	dir, err := notesDir(s.d.DataDir, r.Principal, r.Repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, historyFile), nil
}

// readHistory returns the stored changes oldest first; lines it cannot
// read are skipped.
func (s *Service) readHistory(r *core.IndexedRepo) ([]core.NotesVersion, error) {
	p, err := s.historyPath(r)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReader(io.LimitReader(f, historyReadMax))
	var out []core.NotesVersion
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 1 {
			var v core.NotesVersion
			if json.Unmarshal(line, &v) == nil && v.ID > 0 {
				out = append(out, v)
			}
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// recordVersion appends one change to the repo's notes history. The caller
// holds notesMu and has written the notes; a history that cannot be written
// is logged, never a reason to undo the edit.
func (s *Service) recordVersion(r *core.IndexedRepo, v core.NotesVersion) {
	if v.Before == v.After {
		return
	}
	list, err := s.readHistory(r)
	if err != nil {
		s.log.Warn("knowledge: read notes history", "repo", r.ID, "err", err)
	}
	v.ID = 1
	if n := len(list); n > 0 {
		v.ID = list[n-1].ID + 1
	}
	v.At = s.clock.Now()
	list = append(list, v)
	lines := make([][]byte, 0, len(list))
	total := 0
	for _, x := range list {
		b, err := json.Marshal(x)
		if err != nil {
			s.log.Warn("knowledge: encode notes history", "repo", r.ID, "err", err)
			return
		}
		lines = append(lines, append(b, '\n'))
		total += len(b) + 1
	}
	for len(lines) > 1 && (len(lines) > historyMax || total > historyMaxBytes) {
		total -= len(lines[0])
		lines = lines[1:]
	}
	p, err := s.historyPath(r)
	if err == nil {
		err = writeFileAtomic(p, slices.Concat(lines...))
	}
	if err != nil {
		s.log.Warn("knowledge: write notes history", "repo", r.ID, "err", err)
	}
}

func (s *Service) NotesHistory(ctx context.Context, v core.Viewer, id int64) ([]core.NotesVersion, error) {
	r, err := s.d.Repo.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	s.notesMu.Lock()
	list, err := s.readHistory(r)
	s.notesMu.Unlock()
	if err != nil {
		return nil, err
	}
	slices.Reverse(list)
	return list, nil
}

func (s *Service) NotesVersion(ctx context.Context, v core.Viewer, id, version int64) (*core.NotesVersion, error) {
	list, err := s.NotesHistory(ctx, v, id)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == version {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("knowledge: notes version %d: %w", version, core.ErrNotFound)
}
