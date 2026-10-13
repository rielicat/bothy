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
