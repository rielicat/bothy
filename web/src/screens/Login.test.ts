import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../mock/server';
import { router } from '../lib/router.svelte';
import { session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import ChangePassword from './ChangePassword.svelte';
import Login from './Login.svelte';

async function start(mock = '', path = '/login'): Promise<Call[]> {
  const calls = installApi({ login: false });
  resetMock({ login: false, mock });
  session.me = null;
  session.finishing = false;
  await session.init();
  router.navigate(path);
  return calls;
}

async function signIn(username: string, password: string) {
  await fireEvent.input(screen.getByLabelText('Username'), { target: { value: username } });
  await fireEvent.input(screen.getByLabelText('Password'), { target: { value: password } });
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
}

describe('sign in (§5.9)', () => {
  beforeEach(() => start());

  it('signs in', async () => {
    render(Login);
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Sign in');
    await signIn('sam', 'Granite-River-42-Lantern');
    await waitFor(() => expect(session.me?.user.username).toBe('sam'));
  });

  it('says a wrong password plainly, above the button, and clears the password', async () => {
    render(Login);
    await signIn('sam', 'wrong');
    expect((await screen.findByRole('alert')).textContent).toBe("That username and password don't match.");
    expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('');
    expect(document.activeElement).toBe(screen.getByLabelText('Password'));
  });

  it.each([
    ['login-locked', 'Too many tries. Try again in 5 minutes.'],
    ['login-disabled', 'This account is turned off. Ask an admin to turn it back on.'],
  ])('maps %s', async (mock, text) => {
    await start(mock);
    render(Login);
    await signIn('sam', 'Granite-River-42-Lantern');
    expect((await screen.findByRole('alert')).textContent).toBe(text);
  });

  it('says why it is asking after a session ended', async () => {
    router.navigate('/login?next=%2Fchat%2Fses_1');
    render(Login);
    expect(screen.getByText('You were signed out. Sign in again to continue.')).toBeTruthy();
  });

  it('shows "Finish setup on the server" when setup routes refuse this computer', async () => {
    await start('remote,setup');
    render(Login);
    expect(await screen.findByText('Finish setup on the server')).toBeTruthy();
    expect(screen.getByText('ssh -L 8080:127.0.0.1:8080 <host>')).toBeTruthy();
    expect(screen.getByText('http://localhost:8080/setup')).toBeTruthy();
    expect(screen.queryByLabelText('Password')).toBeNull();
  });

  it('sends a one-time password to choose a new one, carrying where to go next', async () => {
    await start('must-change', '/login?next=%2Fapprovals');
    render(Login);
    await signIn('jordan', 'TempPass-1234-xyz');
    await waitFor(() => expect(router.path).toBe('/password'));
    expect(router.query.get('next')).toBe('/approvals');
  });
});

describe('choose a new password', () => {
  it('sends the one-time password as the old one and goes on', async () => {
    const calls = await start('must-change', '/login?next=%2Fapprovals');
    const login = render(Login);
    await signIn('jordan', 'TempPass-1234-xyz');
    await waitFor(() => expect(router.path).toBe('/password'));
    login.unmount();
    // The server refuses everything else until the password is replaced, so nothing else is asked yet.
    const background = () => calls.filter((c) => /^\/api\/(inbox|approvals|events|usage)/.test(c.url));
    expect(background()).toEqual([]);

    render(ChangePassword);
    expect(screen.getByText('You signed in with a one-time password. Choose your own to continue.')).toBeTruthy();
    expect(screen.queryByLabelText('One-time password')).toBeNull();
    const field = screen.getByLabelText('New password');
    expect(screen.getByText('At least 12 characters.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(screen.getByText('Use at least 12 characters.')).toBeTruthy();
    await fireEvent.input(field, { target: { value: 'Granite-River-42-Lantern' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }));
    await waitFor(() => expect(router.path).toBe('/approvals'));
    expect(JSON.parse(calls.find((c) => c.url === '/api/auth/password')!.body)).toEqual({ old: 'TempPass-1234-xyz', new: 'Granite-River-42-Lantern' });
    await waitFor(() => expect(background().length).toBeGreaterThan(0));
  });

  it('asks for the one-time password again after a reload', async () => {
    await start('', '/password');
    render(ChangePassword);
    expect(screen.getByLabelText('One-time password')).toBeTruthy();
    await fireEvent.input(screen.getByLabelText('New password'), { target: { value: 'Granite-River-42-Lantern' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(screen.getByText('Enter the one-time password you signed in with.')).toBeTruthy();
  });
});
