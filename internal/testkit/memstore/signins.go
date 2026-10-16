package memstore

import (
	"cmp"
	"context"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.SignInStore = (*store)(nil)

type signInKey struct {
	p       core.SignInProvider
	subject string
}

// SignIns implements core.SignInStore.
func (s *store) SignIns() core.SignInRepo { return signInRepo{s} }

type signInRepo struct{ s *store }

func (r signInRepo) Create(_ context.Context, in *core.SignIn) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *in
	row.CreatedAt, row.LastUsed = ms(row.CreatedAt), ms(row.LastUsed)
	switch n := utf8.RuneCountInString(row.Subject); {
	case !oneOf(row.Provider, core.SignInProviders...):
		return invalid("sign_ins.provider")
	case n < 1 || n > 255:
		return invalid("sign_ins.subject")
	case utf8.RuneCountInString(row.Email) > 320:
		return invalid("sign_ins.email")
	case utf8.RuneCountInString(row.Login) > 64:
		return invalid("sign_ins.login")
	case st.users[row.User].ID == "":
		return invalid("sign_ins.user_id: no such user")
	}
	k := signInKey{row.Provider, row.Subject}
	if _, ok := st.signIns[k]; ok {
		return conflict("sign_ins primary key")
	}
	for _, s := range st.signIns {
		if s.User == row.User && s.Provider == row.Provider {
			return conflict("sign_ins_user_provider")
		}
	}
	st.signIns[k] = row
	return nil
}

func (r signInRepo) Get(_ context.Context, p core.SignInProvider, subject string) (*core.SignIn, error) {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.signIns[signInKey{p, subject}]
	if !ok {
		return nil, notFound("sign-in")
	}
	return &s, nil
}

func (r signInRepo) ForUser(_ context.Context, u core.UserID) ([]*core.SignIn, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := []*core.SignIn{}
	for _, s := range st.signIns {
		if s.User == u {
			out = append(out, &s)
		}
	}
	slices.SortFunc(out, func(a, b *core.SignIn) int { return cmp.Compare(a.Provider, b.Provider) })
	return out, nil
}

func (r signInRepo) Touch(_ context.Context, p core.SignInProvider, subject string, at time.Time, email, login string) error {
	st, unlock := r.s.begin()
	defer unlock()
	k := signInKey{p, subject}
	s, ok := st.signIns[k]
	if !ok {
		return notFound("sign-in")
	}
	if utf8.RuneCountInString(email) > 320 || utf8.RuneCountInString(login) > 64 {
		return invalid("sign_ins email or login")
	}
	s.LastUsed, s.Email, s.Login = ms(at), email, login
	st.signIns[k] = s
	return nil
}

func (r signInRepo) Delete(_ context.Context, u core.UserID, p core.SignInProvider) error {
	st, unlock := r.s.begin()
	defer unlock()
	for k, s := range st.signIns {
		if s.User == u && s.Provider == p {
			delete(st.signIns, k)
			return nil
		}
	}
	return notFound("sign-in")
}
