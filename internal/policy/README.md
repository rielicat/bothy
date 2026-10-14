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
`Decide`, since the policy has no sandbox dependency. A tool whose manifest
sets `AlwaysAsk` (v0.2: `repo_open_pr`) meets one more floor after F9,
`floor:always_ask`: every call asks with `allow_once`/`deny` whatever the
preset, a `deny` rule still denies, no grant answers it, and whichever
floor asks first (trifecta, untrusted origin or always-ask) carries the
first-contact warning onto its card.

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

**Releases and votes.** A deploy (`release_deploy`, or any `ActRelease`
action) has its own floor after the restricted-space check: denied outside
interactive runs and outside a user's own principal, denied when the payload
does not match its `releases.environments` entry (repository, workflow,
inputs, `ref_pattern`) or when fewer enabled users than `required` could
approve it besides the requester; otherwise an ask that no preset, default,
rule or grant relaxes (a deny rule still refuses it), offering only
`allow_once`/`deny`, with a multi-approver `ApproverReq` (`users`, `roles`,
`required`, `requester`). Such an approval resolves by votes
(`approval_votes`): the status check, the vote, the recount of still-eligible
allow votes and the decision share one store transaction, so concurrent votes
count once and resolve it once. The requester's allow is refused (they may
deny to withdraw), each user votes once, one deny denies, the `required`th
distinct allow allows; every vote is audited (`approval.vote`, refusals with
outcome `denied`), a vote that does not resolve is published as
`approval.voted`, and requested/voted/resolved events also go to each
eligible approver's own principal. The card leaves out the context line,
whose session title belongs to the requester's principal. Timeouts, parking
and the button MAC and recorded-card rules are unchanged. `Get`, `Pending`,
`Recent`, `Resolve` and the published events fill `Approval.Votes`,
`Approval.Approvals` (required, votes with names and comments, eligible
approvers, `any_admin`) and `Approval.Requester`; a decision's or vote's
reason (`Resolution.Comment`) is stored and sent with `approval.resolved`.
`Voters` lists who may vote.

**Plain copy** (`copy.go`, v0.2 S2). `renderCard` also writes the words every
surface shows, from the Action, the spec and the decision only: `question`,
`consequence`, `primary_label` (allow_once's verb, else "Allow for this
chat"), the `ing` phrase, `kind` (`action`, `pull_request`, `deploy`,
`review` for notes edits) and `tone` (`destructive` for irreversible and
financial actions and schedule deletion, never for deploys), plain field
labels per tool with MCP (schema-declared scalar arguments), admin and
unknown-tool fallbacks, and plain decision warnings. A field that shows the
exact action (a shell command, a message, a comment) replaces `Code`; a value
inside a sentence goes through `inline()`, so it can never break the line.

Checks: `policy.hands_off`, `policy.auto_external_rules`, `budgets.unlimited`.
