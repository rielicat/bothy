import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { approvals as fixtureApprovals } from '../../../mock/fixtures';
import { chatIds } from '../../../mock/domains/chat';
import { api } from '../../lib/api/client';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import ConversationView from './ConversationView.svelte';

const SID = 'ses_01jbudget0000000000000000';
let seq = 100;
const emit = (type: string, data: unknown, run = 'run_live', sid = SID) =>
  FakeEventSource.last.emit(type, { seq: ++seq, type, principal_id: 'usr_01jsam0000000000000000000', session_id: sid, run_id: run, at: new Date().toISOString(), data });

// Activity lines mix plain words with verbatim paths and commands, so they are matched by their whole text.
const text = (t: string | RegExp) => (_: string, el: Element | null) =>
  !!el && el.tagName === 'SPAN' && (typeof t === 'string' ? el.textContent === t : t.test(el.textContent ?? '')) && ![...el.children].some((c) => c.tagName === 'SPAN' && c.textContent === el.textContent);
const loaded = () => waitFor(() => expect(screen.queryByText('Loading…')).toBeNull());
const shell = { tool: 'shell', kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', 'ls -la'], payload: { command: 'ls -la' }, fingerprint: 'f' };

describe('conversation', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    await session.init();
  });

  it('streams a run: Working…, a waiting step with its card, the result, then the persisted reply', async () => {
    render(ConversationView, { id: SID });
    await loaded();

    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    expect(await screen.findByText('Working…')).toBeTruthy();
    emit('run.delta', { text: 'Checking **your** files' });
    const strong = await screen.findByText('your');
    expect(strong.tagName).toBe('STRONG');
    expect(screen.queryByText('Working…')).toBeNull();
    expect(screen.queryByText(/Thinking/)).toBeNull();

    emit('run.tool_call', { call_id: 'tcl_1', tool: 'shell', title: 'Run shell command', action: shell, verdict: 'ask', rule: 'default:local_write' });
    expect(await screen.findByText(text('Waiting for approval to run ls -la'))).toBeTruthy();
    emit('approval.requested', { approval: { ...fixtureApprovals[0], session_id: SID, run_id: 'run_live', tool_call_id: 'tcl_1' } });
    expect(await screen.findByRole('article')).toBeTruthy();
    expect(screen.queryByText(text('Ran ls -la'))).toBeNull();

    emit('run.tool_output', { call_id: 'tcl_1', stream: 1, chunk: 'total 0\n' });
    emit('run.tool_result', { call_id: 'tcl_1', status: 'ok', preview: 'total 0', bytes: 8, ms: 120, truncated: false, untrusted: false });
    emit('message.created', {
      message: { id: 9001, session_id: SID, seq: 1, run_id: 'run_live', role: 'assistant', kind: 'normal', provenance: 'system', tokens: 5, created_at: new Date().toISOString(), parts: [{ t: 'text', text: 'All tidy.' }] },
    });
    expect(await screen.findByText('All tidy.')).toBeTruthy();
    expect(screen.queryByText('your')).toBeNull();

    emit('run.finished', { status: 'budget_exceeded', budget: 'steps', steps: 25, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 });
    expect(await screen.findByText(/Bothy paused after 25 steps to check in\./)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Keep going' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/runs/run_live/continue')).toBe(true));
  });

  it('neutralises links, says once that the chat is exposed and continues in a clean chat', async () => {
    render(ConversationView, { id: 'ses_01jinbox00000000000000000' });
    expect(await screen.findByText(/I would not click it/)).toBeTruthy();
    expect(document.querySelector('a[href*="evil.example"]')).toBeNull();
    expect(document.querySelector('.md-inert')?.textContent).toBe('evil.example');
    expect(screen.getAllByText('From now on, Bothy will ask before sending anything out of this chat.')).toHaveLength(1);
    await fireEvent.click(screen.getByRole('button', { name: 'Chat options' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Continue in a clean chat' }));
    const dialog = await screen.findByRole('dialog', { name: 'Continue in a clean chat?' });
    await fireEvent.click(dialog.querySelector('.btn-primary')!);
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

  it('offers Keep going only after a steps-budget stop', async () => {
    render(ConversationView, { id: SID });
    await loaded();
    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    emit('run.finished', { status: 'budget_exceeded', budget: 'daily_cost', error: 'budget exceeded: daily_cost', steps: 3, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 });
    expect(await screen.findByText(/Bothy has reached today's .*spending limit\. It can start again at midnight/)).toBeTruthy();
    expect(screen.queryByText('budget exceeded: daily_cost')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Keep going' })).toBeNull();

    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' }, 'run_2');
    emit('run.finished', { status: 'budget_exceeded', budget: 'steps', error: 'budget exceeded: steps', steps: 25, usage: { input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 5000 }, 'run_2');
    expect(await screen.findByRole('button', { name: 'Keep going' })).toBeTruthy();
  });

  it('updates a repeated tool_call event in place instead of adding a row', async () => {
    render(ConversationView, { id: SID });
    await loaded();
    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    const call = { call_id: 'tcl_9', tool: 'shell', title: 'Run shell command', action: { ...shell, argv: ['bash', '-lc', 'ls'] }, rule: 'default:local_write' };
    emit('run.tool_call', { ...call, verdict: 'ask' });
    emit('run.tool_call', { ...call, verdict: 'auto' });
    await waitFor(() => expect(screen.getAllByText(text('Running ls…'))).toHaveLength(1));
    expect(screen.queryByText(text(/^Waiting for approval/))).toBeNull();
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

  it('keeps decided cards as one line each, after a reload too', async () => {
    render(ConversationView, { id: chatIds.login });
    expect(await screen.findByText(text(/^You approved changing repos\/acme\/web\/src\/auth\/callback\.ts · /))).toBeTruthy();
    expect(screen.getByText(text(/^You denied opening a pull request in acme\/web · “Not before the freeze ends” · /))).toBeTruthy();
    expect(screen.getByText(text("Didn't open a pull request in acme/web"))).toBeTruthy();
    expect(screen.getByText(text('Changed repos/acme/web/src/auth/callback.ts (+3\u00a0−1)'))).toBeTruthy();
    await fireEvent.click(screen.getAllByRole('button', { name: 'Show' })[0]);
    expect(await screen.findByRole('article')).toBeTruthy();
  });

  it('folds finished steps into a summary line that opens on request', async () => {
    render(ConversationView, { id: chatIds.login });
    const summary = await screen.findByRole('button', { name: 'Got a copy of acme/web and read 3 files in repos/acme/web/src/auth' });
    expect(summary.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByText(text('Read repos/acme/web/src/auth/session.ts'))).toBeNull();
    await fireEvent.click(summary);
    expect(await screen.findByText(text('Read repos/acme/web/src/auth/session.ts'))).toBeTruthy();
    expect(screen.getByText(text('Ran npm test'))).toBeTruthy();
  });

  it('counts a step that did not work and keeps it visible under the summary', async () => {
    render(ConversationView, { id: chatIds.pricing });
    expect(await screen.findByRole('button', { name: "Took 3 steps, 1 didn't work" })).toBeTruthy();
    expect(screen.getByText(text("Couldn't open status.rival-two.example"))).toBeTruthy();
    expect(screen.getByText('Yesterday')).toBeTruthy();
  });

  it('says a provider failure once, in plain words, with the raw message behind Details', async () => {
    render(ConversationView, { id: chatIds.notices });
    expect(await screen.findByText(/^Anthropic had a problem answering\./)).toBeTruthy();
    expect(screen.getAllByText(/had a problem answering/)).toHaveLength(1);
    expect(document.body.textContent).not.toContain('.).');
    expect(screen.getByText('Older messages were summarised so this chat can keep going.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('turns a knowledge citation into a link without the commit in the text', async () => {
    render(ConversationView, { id: chatIds.login });
    const link = await screen.findByRole('link', { name: 'acme/web · src/auth/callback.ts, lines 10–18 ↗' });
    expect(link.getAttribute('href')).toBe('https://github.com/acme/web/blob/4f2c1a9/src/auth/callback.ts#L10-L18');
    expect(link.closest('p')?.textContent).toBe('The change is in acme/web · src/auth/callback.ts, lines 10–18 ↗.');
  });

  it('says a command could not run, not that it finished with errors, when this server cannot run code', async () => {
    session.sandbox = false;
    render(ConversationView, { id: chatIds.notices });
    expect(await screen.findByText(text(/^Bothy can't run code on this server, so it couldn't run python3 build_summary\.py\. How to turn it on$/))).toBeTruthy();
    expect(screen.getByText(text("Couldn't run python3 build_summary.py"))).toBeTruthy();
    expect(screen.queryByText(/finished with errors/)).toBeNull();
    expect(screen.getByRole('button', { name: 'How to turn it on' })).toBeTruthy();
  });

  it('renames inline in the h1: Enter saves once, Esc cancels, and focus comes back to the title', async () => {
    render(ConversationView, { id: chatIds.login });
    const title = await screen.findByRole('button', { name: 'Fix login redirect' });
    await fireEvent.click(title);
    const field = screen.getByLabelText('Chat title') as HTMLInputElement;
    expect(field.closest('h1')).toBeTruthy();
    await fireEvent.input(field, { target: { value: 'Fix the sign-in return address' } });
    await fireEvent.keyDown(field, { key: 'Enter' });
    await fireEvent.blur(field);
    const renamed = await screen.findByRole('button', { name: 'Fix the sign-in return address' });
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
    await waitFor(() => expect(document.activeElement).toBe(renamed));

    await fireEvent.click(renamed);
    await fireEvent.keyDown(screen.getByLabelText('Chat title'), { key: 'Escape' });
    expect(await screen.findByRole('button', { name: 'Fix the sign-in return address' })).toBeTruthy();
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
  });

  it('opens a step’s Details with the exact request and how it was allowed', async () => {
    render(ConversationView, { id: chatIds.login });
    const line = await screen.findByRole('button', { name: 'Ran npm test' });
    await fireEvent.click(line);
    expect(await screen.findByText('Allowed by a saved permission')).toBeTruthy();
    expect(screen.getByText('What Bothy asked for')).toBeTruthy();
  });
});

describe('new chat', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    await session.init();
  });

  it('fills the composer from a suggestion without sending, and stores nothing before the first message', async () => {
    render(ConversationView, { id: '' });
    expect(await screen.findByRole('heading', { level: 1, name: 'What should Bothy work on?' })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'What can you do for our team?' }));
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('What can you do for our team?');
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    // An admin without GitHub is offered to connect it instead of the meeting-notes idea.
    expect(screen.getByRole('link', { name: 'Connect GitHub so Bothy can read your code' })).toBeTruthy();

    await fireEvent.keyDown(screen.getByLabelText('Message'), { key: 'Enter' });
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && /\/api\/sessions\/[^/]+\/messages$/.test(c.url))).toBe(true));
    const posts = calls.filter((c) => c.method === 'POST').map((c) => c.url);
    expect(posts[0]).toBe('/api/sessions');
    expect(router.path).toMatch(/^\/chat\/ses_/);
  });

  it('adds a line instead of sending on Enter with a touch screen', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('coarse'), addEventListener() {}, removeEventListener() {} }));
    render(ConversationView, { id: SID });
    const box = await screen.findByLabelText('Message');
    await fireEvent.input(box, { target: { value: 'first line' } });
    const allowed = await fireEvent.keyDown(box, { key: 'Enter' });
    expect(allowed).toBe(true);
    expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/messages'))).toBe(false);
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

  const hit = (n: number) => document.getElementById(`m${n}`)?.classList.contains('bg-warning-subtle');

  it('loads older messages until the hit is on screen, and follows later hits in the same chat', async () => {
    router.navigate(`/chat/${S}#m12`);
    render(ConversationView, { id: S });
    await waitFor(() => expect(hit(12)).toBe(true));

    router.navigate(`/chat/${S}#m250`);
    await waitFor(() => expect(hit(250)).toBe(true));
    expect(hit(12)).toBe(false);
  });
});
