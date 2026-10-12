package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Config   config.View
	Audit    core.Auditor
	Bus      core.Bus
	Redactor core.Redactor
	DataDir  string // setup-token file location
	Clock    core.Clock
	Log      *slog.Logger
}

// Service implements core.Auth and core.CheckProvider.
type Service struct {
	core.Auth
	dataDir string
}

func New(d Deps) (*Service, error) { return &Service{dataDir: d.DataDir}, nil }

func (s *Service) Checks() []core.Check { return nil }

// EnsureSetupToken is a boot-path stub until M11 lands: it writes
// <DataDir>/setup-token (0600) once so the boot banner appears. There is no
// owner check, hash in meta, expiry or claim yet.
func (s *Service) EnsureSetupToken(ctx context.Context) (string, bool, error) {
	if s.dataDir == "" {
		return "", false, nil
	}
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return "", false, err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, err
	}
	tok := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	f, err := os.OpenFile(filepath.Join(s.dataDir, "setup-token"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return "", false, err
	}
	if err := f.Close(); err != nil {
		return "", false, err
	}
	return tok, true, nil
}
