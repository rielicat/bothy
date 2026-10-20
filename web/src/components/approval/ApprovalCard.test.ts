import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { member, owner } from '../../../mock/fixtures';
import { resetMock } from '../../../mock/server';
import { setCsrf } from '../../lib/api/client';
import type { Approval } from '../../lib/api/types.gen';
import { router } from '../../lib/router.svelte';
import { approvals, session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import ApprovalCard from './ApprovalCard.svelte';
import { votes } from './votes.svelte';

function approval(extra: Partial<Approval> = {}): Approval {
  return {
    id: 'apr_01jshell00000000000000000',
    run_id: 'run_1',
    session_id: 'ses_1',
    principal_id: owner.id,
    tool_call_id: 'tcl_1',
    // The card reads the Action's known fields; prose in other payload fields never reaches the card face.
    action: { tool: 'shell', kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', 'rm -rf build ‮/ txt.exe'], payload: { note: 'MODEL PROSE: totally safe, click allow' }, fingerprint: 'f' },
    decision: { verdict: 'ask', rule: 'default:local_write', reason: 'Changes files in your workspace', floor: false, approver: { user_id: owner.id, admin_only: false }, timeout: 0, on_timeout: 'deny', choices: ['allow_once', 'deny'] },
    card: {
      title: 'Run shell command',
      risk: 'local_write',
      fields: [{ label: 'Tool', value: 'shell', mono: true }],
      warnings: ['First contact with https://аpple.example.'],
      context: 'Telegram · DM · session “Q3 report”',
      choices: ['allow_once', 'allow_session', 'allow_always', 'deny'],
      expires: '2099-01-01T00:00:00Z',
      web_url: 'https://bothy.example/approvals/apr_1',
    },
    status: 'pending',
    approver: { user_id: owner.id, admin_only: false },
    on_timeout: 'deny',
    created_at: '2026-10-12T10:00:00Z',
    expires_at: '2099-01-01T00:00:00Z',
    ...extra,
  };
}

const del = (extra: Partial<Approval> = {}) =>
  approval({
    action: { tool: 'fs_delete', kind: 'fs.delete', side_effect: 'irreversible', paths: ['notes/staging.md'], payload: { path: 'notes/staging.md' }, diff: '--- a/notes/staging.md\n+++ /dev/null\n@@ -1 +0,0 @@\n-# Staging\n', fingerprint: 'f' },
    card: { ...approval().card, risk: 'irreversible', choices: ['allow_once', 'allow_session', 'deny'], warnings: [] },
    ...extra,
  });

const deploy = (extra: Partial<Approval> = {}) =>
  approval({
    id: 'apr_01jdeployweb0000000000000',
    principal_id: member.id,
    action: { tool: 'release_deploy', kind: 'release', side_effect: 'irreversible', payload: { environment: 'production', repo: 'acme/web', ref: 'main', inputs: {} }, fingerprint: 'f' },
    card: { ...approval().card, risk: 'irreversible', choices: ['allow_once', 'deny'], warnings: [], context: 'Slack · group · session “Release train”' },
    approver: { user_id: '', admin_only: false, users: [owner.id], roles: ['admin'], required: 2, requester: member.id } as Approval['approver'],
    ...extra,
  });

const buttons = (root: HTMLElement) => [...root.querySelectorAll<HTMLButtonElement>('button[data-choice]')];
const describedBy = (el: HTMLElement) => document.getElementById(el.getAttribute('aria-describedby') ?? '')?.textContent?.trim();

const refused = (status: number, code: string, message: string) =>
  new Response(JSON.stringify({ error: { code, message } }), { status, headers: { 'Content-Type': 'application/json' } });

describe('ApprovalCard', () => {
  let calls: Call[];

  beforeEach(() => {
    calls = installApi();
    setCsrf('mock-csrf');
    session.me = { user: owner, csrf: 'mock-csrf', mode: 'multi', principals: [], paused: false, sandbox_available: true, banners: [], access: { peer: 'local', tls: false } };
    approvals.reset();
    votes.reset();
  });

  const posts = () => calls.filter((c) => c.method === 'POST');

  it('leaves "From" off a card inside its own chat, and keeps it anywhere else', () => {
    router.navigate('/chat/ses_1');
    const own = render(ApprovalCard, { approval: approval() });
    expect(screen.getByRole('article').textContent).not.toContain('From');
    own.unmount();
    router.navigate('/knowledge/projects/acme/web');
    render(ApprovalCard, { approval: approval() });
    expect(screen.getByRole('article').textContent).toContain('From “Q3 report”');
  });

  it('asks a plain question from the Action and never shows other payload prose on its face', () => {
    const { container } = render(ApprovalCard, { approval: approval() });
    const card = screen.getByRole('article');
    expect(within(card).getByRole('heading', { level: 2 }).textContent).toBe('Run this command?');
    expect(card.textContent).toContain('You asked on Telegram.');
    expect(card.textContent).toContain('From “Q3 report”');
    expect(card.textContent).not.toContain('MODEL PROSE');
    expect(card.textContent).not.toContain('session');
    expect(card.textContent).not.toContain('bothy.example/approvals');
    expect(container.querySelector('img')).toBeNull();
  });

  it('carries one status signal: a type icon, no status word, no warning border, no tinted field box', () => {
    render(ApprovalCard, { approval: approval() });
    const card = screen.getByRole('article');
    expect(card.querySelector(':scope > svg use')?.getAttribute('href')).toBe('#i-terminal');
    expect(card.textContent).not.toMatch(/Waiting for you/);
    expect(card.className).not.toMatch(/border-warning|border-accent/);
    const fields = card.querySelector('dl');
    expect(fields?.closest('.bg-subtle')).toBeNull();
  });

  it('says a schedule started a request instead of saying you asked', () => {
    const a = approval();
    render(ApprovalCard, { approval: { ...a, card: { ...a.card, context: 'Telegram · DM · schedule · session “Morning briefing”' } } });
    const card = screen.getByRole('article');
    expect(card.textContent).toContain('Started by a schedule on Telegram.');
    expect(card.textContent).not.toContain('You asked');
  });

  it('names a grant that pins the whole request by what it does', async () => {
    const a = approval();
    render(ApprovalCard, {
      approval: {
        ...a,
        action: { tool: 'memory', kind: 'memory', side_effect: 'local_write', payload: { op: 'add', new: 'Releases go out on Tuesdays.' }, fingerprint: 'f' },
      },
    });
    expect(screen.getByRole('button', { name: 'Always allow remembering this exact note…' })).toBeTruthy();
  });

  it('asks an admin to remember a note for everyone, and tells the member who asked that it waits for one', async () => {
    const team = approval({
      principal_id: member.id,
      action: { tool: 'memory', kind: 'memory', side_effect: 'admin', payload: { file: 'TEAM', op: 'add', new: 'Our deploy day is Thursday.' }, fingerprint: 'f' },
      card: { ...approval().card, risk: 'admin', choices: ['allow_once', 'deny'], warnings: ['This chat has seen content from outside your team. Check that a person asked for this.'], context: 'Web · session “Deploy day”' },
      approver: { user_id: '', admin_only: true },
    });
    const a = render(ApprovalCard, { approval: team });
    const card = screen.getByRole('article');
    expect(within(card).getByRole('heading', { level: 2 }).textContent).toBe('Remember this for everyone?');
    expect(card.textContent).toContain('Bothy will use this in every chat, for everyone.');
    expect(card.textContent).toContain('Our deploy day is Thursday.');
    expect(card.textContent).toContain('Check that a person asked for this.');
    expect(buttons(card).map((b) => b.textContent?.trim())).toEqual(['Remember for everyone', 'Deny']);
    expect(screen.queryByRole('button', { name: /Always allow/ })).toBeNull();
    a.unmount();
    session.me = { ...session.me!, user: member };
    render(ApprovalCard, { approval: team });
    expect(buttons(screen.getByRole('article'))).toHaveLength(0);
    expect(screen.getByRole('status').textContent).toContain('Waiting for an admin.');
  });

  it("says a parked request waits in the card's body, not squeezed into the meta line", () => {
    render(ApprovalCard, { approval: approval({ status: 'parked', card: { ...approval().card, context: 'Telegram · DM · schedule · session “Morning briefing”' } }) });
    const card = screen.getByRole('article');
    const line = screen.getByText('Bothy stopped waiting. Approve to let it carry on.');
    expect(line.className).toMatch(/text-body/);
    const meta = card.querySelector('[data-details]')!.previousElementSibling!;
    expect(meta.textContent?.replace(/\s+/g, ' ').trim()).toBe('From “Morning briefing” · Details');
  });

  it("gives a schedule's request no 2-minute deadline while it waits, and its real one once parked", () => {
    const sched = { ...approval().card, context: 'Web · schedule · session “Digest”', expires: new Date(Date.now() + 90_000).toISOString() };
    const { unmount } = render(ApprovalCard, { approval: approval({ on_timeout: 'park', card: sched }) });
    expect(document.body.textContent).not.toMatch(/Expires in/);
    unmount();
    render(ApprovalCard, { approval: approval({ status: 'parked', on_timeout: 'park', card: sched, park_until: new Date(Date.now() + 23.2 * 3_600_000).toISOString() }) });
    expect(screen.getByText('Bothy stopped waiting. Approve within 23 h to let it carry on.')).toBeTruthy();
  });

  it("names the schedule, not the chat, on a scheduled run's grant button", () => {
    render(ApprovalCard, { approval: approval({ card: { ...approval().card, context: 'Web · schedule · session “Digest · 2026-10-19”' } }) });
    expect(buttons(screen.getByRole('article')).map((b) => b.textContent?.trim())).toEqual(['Run command', 'Allow for this schedule', 'Deny']);
  });

  it('shows bidi and zero-width characters instead of applying them', () => {
    const { container } = render(ApprovalCard, { approval: approval() });
    expect(container.textContent).toContain('⟨U+202E⟩');
    expect(container.textContent).not.toContain('‮');
  });

  it('offers exactly the card choices with the verb first, and sends the decision', async () => {
    render(ApprovalCard, { approval: approval() });
    const card = screen.getByRole('article');
    expect(buttons(card).map((b) => b.textContent?.trim())).toEqual(['Run command', 'Allow for this chat', 'Deny']);
    for (const b of buttons(card)) expect(b.getAttribute('type')).toBe('button');
    // The link says "this command"; the dialog it opens names the grant in full.
    expect(screen.getByRole('button', { name: 'Always allow this command…' })).toBeTruthy();

    await fireEvent.click(buttons(card)[0]);
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe('/api/approvals/apr_01jshell00000000000000000');
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'allow_once' });
    expect(posts()[0].headers['x-csrf-token']).toBe('mock-csrf');
    // Inline cards collapse to their decided line, which takes focus.
    await waitFor(() => expect(screen.queryByRole('article')).toBeNull());
    expect(document.querySelector('[data-decided]')?.textContent).toContain('You approved running');
  });

  it('fills the primary button red only for actions that cannot be undone, and never Deny', () => {
    const a = render(ApprovalCard, { approval: del() });
    const [primary, session_, deny] = buttons(screen.getByRole('article'));
    expect(primary.textContent?.trim()).toBe('Delete file');
    expect(primary.className).toContain('btn-danger');
    expect(session_.className).toContain('btn-secondary');
    expect(deny.className).not.toContain('danger');
    expect(screen.getByRole('article').textContent).toContain("This can't be undone.");
    // Never "Always allow" for something that can't be undone.
    expect(screen.queryByRole('button', { name: /Always allow/ })).toBeNull();
    a.unmount();
    render(ApprovalCard, { approval: approval() });
    expect(buttons(screen.getByRole('article'))[0].className).toContain('btn-primary');
  });

  it('asks for an optional reason on Deny and sends it with the decision', async () => {
    render(ApprovalCard, { approval: approval() });
    await fireEvent.click(screen.getByRole('button', { name: 'Deny' }));
    const field = screen.getByLabelText(/Reason/);
    expect(document.activeElement).toBe(field);
    await fireEvent.input(field, { target: { value: 'Not on the build box' } });
    await fireEvent.submit(field.closest('form')!);
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'deny', comment: 'Not on the build box' });
  });

  it('cancels the reason field with Escape and puts focus back on Deny', async () => {
    render(ApprovalCard, { approval: approval() });
    await fireEvent.click(screen.getByRole('button', { name: 'Deny' }));
    await fireEvent.keyDown(screen.getByLabelText(/Reason/), { key: 'Escape' });
    await waitFor(() => expect(document.activeElement?.textContent?.trim()).toBe('Deny'));
    expect(posts()).toHaveLength(0);
  });

  it('says so when the decision could not be sent, and lets you try again', async () => {
    resetMock({ mock: 'approvals-fail' });
    approvals.pending = [approval()];
    render(ApprovalCard, { approval: approval() });
    await fireEvent.click(screen.getByRole('button', { name: 'Run command' }));
    expect((await screen.findByRole('alert')).textContent).toContain("Couldn't send your decision. Try again.");
    expect(screen.getByRole('button', { name: 'Run command' }).getAttribute('aria-disabled')).toBeNull();
  });

  it('says a request was already decided instead of asking to try again, and shows how it ended', async () => {
    const decided = approval({ status: 'allowed', choice: 'allow_once', decided_by: owner.id, decided_via: 'telegram', decided_at: '2026-10-12T10:05:00Z' });
    calls = installApi({
      override: (method, url) =>
        method === 'POST' ? refused(409, 'conflict', 'already decided') : method === 'GET' && url === `/api/approvals/${decided.id}` ? decided : undefined,
    });
    render(ApprovalCard, { approval: approval(), mode: 'inbox' });
    await fireEvent.click(screen.getByRole('button', { name: 'Run command' }));
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe('This request was already decided.');
    expect(buttons(screen.getByRole('article'))).toHaveLength(0);
  });

  it("gives the server's reason when it refuses the decision", async () => {
    calls = installApi({ override: (method) => (method === 'POST' ? refused(403, 'forbidden', 'you asked for this, so someone else has to approve it') : undefined) });
    render(ApprovalCard, { approval: approval() });
    await fireEvent.click(screen.getByRole('button', { name: 'Run command' }));
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe('You asked for this, so someone else has to approve it.');
    expect(calls.some((c) => c.method === 'GET' && c.url === '/api/approvals/apr_01jshell00000000000000000')).toBe(true);
  });

  it('confirms "Always allow" in a dialog before creating a standing permission', async () => {
    render(ApprovalCard, { approval: approval() });
    await fireEvent.click(screen.getByRole('button', { name: /Always allow/ }));
    const dialog = await screen.findByRole('dialog');
    // The grant is Bothy's wording, not something the person typed, so no curly quotes (§14.2); the command is in mono.
    expect(dialog.textContent).toContain("Bothy won't ask again before running rm -rf build ⟨U+202E⟩/ txt.exe, in any chat.");
    expect(dialog.querySelector('.font-mono')?.textContent).toContain('rm -rf build');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Always allow' }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'allow_always' });
  });

  it('shows no buttons to someone who cannot decide, and fails closed without an approver', () => {
    const a = render(ApprovalCard, { approval: approval({ approver: { user_id: 'usr_other', admin_only: false } }) });
    expect(buttons(screen.getByRole('article'))).toHaveLength(0);
    expect(screen.getByRole('status').textContent).toContain('Waiting for someone else.');
    a.unmount();
    const b = render(ApprovalCard, { approval: approval({ approver: { user_id: '', admin_only: false } }) });
    expect(buttons(b.container)).toHaveLength(0);
    b.unmount();
    render(ApprovalCard, { approval: approval({ card: { ...approval().card, choices: [] } }) });
    expect(buttons(screen.getByRole('article'))).toHaveLength(0);
  });

  it('shows the decided line, then the read-only card on Show', async () => {
    render(ApprovalCard, { approval: del({ status: 'denied', choice: 'deny', decided_by: member.id, decided_via: 'telegram', decided_at: new Date().toISOString() }), mode: 'decided' });
    expect(document.querySelector('[data-decided]')?.textContent).toContain('Alex denied in Telegram');
    await fireEvent.click(screen.getByRole('button', { name: 'Show' }));
    const card = screen.getByRole('article');
    expect(buttons(card)).toHaveLength(0);
    expect(card.textContent).toContain('Delete notes/staging.md?');
  });

  it('collapses Bothy’s explanation and the exact action behind Details', async () => {
    render(ApprovalCard, { approval: approval(), reason: 'I need this to build the site.' });
    expect(screen.queryByText(/Exact action/)).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Details' }));
    const panel = document.querySelector('[data-details]')!;
    expect(panel.textContent).toContain('Why Bothy asked: Changes files in your workspace');
    expect(panel.textContent).toContain('If nobody answers: it will be denied');
    expect(panel.textContent).toContain('Exact action');
    expect(panel.textContent).toContain('MODEL PROSE');
    const explanation = within(panel as HTMLElement).getByRole('button', { name: "Bothy's explanation (written by the AI, not checked)" });
    expect(explanation.getAttribute('aria-expanded')).toBe('false');
  });

  it('counts down to the approval expiry when the card carries Go zero time', () => {
    const a = approval({ card: { ...approval().card, expires: '0001-01-01T00:00:00Z' }, expires_at: new Date(Date.now() + 5 * 60_000 + 5_000).toISOString() });
    render(ApprovalCard, { approval: a });
    expect(screen.getByRole('article').textContent).toMatch(/Expires in [45] min/);
  });

  it('decides with A, S and D only while the card has focus in a chat', async () => {
    render(ApprovalCard, { approval: approval() });
    const card = screen.getByRole('article');
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(0);
    card.focus();
    await fireEvent.keyDown(card, { key: 's' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'allow_session' });
  });
});

