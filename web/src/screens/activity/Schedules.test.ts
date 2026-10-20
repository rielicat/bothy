import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { schedules as seeded, state as mockActivity } from '../../../mock/domains/activity';
import { state as mockSettings } from '../../../mock/domains/settings';
import { toaster } from '../../components/ui';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Schedules from './Schedules.svelte';

async function start(role?: string, override?: (method: string, url: string) => unknown, ready = 'Morning briefing', seed?: () => void) {
  const calls = installApi({ override });
  seed?.();
  session.ready = false;
  await session.init();
  if (role) session.me = { ...session.me!, user: { ...session.me!.user, role } };
  router.navigate('/activity/schedules');
  render(Schedules);
  await screen.findByText(ready);
  return calls;
}

const blankList = (m: string, url: string) => (url === '/api/schedules/examples' ? { examples: [], checkin: { empty: true, lines: [] } } : undefined);
const patched = (calls: { method: string; url: string }[], id: string) => calls.some((c) => c.method === 'PATCH' && c.url === `/api/schedules/${id}`);
const checkinOnly = () => (mockActivity.schedules = mockActivity.schedules.filter((s) => s.kind === 'heartbeat'));
const searchOn = () => (mockSettings.effective.entries.find((e) => e.path === 'search.provider')!.value = 'searxng');
const examplesSection = () => screen.queryByRole('heading', { name: 'Examples' })?.closest('section');

