# internal/httpapi

`httpapi.Server` serves every route of SPEC §8.3 on the stdlib `ServeMux`.
The outer chain adds a request id, recovers panics, sets the security headers
(strict CSP, nosniff, no-referrer, COOP/CORP, `no-store` on `/api` and `/v1`,
HSTS under TLS; never any CORS header), enforces the `Host` allowlist (421),
applies the exposure gate on the peer address (loopback, the container's
default gateway and `local_cidrs` are local; RFC1918/ULA is LAN; plain HTTP
only for local peers, or LAN with `insecure_lan`; `X-Forwarded-*` honoured
only from `trusted_proxies`) and rejects credential-named query parameters.
Each route then caps its body, applies safe mode and the setup rules (local
peers only, 404 after completion), authenticates (`bothy_session` cookie on
plain HTTP, `__Host-bothy` under TLS, or a bearer PAT; `/v1` is bearer-only),
runs Go's `CrossOriginProtection` on every unsafe request plus the
`X-CSRF-Token` synchroniser check for cookie and setup sessions, authorizes by
role and token scope, and rate-limits tokens. Handlers always pass the actor's
`Viewer`; by-ID routes look rows up through the store before delegating, so
foreign ids are 404. `GET /api/events` streams the bus as SSE with a hello
frame, `Last-Event-ID` replay, `resync` across boots or on overflow, 15 s
pings and at most four streams per user. `/v1/chat/completions` maps OpenAI
requests onto runs (`bothy`, `bothy-fast`) or onto `LLM.Passthrough` for
`<provider>/<model>`, streaming `chat.completion.chunk` frames. Uploads and
workspace downloads go through `os.Root` under `<WorkDir>/ws/<id>`, the
persona file through `os.Root` under `<DataDir>`; `DELETE /api/users/{id}`
with `purge=true` wipes the user's boxes, vault scope, grants, schedules,
identities, memory files, workspaces and sessions and keeps audit rows.
