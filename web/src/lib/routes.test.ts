import { describe, expect, it } from 'vitest';
import { legacyPatterns, match, patterns, redirect, resolve, settingsHome } from './routes';

describe('routes', () => {
  it('matches static and parameterised patterns', () => {
    expect(resolve('/chat')).toEqual({ pattern: '/chat', params: {} });
    expect(resolve('/chat/ses_01')).toEqual({ pattern: '/chat/:id', params: { id: 'ses_01' } });
    expect(resolve('/settings/general/')).toEqual({ pattern: '/settings/:group', params: { group: 'general' } });
    expect(resolve('/settings/security/keys')).toEqual({ pattern: '/settings/:group/:item', params: { group: 'security', item: 'keys' } });
    expect(resolve('/knowledge/projects/acme/web')).toEqual({ pattern: '/knowledge/projects/:owner/:repo', params: { owner: 'acme', repo: 'web' } });
    expect(resolve('/activity/schedules/checkin')).toEqual({ pattern: '/activity/schedules/:id', params: { id: 'checkin' } });
  });

  it('rejects unknown paths and extra segments', () => {
    expect(resolve('/nope')).toBeNull();
    expect(resolve('/chat/a/b')).toBeNull();
    expect(resolve('/knowledge/projects/acme')).toBeNull();
    expect(resolve('/settings/a/b/c')).toBeNull();
  });

  it('decodes params and refuses malformed escapes', () => {
    expect(match('/activity/tasks/:id', '/activity/tasks/a%20b')).toEqual({ id: 'a b' });
    expect(match('/activity/tasks/:id', '/activity/tasks/%E0%A4%A')).toBeNull();
  });

  it('has the final route list from the plan', () => {
    expect(patterns).toEqual([
      '/setup',
      '/login',
      '/password',
      '/chat',
      '/chat/:id',
      '/approvals',
      '/approvals/:id',
      '/knowledge',
      '/knowledge/search',
      '/knowledge/memory',
      '/knowledge/skills',
      '/knowledge/skills/:name',
      '/knowledge/projects/:owner/:repo',
      '/activity',
      '/activity/tasks/:id',
      '/activity/schedules',
      '/activity/schedules/:id',
      '/activity/releases',
      '/activity/spending',
      '/activity/audit',
      '/settings',
      '/settings/:group',
      '/settings/:group/:item',
      '/dev/ui',
    ]);
  });

  it('never lets two patterns claim the same address', () => {
    for (const p of [...patterns, ...legacyPatterns]) {
      const sample = p.replace(/:(\w+)/g, 'x1');
      const claims = [...patterns, ...legacyPatterns].filter((q) => match(q, sample));
      expect(claims, sample).toEqual([p]);
    }
  });
});

describe('redirects (§4.5)', () => {
  const rows: [string, string][] = [
    ['/runs/run_01', '/activity/tasks/run_01'],
    ['/schedules', '/activity/schedules'],
    ['/schedules/sch_01', '/activity/schedules/sch_01'],
    ['/schedules/new', '/activity/schedules/new'],
    ['/memory', '/knowledge/memory'],
    ['/usage', '/activity/spending'],
    ['/audit', '/activity/audit'],
    ['/settings/channels', '/settings/connections'],
    ['/settings/mcp', '/settings/connections'],
    ['/settings/policy', '/settings/security'],
    ['/settings/sandbox', '/settings/security'],
    ['/settings/vault', '/settings/security/keys'],
    ['/settings/tokens', '/settings/account'],
    ['/settings/persona', '/settings/general'],
    ['/settings/budgets', '/settings/general'],
    ['/settings/skills', '/knowledge/skills'],
  ];
  for (const [from, to] of rows) {
    it(`${from} → ${to}`, () => {
      expect(redirect(from)).toBe(to);
      expect(redirect(`${from}/`)).toBe(to);
      expect(resolve(to), `${to} resolves`).not.toBeNull();
    });
  }

  it('keeps escaped segments as they were', () => {
    expect(redirect('/runs/a%20b')).toBe('/activity/tasks/a%20b');
  });

  it('leaves current addresses alone', () => {
    for (const p of ['/chat', '/approvals/apr_1', '/settings/general', '/settings/connections', '/activity/audit', '/knowledge/skills', '/runs', '/runs/a/b'])
      expect(redirect(p), p).toBeNull();
  });

  it('sends /settings to the first group the viewer can open', () => {
    expect(settingsHome(true)).toBe('/settings/general');
    expect(settingsHome(false)).toBe('/settings/account');
  });
});
