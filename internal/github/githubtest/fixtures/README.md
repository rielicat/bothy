# GitHub REST API fixtures

Response examples copied byte for byte (as `jq` prints them) from GitHub's
OpenAPI description, [github/rest-api-description](https://github.com/github/rest-api-description)
`descriptions/api.github.com/api.github.com.json` (info.version 1.1.4),
`components.examples.<name>.value`, fetched 2026-10-14. The fake server uses
them as response templates and overwrites the fields its state decides
(ids, names, numbers, SHAs, tokens, timestamps).

| File | Operation it answers |
|---|---|
| `private-user-response-with-public-and-private-profile-information.json` | `GET /user` (users/get-authenticated) |
| `repository-items-default-response.json` | item template of `GET /user/repos` (repos/list-for-authenticated-user); also answers `GET /repos/{o}/{r}` (repos/get), whose full-repository shape has the same fields Bothy reads |
| `repository-paginated-2.json` | `GET /installation/repositories` (apps/list-repos-accessible-to-installation) |
| `integration.json` | `GET /app` (apps/get-authenticated) |
| `base-installation-items.json` | `GET /app/installations`, item template of `GET /app/installations/{id}` |
| `installation-token.json` | `POST /app/installations/{id}/access_tokens` (apps/create-installation-access-token) |
| `git-commit-2.json` | `GET /repos/{o}/{r}/git/commits/{sha}` (git/get-commit) |
| `short-blob.json` | `POST /repos/{o}/{r}/git/blobs` (git/create-blob) |
| `git-tree.json` | `POST /repos/{o}/{r}/git/trees` (git/create-tree) |
| `git-commit.json` | `POST /repos/{o}/{r}/git/commits` (git/create-commit) |
| `git-ref.json` | `POST /repos/{o}/{r}/git/refs`, `GET /repos/{o}/{r}/git/ref/{ref}` |
| `pull-request.json` | `POST /repos/{o}/{r}/pulls`, `GET /repos/{o}/{r}/pulls/{n}` |
| `pull-request-simple-items.json` | `GET /repos/{o}/{r}/pulls` (pulls/list) |
| `diff-entry-items.json` | `GET /repos/{o}/{r}/pulls/{n}/files` (pulls/list-files) |
| `pull-request-review-comment-items.json` | `GET /repos/{o}/{r}/pulls/{n}/comments` |
| `issue.json` | `GET /repos/{o}/{r}/issues/{n}` (issues/get) |
| `issue-items.json` | `GET /repos/{o}/{r}/issues` (issues/list-for-repo) |
| `issue-comment-items.json` | `GET /repos/{o}/{r}/issues/{n}/comments` |
| `issue-comment.json` | `POST /repos/{o}/{r}/issues/{n}/comments` (issues/create-comment) |
| `workflow-run-paginated.json` | `GET /repos/{o}/{r}/actions/runs`, `…/actions/workflows/{id}/runs` |
| `workflow-run.json` | `GET /repos/{o}/{r}/actions/runs/{id}` |
| `job-paginated.json` | `GET /repos/{o}/{r}/actions/runs/{id}/jobs` |

Written by hand in the documented shapes, in `githubtest.go`: error bodies
(`{"message", "documentation_url", "status"}`, with `errors` for 422) and the
rate-limit headers and secondary-limit `retry-after` of
[Rate limits for the REST API](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api);
`Link` pagination headers per
[Using pagination in the REST API](https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api);
the bare SHA of the `application/vnd.github.sha` media type of
`GET /repos/{o}/{r}/commits/{ref}`; the `302` of
`GET /repos/{o}/{r}/tarball/{ref}` to a codeload URL; the review items of
`GET /repos/{o}/{r}/pulls/{n}/reviews` (pulls/list-reviews), with the user
object of `pull-request.json`; and the archive layout
`git archive` produces (one `<owner>-<repo>-<sha7>/` folder, a pax global
header carrying the commit SHA); and the `GitHub-Authentication-Token-Expiration`
header of expiring tokens.

The JSON files are © 2020 GitHub, MIT License:

> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.
