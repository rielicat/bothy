import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { api, setCsrf } from '../../lib/api/client';
import type { MemoryDoc } from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import Setup from './Setup.svelte';

describe('first-run wizard', () => {
  let calls: Call[];

  beforeEach(async () => {
    sessionStorage.clear();
    calls = installApi({ setup: true });
    session.me = null;
    await session.init();
  });

  it('claims the token from the URL fragment and strips it at once', async () => {
    history.replaceState(null, '', '/setup#t=demo-setup-token');
    render(Setup);
    expect(location.hash).toBe('');
    expect(await screen.findByText('Create your account')).toBeTruthy();
    const claim = calls.find((c) => c.url === '/api/setup/claim');
    expect(JSON.parse(claim!.body)).toEqual({ token: 'demo-setup-token' });
    expect(calls.every((c) => !c.url.includes('demo-setup-token'))).toBe(true);
  });

  it('explains where to find the token and reports a bad one', async () => {
    history.replaceState(null, '', '/setup');
    render(Setup);
    // Compose (the recommended deployment) names the container bothy-bothy-1,
    // and the boot banner goes to stderr.
    expect(screen.getByText('docker compose logs bothy 2>&1 | grep setup')).toBeTruthy();
    expect(screen.getByText('docker compose exec bothy /bothy setup-token')).toBeTruthy();
    expect(screen.getByText('docker logs bothy 2>&1 | grep setup')).toBeTruthy();
    expect(screen.queryByText('docker exec bothy /bothy setup-token')).toBeNull();
    await fireEvent.input(screen.getByLabelText('Setup token'), { target: { value: 'nope' } });
    await fireEvent.submit(screen.getByLabelText('Setup token').closest('form')!);
    expect((await screen.findByRole('alert')).textContent).toMatch(/not valid/);
  });

  it('resumes at the account step after a reload, with the grant CSRF token', async () => {
    history.replaceState(null, '', '/setup#t=demo-setup-token');
    const first = render(Setup);
    await screen.findByText('Create your account');
    first.unmount();

    // a reload: module memory is gone, the server says the grant is claimed
    setCsrf('');
    await session.init();
    expect(session.setup?.claimed).toBe(true);
    render(Setup);
    expect(await screen.findByText('Create your account')).toBeTruthy();
    await fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'sam' } });
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'Granite-River-42-Lantern' } });
    await fireEvent.submit(screen.getByLabelText('Username').closest('form')!);
    await waitFor(() => expect(calls.some((c) => c.url === '/api/setup/owner')).toBe(true));
    const owner = calls.find((c) => c.url === '/api/setup/owner')!;
    expect(owner.headers['x-csrf-token']).toBe('mock-csrf');
    expect(await screen.findByText('Connect a model')).toBeTruthy();
    expect(sessionStorage.getItem('bothy.setup.csrf')).toBeNull();
  });

  it('strips a mangled fragment without breaking the page', async () => {
    history.replaceState(null, '', '/setup#t=%E0%A4%A');
    render(Setup);
    expect(location.hash).toBe('');
    expect((await screen.findByRole('alert')).textContent).toMatch(/not valid/);
  });
});

describe('first-run wizard after Finish', () => {
  let calls: Call[];

  beforeEach(async () => {
    sessionStorage.clear();
    sessionStorage.setItem('bothy.setup.step', '5');
    calls = installApi({ setup: true, override: (_m, url) => (url === '/api/setup/state' ? { needs_setup: true, claimed: true, owner_set: true, sandbox_available: true } : undefined) });
    await api.post('/api/auth/login', { username: 'sam', password: 'Granite-River-42-Lantern' });
    session.me = null;
    session.finishing = false;
    await session.init();
  });

  const finish = async () => {
    await fireEvent.click(await screen.findByRole('button', { name: 'Finish setup' }));
    expect(await screen.findByText('Bothy is ready')).toBeTruthy();
  };

  it('lets the owner go back to a step and finish again', async () => {
    render(Setup);
    await fireEvent.input(await screen.findByLabelText(/call you/), { target: { value: 'Sam' } });
    await finish();
    expect(session.setup?.needs_setup).toBe(false);
    expect(session.finishing).toBe(true);

    await fireEvent.click(screen.getByRole('button', { name: 'Make it yours' }));
    expect((screen.getByLabelText(/call you/) as HTMLInputElement).value).toBe('Sam');
    await finish();
    expect(calls.filter((c) => c.url === '/api/setup/complete')).toHaveLength(1);
    expect(screen.queryByRole('alert')).toBeNull();

    const user = await api.get<MemoryDoc>('/api/memory/USER');
    expect(user.content.split('\n').filter((l) => l === '- Call me Sam.')).toHaveLength(1);

    await fireEvent.click(screen.getByRole('button', { name: 'Make it yours' }));
    await screen.findByRole('button', { name: 'Finish setup' });
    expect((screen.getByRole('button', { name: 'Done' }) as HTMLButtonElement).disabled).toBe(false);
  });
});
