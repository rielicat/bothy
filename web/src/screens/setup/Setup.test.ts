import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { setCsrf } from '../../lib/api/client';
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
    expect(screen.getByText('docker exec bothy /bothy setup-token')).toBeTruthy();
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
