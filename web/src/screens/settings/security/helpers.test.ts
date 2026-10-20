import { describe, expect, it } from 'vitest';
import type { EgressRecord, Entry, Finding, Grant, GrantView, SignInAdmin, SignInProviderStatus } from '../../../lib/api/types.gen';
import { describeFinding, needsAttention } from './findings';
import { addressSummary, canEnable, checkLine, entryProblem, listProblem, listSummary, listText, orgsProblem, parseList, parseOrgs, rowValue } from './signin';
import { describeGrant, grantMeta, siteName } from './grantText';
import { joinNames, keyName, storedName, usedBy } from './keyNames';
import { covers, recentBlocks, sharedSuffix, siteEntry, siteOf } from './sites';
import { chatChanges, chatChoice, longDuration, scheduledChanges, scheduledChoice } from './timeouts';

const finding = (f: Partial<Finding>): Finding => ({ id: 'x', area: 'x', title: 'Server title', detail: '', severity: 'medium', security: true, auto_fix: false, ...f });

describe('security findings in plain words', () => {
  it('says known findings in company voice and picks their action', () => {
    expect(describeFinding(finding({ id: 'vault.kek_colocated' }))).toMatchObject({ text: 'The key that protects saved keys is stored next to the database.', action: 'how' });
    expect(describeFinding(finding({ id: 'sandbox.unavailable' }))).toMatchObject({ text: 'Running code is off on this server.', action: 'running-code' });
    expect(describeFinding(finding({ id: 'data.permissions', auto_fix: true }))).toMatchObject({ text: 'Data files can be read by other users on this server.', action: 'fix' });
    expect(describeFinding(finding({ id: 'vault.sealed' })).action).toBe('unlock');
    // A key file locks them: no passphrase can, so there is no Unlock.
    expect(describeFinding(finding({ id: 'vault.sealed' }), true)).toMatchObject({ action: 'how', steps: 'Put the key file Bothy was set up with back on the server, then restart Bothy.' });
  });

  it('falls back to the server title for a finding it does not know', () => {
    expect(describeFinding(finding({ id: 'new.check', title: 'Something new' }))).toEqual({ text: 'Something new.', steps: undefined, action: 'how' });
    expect(describeFinding(finding({ id: 'new.check', title: 'Is this new?' })).text).toBe('Is this new?');
  });

  it('keeps notes out of Needs attention and puts the worst first', () => {
    const list = needsAttention([finding({ id: 'a', severity: 'low' }), finding({ id: 'b', severity: 'info' }), finding({ id: 'c', severity: 'critical' })]);
    expect(list.map((f) => f.id)).toEqual(['c', 'a']);
    expect(needsAttention(null)).toEqual([]);
  });
});