// A teammate's or a channel's request: an admin here, or a chat approver in
// their direct messages, decides it (docs/design/ui.md §8.4).
describe('ApprovalCard for people in chat apps', () => {
  let calls: Call[];
  const team = { user_id: '', admin_only: false, team: true, requester_identity: 'slack:U02ALEX' } as Approval['approver'];
  const person = (extra: Partial<Approval> = {}) =>
    approval({
      principal_id: 'spc_01jalexslack000000000000000',
      approver: team,
      chat: { channel: 'slack', chat_id: 'D02ALEX', kind: 'dm' },
      card: { ...approval().card, context: "Alex Kim's direct messages on Slack" },
      requester: { user: '', name: 'Alex Kim', app: 'slack', identity: 'slack:U02ALEX', handle: 'alex', kind: 'teammate' },
      ...extra,
    });
  const channel = (extra: Partial<Approval> = {}) =>
    person({
      principal_id: 'spc_01jeng00000000000000000000',
      chat: { channel: 'slack', chat_id: 'C01ENG', kind: 'group' },
      card: { ...approval().card, context: '#eng on Slack', choices: ['allow_once', 'deny'] },
      requester: { user: '', name: 'Alex Kim', app: 'slack', chat: '#eng', identity: 'slack:U02ALEX', handle: 'alex', kind: 'channel' },
      ...extra,
    });

  beforeEach(() => {
    calls = installApi();
    setCsrf('mock-csrf');
    session.me = { user: owner, csrf: 'mock-csrf', mode: 'multi', principals: [], paused: false, sandbox_available: true, banners: [], access: { peer: 'local', tls: false } };
    approvals.reset();
    votes.reset();
  });

  it('names who asked and where, and never links to a chat admins cannot open', () => {
    router.navigate('/approvals');
    render(ApprovalCard, { approval: person() });
    const card = screen.getByRole('article');
    expect(card.textContent).toContain('Alex Kim (@alex) asked on Slack.');
    // The asked line says who and where, so the meta line leaves the context out.
    expect(card.textContent).not.toContain("Alex Kim's direct messages on Slack");
    expect(card.textContent).not.toContain('You asked');
    expect(card.querySelector('a[href^="/chat/"]')).toBeNull();
  });

  it("lets an admin decide, with the person's name on the standing choices", async () => {
    render(ApprovalCard, { approval: person() });
    const card = screen.getByRole('article');
    expect(buttons(card).map((b) => b.textContent?.trim())).toEqual(['Run command', "Allow in Alex Kim's chat", 'Deny']);
    await fireEvent.click(within(card).getByRole('button', { name: 'Always allow for Alex Kim…' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog.textContent).toContain("Bothy won't ask again before running rm -rf build ⟨U+202E⟩/ txt.exe for Alex Kim on Slack.");
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Always allow' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1));
  });

  it("names a channel's request by the channel and says the deny reason stays out of it", async () => {
    render(ApprovalCard, { approval: channel() });
    const card = screen.getByRole('article');
    expect(card.textContent).toContain('Alex Kim asked in #eng on Slack.');
    expect(card.textContent).toContain('#eng on Slack');
    expect(buttons(card).map((b) => b.textContent?.trim())).toEqual(['Run command', 'Deny']);
    expect(within(card).queryByRole('button', { name: /Always allow/ })).toBeNull();
    await fireEvent.click(within(card).getByRole('button', { name: 'Deny' }));
    expect(describedBy(await within(card).findByLabelText(/Reason/))).toBe("People in #eng won't see this, but Bothy will.");
  });

  // Help text, not a placeholder: it stays while the reason is typed.
  it('tells the person their reason reaches them in their own chat', async () => {
    render(ApprovalCard, { approval: person() });
    await fireEvent.click(within(screen.getByRole('article')).getByRole('button', { name: 'Deny' }));
    const reason = await screen.findByLabelText(/Reason/);
    expect(reason.getAttribute('placeholder')).toBeNull();
    await fireEvent.input(reason, { target: { value: 'Not this week' } });
    expect(describedBy(reason)).toBe('Alex Kim will see this.');
  });

  it("says a schedule started a channel's request, and offers it no standing choices", () => {
    const sched = channel({ requester: { user: '', name: '#eng', app: 'slack', chat: '#eng', kind: 'channel', scheduled: true } });
    render(ApprovalCard, { approval: sched });
    const card = screen.getByRole('article');
    expect(card.textContent).toContain('From #eng on Slack.');
    expect(card.textContent).not.toContain('asked');
    expect(buttons(card).map((b) => b.textContent?.trim())).toEqual(['Run command', 'Deny']);
  });

  it('shows members no buttons and says who can decide', async () => {
    session.me = { ...session.me!, user: member };
    render(ApprovalCard, { approval: person() });
    const card = screen.getByRole('article');
    expect(buttons(card)).toHaveLength(0);
    expect(within(card).getByRole('status').textContent).toContain('Waiting for an approver.');
    await fireEvent.click(within(card).getByRole('button', { name: 'Details' }));
    expect(card.textContent).toContain('Who can approve: an admin, or someone who can approve in a chat app');
  });

  it('names the chat approver who decided, and the person who cancelled', () => {
    const at = new Date().toISOString();
    const a = render(ApprovalCard, {
      approval: person({ status: 'allowed', choice: 'allow_once', decided_by: '', decided_by_identity: 'slack:U01SAM', decided_by_name: 'Sam Patel · Slack', decided_via: 'slack', decided_at: at }),
      mode: 'decided',
    });
    expect(document.querySelector('[data-decided]')?.textContent).toMatch(/^\s*Sam Patel · Slack approved ·/);
    a.unmount();
    render(ApprovalCard, {
      approval: person({ status: 'denied', choice: 'deny', decided_by: '', decided_by_identity: 'slack:U02ALEX', decided_by_name: 'Alex Kim · Slack', decided_via: 'slack', decided_at: at }),
      mode: 'decided',
    });
    expect(document.querySelector('[data-decided]')?.textContent).toMatch(/^\s*Cancelled by Alex Kim ·/);
  });
});

describe('ApprovalCard with several approvers', () => {
  let calls: Call[];

  beforeEach(() => {
    calls = installApi();
    setCsrf('mock-csrf');
    approvals.reset();
    votes.reset();
  });

  const as = (user: typeof owner) => (session.me = { user, csrf: 'mock-csrf', mode: 'multi', principals: [], paused: false, sandbox_available: true, banners: [], access: { peer: 'local', tls: false } });

  it('shows "1 of 2", each vote and who can still approve, with Approve deploy and Deny', async () => {
    as(owner);
    render(ApprovalCard, { approval: deploy(), mode: 'inbox' });
    const card = screen.getByRole('article');
    await waitFor(() => expect(card.textContent).toContain('1 of 2'));
    expect(card.textContent).toContain('Maya Chen approved');
    // The viewer is "you", never named in the third person.
    expect(card.textContent).toContain('One more from you, Priya Rao or any admin');
    expect(card.textContent).toContain('This updates production for everyone using it.');
    expect(card.textContent).toContain('Alex asked in “Release train” on Slack.');
    expect(card.textContent).not.toContain('Requested by');
    expect(buttons(card).map((b) => b.textContent?.trim())).toEqual(['Approve deploy', 'Deny']);
    expect(buttons(card)[0].className).toContain('btn-primary');
    expect(screen.queryByRole('button', { name: /Allow for this chat|Always allow/ })).toBeNull();
  });

  it("names who asked when the other person's chat is private", async () => {
    as(owner);
    render(ApprovalCard, { approval: deploy({ card: { ...deploy().card, context: '' } }), mode: 'inbox' });
    const card = screen.getByRole('article');
    await waitFor(() => expect(card.textContent).toContain('1 of 2'));
    expect(card.textContent).toMatch(/Requested by \S/);
    expect(card.textContent).not.toContain('Requested by Someone in');
  });

  it('gives the requester no decision buttons, only Cancel request', async () => {
    as(owner);
    resetMock({ mock: 'deploy-requester' });
    render(ApprovalCard, { approval: deploy({ principal_id: owner.id, approver: { ...deploy().approver, requester: owner.id } }) });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain("You asked for this, so you can't approve it yourself."));
    expect(buttons(screen.getByRole('article'))).toHaveLength(0);
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel request' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1));
    expect(JSON.parse(calls.find((c) => c.method === 'POST')!.body)).toEqual({ choice: 'deny' });
  });

  it('leaves the count of missing approvals to the Approvals row', async () => {
    as(member);
    const id = deploy().id;
    installApi({
      override: (m, url) =>
        m === 'GET' && url === `/api/approvals/${id}/votes`
          ? { approval_id: id, status: 'pending', required: 2, approvals: 0, votes: [], approvers: 'Sam or any admin', requester: member.id, requester_name: 'Alex', can_vote: false, can_withdraw: true }
          : undefined,
    });
    render(ApprovalCard, { approval: deploy() });
    await waitFor(() => expect(votes.byId[id]?.approvals).toBe(0));
    expect(screen.getByRole('status').textContent).toContain("You asked for this, so you can't approve it yourself.");
    expect(screen.getByRole('article').textContent).toContain('2 more from Sam or any admin');
  });

  it('says you already voted when the server has your vote, and reloads the votes', async () => {
    as(owner);
    calls = installApi({ override: (method) => (method === 'POST' ? refused(409, 'conflict', 'you already voted') : undefined) });
    render(ApprovalCard, { approval: deploy(), mode: 'inbox' });
    await waitFor(() => expect(screen.getByRole('article').textContent).toContain('1 of 2'));
    const loads = () => calls.filter((c) => c.url.endsWith('/votes')).length;
    const before = loads();
    await fireEvent.click(screen.getByRole('button', { name: 'Approve deploy' }));
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe('You already voted.');
    expect(loads()).toBeGreaterThan(before);
  });

  it('tells someone who already approved, in one sentence, whom it waits for', async () => {
    as(owner);
    resetMock({ mock: 'deploy-voted' });
    render(ApprovalCard, { approval: deploy() });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('You approved. Waiting for Priya Rao, Maya Chen or any admin.'));
    const card = screen.getByRole('article');
    // Their own vote reads "You approved", and the row drops its "more from" line.
    await waitFor(() => expect(card.textContent).toMatch(/You approved · /));
    expect(card.textContent).not.toMatch(/more from/);
  });

  it('names who can approve to someone who is not one of them, once', async () => {
    as(owner);
    resetMock({ mock: 'deploy-ineligible' });
    render(ApprovalCard, { approval: deploy({ approver: { user_id: '', admin_only: false, users: ['usr_01jmaya000000000000000000'], roles: [], required: 2, requester: member.id } as Approval['approver'] }) });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Waiting for Priya Rao.'));
    expect(screen.getByRole('article').textContent).not.toMatch(/more from/);
  });
});
