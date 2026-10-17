import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { resetMock } from '../../../mock/server';
import { api, setCsrf } from '../../lib/api/client';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import Memory from './Memory.svelte';

async function start(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  render(Memory);
  await screen.findByText('Things Bothy remembers');
  return calls;
}

const section = (name: string) => screen.getByRole('region', { name });
const EVERYONE = 'Remembered for everyone';

describe('Memory', () => {
  afterEach(() => vi.useRealTimers());

  it('says "Saved", then what it means, and keeps saying it', async () => {
    const calls = await start();
    const about = section('About you');
    await fireEvent.click(within(about).getByRole('button', { name: 'Edit' }));
    const box = within(about).getByLabelText('About you') as HTMLTextAreaElement;
    expect(box.className).not.toMatch(/font-mono/);
    // The live region is already there while editing, so writing "Saved" into it is announced.
    expect(within(about).getByRole('status').textContent).toBe('');
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await fireEvent.input(box, { target: { value: `${box.value} Based in Lisbon.` } });
    await fireEvent.click(within(about).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(within(about).getByRole('status').textContent).toBe('Saved'));
    const put = calls.find((c) => c.method === 'PUT');
    expect(put?.url).toBe('/api/memory/USER');
    expect(put?.headers['if-match']).toBe('e1');
    vi.advanceTimersByTime(2000);
    await waitFor(() => expect(within(about).getByRole('status').textContent).toBe('Changes apply from your next message.'));
    vi.advanceTimersByTime(60_000);
    expect(within(about).getByRole('status').textContent).toBe('Changes apply from your next message.');
    expect(within(about).getByText(/Based in Lisbon/)).toBeTruthy();
  });

  it('warns near the size limit and refuses to save past it', async () => {
    const calls = await start('memory-near-full');
    const things = section('Things Bothy remembers');
    expect(within(things).getByText('This is almost full. Remove anything Bothy no longer needs.')).toBeTruthy();
    await fireEvent.click(within(things).getByRole('button', { name: 'Edit' }));
    const box = within(things).getByLabelText('Things Bothy remembers') as HTMLTextAreaElement;
    await fireEvent.input(box, { target: { value: `${box.value}\n- ${'More to remember. '.repeat(6)}` } });
    await fireEvent.click(within(things).getByRole('button', { name: 'Save' }));
    expect(await within(things).findByText('This is too long to save. Remove something first.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);
  });

  it('says what each empty section is for, with Write where Edit sits', async () => {
    await start('memory-empty');
    expect(within(section('About you')).getByText('Tell Bothy about your role and how you like answers.')).toBeTruthy();
    expect(within(section('Things Bothy remembers')).getByText('Bothy adds things here as you work together.')).toBeTruthy();
    await fireEvent.click(within(section('About you')).getByRole('button', { name: 'Write' }));
    expect(within(section('About you')).getByLabelText('About you')).toBeTruthy();
  });

  it('shows History only once there is something in it', async () => {
    installApi({ override: (m, url) => (m === 'GET' && /\/history\?limit=1$/.test(url) ? { items: [] } : undefined) });
    session.ready = false;
    await session.init();
    render(Memory);
    await screen.findByText('Things Bothy remembers');
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole('button', { name: 'History' })).toBeNull();
  });

  it("puts what's remembered for everyone first, and an admin edits it like the rest", async () => {
    const calls = await start();
    const titles = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(titles).toEqual([EVERYONE, 'About you', 'Things Bothy remembers']);
    const team = section(EVERYONE);
    expect(team.getAttribute('aria-describedby') && document.getElementById(team.getAttribute('aria-describedby')!)?.textContent).toBe(
      'Bothy uses this in every chat, for everyone, including chat apps.',
    );
    expect(within(team).getByText(/Our deploy day is Thursday\./)).toBeTruthy();
    expect(calls.some((c) => c.url === '/api/memory/team')).toBe(true);
    await fireEvent.click(within(team).getByRole('button', { name: 'Edit' }));
    const box = within(team).getByLabelText(EVERYONE) as HTMLTextAreaElement;
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await fireEvent.input(box, { target: { value: `${box.value}\nThe office is closed on Fridays.` } });
    await fireEvent.click(within(team).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(within(team).getByRole('status').textContent).toBe('Saved'));
    const put = calls.find((c) => c.method === 'PUT');
    expect(put?.url).toBe('/api/memory/team');
    expect(put?.headers['if-match']).toBe('e1');
    vi.advanceTimersByTime(2000);
    await waitFor(() => expect(within(team).getByRole('status').textContent).toBe("Changes apply from everyone's next message."));
    expect(within(team).getByText(/The office is closed on Fridays\./)).toBeTruthy();
  });

  it('shows members what everyone shares without a way to change it', async () => {
    await start('member');
    const team = section(EVERYONE);
    expect(within(team).queryByRole('button')).toBeNull();
    expect(within(team).getByText('Bothy uses this in every chat, for everyone. Only admins can change it.')).toBeTruthy();
    expect(within(team).getByText(/Our deploy day is Thursday\./)).toBeTruthy();
    // Their own memory is still theirs to change.
    expect(within(section('About you')).getByRole('button', { name: 'Edit' })).toBeTruthy();
  });

  it('says what an empty section for everyone is for, to admins and to members', async () => {
    await start('team-memory-empty');
    expect(within(section(EVERYONE)).getByText('Add what Bothy should know in every chat, like your deploy day.')).toBeTruthy();
    await fireEvent.click(within(section(EVERYONE)).getByRole('button', { name: 'Write' }));
    expect(within(section(EVERYONE)).getByLabelText(EVERYONE)).toBeTruthy();
    cleanup();
    await start('team-memory-empty,member');
    const team = section(EVERYONE);
    expect(within(team).getByText('Nothing yet. Ask Bothy to remember something for everyone, and an admin can approve it.')).toBeTruthy();
    expect(within(team).queryByRole('button')).toBeNull();
  });

  it('follows an approved note for everyone without a reload', async () => {
    await start('team-memory-ask');
    expect(within(section(EVERYONE)).queryByText(/Customer names/)).toBeNull();
    setCsrf('mock-csrf');
    await api.post('/api/approvals/apr_01jteamask0000000000000000', { choice: 'allow_once' });
    FakeEventSource.last.emit('memory.updated', { seq: 9, type: 'memory.updated', at: new Date().toISOString(), data: { file: 'TEAM', event_id: 1 } });
    expect(await within(section(EVERYONE)).findByText(/Customer names never go in public channels\./)).toBeTruthy();
  });

  it('lists who changed what, with names instead of ids', async () => {
    await start();
    await fireEvent.click(await screen.findByRole('button', { name: 'History' }));
    const dialog = screen.getByRole('dialog', { name: 'History' });
    expect(await within(dialog).findAllByText('Priya Rao (admin)')).toHaveLength(2);
    expect(within(dialog).getByText('You')).toBeTruthy();
    expect(within(dialog).getByRole('link', { name: '“Fix login redirect”' })).toBeTruthy();
    // Changes to what's remembered for everyone are listed under that name; a chat the reader can't open isn't named.
    expect(within(dialog).getAllByText(/· Remembered for everyone ·/).length).toBe(2);
    expect(dialog.textContent).not.toMatch(/usr_|run_|ses_|apr_|TEAM/);
  });
});