describe('grants in plain words', () => {
  const g = (x: Partial<Grant>): Grant => ({
    id: 'grt_1',
    principal_id: 'usr_1',
    scope: 'principal',
    scope_id: '',
    kind: 'domain',
    max_se: 'read',
    allow_when_tainted: false,
    uses: 0,
    max_uses: 0,
    created_by: 'usr_1',
    created_at: '2026-10-10T10:00:00Z',
    expires_at: '0001-01-01T00:00:00Z',
    ...x,
  });

  it('names the site, the chat app or the action, never the tool id', () => {
    expect(describeGrant(g({ value: 'docs.python.org:443' }))).toEqual({ what: 'Reach', exact: 'docs.python.org' });
    expect(describeGrant(g({ value: 'registry.acme.internal:8443' })).exact).toBe('registry.acme.internal:8443');
    expect(describeGrant(g({ kind: 'recipient', value: 'slack:C04ENG:' }))).toEqual({ what: 'Send messages to a chat in Slack', exact: '' });
    expect(describeGrant(g({ kind: 'recipient', value: 'ops@acme.com' }))).toEqual({ what: 'Send messages to', exact: 'ops@acme.com' });
    expect(describeGrant(g({ kind: 'tool', tool: 'shell', pattern: { '/argv/0': 'pytest*' } }))).toEqual({ what: 'Run commands matching', exact: 'pytest*' });
    expect(describeGrant(g({ kind: 'tool', tool: 'mcp__linear__create_issue' })).what).toBe('Use create issue in Linear');
    expect(describeGrant(g({ kind: 'tool', tool: 'schedule_create' })).what).toBe('Change schedules');
    expect(describeGrant(g({ kind: 'recipient', value: 'github:acme/web' }))).toEqual({ what: 'Open pull requests or comment in', exact: 'acme/web' });
    expect(siteName('a.example:443')).toBe('a.example');
  });

  it('puts a pinned request in words and keeps its pattern for Details', () => {
    const whole = describeGrant(g({ kind: 'tool', tool: 'mcp__linear__create_issue', pattern: { '': '{"title":"Fix login\\*"}' } }));
    expect(whole).toMatchObject({ what: 'Use create issue in Linear with these exact details', exact: '' });
    expect(whole.raw).toContain('mcp__linear__create_issue');

    const bash = describeGrant(g({ kind: 'tool', tool: 'shell', pattern: { '/argv/0': 'bash', '/argv': '\\["bash","-lc","ls -la \\*.txt"]' } }));
    expect(bash).toMatchObject({ what: 'Run', exact: 'ls -la *.txt' });
    expect(bash.raw).toContain('/argv/0');

    const env = describeGrant(g({ kind: 'tool', tool: 'shell', pattern: { '/argv/0': '/usr/bin/env', '/argv': '\\["/usr/bin/env","python3","-c","1"]' } }));
    expect(env.exact).toBe('/usr/bin/env python3 -c 1');

    const py = describeGrant(g({ kind: 'tool', tool: 'python', pattern: { '/argv/0': 'python3', '/argv': '\\["python3",".bothy/tmp/run.py"]', '/code': 'print(1)' } }));
    expect(py).toMatchObject({ what: 'Run this exact script', exact: '' });
    expect(py.raw).toContain('print(1)');

    // A live wildcard is a real pattern, so it stays verbatim on the line.
    expect(describeGrant(g({ kind: 'tool', tool: 'shell', pattern: { '/argv': '\\["bash","-lc","make *"]' } }))).toEqual({
      what: 'Run commands matching',
      exact: '\\["bash","-lc","make *"]',
    });
    expect(describeGrant(g({ kind: 'tool', tool: 'shell', pattern: { '/argv/0': 'pytest*' } })).raw).toBeUndefined();
  });

  it('says who it is for, where it applies, and which chat approver allowed it, by name', () => {
    const v = (x: Partial<GrantView>): GrantView => ({ ...g({}), principal_name: 'Maya Chen', created_by_name: 'Maya Chen', ...x });
    expect(grantMeta(v({}), { titles: {}, when: '3 days ago' })).toEqual(['for Maya Chen', '3 days ago']);
    expect(grantMeta(v({ principal_name: 'Alex · Slack', scope: 'session', scope_id: 'ses_9' }), { titles: { ses_9: 'Weekly report' }, when: '' })).toEqual([
      'for Alex · Slack',
      'only in “Weekly report”',
    ]);
    expect(grantMeta(v({ scope: 'schedule', scope_id: 'sch_1' }), { titles: {}, when: '' })).toEqual(['for Maya Chen', 'only for one schedule']);
    expect(grantMeta(v({ principal_name: '#eng · Slack', created_by: '', created_by_identity: 'slack:U01SAM', created_by_name: 'Sam · Slack' }), { titles: {}, when: '' })).toEqual([
      'for #eng · Slack',
      'added by Sam · Slack',
    ]);
  });
});

