# Sign in with Google and GitHub

People can sign in with a Google or GitHub account next to their username and
password. Passwords stay: the owner always keeps one, and an admin can give
anyone a password from People. The design is in [SPEC §15](../SPEC.md#15-sign-in-with-google-and-github).

## Before you start

- Bothy needs a web address that starts with `https://`: set it in
  **Settings → General → Web address** (`server.public_url`). Google and
  GitHub send people back to `<web address>/api/auth/oauth/<provider>/callback`,
  and each provider page in Bothy shows the exact address to paste.
- Decide who may sign in. **Settings → Security → Sign-in → Who can sign in**
  holds one list for both providers, one entry per line:
  - a domain, like `acme.com`, lets in Google Workspace accounts of that
    domain, and only those;
  - an address, like `pat@gmail.com`, lets in that one person with Google
    (a personal Gmail account, or someone whose Google account isn't managed
    by your Workspace) or with a GitHub account that has that address
    verified.

  GitHub accounts need their exact address on the list, or membership of an
  organization listed on the GitHub page. A domain doesn't let GitHub accounts
  in: GitHub confirms an address once and never checks it again, so people who
  left keep a confirmed company address, and anyone who can read a shared
  inbox, help desk or mailing list at the domain can confirm one there.

  Public mail domains and relays such as `gmail.com`, `outlook.com`,
  `duck.com` or `users.noreply.github.com` can't be listed as domains, since
  anyone can get an address there; list each person's address instead.
  Addresses are compared exactly, ignoring upper and lower case of plain
  letters only.
- The list is checked at every sign-in, so removing someone stops their next
  sign-in. It does not sign them out: to sign someone out now, turn their
  account off in **Settings → People**.
- **Create an account the first time someone on the list signs in** (team mode
  only) gives newcomers a member account without a password; members of the
  GitHub organizations you list get one too. Leave it off to
  let only people who already connected Google or GitHub in **Account → Sign-in
  methods** use it; others are asked to talk to an admin. People who already
  have an account should connect the provider before signing in with it, or
  they get a second account.

## Google

1. In Google Cloud, open **APIs & Services → OAuth consent screen** and set it
   up. Choose **Internal** when only your Workspace organization signs in;
   Google then enforces that too.
2. Open **Credentials → Create credentials → OAuth client ID**, type **Web
   application**.
3. Under **Authorized redirect URIs**, add the redirect URI shown in
   **Settings → Security → Google sign-in**.
4. Copy the client ID and client secret into that page and press **Save**.
   Bothy checks them with Google straight away.
5. Turn on **Sign in with Google**.

Bothy asks for the `openid`, `email` and `profile` scopes and reads who signed
in from the ID token Google returns to the server. Only verified addresses
count.

## GitHub

1. Create an OAuth App under your organization (**Settings → Developer
   settings → OAuth Apps → New OAuth App**).
2. Set **Authorization callback URL** to the callback URL shown in **Settings →
   Security → GitHub sign-in**.
3. Generate a client secret, copy the client ID and secret into that page and
   press **Save**.
4. Optionally list organizations: their active members may sign in even when
   their address isn't on the list. Bothy looks each one up on GitHub as you
   save and remembers its numeric id, so if an organization is renamed and
   someone else takes the old name, their members don't get in. A name GitHub
   doesn't know is refused ("GitHub has no organization called …"). GitHub
   answers 60 of these lookups an hour for one server; when it is limiting
   Bothy, the page says so, and saving again an hour later works.
5. Turn on **Sign in with GitHub**.

Bothy asks for `read:org` and `user:email`, checks the person's verified
addresses and memberships with their own token, then revokes the token right
away. Organizations written in `bothy.yaml` by hand, or renamed there, are
checked by name only until Bothy confirms them: open **Settings → Security →
GitHub sign-in** and press **Save**. The security check reminds you. If an
organization restricts OAuth apps, an owner approves Bothy the first time a
member signs in; with SAML single sign-on, members authorize the app for the
organization. Someone whose invitation is still pending can't sign in until
they accept it.

## In bothy.yaml

The same settings, for a file or the environment (`BOTHY_AUTH__SIGN_IN__…`):

```yaml
server:
  public_url: https://bothy.acme.com
auth:
  sign_in:
    allow: [acme.com, pat@gmail.com]
    allow_new: false
    google:
      enabled: true
      client_id: 1234-abc.apps.googleusercontent.com
      client_secret: secret:google_sign_in_client_secret   # or BOTHY_SECRET_GOOGLE_SIGN_IN_CLIENT_SECRET, or aws-sm:…
    github:
      enabled: true
      client_id: Ov23liExample
      client_secret: secret:github_sign_in_client_secret
      orgs: [acme-inc]          # org_ids (each name's GitHub id) is written by Bothy when you press Save in Settings
```

Egress for sign-in goes to `oauth2.googleapis.com`, `github.com` and
`api.github.com` on port 443 only. Bothy never stores a Google or GitHub token;
it keeps which account is connected to whom, with the address or login, and
records every sign-in in the audit log.
