import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { setCsrf } from '../../lib/api/client';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Settings from './Settings.svelte';
import { settingsConfig } from './store.svelte';

async function open(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  setCsrf('mock-csrf');
  settingsConfig.view = null;
  render(Settings, { params: { group: 'people', page: 'people' } });
  return calls;
}

const menu = async (name: string) => {
  await fireEvent.click(await screen.findByRole('button', { name: `Options for ${name}` }));
  return screen.findByRole('menu');
};

describe('Settings → People', () => {
  it('lists people by name with their role, and nobody can change the owner or themselves', async () => {
    await open();
    expect(await screen.findByText('Jordan Kim')).toBeTruthy();
    expect(screen.getAllByText("Member · hasn't signed in yet").length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: 'Options for Sam' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Options for Priya Rao' })).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/usr_/);
  });

  it('adds a person with a one-time password to send them', async () => {
    const calls = await open();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add person' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add person' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Maya Chen' } });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: 'maya' } });
    await fireEvent.click(within(dialog).getByRole('radio', { name: 'Admin' }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('Maya Chen can sign in now. Send them these details:')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Copy' })));
    const post = JSON.parse(calls.find((c) => c.method === 'POST' && c.url === '/api/users')!.body);
    expect(post).toMatchObject({ display_name: 'Maya Chen', username: 'maya', role: 'admin' });
    expect(post.password).toBeUndefined();
    expect(within(dialog).getByText(/One-time password: h7Fq-2mKx-9wPr-tB4n/)).toBeTruthy();
    expect(within(dialog).getByText("They'll choose their own password when they first sign in.")).toBeTruthy();
    // Single-user installs switch to team mode first, silently (D19).
    expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/config' && JSON.parse(c.body).set.mode === 'multi')).toBe(true);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(await screen.findByText('Maya Chen')).toBeTruthy();
  });

  it('checks the username and says when it is taken', async () => {
    await open();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add person' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add person' });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: '-x' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('Enter their name.')).toBeTruthy();
    expect(within(dialog).getByText(/^Use up to 32 letters/)).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Alex Morgan' } });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: 'alex' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('That username is taken. Try another.')).toBeTruthy();
  });

  it('changes a role from the menu', async () => {
    const calls = await open();
    const m = await menu('Alex');
    expect(within(m).getByRole('menuitemradio', { name: 'Member' }).getAttribute('aria-checked')).toBe('true');
    await fireEvent.click(within(m).getByRole('menuitemradio', { name: 'Admin' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/users/usr_01jalex000000000000000000' && JSON.parse(c.body).role === 'admin')).toBe(true));
  });

  it('sets and clears a personal spending limit by username', async () => {
    const calls = await open();
    let m = await menu('Alex');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Spending limit…' }));
    let dialog = await screen.findByRole('dialog', { name: 'Spending limit' });
    expect(within(dialog).getByText('Members without their own limit can spend up to $2.00 a day.')).toBeTruthy();
    await fireEvent.click(within(dialog).getByLabelText('No personal limit'));
    await fireEvent.input(within(dialog).getByLabelText('Daily limit for Alex'), { target: { value: '3' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.per_user_daily_usd.alex': 3 }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Spending limit' })).toBeNull());

    m = await menu('Alex');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Spending limit…' }));
    dialog = await screen.findByRole('dialog', { name: 'Spending limit' });
    expect((within(dialog).getByLabelText('Daily limit for Alex') as HTMLInputElement).value).toBe('3.00');
    await fireEvent.click(within(dialog).getByLabelText('No personal limit'));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.per_user_daily_usd.alex': null }));
  });

  it('turns an account off only after the confirmation, and back on', async () => {
    const calls = await open();
    let m = await menu('Priya Rao');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Turn off account…' }));
    const dialog = await screen.findByRole('dialog', { name: "Turn off Priya Rao's account?" });
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Turn off account' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/users/usr_01jpriya00000000000000000')).toBe(true));
    expect(await screen.findByText('Admin · Turned off', { selector: 'p' })).toBeTruthy();
    m = await menu('Priya Rao');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Turn on account' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && JSON.parse(c.body).disabled === false)).toBe(true));
  });

  it('resets a password to a new one-time password', async () => {
    await open();
    const m = await menu('Jordan Kim');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Reset password…' }));
    const dialog = await screen.findByRole('dialog', { name: "Reset Jordan Kim's password?" });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Reset password' }));
    expect(await within(dialog).findByText(/One-time password: Wd4k-7RmP-q9Xz-3hTn/)).toBeTruthy();
    await waitFor(() => expect(screen.getByRole('dialog', { name: 'New password for Jordan Kim' })).toBe(dialog));
  });

  it("opens Add person when setup's \"Add your team\" sends someone here", async () => {
    router.navigate('/settings/people?add=1');
    await open();
    expect(await screen.findByRole('dialog', { name: 'Add person' })).toBeTruthy();
    expect(router.search).toBe('');
  });

  it('invites the owner to add people when there is nobody else', async () => {
    await open('people-solo');
    expect(await screen.findByText("It's just you so far. Add teammates so they get their own sign-in.")).toBeTruthy();
    expect(screen.getAllByRole('button', { name: 'Add person' })).toHaveLength(1);
  });
});
