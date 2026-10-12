import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { approvals as fixtureApprovals } from '../../../mock/fixtures';
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
    emit('run.finished', { status: 'budget_exceeded', steps: 25, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 });
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
});
