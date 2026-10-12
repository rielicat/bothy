import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { session } from '../lib/stores/session.svelte';
import { installApi } from '../test/api';
import ChannelSetup from './ChannelSetup.svelte';

describe('ChannelSetup', () => {
  it('shows why the config write was refused', async () => {
    const fields = [{ path: 'channels.telegram.enabled', source: { layer: 'ui' }, message: 'channels.telegram.enabled: locked by BOTHY_CONFIG_LOCK' }];
    installApi({
      override: (m, url) =>
        m === 'PATCH' && url === '/api/config' ? new Response(JSON.stringify({ error: { code: 'bad_request', message: 'invalid' }, fields }), { status: 400 }) : undefined,
    });
    await session.init();
    render(ChannelSetup, { name: 'telegram' });
    await fireEvent.input(screen.getByLabelText('Bot token'), { target: { value: '123:abc' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save and test' }));
    expect((await screen.findByRole('alert')).textContent).toContain('locked by BOTHY_CONFIG_LOCK');
  });
});
