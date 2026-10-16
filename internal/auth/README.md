# internal/auth

`auth.Service` implements `core.Auth` (SPEC §4.12, §8.1): the one-time setup
token (sha256 in `meta`, file `<data>/setup-token` 0600, single claim, 24 h),
the 30-minute setup grant with its own CSRF token, owner creation, Argon2id
password hashing behind one global slot with a dummy derivation for unknown
usernames, exponential lockout per username (persisted in `users`) and per
client address (checked and counted inside the hashing slot, so concurrent
guesses cannot outrun it), session cookies (32 random bytes, stored as sha256, idle 7 d,
absolute 30 d, deleted on password, role or disabled changes), personal access
tokens `bothy_pat_<id>_<secret>_<crc>` parsed by fixed widths with a base62
CRC32, pairing codes (8 Crockford characters, link codes 15 min, approve-sender
codes 24 h, 5 redemptions per identity per hour), sender notices, guest spaces
and `ResolveIdentity`. Passwords are checked against
`auth.password_min_length` and the embedded `common_passwords.txt` (matched on
the password and on its de-leeted, digit-stripped core, plus keyboard and
repetition patterns). `SetupGrant`, `SetIdentityStatus` and `UnlinkIdentity`
are extras outside `core.Auth` that `httpapi` reaches by interface assertion,
as are the v0.2 UI ones: `CreateOwnerNamed` (the owner's display name),
`CreateUserOneTime` (a generated one-time password, four or more groups of
four Crockford characters, returned once) and `ResetUserPassword` (the owner
resets anyone else, admins only members; sessions end). Both mark the account
`must_change_password`: cookie actors carry the flag, and `SetPassword`
accepts an empty old password while it is set and clears it.
`UpdateProfile` lets anyone change their own display name, time zone (IANA
names only; new accounts start in the configured one), preferred chat app (one
they have linked) and whether approval requests stay in the web UI (audited
`user.profile`). Every sign-in, password or provider, records `last_login_at`.

Sign in with Google and GitHub (SPEC §15) is another set of extras:
`SetSignIn` (the vault and `egress.SystemClient`, set by `cmd/bothy`),
`SignInOptions`, `StartSignIn`, `StartLink`, `FinishSignIn`, `Unlink`,
`MySignIns`, `SignInStatus`, `CheckSignIn` and `PinGitHubOrgs`. `oauth/` is
the protocol half (PKCE S256, authorize addresses, the code exchange, the id
token claims, the sign-in list and GitHub's organization check; stdlib only,
no redirects followed, no token in any error) and `oauth/oauthtest` fakes both
providers for tests and local stubs. Flows live in memory under the sha256 of
their state (single use, 10 minutes, at most 8 per client, an IPv4 address or
an IPv6 /64, and 1024 in all); the state must equal the sign-in cookie, the
callback's provider must be the flow's, and a link is bound to the hash of
the session cookie that started it. The shared list `auth.sign_in.allow` is
checked at every sign-in and link, comparing ASCII only: Google needs a
verified email that is listed or a Workspace `hd` that is; GitHub needs a
verified address listed exactly (domains never match it) or an active
membership of a listed organization, whose id must equal the one pinned in
`auth.sign_in.github.org_ids` (`PinGitHubOrgs` looks them up when the
organizations are saved). Accounts are never matched by email: a
link is made by a signed-in person, or `auth.sign_in.allow_new` (team mode)
makes a member account without a password (`pw_hash` `"!"`, which password
sign-in treats like an unknown username) and a display name nobody else uses.
Tokens are never kept; GitHub's is revoked at once.
`Checks` contributes `server.exposure`, `server.insecure_lan`,
`tokens.admin_scope`, `tokens.expired_unrevoked` (auto-fix: revoke) and
`auth.sign_in` (a provider is on but its client secret is missing or saved
keys are locked, or GitHub organizations aren't pinned).
