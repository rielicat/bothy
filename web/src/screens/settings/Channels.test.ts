import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { member, owner } from '../../../mock/fixtures';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Channels from './Channels.svelte';

describe('Chat apps: pending senders', () => {
  // Linking hands the chat account that person's chats, memory and vault, so
  // the request must name exactly the person the admin picked.
  it('links a sender only to the person chosen in household mode', async () => {
    const calls = installApi({ override: (m, url) => (url === '/api/users' ? [member, owner] : undefined) });
    await session.init();
    session.me = { ...session.me!, mode: 'multi' };
    render(Channels);
    const link = await screen.findByRole('button', { name: 'Link' });
    const pick = await screen.findByRole('combobox', { name: 'Link to user' });
    expect((pick as HTMLSelectElement).value).toBe('');
    expect((link as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.change(pick, { target: { value: member.id } });
    await fireEvent.click(link);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))!;
    expect(JSON.parse(patch.body)).toEqual({ status: 'linked', user_id: member.id });
  });
});

describe('Chat apps: switches the server refuses', () => {
  const refuse = () => new Response('{"error":{"code":"internal","message":"could not save"}}', { status: 500 });

  it('puts a channel switch back when the config change fails', async () => {
    const calls = installApi({ override: (m, url) => (m === 'PATCH' && url === '/api/config' ? refuse() : undefined) });
    await session.init();
    render(Channels);
    const box = (await screen.findByRole('checkbox', { name: 'Enable Discord' })) as HTMLInputElement;
    const was = box.checked;
    await fireEvent.click(box);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/config')).toBe(true));
    await waitFor(() => expect(box.checked).toBe(was));
  });

  it('puts a group chat switch back when the change fails', async () => {
    const calls = installApi({ override: (m, url) => (m === 'PATCH' && url.startsWith('/api/spaces/') ? refuse() : undefined) });
    await session.init();
    render(Channels);
    const box = (await screen.findAllByRole('checkbox', { name: 'On' }))[0] as HTMLInputElement;
    const was = box.checked;
    await fireEvent.click(box);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/spaces/'))).toBe(true));
    await waitFor(() => expect(box.checked).toBe(was));
  });
});
