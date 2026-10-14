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
environment variable), writes `providers.<id>` and puts the chosen models
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