describe('saved key names', () => {
  it('reads stored names as friendly ones', () => {
    expect(keyName('anthropic_api_key')).toBe('Anthropic API key');
    expect(keyName('telegram_bot_token')).toBe('Telegram bot token');
    expect(keyName('slack_app_token')).toBe('Slack app token');
    expect(keyName('brave_api_key')).toBe('Brave Search API key');
    expect(keyName('my_custom_thing')).toBe('My custom thing');
    expect(keyName('github_webhook_secret')).toBe('GitHub webhook secret');
    // Keys Bothy keeps for itself never show their ids.
    expect(keyName('github_ghc_01m53wrdcr979y0ba9ek63p0vs')).toBe('GitHub connection key');
    expect(keyName('github_gha_01m53wrdcr979y0ba9ek63p0vs_key')).toBe("GitHub app's private key");
    expect(keyName('github_gha_01m53wrdcr979y0ba9ek63p0vs_client_secret')).toBe("GitHub app's client secret");
    expect(keyName('google_sign_in_client_secret')).toBe('Google sign-in client secret');
    expect(usedBy('github_ghc_01m53wrdcr979y0ba9ek63p0vs', [])).toEqual(['GitHub']);
    expect(usedBy('google_sign_in_client_secret', [{ path: 'auth.sign_in.google.client_secret', value: 'secret:google_sign_in_client_secret' } as never])).toEqual(['Sign-in']);
    // A connected tool reads as Connections names it: "Acme helpdesk", not "Acme-helpdesk".
    expect(usedBy('acme_key', [{ path: 'mcp.servers.acme-helpdesk.headers.X-Api-Key', value: 'secret:acme_key' } as never])).toEqual(['Acme helpdesk']);
    expect(keyName('mcp_acme-helpdesk_header')).toBe('Key for Acme helpdesk');
    expect(usedBy('wiki_key', [{ path: 'mcp.servers.deepwiki.headers.Authorization', value: 'secret:wiki_key' } as never])).toEqual(['DeepWiki']);
  });

  it('names aws-sm references by their secret, never the account', () => {
    expect(keyName('aws-sm:prod/openai')).toBe('prod/openai in AWS Secrets Manager');
    expect(keyName('aws-sm:prod/openai#api_key?region=eu-west-1')).toBe('prod/openai (api_key) in AWS Secrets Manager');
    expect(keyName('aws-sm:arn:aws:secretsmanager:us-west-2:123456789012:secret:bothy/slack-AbCdEf#bot')).toBe('bothy/slack-AbCdEf (bot) in AWS Secrets Manager');
    const e = (path: string, value: unknown) => ({ path, value }) as Entry;
    const entries = [e('providers.openai.api_key', 'aws-sm:prod/openai#api_key'), e('mcp.servers.docs.headers.Authorization', 'Bearer aws-sm:prod/openai#api_key'), e('providers.groq.api_key', 'aws-sm:prod/openai#api_key2')];
    expect(usedBy('aws-sm:prod/openai#api_key', entries)).toEqual(['OpenAI', 'Docs']);
    expect(usedBy('aws-sm:prod/openai', entries)).toEqual([]);
  });

  it('finds what uses a key from the settings that point at it', () => {
    const e = (path: string, value: unknown) => ({ path, value }) as Entry;
    const entries = [e('providers.anthropic.api_key', 'secret:anthropic_api_key'), e('search.brave_api_key', 'secret:brave_api_key'), e('channels.telegram.token', 'secret:tg'), e('channels.slack.bot_token', 'secret:tg')];
    expect(usedBy('anthropic_api_key', entries)).toEqual(['Anthropic']);
    expect(usedBy('brave_api_key', entries)).toEqual(['Brave Search']);
    expect(joinNames(usedBy('tg', entries))).toBe('Telegram and Slack');
    expect(joinNames(['A', 'B', 'C'])).toBe('A, B and C');
    expect(usedBy('nothing', entries)).toEqual([]);
  });

  it("names an MCP server's saved header by its server, and counts every form that points at it", () => {
    expect(keyName('mcp_qaecho_header')).toBe('Key for Qaecho');
    const e = (path: string, value: unknown) => ({ path, value }) as Entry;
    const entries = [
      e('mcp.servers.qaecho.headers.Authorization', 'secret:mcp_qaecho_header'),
      e('mcp.servers.qatok.headers.Authorization', 'Bearer secret:qa_token'),
      e('mcp.servers.qaurl.url', 'https://mcp.example.com/mcp?api_key=secret:qa_url_key&x=1'),
      e('mcp.servers.other.headers.Authorization', 'Bearer secret:qa_token_two'),
    ];
    expect(usedBy('mcp_qaecho_header', entries)).toEqual(['Qaecho']);
    expect(usedBy('qa_token', entries)).toEqual(['Qatok']);
    expect(usedBy('qa_url_key', entries)).toEqual(['Qaurl']);
  });

  it('turns a typed name into a stored one', () => {
    expect(storedName('  Brave Search key ')).toBe('brave_search_key');
    expect(storedName('***')).toBe('');
  });
});

