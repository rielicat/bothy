import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessions as fixtureSessions } from '../../../mock/fixtures';
import type { Session } from '../../lib/api/types.gen';
import { router } from '../../lib/router.svelte';
import { approvals, session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import { chats, families, groupChats, PAGE } from './chats.svelte';

const base = fixtureSessions[0];
const at = (y: number, m: number, d: number, h = 12) => new Date(y, m - 1, d, h).toISOString();
const ses = (id: string, title: string, last: string, extra: Partial<Session> = {}): Session => ({ ...base, id, title, last_message_at: last, principal_id: base.principal_id, ...extra });
const rows = (list: Session[], parents: Session[] = []) => families(list, parents).map((r) => [r.head.id, r.kids.map((k) => k.id)]);

describe('families (§4.2)', () => {
  it('hangs a chat from its root, else its parent, one level deep', () => {
    const list = [
      ses('fork2', 'A fork of the fork', at(2026, 10, 14, 11), { parent_id: 'fork1', root_id: 'root' }),
      ses('fork1', 'A fork', at(2026, 10, 14, 10), { parent_id: 'root', root_id: 'root' }),
      ses('root', 'Q3 pricing notes', at(2026, 10, 14, 9)),
      // Its root was deleted: it hangs from its surviving parent.
      ses('orphan', 'Continued from a fork', at(2026, 10, 13), { parent_id: 'fork1', root_id: 'gone' }),
      // Neither is listed any more: it stands alone.
      ses('alone', 'Continued from a deleted chat', at(2026, 10, 12), { parent_id: 'gone', root_id: 'gone' }),
    ];
    expect(rows(list)).toEqual([
      ['root', ['fork2', 'fork1']],
      ['fork1', ['orphan']],
      ['alone', []],
    ]);
  });

  it("heads a schedule's results with the newest, and keeps a chat continued from one of them in its series", () => {
    const list = [
      ses('new', 'Weekly pull request summary · 19 Oct', at(2026, 10, 19, 9), { schedule_id: 'sch_prs' }),
      ses('chase', 'Chase the waiting reviews', at(2026, 10, 18), { parent_id: 'old', root_id: 'old' }),
      ses('old', 'Weekly pull request summary · 12 Oct', at(2026, 10, 12, 9), { schedule_id: 'sch_prs' }),
      ses('other', 'Morning briefing · 19 Oct', at(2026, 10, 19, 8), { schedule_id: 'sch_brief' }),
    ];
    expect(rows(list)).toEqual([
      ['new', ['chase', 'old']],
      ['other', []],
    ]);
    expect(families(list)[0].key).toBe('s:sch_prs');
  });

  it("draws a family whose head isn't on a loaded page from `parents`, once", () => {
    const head = ses('root', 'Q3 pricing notes', at(2026, 10, 1));
    const kid = ses('kid', 'Pricing page copy', at(2026, 10, 14), { parent_id: 'root', root_id: 'root' });
    expect(rows([kid], [head])).toEqual([['root', ['kid']]]);
    // Its page loaded later: the listed copy wins and nothing repeats.
    expect(rows([kid, { ...head, title: 'Renamed' }], [head])).toEqual([['root', ['kid']]]);
    expect(families([kid, { ...head, title: 'Renamed' }], [head])[0].head.title).toBe('Renamed');
  });

  it('places a family by its newest message, in the bucket of that message', () => {
    const now = new Date(2026, 9, 14, 15).getTime();
    const list = [
      ses('kid', 'Pricing page copy', at(2026, 10, 14, 9), { parent_id: 'root', root_id: 'root' }),
      ses('plain', 'Fix login redirect', at(2026, 10, 14, 8)),
      ses('root', 'Q3 pricing notes', at(2026, 9, 1)),
    ];
    const { groups } = groupChats(families(list), now);
    expect(groups.map((g) => [g.label, g.items.map((r) => r.head.title)])).toEqual([['Today', ['Q3 pricing notes', 'Fix login redirect']]]);
  });
});

describe('groupChats', () => {
  const now = new Date(2026, 9, 14, 15).getTime();
  const list = [
    ses('a', 'Just now', at(2026, 10, 14, 9)),
    ses('b', 'This morning', at(2026, 10, 14, 0)),
    ses('c', 'Yesterday', at(2026, 10, 13, 23)),
    ses('d', 'A week ago', at(2026, 10, 7, 1)),
    ses('e', 'Eight days ago', at(2026, 10, 6, 23)),
  ];

  it('groups rows into Today, Previous 7 days and Older by local day', () => {
    const { groups, more } = groupChats(families(list), now);
    expect(groups.map((g) => [g.label, g.items.map((r) => r.head.title)])).toEqual([
      ['Today', ['Just now', 'This morning']],
      ['Previous 7 days', ['Yesterday', 'A week ago']],
      ['Older', ['Eight days ago']],
    ]);
    expect(more).toBe(false);
  });

  it('counts rows, not chats, up to the limit', () => {
    const family = [...list, ses('k1', 'Kid', at(2026, 10, 14, 10), { parent_id: 'a', root_id: 'a' }), ses('k2', 'Kid', at(2026, 10, 14, 10), { parent_id: 'a', root_id: 'a' })];
    const { groups, more } = groupChats(families(family), now, 2);
    expect(groups.flatMap((g) => g.items.map((r) => r.head.id))).toEqual(['a', 'b']);
    expect(more).toBe(true);
  });

  it('leaves out empty groups', () => {
    expect(groupChats(families([list[1]]), now).groups.map((g) => g.key)).toEqual(['today']);
  });
});

describe('chat list store', () => {
  let calls: Call[];
  beforeEach(async () => {
    calls = installApi();
    await session.init();
  });
  afterEach(() => {
    chats.reset();
    localStorage.clear();
  });

  function serve(pages: { items: Session[]; next?: number; parents?: Session[] }[]) {
    calls = installApi({
      override: (_m, url) => {
        if (!url.startsWith('/api/sessions?')) return undefined;
        const before = Number(new URLSearchParams(url.split('?')[1]).get('before') ?? 0);
        return pages[before] ?? { items: [] };
      },
    });
  }

  it('never lists a chat without messages (S14)', async () => {
    serve([{ items: [ses('ses_used', 'Release plan', base.last_message_at), ses('ses_zero', 'Never used', '0001-01-01T00:00:00Z')] }]);
    await chats.load();
    expect(chats.visible.map((s) => s.id)).toEqual(['ses_used']);
  });

  it('asks only for the list: the server titles every chat', async () => {
    serve([{ items: [ses('ses_a', 'Draft the October release notes from the merged', base.last_message_at)] }]);
    await chats.load();
    expect(chats.title(chats.visible[0])).toBe('Draft the October release notes from the merged');
    expect(calls.filter((c) => c.url.includes('/messages'))).toEqual([]);
  });

  it('follows runs and titles from the event stream', async () => {
    serve([{ items: [ses('ses_a', 'Release plan', base.last_message_at), ses('ses_b', 'Plan the Q4 offsite', base.last_message_at)] }]);
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
    emit('message.created', 'ses_b', {});
    expect(chats.visible.map((s) => s.id)).toEqual(['ses_b', 'ses_a']);
    emit('session.updated', 'ses_b', { title: 'Q4 offsite plan', taint: 0, epoch: 0 });
    expect(chats.title(chats.visible[0])).toBe('Q4 offsite plan');
    off();
  });

  it('marks a chat that waits for approval', async () => {
    serve([{ items: [ses('ses_a', 'Release plan', base.last_message_at)] }]);
    await chats.load();
    approvals.pending = [{ id: 'apr_1', session_id: 'ses_a', status: 'pending' } as never];
    expect(chats.waiting('ses_a')).toBe(true);
    approvals.reset();
    expect(chats.waiting('ses_a')).toBe(false);
  });

  it('reads up to three pages at first for a screenful of rows, then one page per Show more', async () => {
    // Fifty results of one schedule fold into a single row.
    const results = (from: number) => Array.from({ length: 50 }, (_, i) => ses(`ses_r${from + i}`, `Check-in · ${from + i}`, at(2026, 10, 14, 23) , { schedule_id: 'sch_checkin' }));
    serve([
      { items: results(0), next: 1 },
      { items: results(50), next: 2 },
      { items: [...results(100), ...Array.from({ length: 40 }, (_, i) => ses(`ses_c${i}`, `Chat ${i}`, at(2026, 10, 1)))], next: 3 },
      { items: Array.from({ length: 5 }, (_, i) => ses(`ses_o${i}`, `Older ${i}`, at(2026, 9, 1))) },
    ]);
    await chats.load();
    expect(calls.filter((c) => c.url.startsWith('/api/sessions?')).map((c) => c.url)).toEqual(['/api/sessions?limit=50', '/api/sessions?limit=50&before=1', '/api/sessions?limit=50&before=2']);
    expect(chats.rows).toHaveLength(41);
    expect(chats.shown).toBe(PAGE);
    chats.showMore();
    expect(chats.shown).toBe(PAGE * 2);
    await vi.waitFor(() => expect(chats.rows).toHaveLength(46));
  });

  it('keeps the anchors a page sends, and drops a deleted one', async () => {
    const head = ses('root', 'Q3 pricing notes', at(2026, 9, 1));
    serve([{ items: [ses('kid', 'Pricing page copy', base.last_message_at, { parent_id: 'root', root_id: 'root' })], parents: [head] }]);
    await chats.load();
    expect(chats.rows.map((r) => [r.head.id, r.kids.map((k) => k.id)])).toEqual([['root', ['kid']]]);
    chats.remove('root');
    expect(chats.rows.map((r) => r.head.id)).toEqual(['kid']);
  });

  it('remembers opened families in the browser, newest first, at most 100', () => {
    for (let i = 0; i < 101; i++) chats.toggle(`k${i}`);
    expect(chats.isOpen('k100')).toBe(true);
    const kept = JSON.parse(localStorage.getItem('bothy.sidebar.open') ?? '[]');
    expect(kept).toHaveLength(100);
    expect(kept[0]).toBe('k100');
    chats.toggle('k100');
    expect(chats.isOpen('k100')).toBe(false);
    expect(JSON.parse(localStorage.getItem('bothy.sidebar.open') ?? '[]')[0]).toBe('k99');
  });

  it('works without browser storage', async () => {
    const broken = { getItem: () => { throw new Error('denied'); }, setItem: () => { throw new Error('denied'); } };
    vi.stubGlobal('localStorage', broken);
    vi.resetModules();
    try {
      const fresh = (await import('./chats.svelte')).chats;
      expect(fresh.isOpen('root')).toBe(false);
      fresh.toggle('root');
      expect(fresh.isOpen('root')).toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('lists a clean chat under the one it came from at once, and opens it', async () => {
    const [pricing] = fixtureSessions.filter((s) => s.title === 'Q3 pricing notes');
    await chats.load();
    await chats.fork(pricing.id);
    const row = chats.rows.find((r) => r.head.id === pricing.id)!;
    expect(row.kids[0].title).toBe('Q3 pricing notes (clean)');
    expect(router.path).toBe(`/chat/${row.kids[0].id}`);
    expect(calls.some((c) => c.method === 'POST' && c.url === `/api/sessions/${pricing.id}/fork`)).toBe(true);
  });
});
