package store

import (
	"context"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.SignInStore = (*DB)(nil)

// SignIns implements core.SignInStore.
func (h *handle) SignIns() core.SignInRepo { return signInRepo{h} }

const signInCols = `provider, subject, user_id, email, login, created_at, last_used_at`

func scanSignIn(sc scanner) (*core.SignIn, error) {
	var s core.SignIn
	var created, used int64
	if err := sc.Scan(&s.Provider, &s.Subject, &s.User, &s.Email, &s.Login, &created, &used); err != nil {
		return nil, err
	}
	s.CreatedAt, s.LastUsed = fromMS(created), fromMS(used)
	return &s, nil
}

type signInRepo struct{ h *handle }

func (r signInRepo) Create(ctx context.Context, s *core.SignIn) error {
	if !oneOf(s.Provider, core.SignInProviders...) {
		return invalid("sign_ins.provider")
	}
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO sign_ins(`+signInCols+`) VALUES (?,?,?,?,?,?,?)`,
		string(s.Provider), s.Subject, string(s.User), s.Email, s.Login, ms(s.CreatedAt), ms(s.LastUsed))
	return dbErr("create sign-in", err)
}

func (r signInRepo) Get(ctx context.Context, p core.SignInProvider, subject string) (*core.SignIn, error) {
	return queryOne(ctx, r.h.rq, "sign-in", scanSignIn, `SELECT `+signInCols+` FROM sign_ins WHERE provider = ? AND subject = ?`,
		string(p), subject)
}

func (r signInRepo) ForUser(ctx context.Context, u core.UserID) ([]*core.SignIn, error) {
	out, err := queryAll(ctx, r.h.rq, "sign-ins", scanSignIn, `SELECT `+signInCols+` FROM sign_ins WHERE user_id = ? ORDER BY provider`, string(u))
	if out == nil && err == nil {
		out = []*core.SignIn{}
	}
	return out, err
}

func (r signInRepo) Touch(ctx context.Context, p core.SignInProvider, subject string, at time.Time, email, login string) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE sign_ins SET last_used_at = ?, email = ?, login = ? WHERE provider = ? AND subject = ?`,
		ms(at), email, login, string(p), subject)
	return mustAffect("touch sign-in", res, err)
}

func (r signInRepo) Delete(ctx context.Context, u core.UserID, p core.SignInProvider) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM sign_ins WHERE user_id = ? AND provider = ?`, string(u), string(p))
	return mustAffect("delete sign-in", res, err)
}
