package store

import (
	"context"
	"database/sql"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.ReleaseStore = (*DB)(nil)

// Releases implements core.ReleaseStore.
func (h *handle) Releases() core.ReleaseRepo { return releaseRepo{h} }

var deployStatuses = []core.DeployStatus{core.DeployDispatched, core.DeployRunning, core.DeploySucceeded, core.DeployFailed, core.DeployUnknown}

var deployVias = []string{"", core.DeployViaConnection, core.DeployViaToken}

const deployCols = `id, environment, repo, workflow, ref, principal_id, session_id, run_id, approval_id, requested_by, approved_by,
	chat, status, workflow_run_id, run_url, conclusion, error, created_at, updated_at, finished_at, via`

func scanDeploy(sc scanner) (*core.Deploy, error) {
	var d core.Deploy
	var approved string
	var chat sql.NullString
	var created, updated, finished int64
	if err := sc.Scan(&d.ID, &d.Environment, &d.Repo, &d.Workflow, &d.Ref, &d.Principal, &d.Session, &d.Run, &d.Approval,
		&d.RequestedBy, &approved, &chat, &d.Status, &d.WorkflowRun, &d.RunURL, &d.Conclusion, &d.Error,
		&created, &updated, &finished, &d.Via); err != nil {
		return nil, err
	}
	if err := decodeJSON("release_deploys.approved_by", approved, &d.ApprovedBy); err != nil {
		return nil, err
	}
	var err error
	if d.Chat, err = decodeNullable[core.ChatRef]("release_deploys.chat", chat); err != nil {
		return nil, err
	}
	d.CreatedAt, d.UpdatedAt, d.FinishedAt = fromMS(created), fromMS(updated), fromMS(finished)
	return &d, nil
}

// deployVisible is the Viewer rule for deploy records: the viewer's
// principals, every row for admins.
func deployVisible(v core.Viewer) (cond string, args []any, ok bool) {
	if v.Admin && !v.System {
		return "1", nil, true
	}
	return viewerCond("principal_id", v)
}

type releaseRepo struct{ h *handle }

func deployArgs(d *core.Deploy) ([]any, error) {
	approved := d.ApprovedBy
	if approved == nil {
		approved = []core.UserID{}
	}
	ab, err := jsonText("release_deploys.approved_by", approved)
	if err != nil {
		return nil, err
	}
	chat, err := jsonNullable("release_deploys.chat", d.Chat)
	if err != nil {
		return nil, err
	}
	return []any{d.Environment, d.Repo, d.Workflow, d.Ref, string(d.Principal), string(d.Session), string(d.Run),
		string(d.Approval), string(d.RequestedBy), ab, chat, string(d.Status), d.WorkflowRun, d.RunURL, d.Conclusion,
		d.Error, ms(d.CreatedAt), ms(d.UpdatedAt), ms(d.FinishedAt), d.Via}, nil
}

func validDeploy(d *core.Deploy) error {
	if !oneOf(d.Status, deployStatuses...) {
		return invalid("release_deploys.status")
	}
	if !oneOf(d.Via, deployVias...) {
		return invalid("release_deploys.via")
	}
	return nil
}

func (r releaseRepo) Create(ctx context.Context, d *core.Deploy) error {
	if err := validDeploy(d); err != nil {
		return err
	}
	id := idOr(d.ID, core.PrefixDeploy)
	args, err := deployArgs(d)
	if err != nil {
		return err
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO release_deploys(`+deployCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		append([]any{id}, args...)...); err != nil {
		return dbErr("create deploy", err)
	}
	d.ID = id
	return nil
}

func (r releaseRepo) Update(ctx context.Context, d *core.Deploy) error {
	if err := validDeploy(d); err != nil {
		return err
	}
	args, err := deployArgs(d)
	if err != nil {
		return err
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE release_deploys SET environment = ?, repo = ?, workflow = ?, ref = ?, principal_id = ?,
		session_id = ?, run_id = ?, approval_id = ?, requested_by = ?, approved_by = ?, chat = ?, status = ?, workflow_run_id = ?,
		run_url = ?, conclusion = ?, error = ?, created_at = ?, updated_at = ?, finished_at = ?, via = ? WHERE id = ?`, append(args, d.ID)...)
	return mustAffect("update deploy", res, err)
}

func (r releaseRepo) Get(ctx context.Context, v core.Viewer, id string) (*core.Deploy, error) {
	cond, args, ok := deployVisible(v)
	if !ok {
		return nil, notFound("deploy")
	}
	return queryOne(ctx, r.h.rq, "deploy", scanDeploy, `SELECT `+deployCols+` FROM release_deploys WHERE id = ? AND `+cond,
		append([]any{id}, args...)...)
}

func (r releaseRepo) List(ctx context.Context, v core.Viewer, f core.DeployFilter) ([]*core.Deploy, error) {
	cond, args, ok := deployVisible(v)
	if !ok {
		return nil, nil
	}
	q := `SELECT ` + deployCols + ` FROM release_deploys WHERE ` + cond
	if f.Environment != "" {
		q += ` AND environment = ?`
		args = append(args, f.Environment)
	}
	if f.Open {
		q += ` AND status IN ('dispatched', 'running')`
	}
	if f.Page.Before > 0 {
		q += ` AND created_at < ?`
		args = append(args, f.Page.Before)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, pageLimit(f.Page.Limit))
	return queryAll(ctx, r.h.rq, "deploys", scanDeploy, q, args...)
}
