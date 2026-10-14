# internal/egress (M6)

Everything that leaves the gateway goes through this package (SPEC §2.8,
§5.8 "Egress holds", §10 #5). `egress.Service` implements `core.Egress`,
contributes the `web_fetch`, `web_search` and `net_allow` tools, and the
doctor checks `egress.wildcards`, `egress.shared_tenant_allowed`,
`egress.presets_enabled` and `mcp.http_plaintext`.

## Three dialers (and the credentials client)

| Dialer | Used by | May reach |
|---|---|---|
| box | tool boxes, through `/run/bothy/egress/<h>/proxy.sock` | `egress.allow`, enabled presets, the box's `Extra`, active domain grants, held approvals |
| public (`PublicClient`) | `web_fetch` | any public address; GET only, no cookies, no `Authorization`, redirects re-checked (≤ `web_fetch.max_redirects`) |
| system (`SystemClient`) | providers, channels, search APIs, HTTP MCP | exactly the destination's `host:port` list; private ranges only with `AllowPrivate` |

All three refuse hosts matching `egress.deny`, resolve once with the gateway
resolver (answers cached for `egress.dns.max_ttl`, at most 60 s), refuse the
destination if **any** answer is blocked or denylisted, dial only address
literals, and re-check the final socket address in `net.Dialer.Control`. The blocklist (`addr.go`) unmaps
IPv4-mapped addresses and re-checks the IPv4 embedded in NAT64, 6to4, Teredo
and IPv4-compatible forms. Cloud metadata addresses, and the ECS and EKS
container credential endpoints (`169.254.170.2`, `169.254.170.23`,
`fd00:ec2::23`), are refused even with `AllowPrivate`. Hosts are normalized
to lowercase punycode; numeric forms that inet_aton accepts (`2130706433`,
`0x7f000001`, `017700000001`, `127.1`) are treated as the IPv4 literals they
are.

## Credentials client

`CredentialsClient(relativeURI, fullURI)` is the one exception to the
metadata block, for the bedrock provider's AWS credential chain only
(`cmd/bothy` hands it to `provider.Deps.Credentials`, and the vault's aws-sm
references borrow that chain; boxes, tools, `web_fetch` and MCP never get
it). It dials fixed address literals with a
socket `Control` that accepts exactly the target: the instance metadata
service at `169.254.169.254:80` and `[fd00:ec2::254]:80`, where only the
IMDSv2 token `PUT /latest/api/token` (with its TTL header) and token-bearing
`GET`s of `/latest/meta-data/iam/security-credentials/[<role>]` and
`/latest/meta-data/placement/region` pass (IMDSv1, user data and the identity
document are refused); `GET` of exactly `AWS_CONTAINER_CREDENTIALS_RELATIVE_URI`
on `169.254.170.2:80`; and `GET` of exactly `AWS_CONTAINER_CREDENTIALS_FULL_URI`
(an ECS/EKS agent address or loopback is dialed directly, a host name through
the ordinary pinned dial). No proxy, no redirects, no keep-alive, response
bodies capped at 1 MiB, `egress.deny` honoured; every connection and refusal
is an `egress_log` row of the system dialer with destination
`credentials:aws`.

## Box proxy

`OpenBox` listens on `egress/hex(sha256(key))[:32]/proxy.sock` (0660, group
10001); the listener that accepted a connection identifies the box. Each
connection is one `CONNECT host:port` (TLS only: the ClientHello's SNI must
equal the CONNECT host, and is replayed upstream byte for byte) or, with
`egress.allow_plain_http`, one absolute-form HTTP request whose `Host` header
is forced to the target. The decision order is SPEC §2.8: port, denylist,
allowlist sources, trifecta (an Exposed session suspends grants), then
hold-for-approval or 403. Only an interactive binding is held, and only
while its exec runs (the runner calls `Unbind` when the exec returns) in a
run that is still running: background processes outlive both. A held CONNECT
asks `Policy.Decide` with the `net_allow` spec, an Action
`{kind: net, host, port}` and the run's provenance (so F6 applies to runs
started by untrusted messages), requests an approval for tool call
`<bound call>#net`, and waits up to `egress.hold_for_approval`;
concurrent holds for the same destination share one card. `allow_once` admits
the destination until the bound tool call's exec returns (an approval that
arrives later admits only the connection that waited for it), `allow_session`
and `allow_always` store the policy's narrow grant. A denied or expired hold is
remembered for the rest of the tool call (retries get an immediate 403, not a
new card), and one tool call can ask about at most 8 destinations. Byte caps
(`egress.caps`, 0 = no cap) count per session, survive the box being
destroyed and recreated, and close the connection when exceeded. Grants with
`max_uses` are charged one use per admitted connection. `TakeActivity`
reports whether the box moved bytes in either direction since the last call,
including on tunnels opened during an earlier call.

Every connection of every dialer becomes one `egress_log` row (written in
batches by `Run`, retried when the store fails; pooled system connections are
logged when they close). Box denials publish `egress.blocked` and audit
`egress.deny` once per tool call, destination and reason (once per 10 minutes
for boxes that are never bound), so a retry loop cannot flood the
append-only audit log; approved holds audit `egress.allow`.

## Tools

- `web_fetch` (read, Taint Untrusted): normalizes the URL (punycode host, no
  default port, no fragment, no credentials) into `Action.URL`, fetches it with
  the session's public client, renders HTML to text with `x/net/html`, and caps
  the body at `web_fetch.max_bytes`.
- `web_search` (read, Taint Untrusted): SearXNG JSON, Brave or Tavily through
  `SystemClient`; keys come from the vault (global scope). Result URLs are
  recorded in `session_urls` in the same normalized form `web_fetch` uses, so
  the F8 provenance check matches them. Hidden when `search.provider` is
  `none`. `TestSearch` runs one query with unsaved settings through the same
  request path (`POST /api/search/test`); failures carry a
  `core.FailureCode` (`key_rejected`, `rate_limited`, `provider_error` or a
  transport code).
- `net_allow` (external_send, lazy): plans `{kind: net, host, port}` for the
  F9 card; the runner stores the grant on approval and `Exec` reports whether
  one the proxy would honour is active (and whether the trifecta suspends
  it). Hidden without a sandbox.

## URL form for `session_urls` (F8)

`web_fetch` puts, and `web_search` records, URLs in one canonical form, so
`Sessions().HasURL(session, action.URL)` matches: lowercase scheme (`http` or
`https`), lowercase IDNA punycode host (IPv6 in brackets, IPv4 numeric forms
canonicalized), default port dropped, no userinfo, fragment dropped, empty
path as `/`, the path as `url.URL.EscapedPath`, the raw query kept verbatim.
URLs from user messages must be recorded in exactly this form.
