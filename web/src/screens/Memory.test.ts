import { render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { router } from '../lib/router.svelte';
import { session } from '../lib/stores/session.svelte';
import { installApi } from '../test/api';
import Memory from './Memory.svelte';

describe('Memory editor', () => {
  it('keeps the editor on the file of the selected tab when loads finish out of order', async () => {
    let releaseUser: () => void = () => {};
    const userGate = new Promise<void>((r) => (releaseUser = r));
    installApi();
    await session.init();
    const base = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === '/api/memory/USER') await userGate;
      return base(input, init);
    }) as typeof fetch;
    router.navigate('/memory');
    render(Memory);
    await screen.findByText(/Facts and preferences/);
    router.setQuery({ file: 'USER' });
    await new Promise((r) => setTimeout(r, 0));
    router.setQuery({ file: 'HEARTBEAT' });
    const editor = () => (screen.getByLabelText('Heartbeat') as HTMLTextAreaElement).value;
    await waitFor(() => expect(editor()).toContain('dentist'));
    releaseUser();
    await new Promise((r) => setTimeout(r, 20));
    expect(editor()).toContain('dentist');
  });
});
