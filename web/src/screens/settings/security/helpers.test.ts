import { describe, expect, it } from 'vitest';
import type { EgressRecord, Entry, Finding, Grant, Principal } from '../../../lib/api/types.gen';
import { describeFinding, needsAttention } from './findings';
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
    expect(describeGrant(g({ kind: 'tool', tool: 'mcp__linear__create_issue' })).what).toBe('Use create issue in linear');
    expect(describeGrant(g({ kind: 'tool', tool: 'schedule_create' })).what).toBe('Change schedules');
    expect(siteName('a.example:443')).toBe('a.example');
  });

  it('says who it is for and where it applies, by name', () => {
    const principals = [
      { id: 'usr_1', kind: 'user', display_name: 'Maya Chen' },
      { id: 'spc_1', kind: 'space', display_name: 'Ops team' },
    ] as Principal[];
    expect(grantMeta(g({}), { principals, titles: {}, when: '3 days ago' })).toEqual(['for Maya Chen', '3 days ago']);
    expect(grantMeta(g({ principal_id: 'spc_1', scope: 'session', scope_id: 'ses_9' }), { principals, titles: { ses_9: 'Weekly report' }, when: '' })).toEqual([
      'in the group chat “Ops team”',
      'only in “Weekly report”',
    ]);
    expect(grantMeta(g({ scope: 'schedule', scope_id: 'sch_1' }), { principals, titles: {}, when: '' })).toEqual(['for Maya Chen', 'only for one schedule']);
  });
});

describe('saved key names', () => {
  it('reads stored names as friendly ones', () => {
    expect(keyName('anthropic_api_key')).toBe('Anthropic API key');
    expect(keyName('telegram_bot_token')).toBe('Telegram bot token');
    expect(keyName('slack_app_token')).toBe('Slack app token');
    expect(keyName('brave_api_key')).toBe('Brave Search API key');
    expect(keyName('my_custom_thing')).toBe('My custom thing');
  });

  it('names aws-sm references by their secret, never the account', () => {
    expect(keyName('aws-sm:prod/openai')).toBe('prod/openai in AWS Secrets Manager');
    expect(keyName('aws-sm:prod/openai#api_key?region=eu-west-1')).toBe('prod/openai (api_key) in AWS Secrets Manager');
    expect(keyName('aws-sm:arn:aws:secretsmanager:us-west-2:123456789012:secret:bothy/slack-AbCdEf#bot')).toBe('bothy/slack-AbCdEf (bot) in AWS Secrets Manager');
    const e = (path: string, value: unknown) => ({ path, value }) as Entry;
    const entries = [e('providers.openai.api_key', 'aws-sm:prod/openai#api_key'), e('mcp.servers.docs.headers.Authorization', 'Bearer aws-sm:prod/openai#api_key'), e('providers.groq.api_key', 'aws-sm:prod/openai#api_key2')];
    expect(usedBy('aws-sm:prod/openai#api_key', entries)).toEqual(['OpenAI', 'docs']);
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
