import { describe, expect, it } from 'vitest';
import { patterns } from '../lib/routes';
import { groupOf, load } from '.';

// One address per pattern, with params the screens accept.
const params: Record<string, Record<string, string>[]> = {
  '/approvals/:id': [{ id: 'apr_1' }],
  '/knowledge/skills/:name': [{ name: 'release-notes' }],
  '/knowledge/projects/:owner/:repo': [{ owner: 'acme', repo: 'web' }],
  '/activity/tasks/:id': [{ id: 'run_1' }],
  '/activity/schedules/:id': [{ id: 'new' }, { id: 'checkin' }],
  '/settings/:group': ['account', 'general', 'models', 'connections', 'people', 'security', 'advanced'].map((group) => ({ group })),
  '/settings/:group/:item': [
    { group: 'general', item: 'personality' },
    { group: 'security', item: 'permissions' },
    { group: 'security', item: 'websites' },
    { group: 'security', item: 'keys' },
    { group: 'connections', item: 'slack' },
    { group: 'connections', item: 'github' },
    { group: 'connections', item: 'search' },
    { group: 'connections', item: 'mcp-files' },
  ],
};

describe('route groups', () => {
  it('keeps chat in the entry and puts every other screen in a group', () => {
    expect(groupOf('/chat')).toBeNull();
    expect(groupOf('/chat/:id')).toBeNull();
    expect(groupOf('/dev/ui')).toBeNull();
    expect(['/setup', '/login', '/password'].map(groupOf)).toEqual(['auth', 'auth', 'auth']);
    expect(groupOf('/approvals/:id')).toBe('approvals');
    expect(groupOf('/knowledge/search')).toBe('knowledge');
    expect(groupOf('/activity/audit')).toBe('activity');
    expect(groupOf('/settings/:group/:item')).toBe('settings');
  });

  it('has a screen for every route pattern', async () => {
    for (const pattern of patterns) {
      const g = groupOf(pattern);
      if (!g) continue;
      const routes = await load(g);
      for (const p of params[pattern] ?? [{}]) expect(routes.screen({ pattern, params: p }), `${pattern} ${JSON.stringify(p)}`).toBeTruthy();
    }
  });

  it('names nothing for items a group does not have', async () => {
    const settings = await load('settings');
    expect(settings.screen({ pattern: '/settings/:group/:item', params: { group: 'security', item: 'nope' } })).toBeNull();
    expect(settings.screen({ pattern: '/settings/:group/:item', params: { group: 'people', item: 'x' } })).toBeNull();
    expect((await load('auth')).screen({ pattern: '/chat', params: {} })).toBeNull();
  });

  it('fetches each chunk once', () => {
    expect(load('activity')).toBe(load('activity'));
  });
});
