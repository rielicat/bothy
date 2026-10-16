import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { toaster } from '../../components/ui';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Schedules from './Schedules.svelte';

async function start(role?: string, override?: (method: string, url: string) => unknown) {
  const calls = installApi({ override });
  session.ready = false;
  await session.init();
  if (role) session.me = { ...session.me!, user: { ...session.me!.user, role } };
  router.navigate('/activity/schedules');
  render(Schedules);
  await screen.findByText('Morning briefing');
  return calls;
}

describe('Schedules (docs/design/ui.md §10.3)', () => {
  it('says an account\'s schedules post here: accounts have no chat app', async () => {
    await start();
    expect(screen.getByText('Morning briefing').closest('li')!.textContent).toContain('Posts here');
  });

  it('confirms Run now in a short toast that opens the task', async () => {
    toaster.dismiss();
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'More for Morning briefing' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Schedule started.'));
    expect(toaster.current?.action?.label).toBe('View');
  });

  it('lists the schedules people set up in chat apps for admins, to turn off or delete', async () => {
    const calls = await start();
    const section = (await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!;
    const s = within(section);
    expect(s.getByText('Schedules people set up in chat apps. Their results go to that chat.')).toBeTruthy();
    const row = s.getByText('Weekly sales summary').closest('li')!;
    expect(row.textContent).toMatch(/Alex Kim · Slack · Mondays at .+ · Next: /);
    expect(s.getByText('Ops handover').closest('li')!.textContent).toMatch(/Ops team · Telegram · .+ · Off\s*Delete…\s*$/);

    // Named like the switches above: "Weekly sales summary, switch, on".
    await fireEvent.click(s.getByRole('switch', { name: 'Weekly sales summary' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/schedules/sch_01jteamsales0000000000000')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body)).toEqual({ enabled: false });

    await fireEvent.click(s.getByRole('button', { name: 'Delete Ops handover…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete “Ops handover”?' });
    expect(within(dialog).getByText("It stops running. Past results stay in that chat. This can't be undone.")).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete schedule' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/schedules/sch_01jteamops00000000000000')).toBe(true));
    await waitFor(() => expect(s.queryByText('Ops handover')).toBeNull());
  });

  it("says a chat app's schedule that is on but has no next run isn't scheduled, rather than off", async () => {
    await start(undefined, (m, url) =>
      m === 'GET' && url === '/api/team/schedules'
        ? [{ id: 'sch_01jteamonce00000000000000', name: 'Launch reminder', owner: '#eng', owner_kind: 'channel', app: 'slack', kind: 'once', spec: '2026-10-01T09:00:00Z', tz: 'UTC', enabled: true }]
        : undefined,
    );
    const row = (await screen.findByText('Launch reminder')).closest('li')!;
    expect(row.textContent).toMatch(/#eng · Slack · .+ · Not scheduled/);
    expect(within(row).getByRole('switch', { name: 'Launch reminder' }).getAttribute('aria-checked')).toBe('true');
  });

  it("says when the chat apps' schedules didn't load, and tries again", async () => {
    let fail = true;
    const calls = await start(undefined, (m, url) => (m === 'GET' && url === '/api/team/schedules' && fail ? new Response('{"error":{"code":"internal","message":"x"}}', { status: 500 }) : undefined));
    const section = (await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!;
    expect(await within(section).findByText("These schedules didn't load. Check your connection and try again.")).toBeTruthy();
    fail = false;
    await fireEvent.click(within(section).getByRole('button', { name: 'Try again' }));
    expect(await within(section).findByText('Weekly sales summary')).toBeTruthy();
    expect(calls.filter((c) => c.url === '/api/team/schedules')).toHaveLength(2);
  });

  it("doesn't show members the schedules of chat apps", async () => {
    const calls = await start('member');
    expect(screen.queryByRole('heading', { name: 'In chat apps' })).toBeNull();
    expect(calls.some((c) => c.url === '/api/team/schedules')).toBe(false);
  });
});
