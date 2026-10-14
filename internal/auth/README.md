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
`UpdateProfile` lets anyone change their own display name, preferred chat
app (one they have linked) and whether approval requests stay in the web UI
(audited `user.profile`). A password sign-in records `last_login_at`.
`Checks` contributes `server.exposure`, `server.insecure_lan`,
`tokens.admin_scope` and `tokens.expired_unrevoked` (auto-fix: revoke).