describe('websites running code can reach', () => {
  it('normalises what people type into an allowlist entry', () => {
    expect(siteEntry('https://Docs.Example.com/guide?x=1')).toBe('docs.example.com');
    expect(siteEntry('*.example.com')).toBe('*.example.com');
    expect(siteEntry('registry.acme.internal:8443')).toBe('registry.acme.internal:8443');
    expect(siteEntry('api.example.com:443')).toBe('api.example.com');
    expect(siteEntry('10.0.0.5')).toBe('10.0.0.5');
    expect(siteEntry('not a site')).toBeNull();
    expect(siteEntry('localhost')).toBeNull();
  });

  it('matches hosts the way the proxy does', () => {
    expect(covers('*.wikipedia.org', 'en.wikipedia.org', 443)).toBe(true);
    expect(covers('*.wikipedia.org', 'wikipedia.org', 443)).toBe(false);
    expect(covers('pypi.org', 'pypi.org', 8443)).toBe(false);
    expect(covers('registry.acme.internal:8443', 'registry.acme.internal', 8443)).toBe(true);
    expect(siteOf('pypi.org', 443)).toBe('pypi.org');
    expect(siteOf('pypi.org', 80)).toBe('pypi.org:80');
  });

  it('lists each blocked site once, newest first, skipping allowed ones', () => {
    const r = (id: number, host: string, at: string, x: Partial<EgressRecord> = {}): EgressRecord =>
      ({ id, host, port: 443, at, dialer: 'box', decision: 'deny', dest: '', bytes_up: 0, bytes_down: 0, duration: 0, ...x }) as EgressRecord;
    const blocks = recentBlocks(
      [
        r(1, 'pypi.org', '2026-10-10T10:00:00Z'),
        r(2, 'pypi.org', '2026-10-10T11:00:00Z'),
        r(3, 'evil.example', '2026-10-10T09:00:00Z', { decision: 'held_deny' }),
        r(4, 'ok.example', '2026-10-10T12:00:00Z', { decision: 'allow' }),
        r(5, 'api.github.com', '2026-10-10T12:00:00Z'),
        r(6, 'web.example', '2026-10-10T12:00:00Z', { dialer: 'public' }),
      ],
      ['api.github.com'],
    );
    expect(blocks.map((b) => b.id)).toEqual([2, 3]);
  });

  it('spots shared hosting', () => {
    expect(sharedSuffix('acme.github.io', ['.github.io'])).toBe('.github.io');
    expect(sharedSuffix('docs.acme.com', ['.github.io'])).toBeUndefined();
  });
});

