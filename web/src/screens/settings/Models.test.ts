import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { config } from '../../lib/config.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Models from './Models.svelte';

describe('Providers: Test', () => {
  // A provider set to one wire API must be tested over that API, not the
  // default for its type.
  it('tests a provider over its configured api', async () => {
    const calls = installApi();
    await session.init();
    await config.load(true);
    await config.set({ 'providers.openai.api': 'chat' }, true);
    render(Models);
    const row = (await screen.findByRole('checkbox', { name: 'Enable openai' })).parentElement!;
    await fireEvent.click(within(row).getByRole('button', { name: 'Test' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/providers/test')).toBe(true));
    const post = calls.find((c) => c.method === 'POST' && c.url === '/api/providers/test')!;
    expect(JSON.parse(post.body)).toMatchObject({ kind: 'openai', api: 'chat' });
  });
});
