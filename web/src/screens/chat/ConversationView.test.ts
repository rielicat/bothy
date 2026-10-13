import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { approvals as fixtureApprovals } from '../../../mock/fixtures';
import { api } from '../../lib/api/client';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import ConversationView from './ConversationView.svelte';

const SID = 'ses_01jbudget0000000000000000';
let seq = 100;
const emit = (type: string, data: unknown, run = 'run_live') =>
  FakeEventSource.last.emit(type, { seq: ++seq, type, principal_id: 'usr_01jsam0000000000000000000', session_id: SID, run_id: run, at: new Date().toISOString(), data });

describe('conversation', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    await session.init();
  });

  it('streams a run: deltas, tool rows, an inline approval, then the persisted reply', async () => {
    render(ConversationView, { id: SID });
    expect(await screen.findByText('What can I help with?')).toBeTruthy();

    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    emit('run.delta', { text: 'Checking **your** files' });
    const strong = await screen.findByText('your');
    expect(strong.tagName).toBe('STRONG');

    emit('run.tool_call', {
      call_id: 'tcl_1',
      tool: 'shell',
      title: 'Run shell command',
      action: { tool: 'shell', kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', 'ls -la'], payload: { command: 'ls -la' }, fingerprint: 'f' },
      verdict: 'ask',
      rule: 'default:local_write',
    });
    expect(await screen.findByText('Ran `ls -la`')).toBeTruthy();
    expect(screen.getByText('needs approval')).toBeTruthy();

    emit('run.status', { status: 'waiting_approval' });
    emit('approval.requested', { approval: { ...fixtureApprovals[0], session_id: SID, run_id: 'run_live' } });
    expect(await screen.findByRole('article')).toBeTruthy();
    expect(screen.getByRole('status').textContent).toContain('Waiting for your approval');

    emit('run.tool_output', { call_id: 'tcl_1', stream: 1, chunk: 'total 0\n' });
    emit('run.tool_result', { call_id: 'tcl_1', status: 'ok', preview: 'total 0', bytes: 8, ms: 120, truncated: false, untrusted: false });
    emit('message.created', {
      message: { id: 9001, session_id: SID, seq: 1, run_id: 'run_live', role: 'assistant', kind: 'normal', provenance: 'system', tokens: 5, created_at: new Date().toISOString(), parts: [{ t: 'text', text: 'All tidy.' }] },
    });
    expect(await screen.findByText('All tidy.')).toBeTruthy();
    expect(screen.queryByText('your')).toBeNull();

    emit('run.usage', { call: { breakdown: { constitution: 1, persona: 1, memory: 1, skills: 1, pins: 0, tools: 1, summary: 0, history: 10, tool_results: 0, turn_context: 1, total: 16, window: 100, reserve: 10, cacheable: 3 }, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 } }, run_cost_micros: 5000, day_cost_micros: 1, day_limit_micros: 2 });
    emit('run.finished', { status: 'budget_exceeded', budget: 'steps', steps: 25, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 });
    const cont = await screen.findByRole('button', { name: /Continue \(\+10 steps\)/ });
    await fireEvent.click(cont);
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/runs/run_live/continue')).toBe(true));
    expect(screen.getByRole('button', { name: /Context: 16% of the window used/ })).toBeTruthy();
  });

  it('neutralises links and offers a clean fork in an exposed session', async () => {
    render(ConversationView, { id: 'ses_01jinbox00000000000000000' });
    expect(await screen.findByText(/I would not click it/)).toBeTruthy();
    expect(document.querySelector('a[href*="evil.example"]')).toBeNull();
    expect(document.querySelector('.md-inert')?.textContent).toBe('evil.example');
    expect(screen.getByRole('button', { name: /Trifecta status: .*Exposed/ })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Fork clean' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/fork'))).toBe(true));
  });

  it('runs slash commands instead of sending them', async () => {
    render(ConversationView, { id: SID });
    const box = await screen.findByLabelText('Message');
    await fireEvent.input(box, { target: { value: '/pin Always answer in English' } });
    await fireEvent.keyDown(box, { key: 'Enter' });
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/pins'))).toBe(true));
    expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/messages'))).toBe(false);
    expect(JSON.parse(calls.find((c) => c.url.endsWith('/pins') && c.method === 'POST')!.body)).toEqual({ text: 'Always answer in English' });
  });

  it('offers Continue only after a steps-budget stop', async () => {
    render(ConversationView, { id: SID });
    await screen.findByText('What can I help with?');
    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    emit('run.finished', { status: 'budget_exceeded', budget: 'daily_cost', error: 'budget exceeded: daily_cost', steps: 3, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 });
    expect(await screen.findByText('budget exceeded: daily_cost')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Continue/ })).toBeNull();

    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' }, 'run_2');
    emit('run.finished', { status: 'budget_exceeded', budget: 'steps', error: 'budget exceeded: steps', steps: 25, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 }, 'run_2');
    expect(await screen.findByRole('button', { name: /Continue \(\+10 steps\)/ })).toBeTruthy();
  });

  it('updates a repeated tool_call event in place instead of adding a row', async () => {
    render(ConversationView, { id: SID });
    await screen.findByText('What can I help with?');
    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    const call = { call_id: 'tcl_9', tool: 'shell', title: 'Run shell command', action: { tool: 'shell', kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', 'ls'], payload: { command: 'ls' }, fingerprint: 'f' }, rule: 'default:local_write' };
    emit('run.tool_call', { ...call, verdict: 'ask' });
    emit('run.tool_call', { ...call, verdict: 'auto' });
    await waitFor(() => expect(screen.getAllByText('Ran `ls`')).toHaveLength(1));
    expect(screen.queryByText('needs approval')).toBeNull();
  });

  // The approval was decided from Telegram, or expired, while the laptop slept.
  it('drops an inline card that was resolved while the stream was away', async () => {
    const trip = fixtureApprovals[0];
    render(ConversationView, { id: trip.session_id });
    expect((await screen.findByRole('article')).dataset.approval).toBe(trip.id);
    await api.post(`/api/approvals/${trip.id}`, { choice: 'deny' });
    FakeEventSource.last.emit('resync', {});
    await waitFor(() => expect(screen.queryByRole('article')).toBeNull());
  });
});

describe('conversation search hits', () => {
  const S = 'ses_01jslack00000000000000000';
  const all = Array.from({ length: 300 }, (_, i) => ({
    id: i + 1,
    session_id: S,
    seq: i + 1,
    role: 'user',
    kind: 'normal',
    provenance: 'user',
    tokens: 5,
    created_at: '2026-10-12T00:00:00Z',
    parts: [{ t: 'text', text: `note number ${i + 1}` }],
  }));

  beforeEach(async () => {
    installApi({
      override: (_m, url) => {
        const u = new URL(url, 'http://bothy');
        if (u.pathname !== `/api/sessions/${S}/messages`) return undefined;
        const older = all.filter((m) => m.seq < (Number(u.searchParams.get('before')) || Infinity));
        const limit = Number(u.searchParams.get('limit') ?? 50);
        return { messages: older.slice(-limit), has_more: older.length > limit };
      },
    });
    await session.init();
  });

  const hit = (seq: number) => document.getElementById(`m${seq}`)?.classList.contains('ring-warn');

  it('loads older messages until the hit is on screen, and follows later hits in the same chat', async () => {
    router.navigate(`/chat/${S}#m12`);
    render(ConversationView, { id: S });
    await waitFor(() => expect(hit(12)).toBe(true));

    router.navigate(`/chat/${S}#m250`);
    await waitFor(() => expect(hit(250)).toBe(true));
    expect(hit(12)).toBe(false);
  });
});

