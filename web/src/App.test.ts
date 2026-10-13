import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';
import { router } from './lib/router.svelte';
import { session } from './lib/stores/session.svelte';
import { installApi } from './test/api';

// The owner exists but setup is not finished, and this browser has no
// session: another origin (localhost vs 127.0.0.1), another browser, or an
// expired cookie.
describe('route guard during setup', () => {
  beforeEach(async () => {
    sessionStorage.clear();
    installApi({ setup: true, override: (_m, url) => (url === '/api/setup/state' ? { needs_setup: true, claimed: true, owner_set: true, sandbox_available: true } : undefined) });
    session.ready = false;
    session.me = null;
    await session.init();
  });

  for (const path of ['/', '/setup', '/chat']) {
    it(`sends a signed-out owner from ${path} to log in, then back to setup`, async () => {
      router.navigate(path);
      render(App);
      await waitFor(() => expect(router.path).toBe('/login'));
      expect(router.query.get('next')).toBe('/setup');
      expect(screen.getByText('Log in to finish setting up Bothy.')).toBeTruthy();

      await fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'sam' } });
      await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'Granite-River-42-Lantern' } });
      await fireEvent.submit(screen.getByLabelText('Username').closest('form')!);
      await waitFor(() => expect(router.path).toBe('/setup'));
      expect(await screen.findByText('Connect a model')).toBeTruthy();
    });
  }
});

describe('route guard after the wizard finished', () => {
  beforeEach(async () => {
    installApi();
    session.ready = false;
    await session.init();
  });

  it('keeps the wizard open on its last step, then lets the app routes through', async () => {
    session.setup = { needs_setup: false, claimed: true, owner_set: true, sandbox_available: true };
    session.finishing = true;
    router.navigate('/setup');
    render(App);
    await new Promise((r) => setTimeout(r, 20));
    expect(router.path).toBe('/setup');
    session.finishing = false;
    router.navigate('/chat');
    await new Promise((r) => setTimeout(r, 20));
    expect(router.path).toBe('/chat');
  });
});
