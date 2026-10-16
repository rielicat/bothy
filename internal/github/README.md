# github

`core.GitHub` (v0.2 brief §4). A connection is a fine-grained personal access
token or a GitHub App (app id + private key; installation tokens are minted
with an RS256 JWT and cached until five minutes before they expire, and
registered with the redactor). `Connect` probes the credentials, stores the
secret in the global vault scope (or takes an existing `secret:<name>`) and
allowlists repositories; rows live in migration `0002_repos.sql` through
`core.GitHubStore`. Every API call goes through `Egress.SystemClient` for the
`github.api_base` host (plus codeload for archives), follows redirects with the
token only on the API host, pages with `Link`, and turns 401/403/404/422/5xx and
primary or secondary rate limits (`x-ratelimit-*`, `retry-after`) into plain
errors; a 401 drops the cached token and retries once.

The service is also `core.GitHubAppMaker` (`apps.go`, SPEC §14.3): Bothy's own
GitHub App, created through GitHub's App Manifest flow. `AppManifest` builds
the manifest (private, no webhook, and `appPermissions`: metadata read;
contents, pull requests, issues and Actions write, the least the tools and the
release dispatcher call; never administration, secrets or workflows, so a pull
request that edits CI fails with GitHub's reason). `CreateApp` redeems GitHub's
one-time code (`POST /app-manifests/{code}/conversions`, unauthenticated,
never while the vault is sealed, the code scrubbed from every error), keeps the
private key and client secret as `github_<gha id>_key` and
`github_<gha id>_client_secret` in the global vault scope and records the app
with its page: `<web>` and the path of GitHub's `html_url` (`/github-apps/<slug>`
on Enterprise Server), derived when that path isn't the app's
(migration `0012_github_apps.sql`). `InstallApp` connects an installation only
after the app's JWT finds it (`GET /app/installations/{id}`) on the app's
current owner (`GET /app`, which also records a transfer), by GitHub's account id, as an App
connection that owns the key, moves the repositories of the connection it
replaces, and follows a reinstall; `CheckApp` finds the owner's installation itself.
Disconnecting or starting over retires the app (keys deleted, row kept so
Settings can say where to delete it on GitHub); Bothy never deletes apps
there.

Tools (gated on an allowlisted repository; never offered to restricted spaces):
`repo_list` names the account Bothy acts as and counts each checkout's
unsent and already-sent changes (never paths: it is trusted metadata);
`repo_checkout` downloads the tarball of a resolved commit and
extracts it through `os.Root` into `repos/<owner>/<name>` (size and file caps,
links, devices, `.git` and unsafe names dropped, swapped in atomically) and
keeps the manifest and pristine archive under `<data>/github/checkouts`, out of
every box's reach, then publishes `repo.checkout`; `repo_changes` diffs the
workspace copy against that base, honouring `.gitignore`; `repo_open_pr`
(`AlwaysAsk`, recipient `github:<owner>/<name>` for first contact) puts the
full diff on the card behind a format-patch style header (repository, head →
base, title, indented description), re-hashes every file at Exec so only the approved bytes
are sent, and writes blobs, a tree on the checked-out commit, a commit, a
branch and the pull request (deleting the branch if the PR is refused), then
records in the manifest what each file was when sent, so `repo_changes` and
later pull requests set it apart until it changes again (the checkout is
shared by all of a principal's sessions); `github_issues`, `github_pr` (with each
reviewer's latest verdict and the reviews still requested), `github_runs`
(read) and `github_comment` (external_send). Content tools and `repo_checkout` taint the session private
and untrusted and mark their results untrusted, so they are spotlighted and
the trifecta floor applies. The service is also the `core.WorkflowDispatcher`
the release module uses for allowlisted repositories (`Serves`): Dispatch
sends `workflow_dispatch` with `return_run_details` (asking again without it
after a 422 that names it, finding the run among recent `workflow_dispatch`
runs after a 204), and runs keep only checked fields with links built from
the web base. RunStatus and `FailedStep` also read a repository its connection
no longer allows (the release module follows a deploy through the dispatcher
that started it); one no connection holds is refused. `FailedStep` names the
step a failed run stopped at for `release.finished` (the release module cleans
it and never shows it to the model). `Workflows` lists an allowed
repository's `.github/workflows/` files (names cleaned to one plain line),
`BranchHead` reads its default branch's commit, and `CommitSubject` a
commit's first line for deploy lists (also after the allowlist drops the
repository, like `FailedStep`). `githubtest` is a fake GitHub API built
from the examples in
GitHub's REST description, including `workflow_dispatch`, the App Manifest
flow's web pages and conversion, and installation token permissions.
