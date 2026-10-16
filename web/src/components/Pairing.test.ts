import { render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { session } from '../lib/stores/session.svelte';
import { installApi } from '../test/api';
import { FakeEventSource } from '../test/setup';
import Pairing from './Pairing.svelte';

const notice = "That code was shared in a group, so it no longer works. Here's a new one. Send it in a direct message.";

describe('Pairing', () => {
  it('replaces the code Bothy cancelled after it was posted in a group', async () => {
    const calls = installApi();
    await session.init();
    render(Pairing, { channel: 'telegram', auto: true });
    expect(await screen.findByText('/pair 7KQ2-MX4D')).toBeTruthy();
    const made = () => calls.filter((c) => c.method === 'POST' && c.url === '/api/pairing').length;
    expect(made()).toBe(1);

    FakeEventSource.last?.emit('pairing.burned', { type: 'pairing.burned', data: { channel: 'telegram', code: 'ZZZZ-ZZZZ' } });
    await new Promise((r) => setTimeout(r, 20));
    expect(made()).toBe(1);
    expect(screen.queryByText(notice)).toBeNull();

    FakeEventSource.last?.emit('pairing.burned', { type: 'pairing.burned', data: { channel: 'telegram', code: '7KQ2-MX4D' } });
    await waitFor(() => expect(made()).toBe(2));
    expect(await screen.findByText(notice)).toBeTruthy();
    expect(screen.getByText('/pair 7KQ2-MX4D')).toBeTruthy();
  });
});
