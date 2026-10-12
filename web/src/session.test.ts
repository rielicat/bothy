import { describe, expect, it } from 'vitest';
import { session } from './lib/stores/session.svelte';
import { installApi } from './test/api';
import { FakeEventSource } from './test/setup';

describe('session', () => {
  it('logs out when the event stream is refused because the session expired', async () => {
    let expired = false;
    installApi({ override: (m, url) => (expired && url === '/api/auth/me' ? new Response('{"error":{"code":"unauthorized","message":"log in"}}', { status: 401 }) : undefined) });
    await session.init();
    expect(session.me).not.toBeNull();
    expired = true;
    FakeEventSource.last.fail(true);
    await new Promise((r) => setTimeout(r, 0));
    expect(session.me).toBeNull();
  });
});
