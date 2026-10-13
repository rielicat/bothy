# Security policy

Bothy runs an AI agent with access to your credentials, chats and files, so
we treat security reports as the most important issues we get.

## Reporting a vulnerability

Report privately through GitHub: open
[a private security advisory](https://github.com/rielicat/bothy/security/advisories/new)
on this repository. Please do not open a public issue, pull request or
discussion for a suspected vulnerability.

A useful report says which version or commit you tested, how Bothy was
deployed (compose, single container, native binary), the steps to reproduce,
and what an attacker gains. A proof of concept helps but is not required.

## What we commit to

| Step | Target |
|---|---|
| Acknowledge your report | within 72 hours |
| Initial assessment and severity (CVSS v4) | within 7 days |
| Fix released for critical and high severity | within 14 days of confirmation |
| Fix released for medium and low severity | in the next scheduled release |
| Public advisory | when the fix ships, coordinated with you |

We keep you informed until the issue is closed, credit you in the advisory
unless you prefer otherwise, and request a CVE for confirmed issues. If a fix
needs longer than the targets above, we tell you why and agree on a date;
coordinated disclosure happens no later than 90 days after your report.

## Supported versions

Bothy is pre-1.0. Security fixes go into the latest minor release line only
(currently 0.1.x). Upgrade by installing the newest signed release; there is
no auto-update.

## Scope

In scope, for example:

- Sandbox escape, or tool execution outside a box or bwrap jail.
- Bypassing the egress proxy, the SSRF guard or the per-box allowlist.
- Reading secrets: vault values, API keys or the KEK reaching a model, a box,
  a log, an audit row, a channel message or an SSE stream.
- Authentication or authorization bypass, CSRF, cross-principal data access
  (another user's or space's sessions, memory, approvals or workspace).
- Forging, replaying or truncation-tricking an approval card or button.
- Prompt injection that defeats a deterministic control (policy floors, the
  trifecta guard, first-contact approval), as opposed to the model merely
  saying something wrong.
- Supply-chain issues in our release artifacts, images or install script.

Out of scope: findings that require an already-compromised host or Docker
daemon; deployments where the operator turned a documented safety control off
(for example `insecure_lan`, the hands-off preset or `BOTHY_KEK` in the
environment), unless the documentation misdescribes the risk; denial of
service through the operator's own budget settings; and model output quality.

## Safe harbor

We will not pursue or support legal action against good-faith research that
follows this policy, avoids privacy violations and data destruction, and only
tests systems you own or are authorized to test.

## Verifying releases

Every release ships `checksums.txt` signed with Sigstore by this repository's
release workflow, plus SBOMs and SLSA build provenance. `deploy/install.sh`
checks both; by hand:

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/rielicat/bothy/.github/workflows/release.yml@refs/tags/v0.1.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
cosign verify ghcr.io/rielicat/bothy:0.1.0 \
  --certificate-identity "https://github.com/rielicat/bothy/.github/workflows/release.yml@refs/tags/v0.1.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
