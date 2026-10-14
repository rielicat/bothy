import { describe, expect, it, vi } from 'vitest';
import { member } from '../mock/fixtures';
import { api, setCsrf } from './lib/api/client';
import type { Me } from './lib/api/types.gen';
import { events } from './lib/sse.svelte';
import { approvals, session } from './lib/stores/session.svelte';
import { installApi } from './test/api';
import { FakeEventSource } from './test/setup';

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('session', () => {
  it('logs out when the event stream is refused because the session expired', async () => {
    let expired = false;
    installApi({ override: (m, url) => (expired && url === '/api/auth/me' ? new Response('{"error":{"code":"unauthorized","message":"log in"}}', { status: 401 }) : undefined) });
    await session.init();
    expect(session.me).not.toBeNull();
    expired = true;
    FakeEventSource.last.fail(true);
    await tick();
    expect(session.me).toBeNull();
  });

  // Someone else signed in from another tab: the cookie is theirs now.
  it('logs out when the stream comes back as another user', async () => {
    let other: Me | null = null;
    installApi({ override: (_m, url) => (other && url === '/api/auth/me' ? other : undefined) });
    await session.init();
    const me = session.me!;
    other = { ...me, user: member, csrf: 'other-csrf' } as Me;
    FakeEventSource.last.fail(true);
    await tick();
    await tick();
    expect(session.me).toBeNull();
  });

  it("tells this user's other tabs when signing out, but not on a plain 401", async () => {
    let dead = false;
    installApi({ override: (_m, url) => (dead && url === '/api/sessions' ? new Response('{"error":{"code":"unauthorized","message":"log in"}}', { status: 401 }) : undefined) });
    const told = vi.spyOn(events, 'logout');
    await session.init();
    await session.logout();
    expect(told).toHaveBeenCalledTimes(1);

    await api.post('/api/auth/login', { username: 'sam', password: 'Granite-River-42-Lantern' });
    await session.init();
    expect(session.me).not.toBeNull();
    dead = true;
    await api.get('/api/sessions').catch(() => {});
    expect(session.me).toBeNull();
    expect(told).toHaveBeenCalledTimes(1);
  });

  it('follows the kill switch from banner events', async () => {
    installApi();
    await session.init();
    const banner = (data: object) => FakeEventSource.last.emit('banner', { seq: 1, type: 'banner', at: '2026-10-12T00:00:00Z', data });
    banner({ level: 'error', code: 'paused', text: 'Bothy is paused by an admin.' });
    expect(session.paused).toBe(true);
    banner({ level: 'error', code: 'paused', text: '', clear: true });
    expect(session.paused).toBe(false);
  });

  // Another tab changed the password, which rotated the session and its token.
  it('picks up a rotated CSRF token and retries the write once', async () => {
    const calls = installApi();
    await session.init();
    setCsrf('stale-token');
    await api.post('/api/admin/pause', { paused: false });
    const pauses = calls.filter((c) => c.url === '/api/admin/pause');
    expect(pauses.map((c) => c.headers['x-csrf-token'])).toEqual(['stale-token', 'mock-csrf']);
    expect(calls.filter((c) => c.url === '/api/auth/me').length).toBeGreaterThanOrEqual(2);
  });

  it('does not retry a write as a different user', async () => {
    let other: Me | null = null;
    const calls = installApi({ override: (_m, url) => (other && url === '/api/auth/me' ? other : undefined) });
    await session.init();
    other = { ...session.me!, user: member, csrf: 'mock-csrf' } as Me;
    setCsrf('stale-token');
    await expect(api.post('/api/admin/pause', { paused: false })).rejects.toMatchObject({ status: 403 });
    expect(calls.filter((c) => c.url === '/api/admin/pause')).toHaveLength(1);
    expect(session.me).toBeNull();
  });

  it('keeps dismissed banners for the tab, and brings one back when it is raised again', async () => {
    installApi();
    sessionStorage.clear();
    await session.init();
    session.dismiss('restart_required');
    expect(JSON.parse(sessionStorage.getItem('bothy.dismissed')!)).toEqual(['restart_required']);
    FakeEventSource.last.emit('banner', { seq: 1, type: 'banner', at: '2026-10-12T00:00:00Z', data: { level: 'info', code: 'restart_required', text: 'Restart to apply: x' } });
    expect(session.dismissed).toEqual([]);
    expect(JSON.parse(sessionStorage.getItem('bothy.dismissed')!)).toEqual([]);
  });

  it('remembers that the session ran out, until the next sign-in', async () => {
    installApi();
    await session.init();
    session.expire();
    expect(session.expired).toBe(true);
    await api.post('/api/auth/login', { username: 'sam', password: 'Granite-River-42-Lantern' });
    await session.init();
    expect(session.expired).toBe(false);
  });

  it('counts as waiting what the inbox says the viewer can decide', async () => {
    installApi();
    await session.init();
    const ask = (user_id: string, admin_only = false) => ({ id: `apr_${user_id}${admin_only}`, status: 'pending', approver: { user_id, admin_only } }) as never;
    approvals.forYou = 4;
    expect(approvals.waiting).toBe(4);
    // Without the inbox: admins count everything open, members what names them.
    approvals.forYou = null;
    approvals.pending = [ask(session.me!.user.id), ask('usr_other'), ask('', true)];
    expect(approvals.waiting).toBe(3);
    session.me = { ...session.me!, user: { ...member } } as Me;
    approvals.pending = [ask(member.id), ask('usr_other'), ask('', true)];
    expect(approvals.waiting).toBe(1);
  });
});
