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
      expect(await screen.findByText('Sign in to finish setting up Bothy.')).toBeTruthy();

      await fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'sam' } });
      await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'Granite-River-42-Lantern' } });
      await fireEvent.submit(screen.getByLabelText('Username').closest('form')!);
      await waitFor(() => expect(router.path).toBe('/setup'));
      // The fixture config already has a main model, so setup resumes at its last step.
      expect(await screen.findByText('Bothy is ready')).toBeTruthy();
    });
  }
});

describe('the home address', () => {
  beforeEach(async () => {
    installApi();
    session.ready = false;
    await session.init();
  });

  // "/" is no page of its own; it must never flash "Page not found" on its way.
  it('goes straight on to chat without showing a missing page', async () => {
    const seen: string[] = [];
    const watch = new MutationObserver((records) => {
      for (const r of records) for (const n of r.addedNodes) seen.push(n.textContent ?? '');
      seen.push(document.title);
    });
    watch.observe(document, { childList: true, subtree: true, characterData: true });
    router.navigate('/');
    render(App);
    await waitFor(() => expect(router.path).toBe('/chat'));
    await new Promise((r) => setTimeout(r, 0));
    watch.disconnect();
    expect(seen.filter((t) => t.includes('Page not found'))).toEqual([]);
  });
});

describe('a signed-out browser', () => {
  it('goes to sign-in without loading the screen it asked for', async () => {
    const calls = installApi({ override: (_m, url) => (url === '/api/auth/me' ? new Response('{"error":{"code":"unauthorized","message":"x"}}', { status: 401 }) : undefined) });
    session.ready = false;
    session.me = null;
    await session.init();
    router.navigate('/chat');
    render(App);
    await waitFor(() => expect(router.path).toBe('/login'));
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.filter((c) => /^\/api\/(models|refine|knowledge|github)/.test(c.url))).toEqual([]);
  });
});

describe('a server that does not answer at start', () => {
  for (const [what, answer] of [
    ['restarting', () => new Response('{"error":{"code":"unavailable","message":"x"}}', { status: 503 })],
    [
      'out of reach',
      () => {
        throw new TypeError('Failed to fetch');
      },
    ],
  ] as const) {
    it(`says Bothy isn't responding instead of "signed out" when it is ${what}`, async () => {
      installApi({ override: (_m, url) => (url.startsWith('/api/') ? answer() : undefined) });
      session.ready = false;
      session.me = null;
      await session.init();
      router.navigate('/settings/models');
      render(App);
      expect(await screen.findByText("Bothy isn't responding. It may be restarting.")).toBeTruthy();
      expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
      await new Promise((r) => setTimeout(r, 0));
      expect(router.path).toBe('/settings/models');
      expect(screen.queryByText(/signed out/)).toBeNull();
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

describe('route guard for a one-time password', () => {
  beforeEach(async () => {
    installApi();
    session.ready = false;
    await session.init();
  });

  it('keeps someone who must choose a password on that screen after a reload', async () => {
    session.me = { ...session.me!, user: { ...session.me!.user, must_change_password: true } };
    router.navigate('/approvals');
    render(App);
    await waitFor(() => expect(router.path).toBe('/password'));
    expect(router.query.get('next')).toBe('/approvals');
    expect(await screen.findByText('Choose a new password')).toBeTruthy();
  });
});
