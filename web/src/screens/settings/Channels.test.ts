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

  // The gateway lets only the owner link a chat account to someone else.
  it('offers an admin no one to link to but themselves', async () => {
    const calls = installApi({ override: (m, url) => (url === '/api/users' ? [member, owner] : undefined) });
    await session.init();
    session.me = { ...session.me!, mode: 'multi', user: { ...session.me!.user, role: 'admin' } };
    render(Channels);
    const link = await screen.findByRole('button', { name: 'Link' });
    expect(screen.queryByRole('combobox', { name: 'Link to user' })).toBeNull();
    expect(calls.some((c) => c.url === '/api/users')).toBe(false);
    await fireEvent.click(link);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))!;
    expect(JSON.parse(patch.body)).toEqual({ status: 'linked', user_id: session.me!.user.id });
  });
});
