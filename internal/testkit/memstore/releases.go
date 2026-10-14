package memstore

import (
	"context"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.ReleaseStore = (*store)(nil)

type deployRow struct {
	d core.Deploy
}

// Releases implements core.ReleaseStore.
func (s *store) Releases() core.ReleaseRepo { return releaseRepo{s} }

type releaseRepo struct{ s *store }

func normDeploy(d core.Deploy) (core.Deploy, error) {
	var err error
	if d.ApprovedBy == nil {
		d.ApprovedBy = []core.UserID{}
	}
	if d.ApprovedBy, err = jsonCol("release_deploys.approved_by", d.ApprovedBy); err != nil {
		return d, err
	}
	if d.Chat, err = jsonCol("release_deploys.chat", d.Chat); err != nil {
		return d, err
	}
	d.CreatedAt, d.UpdatedAt, d.FinishedAt = ms(d.CreatedAt), ms(d.UpdatedAt), ms(d.FinishedAt)
	return d, nil
}

func validDeployStatus(s core.DeployStatus) bool {
	return oneOf(s, core.DeployDispatched, core.DeployRunning, core.DeploySucceeded, core.DeployFailed, core.DeployUnknown)
}

func validDeployVia(v string) bool { return oneOf(v, "", core.DeployViaConnection, core.DeployViaToken) }

func visibleDeploy(v core.Viewer, d core.Deploy) bool { return v.Can(d.Principal) || v.Admin }

func (r releaseRepo) Create(_ context.Context, d *core.Deploy) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *d
	row.ID = idOr(row.ID, core.PrefixDeploy)
	switch {
	case !validDeployStatus(row.Status):
		return invalid("release_deploys.status")
	case !validDeployVia(row.Via):
		return invalid("release_deploys.via")
	case st.principals[row.Principal].ID == "":
		return invalid("release_deploys.principal_id: no such principal")
	}
	if _, ok := st.deploys[row.ID]; ok {
		return conflict("release_deploys.id")
	}
	row, err := normDeploy(row)
	if err != nil {
		return err
	}
	st.deploys[row.ID] = deployRow{d: row}
	d.ID = row.ID
	return nil
}

func (r releaseRepo) Update(_ context.Context, d *core.Deploy) error {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.deploys[d.ID]; !ok {
		return notFound("deploy")
	}
	switch {
	case !validDeployStatus(d.Status):
		return invalid("release_deploys.status")
	case !validDeployVia(d.Via):
		return invalid("release_deploys.via")
	case st.principals[d.Principal].ID == "":
		return invalid("release_deploys.principal_id: no such principal")
	}
	row, err := normDeploy(*d)
	if err != nil {
		return err
	}
	st.deploys[d.ID] = deployRow{d: row}
	return nil
}

func (r releaseRepo) Get(_ context.Context, v core.Viewer, id string) (*core.Deploy, error) {
	st, unlock := r.s.begin()
	defer unlock()
	row, ok := st.deploys[id]
	if !ok || !visibleDeploy(v, row.d) {
		return nil, notFound("deploy")
	}
	d := must(normDeploy(row.d))
	return &d, nil
}

func (r releaseRepo) List(_ context.Context, v core.Viewer, f core.DeployFilter) ([]*core.Deploy, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Deploy
	for _, row := range st.deploys {
		d := row.d
		switch {
		case !visibleDeploy(v, d),
			f.Environment != "" && d.Environment != f.Environment,
			f.Open && d.Status.Terminal(),
			f.Page.Before > 0 && unixMS(d.CreatedAt) >= f.Page.Before:
			continue
		}
		out = append(out, must(normDeploy(d)))
	}
	out = newestFirst(out, pageLimit(f.Page.Limit), func(d core.Deploy) (int64, string) { return unixMS(d.CreatedAt), d.ID })
	return ptrs(out), nil
}
