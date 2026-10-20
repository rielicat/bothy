import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { approvals as fixtureApprovals, sessions as fixtureSessions } from '../../../mock/fixtures';
import { chatIds } from '../../../mock/domains/chat';
import { resetMock } from '../../../mock/server';
import { chats } from '../../components/shell/chats.svelte';
import { api } from '../../lib/api/client';
import type { Message, SessionFrom } from '../../lib/api/types.gen';
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

  it("says a chat the viewer can't open doesn't exist, not to check the connection", async () => {
    const gone = () => new Response(JSON.stringify({ error: { code: 'not_found', message: 'not found' } }), { status: 404 });
    calls = installApi({ override: (m, url) => (m === 'GET' && url.startsWith('/api/sessions/ses_someone_else') ? gone() : undefined) });
    render(ConversationView, { id: 'ses_someone_else' });
    expect(await screen.findByText("This chat doesn't exist or was deleted.")).toBeTruthy();
    expect(screen.queryByText(/Check your connection/)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });

  it('streams a run: Working…, a waiting step with its card, the result, then the persisted reply', async () => {
    render(ConversationView, { id: SID });
    await loaded();

    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' });
    expect(await screen.findByText('Working…')).toBeTruthy();
    // Stop is a quiet second action beside Send, never a filled button (§7.5).
    const stop = screen.getByRole('button', { name: 'Stop' });
    expect(stop.classList.contains('btn-secondary')).toBe(true);
    expect(stop.className).not.toMatch(/\bbg-text\b|btn-primary/);
    // Bothy's picture marks the turn while it works, before any words arrive.
    expect(screen.getByText('Working…').parentElement!.querySelector('img[src="/api/agent/picture"]')).toBeTruthy();
    emit('run.delta', { text: 'Checking **your** files' });
    const strong = await screen.findByText('your');
    expect(strong.tagName).toBe('STRONG');
    // No caret or pulsing dot after streaming text (D35).
    expect(strong.closest('section')!.querySelector('[class*="animate-pulse"], [class*="animate-ping"], [class*="caret"]')).toBeNull();
    expect(screen.queryByText('Working…')).toBeNull();
    expect(screen.queryByText(/Thinking/)).toBeNull();

    emit('run.tool_call', { call_id: 'tcl_1', tool: 'shell', title: 'Run shell command', action: shell, verdict: 'ask', rule: 'default:local_write' });
    expect(await screen.findByText(text('Waiting for approval to run ls -la'))).toBeTruthy();
    emit('approval.requested', { approval: { ...fixtureApprovals[0], session_id: SID, run_id: 'run_live', tool_call_id: 'tcl_1' } });
    expect(await screen.findByRole('article')).toBeTruthy();
    // With the card right below, the line says only whose approval it waits for.
    expect(await screen.findByText(text('Waiting for your approval'))).toBeTruthy();
    expect(screen.queryByText(text('Waiting for approval to run ls -la'))).toBeNull();
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

  it('keeps following a card that grows after it renders while the reader is at the bottom', async () => {
    let grow = () => {};
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(cb: () => void) {
          grow = cb;
        }
        observe() {}
        disconnect() {}
      },
    );
    const sc = (document.scrollingElement ?? document.documentElement) as HTMLElement;
    let height = 1000;
    let top = 0;
    Object.defineProperty(sc, 'scrollHeight', { configurable: true, get: () => height });
    Object.defineProperty(sc, 'clientHeight', { configurable: true, get: () => 500 });
    Object.defineProperty(sc, 'scrollTop', { configurable: true, get: () => top, set: (v: number) => (top = Math.min(v, height - 500)) });
    try {
      render(ConversationView, { id: SID });
      await loaded();
      sc.scrollTop = height;
      height = 1400;
      grow();
      expect(sc.scrollTop).toBe(900);

      // Someone who scrolled up to read stays where they are.
      sc.scrollTop = 100;
      window.dispatchEvent(new Event('scroll'));
      height = 1800;
      grow();
      expect(sc.scrollTop).toBe(100);
    } finally {
      vi.unstubAllGlobals();
      for (const p of ['scrollHeight', 'clientHeight', 'scrollTop']) delete (sc as unknown as Record<string, unknown>)[p];
    }
  });

  it('neutralises links, says once that the chat is exposed and starts a clean related chat', async () => {
    render(ConversationView, { id: 'ses_01jinbox00000000000000000' });
    expect(await screen.findByText(/I would not click it/)).toBeTruthy();
    expect(document.querySelector('a[href*="evil.example"]')).toBeNull();
    expect(document.querySelector('.md-inert')?.textContent).toBe('evil.example');
    expect(screen.getAllByText('From now on, Bothy will ask before sending anything out of this chat.')).toHaveLength(1);
    await fireEvent.click(screen.getByRole('button', { name: 'Chat options' }));
    // One way to start fresh: the menu offers no second, identical action.
    expect(screen.queryByRole('menuitem', { name: 'Continue in a clean chat' })).toBeNull();
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'New related chat' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/fork'))).toBe(true));
  });

  it("hands Delete to the shell's one dialog instead of keeping its own", async () => {
    render(ConversationView, { id: SID });
    await screen.findByRole('button', { name: 'Team budget spreadsheet' });
    await fireEvent.click(screen.getAllByRole('button', { name: 'Chat options' })[0]);
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete chat…' }));
    expect(chats.deleting?.id).toBe(SID);
    expect(screen.queryByText(/^Delete “/)).toBeNull();
    chats.deleting = null;
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
    // Daily limits reset on the workspace's clock (UTC in the mock), not the person's own (Europe/London).
    expect(await screen.findByText(/Bothy has reached today's .*spending limit\. It can start again at midnight \(UTC\)\./)).toBeTruthy();
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
    // Right under its own step line, the decided line doesn't name the action again.
    expect(await screen.findByText(text(/^You approved it · /))).toBeTruthy();
    expect(screen.getByText(text('Changed acme/web · src/auth/callback.ts (+3\u00a0−1)'))).toBeTruthy();
    expect(screen.getByText(text(/^You denied opening a pull request in acme\/web · “Not before the freeze ends” · /))).toBeTruthy();
    // The denial's decided line says it; no second "Didn't …" line repeats it.
    expect(screen.queryByText(text("Didn't open a pull request in acme/web"))).toBeNull();
    await fireEvent.click(screen.getAllByRole('button', { name: 'Show' })[0]);
    const card = await screen.findByRole('article');
    // The line above says the decision, so the read-only card doesn't say it again.
    expect(card.querySelector('[data-decided]')).toBeNull();
  });

  it('folds finished steps into a summary line that opens on request', async () => {
    render(ConversationView, { id: chatIds.login });
    const summary = await screen.findByRole('button', { name: 'Got a copy of acme/web and read 3 files in src/auth' });
    expect(summary.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByText(text('Read acme/web · src/auth/session.ts'))).toBeNull();
    await fireEvent.click(summary);
    expect(await screen.findByText(text('Read acme/web · src/auth/session.ts'))).toBeTruthy();
    expect(screen.getByText(text('Ran npm test'))).toBeTruthy();
  });

  it("says a step that didn't work once, on its own line, and folds only the steps that worked", async () => {
    render(ConversationView, { id: chatIds.pricing });
    expect(await screen.findByRole('button', { name: 'Searched the web and read rival-one.example' })).toBeTruthy();
    expect(screen.getByText(text("Couldn't open status.rival-two.example"))).toBeTruthy();
    expect(screen.queryByText(/didn't work/)).toBeNull();
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

  it('marks a refined reply with the drafts it was chosen from', async () => {
    render(ConversationView, { id: chatIds.pricing });
    expect(await screen.findByText('Refined from 3 drafts')).toBeTruthy();
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
    // The notice names the command and the fix, so the step's own failed line isn't shown too.
    expect(screen.queryByText(text("Couldn't run python3 build_summary.py"))).toBeNull();
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
    const how = await screen.findByText('Allowed by a saved permission');
    const asked = screen.getByText('What Bothy asked for');
    // Plain parts first, then the exact request; the decision word isn't repeated in Technical.
    expect(how.compareDocumentPosition(asked) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(asked.closest('[data-details]')!.textContent).not.toMatch(/Decision /);
  });
});

describe('where a chat came from (§7.1)', () => {
  const budget = fixtureSessions.find((s) => s.id === SID)!;
  const userMsg = (text: string, sender: string): Message => ({ id: 1, session_id: SID, seq: 1, role: 'user', kind: 'normal', provenance: 'user', sender, tokens: 1, created_at: budget.created_at, parts: [{ t: 'text', text }] });
  const reply = (text: string): Message => ({ id: 2, session_id: SID, seq: 2, run_id: 'run_1', role: 'assistant', kind: 'normal', provenance: 'system', tokens: 1, created_at: budget.created_at, parts: [{ t: 'text', text }] });

  function serve(from: SessionFrom | undefined, messages: Message[] = []) {
    return installApi({
      override: (m, url) => {
        if (m !== 'GET') return undefined;
        if (url === `/api/sessions/${SID}`) return { session: budget, pins: [], pending_approvals: [], refine: '', from };
        if (url.startsWith(`/api/sessions/${SID}/messages`)) return { messages, has_more: false };
        return undefined;
      },
    });
  }

  beforeEach(async () => {
    installApi();
    await session.init();
  });

  it.each([
    [{ kind: 'chat', id: 'ses_parent', title: 'Q3 pricing notes' }, 'Continued from “Q3 pricing notes”', '/chat/ses_parent'],
    [{ kind: 'chat' }, 'Continued from a deleted chat', null],
    [{ kind: 'schedule', id: 'sch_brief', title: 'Morning briefing' }, 'From the schedule “Morning briefing”', '/activity/schedules/sch_brief'],
    [{ kind: 'checkin', id: 'sch_checkin', title: 'Check-in' }, 'From your check-in', '/activity/schedules/checkin'],
    [{ kind: 'schedule' }, 'From a deleted schedule', null],
  ])('says it in the first line of the transcript: %j', async (from, words, href) => {
    serve(from as SessionFrom, [userMsg('Hello', 'web:usr_1')]);
    render(ConversationView, { id: SID });
    const line = await screen.findByText((_, el) => el?.tagName === 'P' && el.textContent === words);
    expect(line.closest('section')?.firstElementChild).toBe(line);
    const link = line.querySelector('a');
    expect(link?.getAttribute('href') ?? null).toBe(href);
  });

  it("names a related chat's parent in the header on wide screens, and keeps the line for phones", async () => {
    serve({ kind: 'chat', id: 'ses_parent', title: 'Q3 pricing notes' }, [userMsg('Hello', 'web:usr_1')]);
    render(ConversationView, { id: SID });
    await screen.findByText('Hello');
    const crumb = await screen.findByRole('link', { name: 'Continued from Q3 pricing notes' });
    expect(crumb.closest('header')).toBeTruthy();
    expect(crumb.getAttribute('href')).toBe('/chat/ses_parent');
    const line = screen.getByText((_, el) => el?.tagName === 'P' && el.textContent === 'Continued from “Q3 pricing notes”');
    expect(line.classList.contains('lg:hidden')).toBe(true);
  });

  it('keeps the other origins in the transcript only', async () => {
    serve({ kind: 'schedule', id: 'sch_brief', title: 'Morning briefing' }, [userMsg('Hello', 'web:usr_1')]);
    render(ConversationView, { id: SID });
    const line = await screen.findByText((_, el) => el?.tagName === 'P' && el.textContent === 'From the schedule “Morning briefing”');
    expect(line.classList.contains('lg:hidden')).toBe(false);
    expect(document.querySelector('header a[href^="/activity/schedules"]')).toBeNull();
  });

  it("puts Bothy's picture at the start of each of its turns, and nowhere else", async () => {
    const turn2 = { ...userMsg('And the other one?', 'web:usr_1'), id: 3, seq: 3 };
    const reply2 = { ...reply('That one too.'), id: 4, seq: 4, run_id: 'run_2' };
    serve(undefined, [userMsg('Hello', 'web:usr_1'), reply('Hi there.'), turn2, reply2]);
    render(ConversationView, { id: SID });
    await screen.findByText('That one too.');
    const pictures = [...document.querySelectorAll<HTMLImageElement>('section img[src="/api/agent/picture"]')];
    expect(pictures).toHaveLength(2);
    expect(pictures.every((p) => p.alt === '')).toBe(true);
    expect(pictures.map((p) => p.parentElement!.textContent?.trim())).toEqual([expect.stringContaining('Hi there.'), expect.stringContaining('That one too.')]);
  });

  it('says nothing for a chat that came from nowhere', async () => {
    serve(undefined, [userMsg('Hello', 'web:usr_1')]);
    render(ConversationView, { id: SID });
    await screen.findByText('Hello');
    expect(screen.queryByText(/^(Continued from|From )/)).toBeNull();
  });

  it("captions what a schedule sent and says a check-in's all-clear in plain words", async () => {
    serve({ kind: 'schedule', id: 'sch_brief', title: 'Morning briefing' }, [userMsg('Summarise the overnight alerts.', 'schedule:sch_brief'), reply('No alerts overnight.')]);
    const view = render(ConversationView, { id: SID });
    expect(await screen.findByText('Sent by the schedule')).toBeTruthy();
    view.unmount();

    serve({ kind: 'checkin', id: 'sch_checkin', title: 'Check-in' }, [userMsg('- Tell me if a deploy failed.', 'schedule:sch_checkin'), reply('HEARTBEAT_OK')]);
    render(ConversationView, { id: SID });
    expect(await screen.findByText('Your check-in list')).toBeTruthy();
    expect(screen.getByText('Nothing needs your attention right now.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('HEARTBEAT');
  });

  it("doesn't caption what the person typed", async () => {
    serve(undefined, [userMsg('Summarise the overnight alerts.', 'web:usr_1')]);
    render(ConversationView, { id: SID });
    await screen.findByText('Summarise the overnight alerts.');
    expect(screen.queryByText('Sent by the schedule')).toBeNull();
  });

  it('shows the welcome message as Bothy speaking, with the starters under it until the person writes', async () => {
    const welcome: Message = { id: 1, session_id: SID, seq: 1, role: 'assistant', kind: 'notice', provenance: 'system', sender: 'bothy:welcome', tokens: 0, created_at: budget.created_at, parts: [{ t: 'text', text: "Hi Sam, I'm Bothy.\n\n- **Approvals** shows what's waiting for a decision." }] };
    serve(undefined, [welcome]);
    render(ConversationView, { id: SID });
    // Markdown, as a reply reads, not a muted notice line.
    expect((await screen.findByText('Approvals')).tagName).toBe('STRONG');
    const ask = await screen.findByRole('button', { name: 'What can you do for our team?' });
    expect(screen.queryByRole('heading', { name: 'What should Bothy work on?' })).toBeNull();
    await fireEvent.click(ask);
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('What can you do for our team?');
    emit('message.created', { message: { ...welcome, id: 2, seq: 2, role: 'user', kind: 'normal', provenance: 'user', sender: 'web:usr_1', parts: [{ t: 'text', text: 'What can you do for our team?' }] } }, '');
    await waitFor(() => expect(screen.queryByRole('button', { name: 'What can you do for our team?' })).toBeNull());
  });
});

describe('new chat', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    await session.init();
  });

  it('fills the composer from a starter without sending, and stores nothing before the first message', async () => {
    resetMock({ mock: 'knowledge-empty,github-none' });
    render(ConversationView, { id: '' });
    expect(await screen.findByRole('heading', { level: 1, name: 'What should Bothy work on?' })).toBeTruthy();
    // A row can carry longer words for the composer than it shows.
    await fireEvent.click(await screen.findByRole('button', { name: 'Turn meeting notes into a list of tasks' }));
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('Turn these meeting notes into a list of tasks, with an owner for each:\n\n');
    await fireEvent.click(screen.getByRole('button', { name: 'What can you do for our team?' }));
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('What can you do for our team?');
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    // An admin without GitHub is offered to connect it.
    expect(screen.getByRole('link', { name: 'Connect GitHub so Bothy can read your code' }).getAttribute('href')).toBe('/settings/connections/github');

    await fireEvent.keyDown(screen.getByLabelText('Message'), { key: 'Enter' });
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && /\/api\/sessions\/[^/]+\/messages$/.test(c.url))).toBe(true));
    const posts = calls.filter((c) => c.method === 'POST').map((c) => c.url);
    expect(posts[0]).toBe('/api/sessions');
    expect(router.path).toMatch(/^\/chat\/ses_/);
  });

  it('keeps the first message on screen as the reply starts, without reloading the new chat', async () => {
    render(ConversationView, { id: '' });
    const box = await screen.findByLabelText('Message');
    await fireEvent.input(box, { target: { value: 'Draft the launch post' } });
    await fireEvent.keyDown(box, { key: 'Enter' });
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && /\/api\/sessions\/[^/]+\/messages$/.test(c.url))).toBe(true));
    const sid = decodeURIComponent(router.path.split('/').pop()!);
    const at = new Date().toISOString();
    emit('message.created', { message: { id: 7001, session_id: sid, seq: 1, role: 'user', kind: 'normal', provenance: 'user', tokens: 3, created_at: at, parts: [{ t: 'text', text: 'Draft the launch post' }] } }, '', sid);
    emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' }, 'run_new', sid);
    expect(await screen.findByRole('heading', { level: 1, name: 'Draft the launch post' })).toBeTruthy();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getAllByText('Draft the launch post').length).toBeGreaterThan(1);
    // A reload's snapshot could predate the stored message and wipe it from the screen.
    expect(calls.some((c) => c.method === 'GET' && c.url.startsWith(`/api/sessions/${sid}`))).toBe(false);
  });

  it('stays the new chat when a file is attached before the first message', async () => {
    render(ConversationView, { id: '' });
    await screen.findByRole('heading', { level: 1, name: 'What should Bothy work on?' });
    const input = document.querySelector<HTMLInputElement>('input[type=file]')!;
    await fireEvent.change(input, { target: { files: [new File(['q3'], 'report-q3.pdf', { type: 'application/pdf' })] } });
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/uploads')).toBe(true));
    expect(router.path).toMatch(/^\/chat\/ses_/);
    expect(screen.getByRole('heading', { level: 1, name: 'What should Bothy work on?' })).toBeTruthy();
    expect(screen.queryByRole('heading', { level: 1, name: 'Chat' })).toBeNull();
    expect(await screen.findByRole('button', { name: 'What can you do for our team?' })).toBeTruthy();
  });

  it('puts a ?prompt= from Knowledge in the composer without sending it', async () => {
    router.navigate('/chat?prompt=About%20acme%2Fweb%3A%20');
    render(ConversationView, { id: '' });
    await waitFor(() => expect((screen.getByLabelText('Message') as HTMLTextAreaElement).value).toBe('About acme/web: '));
    expect(calls.some((c) => c.method === 'POST' && c.url.startsWith('/api/sessions'))).toBe(false);
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
