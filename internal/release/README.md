# internal/release (v0.2 F5)

`release.Service` provides `release_deploy` and `release_status`, follows
dispatched workflow runs and reports their outcome. It is built by
`cmd/bothy` over `core.Store`, the store's `core.ReleaseRepo`, the vault, the
system egress client and the channel hub (`Messenger`).

**release_deploy** `{environment, ref}` (irreversible, interactive runs only,
not offered to spaces, hidden while `releases.environments` is empty) plans an
`ActRelease` action whose payload is what the approvers see and exactly what
runs: environment, repository, workflow, ref (defaulting to `ref_pattern`
when it names one ref; otherwise it must match the glob) and the fixed
inputs. Policy decides it with the release floor and the environment's
approvers vote on it (see `internal/policy`). `Exec` refuses unless the
environment is unchanged and this very call's approval was allowed by at
least `required` approvers other than the requester, whose votes still
satisfy the environment's current approvers and `required` (an approval may
park while an admin tightens them), then dispatches the
workflow and records a `core.Deploy` (also when GitHub refuses, as failed).
The dispatch and the record survive a cancelled tool call.

**GitHub.** A deploy of a repository on a GitHub connection's allowlist is
dispatched and followed through that connection (`Deps.Connections`, the
GitHub module's `core.WorkflowDispatcher`), chosen per call. Every other
repository uses `GitHub`, a `core.WorkflowDispatcher` over the REST API at
`releases.github_api` (locked; https, or http only for a loopback address;
private addresses only for a non-default host) with the token from
`releases.github_token`, resolved from the vault on every call and sent only
to that host. Dispatch asks for the run id (`return_run_details`); a server
that refuses that key with 422 is asked once more without it, and a 204
falls back to finding the newest `workflow_dispatch` run of the ref created
since the dispatch (30 s).
Runs keep only checked fields (status and conclusion enums, numbers, a hex
sha); run links are built from the web base, never taken from `html_url`, and
repository-authored names and titles are dropped, so results stay trusted.

**Following.** `Run` checks open deploys every `releases.poll_interval` and
right after a dispatch, including those left open by a restart. A finished
run, a run GitHub no longer knows, a run that was never identified or one
older than `releases.follow_for` ends the deploy; the outcome is posted as a
Bothy notice to the session (web UI, `message.created`) and, for chat runs,
to the originating chat, and audited as `release.finish`
(`release.dispatch` for dispatches and refusals).

**release_status** `{environment?}` (read) lists environments with who
approves and their last three deploys visible to the caller, with the live
state of up to three followed runs.

Check: `releases.github_token` (an environment whose repository no
connection serves, and the token is missing).
`actionstest` fakes the Actions endpoints for unit and e2e tests.
