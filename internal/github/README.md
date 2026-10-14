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

Tools (gated on an allowlisted repository; never offered to restricted spaces):
`repo_list`; `repo_checkout` downloads the tarball of a resolved commit and
extracts it through `os.Root` into `repos/<owner>/<name>` (size and file caps,
links, devices, `.git` and unsafe names dropped, swapped in atomically) and
keeps the manifest and pristine archive under `<data>/github/checkouts`, out of
every box's reach, then publishes `repo.checkout`; `repo_changes` diffs the
workspace copy against that base, honouring `.gitignore`; `repo_open_pr`
(`AlwaysAsk`, recipient `github:<owner>/<name>` for first contact) puts the
full diff on the card behind a format-patch style header (repository, head →
base, title, indented description), re-hashes every file at Exec so only the approved bytes
are sent, and writes blobs, a tree on the checked-out commit, a commit, a
branch and the pull request (deleting the branch if the PR is refused);
`github_issues`, `github_pr`, `github_runs` (read) and `github_comment`
(external_send). Content tools and `repo_checkout` taint the session private
and untrusted and mark their results untrusted, so they are spotlighted and
the trifecta floor applies. `githubtest` is a fake GitHub API built from the examples in GitHub's
REST description.
