import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { sessions as fixtureSessions } from '../../../mock/fixtures';
import type { Me, Message, Session } from '../../lib/api/types.gen';
import { approvals, session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import { chats, firstWords, groupChats, PAGE } from './chats.svelte';

const base = fixtureSessions[0];
const at = (y: number, m: number, d: number, h = 12) => new Date(y, m - 1, d, h).toISOString();
const ses = (id: string, title: string, last: string, extra: Partial<Session> = {}): Session => ({ ...base, id, title, last_message_at: last, principal_id: base.principal_id, ...extra });
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('firstWords', () => {
  it('keeps short text and cuts long text at 48 characters', () => {
    expect(firstWords('  Fix the   login redirect ')).toBe('Fix the login redirect');
    const long = 'Draft the October release notes from the merged pull requests in acme/web';
    expect(firstWords(long)).toBe('Draft the October release notes from the merged…');
    expect(firstWords(long).length).toBeLessThanOrEqual(49);
  });
});

describe('groupChats', () => {
  const now = new Date(2026, 9, 14, 15).getTime();
  const list = [
    ses('a', 'Group', at(2026, 10, 14, 9), { principal_id: 'spc_1' }),
    ses('b', 'This morning', at(2026, 10, 14, 0)),
    ses('c', 'Yesterday', at(2026, 10, 13, 23)),
    ses('d', 'A week ago', at(2026, 10, 7, 1)),
    ses('e', 'Eight days ago', at(2026, 10, 6, 23)),
  ];

  it('puts group chats first, then Today, Previous 7 days and Older by local day', () => {
    const { groups, more } = groupChats(list, new Set(['spc_1']), now);
    expect(groups.map((g) => [g.label, g.items.map((s) => s.title)])).toEqual([
      ['Group chats', ['Group']],
      ['Today', ['This morning']],
      ['Previous 7 days', ['Yesterday', 'A week ago']],
      ['Older', ['Eight days ago']],
    ]);
    expect(more).toBe(false);
  });

  it('limits the viewer’s own chats, never the group chats', () => {
    const { groups, more } = groupChats(list, new Set(['spc_1']), now, 2);
    expect(groups.flatMap((g) => g.items.map((s) => s.id))).toEqual(['a', 'b', 'c']);
    expect(more).toBe(true);
  });

  it('leaves out empty groups', () => {
    expect(groupChats([list[1]], new Set(), now).groups.map((g) => g.key)).toEqual(['today']);
  });
});

describe('chat list store', () => {
  beforeEach(async () => {
    installApi();
    await session.init();
  });
  afterEach(() => chats.reset());

  function serve(list: Session[], messages: Record<string, Message[]> = {}) {
    installApi({
      override: (_m, url) => {
        if (url.startsWith('/api/sessions?')) return { items: list };
        const id = /^\/api\/sessions\/([^/]+)\/messages/.exec(url)?.[1];
        if (id) return { messages: messages[decodeURIComponent(id)] ?? [], has_more: false };
        return undefined;
      },
    });
  }

  const userSaid = (sid: string, text: string): Message => ({ id: 1, session_id: sid, seq: 1, role: 'user', kind: 'normal', parts: [{ t: 'text', text }], provenance: 'user', tokens: 1, created_at: base.created_at });

  it('never lists a chat without messages (S14)', async () => {
    serve([
      ses('ses_used', 'Release plan', base.last_message_at),
      ses('ses_zero', 'Never used', '0001-01-01T00:00:00Z'),
      ses('ses_blank', '', base.last_message_at),
      ses('ses_typed', '', base.last_message_at),
    ], { ses_typed: [userSaid('ses_typed', 'Draft the October release notes from the merged pull requests')] });
    await chats.load();
    await flush();
    expect(chats.visible.map((s) => s.id)).toEqual(['ses_used', 'ses_typed']);
    expect(chats.title(chats.visible[1])).toBe('Draft the October release notes from the merged…');
  });

  it('names an untitled group chat after the group', async () => {
    const me = session.me!;
    session.me = { ...me, principals: [...me.principals, { id: 'spc_eng', kind: 'space', user_id: me.user.id, display_name: '#eng', workspace_id: 'w', tool_profile: 'restricted', created_at: '' }] } as Me;
    serve([ses('ses_g', '', base.last_message_at, { principal_id: 'spc_eng' })]);
    await chats.load();
    expect(chats.title(chats.visible[0])).toBe('#eng');
  });

  it('follows runs and titles from the event stream', async () => {
    serve([ses('ses_a', 'Release plan', base.last_message_at), ses('ses_b', '', base.last_message_at)]);
    await chats.load();
    const off = chats.listen();
    const emit = (type: string, sid: string, data: object = {}) => FakeEventSource.last.emit(type, { seq: 1, type, session_id: sid, at: new Date().toISOString(), data });
    emit('run.started', 'ses_a', { trigger: 'message', model: 'm' });
    expect(chats.running['ses_a'] ?? false).toBe(true);
    emit('run.status', 'ses_a', { status: 'waiting_approval' });
    expect(chats.running['ses_a'] ?? false).toBe(false);
    emit('run.status', 'ses_a', { status: 'tool' });
    emit('run.finished', 'ses_a', { status: 'done' });
    expect(chats.running['ses_a'] ?? false).toBe(false);
    emit('message.created', 'ses_b', { message: userSaid('ses_b', 'Plan the Q4 offsite') });
    expect(chats.visible.map((s) => s.id)).toEqual(['ses_b', 'ses_a']);
    expect(chats.title(chats.visible[0])).toBe('Plan the Q4 offsite');
    emit('session.updated', 'ses_b', { title: 'Q4 offsite plan', taint: 0, epoch: 0 });
    expect(chats.title(chats.visible[0])).toBe('Q4 offsite plan');
    off();
  });

  it('marks a chat that waits for approval', async () => {
    serve([ses('ses_a', 'Release plan', base.last_message_at)]);
    await chats.load();
    approvals.pending = [{ id: 'apr_1', session_id: 'ses_a', status: 'pending' } as never];
    expect(chats.waiting('ses_a')).toBe(true);
    approvals.reset();
    expect(chats.waiting('ses_a')).toBe(false);
  });

  it('shows more chats in pages of 30', async () => {
    serve(Array.from({ length: 45 }, (_, i) => ses(`ses_${i}`, `Chat ${i}`, base.last_message_at)));
    await chats.load();
    expect(chats.shown).toBe(PAGE);
    chats.showMore();
    expect(chats.shown).toBe(PAGE * 2);
  });
});
