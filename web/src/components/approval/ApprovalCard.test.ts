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
    expect(screen.getByRole('button', { name: 'Always allow running rm -rf build ⟨U+202E⟩/ txt.exe…' })).toBeTruthy();

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
    expect(card.textContent).toContain('One more from Sam, Priya Rao or any admin');
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

  it('gives the requester no decision buttons, only Cancel request, and counts the approvals so far', async () => {
    as(owner);
    resetMock({ mock: 'deploy-requester' });
    render(ApprovalCard, { approval: deploy({ principal_id: owner.id, approver: { ...deploy().approver, requester: owner.id } }) });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('You asked for this. 1 of 2 approvals so far; one more person needs to approve it.'));
    expect(buttons(screen.getByRole('article'))).toHaveLength(0);
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel request' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1));
    expect(JSON.parse(calls.find((c) => c.method === 'POST')!.body)).toEqual({ choice: 'deny' });
  });

  it('tells the requester how many people must approve before anyone has', async () => {
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
    expect(screen.getByRole('status').textContent).toContain('You asked for this, so 2 other people need to approve it.');
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

  it('tells someone who already approved that it waits for one more', async () => {
    as(owner);
    resetMock({ mock: 'deploy-voted' });
    render(ApprovalCard, { approval: deploy() });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('You approved. Waiting for one more.'));
  });

  it('names who can approve to someone who is not one of them', async () => {
    as(owner);
    resetMock({ mock: 'deploy-ineligible' });
    render(ApprovalCard, { approval: deploy({ approver: { user_id: '', admin_only: false, users: ['usr_01jmaya000000000000000000'], roles: [], required: 2, requester: member.id } as Approval['approver'] }) });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Waiting for Priya Rao.'));
  });
});
