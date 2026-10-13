import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { approvals as fixtureApprovals } from '../../mock/fixtures';
import { router } from '../lib/router.svelte';
import { approvals, session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import Approvals from './Approvals.svelte';

// SPEC §9.3 keyboard flow: j/k move between cards, a/s/d decide the selected
// one, and every action is also a native button reachable with Tab.
describe('Approvals inbox keyboard flow', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    // Start empty so the wait below sees this test's load, not the last test's list.
    approvals.reset();
    await session.init();
    // let the login-time fetch land before seeding the inbox
    await waitFor(() => expect(approvals.pending.length).toBe(fixtureApprovals.length));
    // The parked fixture shown as a second pending card; the fixture gateway accepts both.
    const second = { ...fixtureApprovals[1], status: 'pending', created_at: '2099-01-01T00:00:00Z' };
    approvals.pending = [fixtureApprovals[0], second];
    router.navigate('/approvals');
  });

  const posts = () => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/approvals/'));

  it('moves with j/k, decides with a/s/d and updates the badge', async () => {
    render(Approvals, { params: {} });
    const cards = await screen.findAllByRole('article');
    expect(cards).toHaveLength(2);
    expect(cards[0].querySelector('button[aria-keyshortcuts="a"]')).not.toBeNull();

    await fireEvent.keyDown(window, { key: 'j' });
    const second = screen.getAllByRole('article')[1];
    expect(document.activeElement?.closest('article')).toBe(second);
    expect(second.querySelector('button[aria-keyshortcuts="a"]')).not.toBeNull();

    // this card offers only allow once / deny, so "s" does nothing
    await fireEvent.keyDown(window, { key: 's' });
    expect(posts()).toHaveLength(0);
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${fixtureApprovals[1].id}`);
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'allow_once' });
    await waitFor(() => expect(approvals.count).toBe(1));

    await fireEvent.keyDown(window, { key: 'k' });
    await fireEvent.keyDown(window, { key: 's' });
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(JSON.parse(posts()[1].body)).toEqual({ choice: 'allow_session' });
    await waitFor(() => expect(approvals.count).toBe(0));
    expect(await screen.findByText('All clear')).toBeTruthy();
  });

  it('denies with d', async () => {
    render(Approvals, { params: {} });
    await screen.findAllByRole('article');
    await fireEvent.keyDown(window, { key: 'd' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'deny' });
    await waitFor(() => expect(approvals.count).toBe(1));
  });

  it('ignores shortcuts while typing and with modifier keys', async () => {
    render(Approvals, { params: {} });
    await screen.findAllByRole('article');
    const input = document.createElement('input');
    document.body.append(input);
    await fireEvent.keyDown(input, { key: 'a' });
    await fireEvent.keyDown(window, { key: 'a', ctrlKey: true });
    expect(posts()).toHaveLength(0);
    input.remove();
  });

  it('makes every decision reachable as a native button in tab order', async () => {
    render(Approvals, { params: {} });
    const [first] = await screen.findAllByRole('article');
    const buttons = [...first.querySelectorAll('footer button')] as HTMLButtonElement[];
    expect(buttons.map((b) => b.textContent?.replace(/\s+[asld]$/, '').trim())).toEqual(['Allow once', 'Allow for this session', 'Always allow', 'Deny']);
    for (const b of buttons) {
      expect(b.tabIndex).toBe(0);
      expect(b.disabled).toBe(false);
    }
  });

  it('switches to parked and recent with tabs', async () => {
    render(Approvals, { params: {} });
    await fireEvent.click(screen.getByRole('tab', { name: /Recent/ }));
    expect(router.query.get('status')).toBe('recent');
    await waitFor(() => expect(calls.some((c) => c.url.includes('status=recent'))).toBe(true));
  });
});

describe('Approvals inbox shortcut target', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    // Start empty so the wait below sees this test's load, not the last test's list.
    approvals.reset();
    await session.init();
    await waitFor(() => expect(approvals.pending.length).toBe(fixtureApprovals.length));
    const second = { ...fixtureApprovals[1], status: 'pending', created_at: '2099-01-01T00:00:00Z' };
    approvals.pending = [fixtureApprovals[0], second];
    router.navigate('/approvals');
  });

  const posts = () => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/approvals/'));

  it('does not decide a card that slid under the cursor until the user moves', async () => {
    render(Approvals, { params: {} });
    await screen.findAllByRole('article');
    // The selected card is decided from Telegram; the next one takes its place.
    approvals.upsert({ ...fixtureApprovals[0], status: 'allowed' });
    await waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(1));
    expect(screen.getByRole('article').querySelector('[aria-keyshortcuts]')).toBeNull();
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(0);

    await fireEvent.keyDown(window, { key: 'j' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${fixtureApprovals[1].id}`);
  });

  it('acts on the card that holds focus', async () => {
    render(Approvals, { params: {} });
    const cards = await screen.findAllByRole('article');
    await fireEvent.focusIn(cards[1].querySelector('footer button')!);
    await fireEvent.keyDown(window, { key: 'd' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${fixtureApprovals[1].id}`);
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'deny' });
  });

  it('has no single-key shortcut for always allow', async () => {
    render(Approvals, { params: {} });
    const [first] = await screen.findAllByRole('article');
    expect(first.querySelector('button[data-choice="allow_always"]')?.hasAttribute('aria-keyshortcuts')).toBe(false);
    await fireEvent.keyDown(window, { key: 'l' });
    expect(posts()).toHaveLength(0);
  });
});

// The "Open in web to approve" link lands on /approvals/:id, which draws the
// requested card above the Pending list.
describe('Approvals inbox opened at one approval', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    approvals.reset();
    await session.init();
    await waitFor(() => expect(approvals.pending.length).toBe(fixtureApprovals.length));
    router.navigate(`/approvals/${fixtureApprovals[1].id}`);
  });

  const posts = () => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/approvals/'));
  const requested = async () => {
    const top = await screen.findByRole('region', { name: 'Requested approval' });
    await waitFor(() => expect(top.querySelector('footer button')).not.toBeNull());
    return top;
  };

  it('points the shortcuts at the requested card, not the list below it', async () => {
    render(Approvals, { params: { id: fixtureApprovals[1].id } });
    const top = await requested();
    expect(top.querySelector('button[aria-keyshortcuts="a"]')).not.toBeNull();
    const listed = screen.getAllByRole('article').find((a) => a.dataset.approval === fixtureApprovals[0].id)!;
    expect(listed.querySelector('[aria-keyshortcuts]')).toBeNull();

    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${fixtureApprovals[1].id}`);
    await waitFor(() => expect(top.querySelector('footer button')).toBeNull());
    // The requested card is decided; another press must not reach the list.
    await fireEvent.keyUp(window, { key: 'a' });
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(1);
  });

  it('moves into the list with j', async () => {
    render(Approvals, { params: { id: fixtureApprovals[1].id } });
    const top = await requested();
    await fireEvent.keyDown(window, { key: 'j' });
    expect(document.activeElement?.closest('article')?.dataset.approval).toBe(fixtureApprovals[0].id);
    expect(top.querySelector('[aria-keyshortcuts]')).toBeNull();
    await fireEvent.keyDown(window, { key: 'd' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${fixtureApprovals[0].id}`);
  });

  it('keeps keyboard focus on the requested card after deciding it', async () => {
    render(Approvals, { params: { id: fixtureApprovals[1].id } });
    const top = await requested();
    const allow = top.querySelector<HTMLButtonElement>('button[data-choice="allow_once"]')!;
    allow.focus();
    await fireEvent.click(allow);
    await waitFor(() => expect(top.querySelector('footer button')).toBeNull());
    await waitFor(() => expect(document.activeElement?.getAttribute('role')).toBe('status'));
    expect(top.contains(document.activeElement)).toBe(true);
  });
});

describe('Approvals inbox held keys and focus', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    approvals.reset();
    await session.init();
    await waitFor(() => expect(approvals.pending.length).toBe(fixtureApprovals.length));
    const second = { ...fixtureApprovals[1], status: 'pending', created_at: '2099-01-01T00:00:00Z' };
    approvals.pending = [fixtureApprovals[0], second];
    router.navigate('/approvals');
  });

  const posts = () => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/approvals/'));

  it('decides one card per key press, however long the key is held', async () => {
    render(Approvals, { params: {} });
    await screen.findAllByRole('article');
    await fireEvent.keyDown(window, { key: 'd' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    await waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(1));
    await fireEvent.keyDown(window, { key: 'd', repeat: true });
    await fireEvent.keyDown(window, { key: 'd' });
    await new Promise((r) => setTimeout(r, 20));
    expect(posts()).toHaveLength(1);

    await fireEvent.keyUp(window, { key: 'd' });
    await fireEvent.keyDown(window, { key: 'd' });
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(posts()[1].url).toBe(`/api/approvals/${fixtureApprovals[1].id}`);
  });

  it('moves focus to the next card and announces the outcome', async () => {
    render(Approvals, { params: {} });
    const [first] = await screen.findAllByRole('article');
    const allow = first.querySelector<HTMLButtonElement>('button[data-choice="allow_once"]')!;
    allow.focus();
    await fireEvent.click(allow);
    await waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(1));
    await waitFor(() => expect(document.activeElement).not.toBe(document.body));
    expect(document.activeElement?.querySelector('article')?.dataset.approval).toBe(fixtureApprovals[1].id);
    expect(screen.getByText('Allowed once: Run shell command')).toBeTruthy();

    const deny = screen.getByRole('article').querySelector<HTMLButtonElement>('button[data-choice="deny"]')!;
    deny.focus();
    await fireEvent.click(deny);
    expect(await screen.findByText('All clear')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).not.toBe(document.body));
    expect(document.activeElement?.textContent).toContain('All clear');
  });
});