describe('Schedules (docs/design/ui.md §10.3)', () => {
  it("says an account's schedules post to their chats: accounts have no chat app", async () => {
    await start();
    expect(screen.getByText('Morning briefing').closest('li')!.textContent).toContain('Posts to your chats');
  });

  it('opens a schedule from anywhere on its row, its switch and ⋯ staying their own', async () => {
    await start();
    const row = screen.getByText('Morning briefing').closest('li')!;
    expect(row.classList.contains('row-stretch')).toBe(true);
    expect(within(row).getByRole('link', { name: 'Morning briefing' }).classList.contains('stretch-link')).toBe(true);
    const controls = within(row).getByRole('switch', { name: 'Morning briefing' }).closest('.z-10');
    expect(controls?.contains(within(row).getByRole('button', { name: 'More for Morning briefing' }))).toBe(true);
  });

  // A failed run doesn't stop the next one; the row used to drop "Next:".
  it('says when a schedule whose last run failed runs next', async () => {
    await start();
    const row = screen.getByText('Weekly PR digest').closest('li')!;
    expect(row.textContent).toMatch(/Last run failed.* · Next: /);
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

  it('confirms Run now in a short toast that opens the chat the run writes in', async () => {
    toaster.dismiss();
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'More for Morning briefing' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Schedule started.'));
    expect(toaster.current?.action?.label).toBe('Open chat');
    toaster.current!.action!.onclick();
    expect(router.path).toMatch(/^\/chat\/ses_[0-9a-z]{26}$/);
  });

  it("doesn't offer to open a chat the server didn't name", async () => {
    toaster.dismiss();
    await start(undefined, (m, url) => (m === 'POST' && url.endsWith('/run') ? { run_id: 'run_01jx' } : undefined));
    await fireEvent.click(screen.getByRole('button', { name: 'More for Morning briefing' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Schedule started.'));
    expect(toaster.current?.action).toBeUndefined();
  });

  it('offers up to three examples under the list, each opening a prefilled form', async () => {
    await start(undefined, undefined, 'Examples', () => (checkinOnly(), searchOn()));
    const rows = within(examplesSection()!).getAllByRole('link');
    expect(rows.map((r) => r.getAttribute('href'))).toEqual([
      '/activity/schedules/new?example=pr-summary',
      '/activity/schedules/new?example=build-check',
      '/activity/schedules/new?example=news',
    ]);
    expect(rows[0].textContent).toMatch(/^\s*Monday pull request summary\s*Mondays at 9:00( AM)? · Uses GitHub\s*$/);
    expect(rows[2].textContent).toMatch(/Morning news on a topic\s*Weekdays at 8:00( AM)? · Uses web search/);
    // Examples stand in for the empty sentence, and the header keeps its button.
    expect(screen.queryByText('Add your own, like a Monday summary of open pull requests.')).toBeNull();
    const button = screen.getByRole('link', { name: 'New schedule' });
    expect(screen.getByRole('heading', { level: 1 }).parentElement!.contains(button)).toBe(true);
  });

  it('shows an admin what schedules can do before GitHub or web search is set up, leading to what they need', async () => {
    const unready = {
      examples: [
        { id: 'pr-summary', name: 'Monday pull request summary', kind: 'cron', spec: '0 9 * * 1', slot: 'fast', needs: 'github', prompt: '', ready: false },
        { id: 'news', name: 'Morning news on a topic', kind: 'cron', spec: '0 8 * * 1-5', slot: 'fast', needs: 'web_search', prompt: '', ready: false },
      ],
      checkin: { empty: true, lines: [] },
    };
    await start(undefined, (m, url) => (url === '/api/schedules/examples' ? unready : undefined), 'Examples', checkinOnly);
    const [pr, news] = within(examplesSection()!).getAllByRole('link');
    expect(pr.getAttribute('href')).toBe('/settings/connections/github');
    expect(pr.textContent).toMatch(/Connect GitHub first/);
    expect(news.getAttribute('href')).toBe('/settings/connections');
    expect(news.textContent).toMatch(/Turn on web search first/);
  });

  it("leaves out an example named like one of the person's schedules", async () => {
    await start(undefined, undefined, 'Examples', () => {
      checkinOnly();
      mockActivity.schedules.push({ ...seeded[1], id: 'sch_01jmine000000000000000000', name: ' monday pull request SUMMARY' });
    });
    expect(within(examplesSection()!).queryByText('Monday pull request summary')).toBeNull();
    expect(within(examplesSection()!).getByText('Morning build check')).toBeTruthy();
  });

  it('offers no examples to someone with three schedules of their own', async () => {
    // The default fixtures have four.
    await start();
    expect(examplesSection()).toBeUndefined();
  });

  it('says how to start, with New schedule in the header, when there are no schedules and no examples', async () => {
    await start(undefined, (m, url) => (url === '/api/schedules' ? [] : url === '/api/schedules/examples' ? { examples: [], checkin: { empty: false, lines: [] } } : undefined), 'Check-in');
    expect(screen.getByText('Add your own, like a Monday summary of open pull requests.')).toBeTruthy();
    expect(screen.getAllByRole('link', { name: 'New schedule' })).toHaveLength(1);
    expect(screen.getByRole('heading', { level: 1 }).parentElement!.contains(screen.getByRole('link', { name: 'New schedule' }))).toBe(true);
  });

  it('opens the check-in list instead of turning on a check-in with nothing to check', async () => {
    const calls = await start(undefined, (m, url) => (url === '/api/schedules/examples' ? { examples: [], checkin: { empty: true, lines: [] } } : undefined));
    await fireEvent.click(screen.getByRole('switch', { name: 'Check-in' }));
    expect(router.path + router.search).toBe('/activity/schedules/checkin?on=1');
    expect(calls.some((c) => c.method !== 'GET' && c.url.startsWith('/api/schedules') && c.url !== '/api/schedules/preview')).toBe(false);
  });

  it('turns off a check-in whose list is empty', async () => {
    const calls = await start(undefined, blankList, 'Morning briefing', () => (mockActivity.schedules.find((s) => s.kind === 'heartbeat')!.enabled = true));
    await fireEvent.click(screen.getByRole('switch', { name: 'Check-in' }));
    await waitFor(() => expect(patched(calls, 'sch_01jcheckin00000000000000')).toBe(true));
    expect(router.path).toBe('/activity/schedules');
  });

  it('turns on other schedules while the check-in list is empty', async () => {
    const calls = await start(undefined, blankList);
    await fireEvent.click(screen.getByRole('switch', { name: 'Morning briefing' }));
    await waitFor(() => expect(patched(calls, 'sch_01jbrief00000000000000000')).toBe(true));
    expect(router.path).toBe('/activity/schedules');
  });

  it('shows the list only once its examples are in, so the empty sentence never shows first', async () => {
    const calls = installApi();
    checkinOnly();
    let release = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const real = globalThis.fetch;
    vi.stubGlobal('fetch', async (input: string, init?: RequestInit) => (input.startsWith('/api/schedules/examples') && (await gate), real(input, init)));
    session.ready = false;
    await session.init();
    router.navigate('/activity/schedules');
    render(Schedules);
    await waitFor(() => expect(calls.some((c) => c.url === '/api/schedules')).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText('Check-in')).toBeNull();
    expect(screen.queryByText('Add your own, like a Monday summary of open pull requests.')).toBeNull();
    release();
    expect(await screen.findByRole('heading', { name: 'Examples' })).toBeTruthy();
    expect(screen.queryByText('Add your own, like a Monday summary of open pull requests.')).toBeNull();
  });

  it('turns on a check-in that has a list', async () => {
    const calls = await start();
    await fireEvent.click(screen.getByRole('switch', { name: 'Check-in' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/schedules/sch_01jcheckin00000000000000')).toBe(true));
    expect(router.path).toBe('/activity/schedules');
  });

  it("says that deleting a schedule stops it and leaves its past results in the person's chats", async () => {
    const calls = await start();
    await fireEvent.click(screen.getByRole('button', { name: 'More for Weekly PR digest' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete the schedule “Weekly PR digest”?' });
    expect(within(dialog).getByText("It stops running. Its past results stay in your chats. This can't be undone.")).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete schedule' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/schedules/sch_01jprdigest0000000000000')).toBe(true));
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
