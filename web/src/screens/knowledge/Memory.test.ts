import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { resetMock } from '../../../mock/server';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
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

describe('Memory', () => {
  afterEach(() => vi.useRealTimers());

  it('says "Saved", then what it means, and keeps saying it', async () => {
    const calls = await start();
    const about = section('About you');
    await fireEvent.click(within(about).getByRole('button', { name: 'Edit' }));
    const box = within(about).getByLabelText('About you') as HTMLTextAreaElement;
    expect(box.className).not.toMatch(/font-mono/);
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

  it('invites writing when Bothy has remembered nothing', async () => {
    await start('memory-empty');
    expect(screen.getAllByText("Bothy hasn't remembered anything yet.")).toHaveLength(2);
    await fireEvent.click(within(section('About you')).getByRole('button', { name: 'Edit' }));
    expect(within(section('About you')).getByLabelText('About you')).toBeTruthy();
  });

  it('lists who changed what, with names instead of ids', async () => {
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'History' }));
    const dialog = screen.getByRole('dialog', { name: 'History' });
    await within(dialog).findByText('Priya Rao (admin)');
    expect(within(dialog).getByText('You')).toBeTruthy();
    expect(within(dialog).getByRole('link', { name: '“Fix login redirect”' })).toBeTruthy();
    expect(dialog.textContent).not.toMatch(/usr_|run_|ses_/);
  });
});