describe('if nobody answers', () => {
  const base = { interactiveTimeout: '15m0s', interactiveOnTimeout: 'deny', scheduledTimeout: '2m0s', scheduledOnTimeout: 'park', parkTtl: '24h0m0s' };

  it('says durations in words', () => {
    expect(longDuration(90e9)).toBe('90 seconds');
    expect(longDuration(15 * 60e9)).toBe('15 minutes');
    expect(longDuration(3600e9)).toBe('1 hour');
    expect(longDuration(24 * 3600e9)).toBe('24 hours');
    expect(longDuration(72 * 3600e9)).toBe('3 days');
  });

  it('maps the settings to the plan’s choices', () => {
    expect(chatChoice(base).value).toBe('deny-15m');
    expect(chatChoice(base).options.map((o) => o.label)).toEqual(['Deny after 15 minutes', 'Deny after 1 hour', 'Keep waiting (up to 24 hours)']);
    expect(chatChoice({ ...base, interactiveTimeout: '1h0m0s' }).value).toBe('deny-1h');
    expect(chatChoice({ ...base, interactiveOnTimeout: 'park' }).value).toBe('park');
    expect(scheduledChoice(base).options.map((o) => o.label)).toEqual(['Wait up to 24 hours', 'Deny after 1 hour']);
    expect(scheduledChoice({ ...base, scheduledOnTimeout: 'deny', scheduledTimeout: '1h' }).value).toBe('deny-1h');
  });

  it('shows a value set elsewhere as its own option instead of changing it', () => {
    const c = chatChoice({ ...base, interactiveTimeout: '30m' });
    expect(c.value).toBe('current');
    expect(c.options[0].label).toBe('Deny after 30 minutes');
    expect(chatChanges('current')).toBeNull();
  });

  it('writes the timeout and what happens after it', () => {
    expect(chatChanges('deny-1h')).toEqual({ 'policy.approval.interactive_timeout': '1h', 'policy.approval.interactive_on_timeout': 'deny' });
    expect(chatChanges('park')).toEqual({ 'policy.approval.interactive_on_timeout': 'park' });
    expect(scheduledChanges('deny-1h')).toEqual({ 'policy.approval.scheduled_timeout': '1h', 'policy.approval.scheduled_on_timeout': 'deny' });
  });
});

