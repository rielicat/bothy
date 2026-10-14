import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { setCsrf } from '../../lib/api/client';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Settings from './Settings.svelte';

async function open(opts: Parameters<typeof installApi>[0] = {}, role?: string) {
  const calls = installApi(opts);
  session.ready = false;
  await session.init();
  setCsrf('mock-csrf');
  if (role) session.me = { ...session.me!, user: { ...session.me!.user, role } };
  render(Settings, { params: { group: 'account', page: 'tokens' } });
  await screen.findByText('Linked chat apps');
  return calls;
}

describe('Settings → Account', () => {
  it('shows a new token once, with how to use it behind Details', async () => {
    const calls = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create token' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(await within(dialog).findByText('Enter a name.')).toBeTruthy();

    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Laptop' } });
    await fireEvent.click(within(dialog).getByLabelText('Read your chats and activity'));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create token' }));
    await within(dialog).findByText("Copy this token now. It won't be shown again.");
    const post = calls.find((c) => c.method === 'POST' && c.url === '/api/tokens')!;
    expect(JSON.parse(post.body)).toMatchObject({ name: 'Laptop', scopes: ['chat', 'read'], ttl: 90 * 86_400e9, on_approval: 'deny' });
    expect(within(dialog).getByText(/^bothy_pat_/)).toBeTruthy();
    expect(within(dialog).queryByText(/curl/)).toBeNull();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Details' }));
    expect(within(dialog).getAllByText(/curl/).length).toBeGreaterThan(0);

    await fireEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(await screen.findByText('Laptop')).toBeTruthy();
    expect(screen.queryByText(/^bothy_pat_/)).toBeNull();
  });

  it('describes tokens in plain words and never says "never"', async () => {
    await open();
    expect(await screen.findByText('Can chat · Last used 3 h ago')).toBeTruthy();
    expect(screen.getByText('Can chat and read · Not used yet')).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/\bnever\b/i);
  });

  it('revokes a token only after the confirmation', async () => {
    const calls = await open();
    await fireEvent.click((await screen.findAllByRole('button', { name: /^Revoke .+…$/ }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Revoke Open WebUI?' });
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke token' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url.startsWith('/api/tokens/'))).toBe(true));
    await waitFor(() => expect(screen.queryByText('Open WebUI')).toBeNull());
  });

  it('unlinks a chat app and offers to link it again', async () => {
    const calls = await open();
    await screen.findByText('@sam_k');
    await fireEvent.click(screen.getByRole('button', { name: 'Unlink Telegram' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/identities/telegram/5512')).toBe(true));
    expect(await screen.findByText('Not linked')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Link Telegram' }));
    expect(await screen.findByRole('dialog', { name: 'Link Telegram' })).toBeTruthy();
  });

  it('saves where approval requests reach you', async () => {
    const calls = await open();
    const select = (await screen.findByLabelText('Requests reach you in')) as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toEqual(['Bothy only', 'Telegram']);
    await fireEvent.change(select, { target: { value: 'telegram' } });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/me' && JSON.parse(c.body).approvals_in === 'telegram')).toBe(true));
  });

  it('checks the new password before sending it', async () => {
    const calls = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Change password…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Change password' });
    await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'old password!' } });
    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'too short' } });
    await waitFor(() => expect(within(dialog).getByText('3 more characters needed.')).toBeTruthy(), { timeout: 1500 });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    expect(await within(dialog).findByText('Use at least 12 characters.')).toBeTruthy();
    expect(calls.some((c) => c.url === '/api/auth/password')).toBe(false);

    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'a much longer phrase' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/auth/password')).toBe(true));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Change password' })).toBeNull());
  });

  it("says when the current password isn't right", async () => {
    await open({ override: (m, url) => (m === 'POST' && url === '/api/auth/password' ? new Response(JSON.stringify({ error: { code: 'unauthorized', message: 'wrong password' } }), { status: 401 }) : undefined) });
    await fireEvent.click(screen.getByRole('button', { name: 'Change password…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Change password' });
    await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'not it' } });
    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'a much longer phrase' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    expect(await within(dialog).findByText("That's not your current password.")).toBeTruthy();
  });

  it('saves your name, and on servers without a self-service profile only admins can', async () => {
    const calls = await open();
    const name = (await screen.findByLabelText('Name')) as HTMLInputElement;
    await fireEvent.input(name, { target: { value: 'Sam Patel' } });
    await fireEvent.blur(name);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/me' && JSON.parse(c.body).display_name === 'Sam Patel')).toBe(true));
  });

  it('shows a member their name as text when the server has no self-service profile', async () => {
    const notFound = () => new Response(JSON.stringify({ error: { code: 'not_found', message: 'not found' } }), { status: 404 });
    await open({ override: (m, url) => (url === '/api/me' ? notFound() : undefined) }, 'member');
    await waitFor(() => expect(screen.getByText('Only an admin can change it.')).toBeTruthy());
    expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull();
    expect(screen.queryByText('Requests reach you in')).toBeNull();
  });
});
