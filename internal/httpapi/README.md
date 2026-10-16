# internal/httpapi

`httpapi.Server` serves every route of SPEC §8.3 on the stdlib `ServeMux`.
The outer chain adds a request id, recovers panics, sets the security headers
(strict CSP, nosniff, no-referrer, COOP/CORP, `no-store` on `/api` and `/v1`,
HSTS under TLS; never any CORS header), enforces the `Host` allowlist (421),
applies the exposure gate on the peer address (loopback, the container's
default gateway and `local_cidrs` are local; RFC1918/ULA is LAN; plain HTTP
only for local peers, or LAN with `insecure_lan`; `X-Forwarded-*` honoured
only from `trusted_proxies`, and a request a trusted proxy forwarded is never
local, whatever address it names) and rejects credential-named query
parameters. `GET`/`HEAD` `/healthz` and `/readyz` skip the `Host` allowlist
and the exposure gate, so load balancer health checks get 200.
Each route then caps its body, applies safe mode and the setup rules (local
peers only, 404 after completion), authenticates (`bothy_session` cookie on
plain HTTP, `__Host-bothy` under TLS, or a bearer PAT; `/v1` is bearer-only),
runs Go's `CrossOriginProtection` on every unsafe request plus the
`X-CSRF-Token` synchroniser check for cookie and setup sessions, authorizes by
role and token scope, and rate-limits tokens. Handlers always pass the actor's
`Viewer`; by-ID routes look rows up through the store before delegating, so
foreign ids are 404. `GET /api/events` streams the bus as SSE with a hello
frame, `Last-Event-ID` replay, `resync` across boots or on overflow, 15 s
pings, a credential re-check every minute (revoked tokens, ended sessions,
disabled users, role changes and new principals close the stream) and at
most four streams per user: the oldest is closed with a one-minute `retry:`
(every stream starts with 3 s), so extra tabs cannot evict each other in a
loop. With `X-Bothy-Session`, a session that already
has a run is refused (429) rather than steered into a run the request
cannot stream. `/v1/chat/completions` maps OpenAI
requests onto runs (`bothy`, `bothy-fast`) or onto `LLM.Passthrough` for
`<provider>/<model>`, streaming `chat.completion.chunk` frames. Its body is
decoded once; a body over 1 MiB (images) first takes the single large-body
slot, waiting up to 10 s before a 429 with `Retry-After`, and gives it back
once the run is submitted, so concurrent large requests cannot add up past
the memory budget. Refine mode (v0.2 F3) is asked for per message
(`SendReq.refine`, or `X-Bothy-Refine: on|off` on `/v1`), per session
(`PATCH /api/sessions/{id}` `refine`, kept in the meta key
`agent.refine.<session>` and shown in `SessionDetail.refine`), and by the
`agent.refine.enabled` default that `GET /api/refine` reports with the call
bound; `GET /api/runs/{id}` adds the trace of the run's last refined answer,
found among the session's newest messages only for runs with more agent
calls than steps. Uploads and
workspace downloads go through `os.Root` under `<WorkDir>/ws/<id>`, the
persona file through `os.Root` under `<DataDir>`; `DELETE /api/users/{id}`
with `purge=true` wipes the user's boxes, vault scope, grants, schedules,
identities, memory files, project notes, workspaces and their GitHub
checkout data (`<DataDir>/github/checkouts/<ws>`), then deletes the user row,
whose cascade removes sessions, messages, runs and memory events; audit rows
stay. Compactions and MCP installs run in the background, one at a time per
session or server.
The `/api/github` routes (v0.2, `h_github.go`) hand GitHub connection
management to `core.GitHub`: admins test, connect, re-test, set the
repository allowlist and disconnect; any user lists the allowlisted
repositories. Tokens and App keys are write-only; without a GitHub service
the routes answer 503.
The v0.2 knowledge routes (`/api/knowledge/repos[/{id}[/files|/reindex|/notes[/draft]]]`,
`/api/knowledge/search`) sit in `h_knowledge.go`, are appended to the table
in `New`, answer foreign or malformed ids with 404 and need `If-Match` on a
notes `PUT`.
The v0.2 UI routes sit in `h_onboarding.go`: `POST /api/providers/connect`
saves or picks the key (pasted, a saved `secret:<name>` or a detected
environment variable), writes `providers.<id>` (without an `id`, the
kind's default or, when another provider holds it, the next free one such as
`custom-2`) and puts the chosen models
first in both slots in one config change, taking a saved key back if that
change is refused; `POST /api/users/{id}/password` (admin cookie) hands out a
one-time password. `POST /api/users` without a password returns one too. A
cookie session of an account that must change its password gets 403
`password_change_required` everywhere but `GET /api/auth/me`, `POST
/api/auth/logout` and `POST /api/auth/password` (which then needs no old
password). `GET /api/setup/state` says how Bothy was installed (`install`),
and a failed channel test answers 502 with a `core.FailureCode` as its code.
`POST /api/approvals/{id}` on a multi-approver approval records a vote and may
return it still pending; `GET /api/approvals/{id}/votes` gives the caller's
view of the votes (count, who may vote, `can_vote`/`can_withdraw`), and
`GET /api/releases` and `GET /api/releases/deploys` list the release
environments and deploy records (own deploys; everyone's for admins).
`GET /api/inbox` (`h_inbox.go`) lists what waits for a decision in plain
copy, the caller's items first: approvals and note suggestions, and for
admins MCP tools and skills to review and recent unknown senders; new group
chats go to their owner and admins. Items are decided through their own
endpoints.
`h_uistate.go` holds what the redesigned screens used to fall back on:
`GET`/`PATCH /api/me` (the caller's own name, time zone and where approval
requests reach them; the patch is cookie-only, and the owner's new zone
re-syncs their heartbeat), `POST /api/search/test` (admin),
`GET /api/skills/{name}/files/{path...}` (served as sandboxed `text/plain`,
404 for anything the caller cannot see), the notes history routes and
`GET /api/github/repos/{owner}/{repo}/workflows` (admin). It also fills the
provider credential `source`, a changed skill's file diff on its inbox item
and the knowledge `stale` flag (default branch heads cached per repository).
`h_signin.go` serves sign in with Google and GitHub (SPEC §15) through the
auth extras: `GET /api/auth/methods`, `POST /api/auth/oauth/{p}/start`
(20/min per `clientKey`, an IPv4 address or an IPv6 /64, in a bucket of its
own; 409 `wrong_address` when the request's host or scheme isn't
`public_url`'s), the callback (always a 303 to a fixed web-app path, never
echoing the code or state, clearing the `SameSite=Lax` sign-in cookie and
setting the usual Strict session cookie on success; a refused account goes in
the 60-second, page-readable refusal cookie, never the address),
`GET`/`POST`/`DELETE /api/me/sign-ins[/{p}]` (cookie only) and the admin
`GET`/`PATCH /api/sign-in[/{p}]` and `POST /api/sign-in/{p}/check`. A typed
client secret goes to the vault (`saveGlobalKey`, shared with
`POST /api/providers/connect`) and is taken back when the config change is
refused. Saving GitHub organizations pins them by id under their lower-cased
names (`auth.sign_in.github.org_ids`, written through `config.ServerWrite`;
`PATCH /api/config` and `DELETE /api/config` refuse it by any path).

For the v0.2 UI (docs/design/ui.md §17) responses carry names instead of
ids (`names.go`, cached per response and resolved through the caller's own
Viewer, so a chat's title or a schedule's name never reaches anyone else, not
even an admin reading everyone's usage or the audit log): runs list as
`dto.RunView` (`requested_by_name`, `chat_title`, `schedule_name`), usage rows
name people, schedules and chats, approvals carry `decided_by_name`, and audit
rows carry `actor_name` and a one-line `summary` (`audit_summary.go`, phrased
from the approval or tool call the row was about). Egress rows add one
`outcome` (`reached`, `allowed_unreachable`, `blocked`). Transcript pages set
`state` and `approval_id` on tool_call parts from the call record, its
approval and its result (`h_transcript.go`; never stored) and list the
approvals those calls asked for, decided ones included, with the ones the
egress proxy held their connections for (`<call>#net`). `POST /api/sessions`
may carry the first message (202 `SessionCreated`), uploads for such a chat
name `principal_id`, session lists hide sessions without messages, and an
untitled session is named after its first message in lists, details, runs
and search. `GET /api/search` returns chats, project-notes sections and
knowledge code hits as plain-text snippets with UTF-16 match ranges
(`snippet.go`); `GET /api/sessions/{id}/refine-estimate` multiplies the
chat's recent answer-step cost by the configured refine work, whose
multiplier `GET /api/refine` also gives for chats not stored yet.