describe('sign-in list and provider rows', () => {
  const status = (s: Partial<SignInProviderStatus>): SignInProviderStatus => ({
    provider: 'google',
    name: 'Google',
    enabled: false,
    ready: false,
    redirect_uri: '',
    client_id: '',
    secret_saved: false,
    orgs: [],
    ...s,
  });
  const admin = (a: Partial<SignInAdmin>): SignInAdmin => ({ public_url: 'https://bothy.acme.com', address: 'ok', team_mode: true, allow: [], allow_new: false, providers: [], ...a });

  it('reads one entry per line or comma, lower case, without repeats', () => {
    expect(parseList(' Acme.com,pat@Gmail.com\n\n@eng.acme.io\nacme.com ')).toEqual(['acme.com', 'pat@gmail.com', 'eng.acme.io']);
    expect(parseList('')).toEqual([]);
    expect(listText(['a.com', 'b@c.com'])).toBe('a.com\nb@c.com');
    expect(parseOrgs('acme-inc, Acme-Inc,\nlabs')).toEqual(['acme-inc', 'labs']);
  });

  it('refuses what would let strangers in, and what is not an address or a domain', () => {
    expect(entryProblem('acme.com')).toBe('');
    expect(entryProblem('pat@gmail.com')).toBe('');
    expect(entryProblem("o'brien+bothy@acme.co.uk")).toBe('');
    for (const d of ['gmail.com', 'outlook.com', 'proton.me', 'qq.com', 'users.noreply.github.com', 'duck.com', 'yahoo.co.jp', 'att.net'])
      expect(entryProblem(d)).toBe(`Anyone can get an address at ${d}. List each person's address instead.`);
    expect(entryProblem('kim@duck.com')).toBe('');
    expect(entryProblem('583231+octo@users.noreply.github.com')).toBe('');
    expect(entryProblem('acme')).toMatch(/isn't an email address or a domain/);
    expect(entryProblem('pat@acme')).toMatch(/isn't an email address\./);
    expect(entryProblem('a b@acme.com')).toMatch(/isn't an email address\./);
    expect(listProblem(Array.from({ length: 201 }, (_, i) => `p${i}@acme.com`))).toBe('The list can hold up to 200 addresses and domains.');
    expect(orgsProblem(['acme-inc'])).toBe('');
    expect(orgsProblem(['-acme'])).toBe('Enter organization names like acme-inc, separated by commas.');
  });

  it('turns a provider on only when everything is in place', () => {
    const ready = status({ client_id: 'x', secret_saved: true });
    expect(canEnable(admin({ allow: ['acme.com'] }), ready)).toBe(true);
    expect(canEnable(admin({}), ready)).toBe(false);
    expect(canEnable(admin({}), { ...ready, provider: 'github', orgs: ['acme-inc'] })).toBe(true);
    // GitHub ignores domains: it needs an exact address or an organization.
    expect(canEnable(admin({ allow: ['acme.com'] }), { ...ready, provider: 'github' })).toBe(false);
    expect(canEnable(admin({ allow: ['acme.com', 'pat@gmail.com'] }), { ...ready, provider: 'github' })).toBe(true);
    expect(canEnable(admin({ allow: ['acme.com'], address: 'not_https' }), ready)).toBe(false);
    expect(canEnable(admin({ allow: ['acme.com'] }), { ...ready, secret_saved: false })).toBe(false);
  });

  it('says Off, On or Not working', () => {
    expect(rowValue(status({}))).toEqual({ text: 'Off', tone: 'muted' });
    expect(rowValue(status({ enabled: true, ready: true }))).toEqual({ text: 'On', tone: 'muted' });
    expect(rowValue(status({ enabled: true, problem: 'no_secret' }))).toEqual({ text: 'Not working', tone: 'danger' });
    expect(listSummary([])).toBe('Nobody yet');
    expect(listSummary(['a.com'])).toBe('1 address or domain');
    expect(listSummary(['a.com', 'b.com'])).toBe('2 addresses and domains');
    // Domains don't let GitHub accounts in, so the GitHub page counts addresses only.
    expect(addressSummary(['a.com'])).toBe('No email addresses');
    expect(addressSummary(['a.com', 'pat@gmail.com'])).toBe('1 email address');
    expect(addressSummary(['pat@gmail.com', 'kim@acme.com', 'a.com'])).toBe('2 email addresses');
  });

  it('turns the check result into a line', () => {
    expect(checkLine('google', { ok: true })).toEqual({ tone: 'success', text: 'Saved. Google accepted the client ID and secret.' });
    expect(checkLine('github', { ok: false, code: 'key_rejected' }).text).toBe("Saved, but GitHub didn't accept the client ID or secret. Copy them again from GitHub.");
    expect(checkLine('github', { ok: false, code: 'provider_error', error: 'oauth: github token: redirect_uri_mismatch' }).text).toBe(
      "Saved, but GitHub doesn't accept this callback URL. Paste it into the OAuth app's settings.",
    );
    expect(checkLine('google', { ok: false, code: 'provider_error', error: 'oauth: google token: status 400: redirect_uri_mismatch' }).text).toBe(
      "Saved, but Google doesn't accept this redirect URI. Add it to the OAuth client's authorized redirect URIs.",
    );
    expect(checkLine('google', { ok: false, code: 'timeout' })).toEqual({ tone: 'warning', text: "Saved, but Bothy couldn't reach Google to check the details." });
  });

  it('names the provider and the problem of a sign-in finding', () => {
    const fix = '/settings/security/github-sign-in';
    expect(describeFinding(finding({ id: 'auth.sign_in', title: 'GitHub sign-in is on, but its client secret is missing', fix }))).toEqual({
      text: 'GitHub sign-in is on, but its client secret is missing.',
      steps: 'Open Security → GitHub sign-in and save the client secret again.',
      action: 'how',
    });
    // Locked saved keys are unlocked, like the other sealed findings; nothing needs saving again.
    expect(describeFinding(finding({ id: 'auth.sign_in', title: 'Google sign-in is on, but saved keys are locked', fix: '/settings/security/google-sign-in' }))).toEqual({
      text: "Saved keys are locked, so Google sign-in can't work.",
      action: 'unlock',
    });
    expect(describeFinding(finding({ id: 'auth.sign_in', title: "GitHub organizations aren't confirmed", severity: 'low', fix }))).toEqual({
      text: 'GitHub organizations are checked by name only.',
      steps: 'Open Security → GitHub sign-in and press Save so Bothy can confirm the organizations.',
      action: 'how',
    });
  });
});
