import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { setCsrf } from '../../lib/api/client';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import Settings from './Settings.svelte';

async function open(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  setCsrf('mock-csrf');
  router.navigate('/settings/general/personality');
  render(Settings, { params: { group: 'general', item: 'personality', page: 'persona' } });
  const box = (await screen.findByLabelText('Personality')) as HTMLTextAreaElement;
  await waitFor(() => expect(box.value).toMatch(/^# Bothy/));
  return { calls, box };
}

const puts = (calls: Call[]) => calls.filter((c) => c.method === 'PUT' && c.url === '/api/persona');

describe('Settings → General → Personality', () => {
  it('saves with ⌘S and goes back to General', async () => {
    const { calls, box } = await open();
    await fireEvent.input(box, { target: { value: `${box.value}\nKeep it short.` } });
    await fireEvent.keyDown(window, { key: 's', metaKey: true });
    await waitFor(() => expect(puts(calls)).toHaveLength(1));
    expect(JSON.parse(puts(calls)[0].body).content).toMatch(/Keep it short\.$/);
    await waitFor(() => expect(router.path).toBe('/settings/general'));
  });

  it('asks before throwing edits away', async () => {
    const { box } = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(router.path).toBe('/settings/general');

    router.navigate('/settings/general/personality');
    await fireEvent.input(box, { target: { value: 'Different.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog', { name: 'Discard your changes?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Keep editing' }));
    expect(router.path).toBe('/settings/general/personality');
    expect(box.value).toBe('Different.');
  });

  it("doesn't overwrite someone else's newer version", async () => {
    const { calls, box } = await open('persona-changed');
    await fireEvent.input(box, { target: { value: `${box.value}\nMine.` } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Someone else changed the personality while you were editing.')).toBeTruthy();
    expect(puts(calls)).toHaveLength(0);

    await fireEvent.click(screen.getByRole('button', { name: 'See their version' }));
    const dialog = await screen.findByRole('dialog', { name: 'Their version' });
    expect(within(dialog).getByText(/release checklist/)).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Keep mine' }));
    await waitFor(() => expect(puts(calls)).toHaveLength(1));
    expect(JSON.parse(puts(calls)[0].body).content).toMatch(/Mine\.$/);
  });

  it('refuses to save past the size limit', async () => {
    const { calls, box } = await open();
    await fireEvent.input(box, { target: { value: 'x'.repeat(14000) } });
    expect(await screen.findByText("The personality is almost full. Shorten it so there's room for more.")).toBeTruthy();
    await fireEvent.input(box, { target: { value: 'x'.repeat(17000) } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('The personality is too long to save. Remove something first.')).toBeTruthy();
    expect(puts(calls)).toHaveLength(0);
  });
});
