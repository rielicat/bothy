import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { recentApprovals } from '../../mock/fixtures';
import { resetMock } from '../../mock/server';
import { votes } from '../components/approval/votes.svelte';
import { router } from '../lib/router.svelte';
import { approvals, session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import Approvals from './Approvals.svelte';

const DEPLOY = 'apr_01jdeployweb0000000000000';
const SHELL = 'apr_01jshell00000000000000000';
const PR = 'apr_01jpr00000000000000000000';

let calls: Call[];
const posts = () => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/approvals/'));
const live = () => document.querySelector('[aria-live="polite"]')?.textContent ?? '';
const expanded = () => document.querySelector<HTMLElement>('[data-row] article');
const row = (id: string) => document.querySelector<HTMLElement>(`[data-row="a:${id}"]`)!;

async function open(path = '/approvals', params: Record<string, string> = {}, mock = '') {
  calls = installApi();
  if (mock) resetMock({ mock });
  approvals.reset();
  votes.reset();
  await session.init();
  router.navigate(path);
  render(Approvals, { params });
}

describe('Approvals inbox', () => {
  beforeEach(() => open());

  it('lists the requests for you first, one expanded, and the ones waiting for others below', async () => {
    const forYou = await screen.findByRole('heading', { name: 'For you' });
    expect(screen.getByRole('heading', { name: 'Waiting for others' })).toBeTruthy();
    expect(expanded()?.dataset.approval).toBe(DEPLOY);
    expect(within(expanded()!).getByRole('heading', { level: 3 }).textContent).toBe('Deploy acme/web to production?');
    const collapsed = [...document.querySelectorAll<HTMLButtonElement>('[data-row] button[aria-expanded="false"]')].map((b) => b.querySelector('span')?.textContent);
    expect(collapsed).toContain('Delete notes/staging.md?');
    expect(collapsed).toContain('Review changes to Linear tools');
    expect(collapsed.at(-1)).toBe('Deploy acme/api to production?');
    expect(forYou).toBeTruthy();
    expect(screen.getByRole('link', { name: /Waiting/ }).getAttribute('aria-current')).toBe('page');
  });

  it('moves with J and K, opens with Enter and decides the open card with A', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.keyDown(window, { key: 'j' });
    expect(document.activeElement).toBe(row(SHELL).querySelector('button'));
    // The cursor is on a closed row: A must not decide the card that is open elsewhere.
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(0);
    await fireEvent.click(document.activeElement as HTMLButtonElement);
    await waitFor(() => expect(document.activeElement?.closest('article')?.dataset.approval).toBe(SHELL));
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${SHELL}`);
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'allow_once' });
    await waitFor(() => expect(live()).toContain('Approved. Next: Send a message to the Telegram chat?'));
    await waitFor(() => expect(document.activeElement?.tagName).toBe('ARTICLE'));
    expect(expanded()?.textContent).toContain('Send a message to the Telegram chat?');
  });

  it('decides one request per key press, however long the key is held', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${DEPLOY}`);
    await fireEvent.keyDown(window, { key: 'a', repeat: true });
    await fireEvent.keyDown(window, { key: 'a' });
    await new Promise((r) => setTimeout(r, 20));
    expect(posts()).toHaveLength(1);
    await fireEvent.keyUp(window, { key: 'a' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(2));
  });

  it('opens the reason field with D and denies with Enter', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.keyDown(window, { key: 'd' });
    const field = await screen.findByLabelText(/Reason/);
    expect(document.activeElement).toBe(field);
    expect(posts()).toHaveLength(0);
    await fireEvent.submit(field.closest('form')!);
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'deny' });
  });

  it('waits for the person to move before deciding a card that opened on its own', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    // The open request is decided from Slack; the next one opens in its place.
    approvals.upsert({ ...approvals.pending.find((a) => a.id === DEPLOY)!, status: 'allowed' });
    await waitFor(() => expect(expanded()?.dataset.approval).toBe(SHELL));
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(0);
    await fireEvent.keyDown(window, { key: 'k' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${SHELL}`);
  });

  it('offers no shortcut for "Always allow"', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.click(row(SHELL).querySelector('button')!);
    const always = await within(expanded()!).findByRole('button', { name: /Always allow/ });
    expect(always.hasAttribute('aria-keyshortcuts')).toBe(false);
    const primary = within(expanded()!).getByRole('button', { name: 'Run command' });
    expect(primary.getAttribute('aria-keyshortcuts')).toBe('A');
  });

  it('decides review items through their own endpoint', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    const mcp = document.querySelector<HTMLElement>('[data-row="i:itm_01jlinear0000000000000000"] button')!;
    await fireEvent.click(mcp);
    const card = await waitFor(() => document.querySelector<HTMLElement>('[data-item="itm_01jlinear0000000000000000"]')!);
    const selects = within(card).getAllByLabelText('What this does') as HTMLSelectElement[];
    await fireEvent.change(selects[0], { target: { value: 'read' } });
    await fireEvent.click(within(card).getByRole('button', { name: 'Approve tools' }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe('/api/approvals/items/itm_01jlinear0000000000000000');
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'approve', side_effects: { create_issue: 'read', archive_project: 'irreversible' } });
  });
});

describe('Approvals inbox, other states', () => {
  it('opens /approvals/:id with that request expanded, pull request changes shown', async () => {
    await open(`/approvals/${PR}`, { id: PR });
    await waitFor(() => expect(expanded()?.dataset.approval).toBe(PR));
    expect(expanded()!.textContent).toContain('Open a pull request in acme/web?');
    expect(within(expanded()!).getByRole('group', { name: 'Changes to src/auth/redirect.ts' })).toBeTruthy();
    expect(expanded()!.textContent).toContain('This is the first pull request Bothy opens in acme/web.');
  });

  it('says a request was already decided, with its decided line', async () => {
    const done = recentApprovals[0];
    await open(`/approvals/${done.id}`, { id: done.id });
    expect(await screen.findByText("This request was already decided or doesn't exist.")).toBeTruthy();
    await waitFor(() => expect(document.querySelector('[data-decided]')?.textContent).toContain('You approved deleting'));
  });

  it('says when nothing waits', async () => {
    await open('/approvals', {}, 'approvals-empty');
    expect(await screen.findByText('Nothing needs your approval.')).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'For you' })).toBeNull();
  });

  it('lists only action approvals before the server lists review items', async () => {
    await open('/approvals', {}, 'approvals-no-items');
    await screen.findByRole('heading', { name: 'For you' });
    expect(document.querySelector('[data-row^="i:"]')).toBeNull();
  });

  it('groups the Done tab by day, every line naming what was decided', async () => {
    await open('/approvals?tab=done');
    expect(await screen.findByRole('heading', { name: 'Today' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Yesterday' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Earlier' })).toBeTruthy();
    const lines = [...document.querySelectorAll('[data-decided]')].map((l) => l.textContent?.replace(/\s+/g, ' ').trim());
    expect(lines.some((l) => l?.startsWith('You approved deleting notes/old-draft.md'))).toBe(true);
    expect(lines.some((l) => l?.startsWith('You denied running rm -rf build/ · “Not while the build server is in use”'))).toBe(true);
    expect(lines.some((l) => l?.startsWith("Expired: Bothy didn't send data to api.example.com"))).toBe(true);
    expect(screen.getByRole('link', { name: 'Done' }).getAttribute('aria-current')).toBe('page');
  });
});
