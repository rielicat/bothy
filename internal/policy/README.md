# internal/policy (M4)

`policy.Engine` implements `core.Policy` and `core.CheckProvider`;
`policy.Approvals` implements `core.Approvals`. Both are built by `New` over
`core.Store` (grants, approvals, sessions, users, meta), `config.View`,
`core.Bus` and `core.Auditor`.

**Decide** (SPEC §5.8) evaluates floors F1–F9, then the first matching
`policy.rules` entry, active grants, first contact and the preset/class
defaults, and stops at the first verdict. Floors are never relaxed by a rule
or grant (the only exception is an `AllowWhenTainted` grant for exactly the
action's recipient, which lifts the trifecta floor and nothing else; the
action is then allowed as that grant, so every lift counts against its
`max_uses`, and a grant that can no longer be used puts the floor back); a
`deny` rule may still tighten an ask floor. A restricted space never gets
the admin-approved asks of F2/F3: F4 denies first. F2 turns into an
admin ask only through a matching `ask` rule with `side_effect: financial`
or, when no such rule matches, `policy.defaults.financial: ask`; a broad
`ask` rule never loosens it. A rule `ask` and a class-default
or first-contact ask are answered by a matching grant, which is what makes
"allow for session" and "allow always" meaningful; floor asks offer only
`allow_once`/`deny` (except `net_allow`, which offers grants only).
`allow_always` is offered exactly where `allow_session` is, minus
irreversible actions, scheduled/heartbeat/API triggers and shared-tenant
hosts; Exposed and admin-class decisions offer neither. F1 checks trigger,
role, principal kind and the kill switch (`meta.paused`); sandbox
availability is enforced by the registry (`ToolRegistry.Available`) before
`Decide`, since the policy has no sandbox dependency.

**Rules** match tool globs, `argv0` (the first word of a `bash -lc` command
line, never when it contains a shell metacharacter, and never a relative or
workspace path such as `./ls`), domains (`host[:port]`, `*.suffix`), paths
(`*` within a segment, `**` across), JSON-pointer `match` globs over the
payload (`*` crosses `/`), side effect and triggers. An `auto` rule must
cover every domain/path the action touches; `ask`/`deny` rules fire on any.

**NarrowGrant** never builds a whole-tool grant: exec pins `argv[0]` (plus
the whole argv for interpreters, and the code for python), fs writes/deletes pin the parent directory
prefix (exact path at the workspace root), HTTP/net pin `host:port`, sends
pin the recipient, MCP pins the recipient field or the exact payload.
**Grant** validates (no whole-tool or match-everything patterns, no
admin/financial ceilings, wildcard and persistent shared-tenant domains only
from admins) and audits `grant.create` with `via: card` for grants built by
`NarrowGrant` in this process, `via: api` otherwise.

**Approvals** persist the request with a card rendered from the Action only
(`renderCard`: bidi/zero-width characters shown as `⟨U+XXXX⟩`, hosts in
punycode, diffs capped at 64 KB) and only offerable choices (whatever the
caller passed: no grants for `AdminOnly`, `allow_always` only beside
`allow_session`, never for irreversible actions or schedule/heartbeat/API
runs), publish `approval.requested` /
`approval.resolved` (plus an admin-broadcast copy for `AdminOnly`), and
resolve by CAS. `Resolve` takes the approver's role from the store, requires
chat presses to come from a recorded card message, allows only `deny` from a
truncated card and only choices the decision offered. `Await` applies the
timeout itself (deny → `ErrApprovalExpired`, park → `ErrParked`); `Run`
sweeps due approvals every 30 s, including expired parks. `OnResolved`
callbacks run synchronously after allow, deny and expiry, not after
`CancelRun`. Button data is `a:<id>:<o|s|a|d>:<mac8>` with an HMAC key kept
in `meta.approval_mac_key` (created on first boot; a corrupt key fails `New`).

Checks: `policy.hands_off`, `policy.auto_external_rules`, `budgets.unlimited`.
