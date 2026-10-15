import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { setCsrf } from '../../lib/api/client';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import Settings from './Settings.svelte';
import { settingsConfig } from './store.svelte';

const patches = (calls: Call[]) => calls.filter((c) => c.method === 'PATCH' && c.url === '/api/config').map((c) => JSON.parse(c.body).set);

/** The settings row a label belongs to. */
const row = (label: string) => screen.getByText(label, { selector: 'label' }).closest('.grid') as HTMLElement;

describe('Settings → General', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    settingsConfig.view = null;
    render(Settings, { params: { group: 'general', page: 'general' } });
    await screen.findByText('Workspace');
  });

  it('saves a text field on blur and says "Saved" in its live region', async () => {
    const limit = screen.getByLabelText('Daily limit') as HTMLInputElement;
    expect(limit.value).toBe('3.00');
    await fireEvent.input(limit, { target: { value: '7.5' } });
    expect(patches(calls)).toEqual([]);
    await fireEvent.blur(limit);
    await waitFor(() => expect(patches(calls)).toEqual([{ 'budgets.daily_usd': 7.5 }]));
    const live = await within(row('Daily limit')).findByText('Saved');
    expect(live.getAttribute('aria-live')).toBe('polite');
    await waitFor(() => expect(limit.value).toBe('7.50'));
  });

  it('saves on Enter, and not at all when nothing changed', async () => {
    const name = screen.getByLabelText('Name') as HTMLInputElement;
    await fireEvent.keyDown(name, { key: 'Enter' });
    await fireEvent.blur(name);
    expect(patches(calls)).toEqual([]);
    await fireEvent.input(name, { target: { value: 'Ada' } });
    await fireEvent.keyDown(name, { key: 'Enter' });
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.name': 'Ada' }]));
  });

  it("checks Bothy's name the way the server does", async () => {
    const name = screen.getByLabelText('Name') as HTMLInputElement;
    for (const [value, error] of [
      ['   ', 'Enter a name.'],
      ['x'.repeat(41), 'Use at most 40 characters.'],
      ['<b>Ada</b>', 'Leave out < and >.'],
    ]) {
      await fireEvent.input(name, { target: { value } });
      await fireEvent.keyDown(name, { key: 'Enter' });
      expect(await screen.findByText(error)).toBeTruthy();
    }
    expect(patches(calls)).toEqual([]);
  });

  it('saves a choice as soon as it changes', async () => {
    const tone = screen.getByLabelText('Tone') as HTMLSelectElement;
    await fireEvent.change(tone, { target: { value: 'formal' } });
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.tone': 'formal' }]));
    await within(row('Tone')).findByText('Saved');
  });

  it('keeps an invalid value unsaved and says why under the field', async () => {
    const web = screen.getByLabelText('Web address') as HTMLInputElement;
    await fireEvent.input(web, { target: { value: 'bothy.acme' } });
    await fireEvent.keyDown(web, { key: 'Enter' });
    expect(await screen.findByText('Use a full address, like https://bothy.acme.com.')).toBeTruthy();
    expect(web.getAttribute('aria-invalid')).toBe('true');
    expect(patches(calls)).toEqual([]);
    await fireEvent.input(web, { target: { value: 'https://bothy.acme.com/' } });
    await fireEvent.blur(web);
    await waitFor(() => expect(patches(calls)).toEqual([{ 'server.public_url': 'https://bothy.acme.com' }]));
  });

  it('says which settings take effect only after a restart', () => {
    // server.public_url is restart-only in the fixture config; the daily limit applies at once.
    expect(within(row('Web address')).getByText(/Takes effect after a restart\./)).toBeTruthy();
    expect(within(row('Daily limit')).queryByText(/Takes effect after a restart\./)).toBeNull();
  });

  it("says the workspace time zone is new people's default and the daily limit's clock, not everyone's schedules", () => {
    expect(screen.getByText('New people start with this time zone. The daily spending limit resets at midnight here.')).toBeTruthy();
    expect(screen.queryByText(/Used for schedules/)).toBeNull();
  });

  it('keeps the standing instructions as one list item per line', async () => {
    const box = screen.getByLabelText('Standing instructions');
    await fireEvent.input(box, { target: { value: 'Reply in British English.\n\n  Link to the code.  ' } });
    await fireEvent.blur(box);
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.standing_rules': ['Reply in British English.', 'Link to the code.'] }]));
  });

  it("shows a conflict instead of overwriting another admin's change", async () => {
    const name = screen.getByLabelText('Name') as HTMLInputElement;
    await fireEvent.input(name, { target: { value: 'Ada' } });
    // Someone else saves the same setting while this field is being edited.
    await settingsConfig.save({ 'agent.name': 'Grace' });
    expect(await screen.findByText(/Someone else changed this setting\. Reload to see their change\./)).toBeTruthy();
    const before = patches(calls).length;
    await fireEvent.blur(name);
    expect(patches(calls).length).toBe(before);
    await fireEvent.click(screen.getByRole('button', { name: 'Reload' }));
    expect(name.value).toBe('Grace');
    expect(screen.queryByText(/Someone else changed this setting/)).toBeNull();
  });
});

describe('Settings → General, who sees it', () => {
  it('tells a member that only admins can see it', async () => {
    installApi();
    session.ready = false;
    await session.init();
    session.me = { ...session.me!, user: { ...session.me!.user, role: 'member' } };
    render(Settings, { params: { group: 'general', page: 'general' } });
    expect(await screen.findByText('Only admins can see this page.')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'General' })).toBeTruthy();
  });

  it('says a settings page that does not exist does not exist', async () => {
    installApi();
    session.ready = false;
    await session.init();
    render(Settings, { params: { group: 'nope', page: 'nope' } });
    expect(await screen.findByText("This page doesn't exist.")).toBeTruthy();
  });
});
