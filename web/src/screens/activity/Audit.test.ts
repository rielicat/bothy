import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import { loadReleases } from './api';
import Audit from './Audit.svelte';

async function start(path: string, mock = ''): Promise<Call[]> {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  await loadReleases(true);
  router.navigate(path);
  render(Audit);
  return calls;
}

describe('Audit log (docs/design/ui.md §10.6)', () => {
  it('reads as who did what, with a result word only when the sentence leaves it out', async () => {
    await start('/activity/audit');
    expect(await screen.findByText('Approved deleting notes/staging.md')).toBeTruthy();
    const blocked = screen.getByText('Blocked from sending to paste.example.net');
    // The sentence already says "Blocked", so no word repeats it.
    expect(blocked.closest('li')!.textContent).not.toMatch(/Blocked\b(?! from)/);
    expect(screen.queryByText('Blocked')).toBeNull();
    expect(document.body.textContent).not.toMatch(/\b(usr|run|ses)_[0-9a-z]|\bok\b/);
  });

  it('keeps filters in the address and asks the server for the period', async () => {
    const calls = await start('/activity/audit');
    await screen.findByText('Approved deleting notes/staging.md');
    await fireEvent.change(screen.getByLabelText('Period'), { target: { value: '30' } });
    expect(router.query.get('period')).toBe('30');
    await waitFor(() => expect(calls.filter((c) => c.url.startsWith('/api/audit?')).length).toBe(2));
    await fireEvent.change(screen.getByLabelText('Action'), { target: { value: 'keys' } });
    expect(await screen.findByText('Used a saved key')).toBeTruthy();
    expect(screen.queryByText('Signed in')).toBeNull();
  });

  it('offers Clear filters when the last 7 days are empty, and clears to all time', async () => {
    await start('/activity/audit', 'activity-empty');
    expect(await screen.findByText('Nothing matches these filters.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(router.query.get('period')).toBe('all');
  });

  it('expands a row in place with a Raw record', async () => {
    await start('/activity/audit');
    const row = await screen.findByRole('button', { name: /Approved deleting/ });
    expect(row.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(row);
    expect(row.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByRole('button', { name: 'Raw record' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Open the task' })).toBeTruthy();
  });

  it('checks the log and says where it breaks', async () => {
    await start('/activity/audit', 'audit-broken');
    await screen.findByText('Approved deleting notes/staging.md');
    await fireEvent.click(screen.getByRole('button', { name: 'Check the log' }));
    expect(await screen.findByText(/^The entry from .+ doesn't match the ones before it\. Entries from there on may have been changed\./)).toBeTruthy();
  });

  it('allows a blocked site for everyone with the narrow grant', async () => {
    const calls = await start('/activity/audit?view=web');
    expect(await screen.findByText(/Reached api\.github\.com/)).toBeTruthy();
    expect(screen.getByText(/Allowed, but api\.example\.com didn't answer/)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Allow this site…' }));
    expect(await screen.findByText("This applies to everyone's tasks until you remove it in Settings → Security.")).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Allow site' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/grants')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.url === '/api/grants' && c.method === 'POST')!.body)).toMatchObject({ kind: 'domain', value: 'paste.example.net:443', max_se: 'external_send' });
  });

  it('is for admins only', async () => {
    const calls = await start('/activity/audit', 'member');
    expect(await screen.findByText('Only admins can see this page.')).toBeTruthy();
    expect(calls.some((c) => c.url.startsWith('/api/audit'))).toBe(false);
  });
});
