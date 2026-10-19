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

  it("names a schedule's time zone when it isn't the viewer's", async () => {
    await start(undefined, (m, url) => {
      if (m !== 'GET' || url !== '/api/schedules') return undefined;
      const base = { id: 'sch_tokyo', name: 'Morning briefing', kind: 'cron', spec: '0 6 * * *', prompt: 'x', slot: 'main', enabled: true, failures: 0, missed_run: 'skip', created_by: 'u', created_at: '', updated_at: '' };
      return [{ ...base, tz: 'Asia/Tokyo' }, { ...base, id: 'sch_mine', name: 'Mine', tz: session.me?.user.timezone }];
    });
    expect(screen.getByText('Morning briefing').closest('li')!.textContent).toContain('Tokyo time');
    expect(screen.getByText('Mine').closest('li')!.textContent).not.toMatch(/ time\b/);
  });

  it('confirms Run now in a short toast that opens the task', async () => {
    toaster.dismiss();
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'More for Morning briefing' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Schedule started.'));
    expect(toaster.current?.action?.label).toBe('View');
  });

  it("says why Run now didn't start, when trying again can't help", async () => {
    const why = "The check-in list is empty, so there's nothing to check yet. Add something to it first.";
    await start(undefined, (m, url) =>
      m === 'POST' && url.endsWith('/run') ? new Response(JSON.stringify({ error: { code: 'bad_request', message: why } }), { status: 400 }) : undefined,
    );
    await fireEvent.click(screen.getByRole('button', { name: 'More for Morning briefing' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    expect(await screen.findByText(why)).toBeTruthy();
    expect(screen.queryByText("Couldn't start it. Try again.")).toBeNull();
  });

  it('lists the schedules people set up in chat apps for admins, to turn off or delete', async () => {
    const calls = await start();
    const section = (await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!;
    const s = within(section);
    expect(s.getByText('Schedules people set up in chat apps. Their results go to that chat.')).toBeTruthy();
    const row = s.getByText('Weekly sales summary').closest('li')!;
    expect(row.textContent).toMatch(/Alex Kim · Slack · Mondays at .+ · Next: /);
    expect(s.getByText('Ops handover').closest('li')!.textContent).toMatch(/Ops team · Telegram · .+ · Off\s*$/);

    // Named like the switches above: "Weekly sales summary, switch, on".
    await fireEvent.click(s.getByRole('switch', { name: 'Weekly sales summary' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/schedules/sch_01jteamsales0000000000000')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body)).toEqual({ enabled: false });

    // The same ⋯ menu as the schedules above, holding one danger item.
    await fireEvent.click(s.getByRole('button', { name: 'More actions for Ops handover' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete “Ops handover”?' });
    expect(within(dialog).getByText("It stops running. Past results stay in that chat. This can't be undone.")).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete schedule' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/schedules/sch_01jteamops00000000000000')).toBe(true));
    await waitFor(() => expect(s.queryByText('Ops handover')).toBeNull());
  });

  it("shows the next run the server gives a chat app's schedule that was just turned on", async () => {
    await start(undefined, (m, url) =>
      m === 'PATCH' && url === '/api/schedules/sch_01jteamops00000000000000'
        ? { id: 'sch_01jteamops00000000000000', enabled: true, next_run_at: '2030-01-07T09:00:00Z' }
        : undefined,
    );
    const section = (await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!;
    const row = within(section).getByText('Ops handover').closest('li')!;
    await fireEvent.click(within(row).getByRole('switch', { name: 'Ops handover' }));
    await waitFor(() => expect(row.textContent).toMatch(/Ops team · Telegram · .+ · Next: /));
    expect(row.textContent).not.toContain('Not scheduled');
  });

  it("names the time zone of a chat app's schedule set in another zone, as for the schedules above", async () => {
    await start(undefined, (m, url) =>
      m === 'GET' && url === '/api/team/schedules'
        ? [{ id: 'sch_01jteamtokyo0000000000000', name: 'Tokyo standup', owner: 'Ken Sato', owner_kind: 'teammate', app: 'slack', kind: 'cron', spec: '0 9 * * 1-5', tz: 'Asia/Tokyo', enabled: false }]
        : undefined,
    );
    const row = (await screen.findByText('Tokyo standup')).closest('li')!;
    expect(row.textContent).toMatch(/Ken Sato · Slack · Weekdays at .+, Tokyo time · Off/);
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
