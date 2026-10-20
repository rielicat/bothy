import { waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import { attention } from './attention.svelte';

describe('attention dot', () => {
  // Approving a new server's actions clears its banner but sends no
  // mcp.pin_pending, so the Connections dot stayed until a reload.
  it('looks again when a banner changes', async () => {
    let state = 'pins_pending';
    installApi({ override: (m, url) => (m === 'GET' && url === '/api/mcp' ? [{ name: 'acme-helpdesk', transport: 'http', state, protocol: '', tools: 2, pending: state === 'pins_pending' ? 2 : 0, since: '' }] : undefined) });
    session.ready = false;
    await session.init();
    await attention.refresh();
    expect(attention.tools).toBe(true);
    const off = attention.listen();
    try {
      state = 'ready';
      FakeEventSource.last.emit('banner', { seq: 2, type: 'banner', at: new Date().toISOString(), data: { level: 'warn', code: 'mcp_pins', text: '', clear: true } });
      await waitFor(() => expect(attention.tools).toBe(false), { timeout: 3000 });
    } finally {
      off();
    }
  });
});
