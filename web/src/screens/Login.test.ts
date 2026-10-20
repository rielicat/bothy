import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../mock/server';
import { away } from '../lib/away';
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
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Sign in to Bothy');
    await signIn('sam', 'Granite-River-42-Lantern');
    await waitFor(() => expect(session.me?.user.username).toBe('sam'));
  });

  it('sends a forgotten password to an admin and offers no reset it cannot do', () => {
    render(Login);
    expect(screen.getByText('Forgot your password? Ask an admin.')).toBeTruthy();
    expect(screen.queryByText(/Forgot it/)).toBeNull();
    expect(screen.queryByRole('link', { name: /reset|forgot/i })).toBeNull();
    expect(screen.getByText(location.host)).toBeTruthy();
  });

  it('shows and hides the password with the eye in the field', async () => {
    render(Login);
    const field = screen.getByLabelText('Password') as HTMLInputElement;
    const eye = screen.getByRole('button', { name: 'Show password' });
    expect(eye.getAttribute('aria-controls')).toBe(field.id);
    await fireEvent.click(eye);
    expect(field.type).toBe('text');
    await fireEvent.click(screen.getByRole('button', { name: 'Hide password' }));
    expect(field.type).toBe('password');
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
    expect(screen.getByText((_, el) => el?.tagName === 'CODE' && el.textContent === 'ssh -L 8080:127.0.0.1:8080 <host>')).toBeTruthy();
    // The placeholder is explained, and the address is opened on this computer.
    expect(screen.getByText(/For safety, setup only works on the computer Bothy runs on\. Run this on your own computer/)).toBeTruthy();
    expect(screen.getByText(/^\s*Replace/).textContent!.replace(/\s+/g, ' ').trim()).toBe("Replace <host> with your server's address, then open http://localhost:8080/setup in your browser.");
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

  it('says the one-time password typed again after a reload is not a new one', async () => {
    await start('', '/password');
    installApi({ login: false, override: (m, url) => (m === 'POST' && url === '/api/auth/password' ? new Response(JSON.stringify({ error: { code: 'bad_request', message: 'the new password must differ from the current one' } }), { status: 400 }) : undefined) });
    render(ChangePassword);
    await fireEvent.input(screen.getByLabelText('One-time password'), { target: { value: 'Granite-River-42-Lantern' } });
    await fireEvent.input(screen.getByLabelText('New password'), { target: { value: 'Granite-River-42-Lantern' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(await screen.findByText('Choose a new password, not the one-time password.')).toBeTruthy();
    expect(screen.queryByText("That one-time password didn't work. Ask an admin for a new one.")).toBeNull();
  });

  it('says when too many wrong one-time passwords locked the account', async () => {
    await start('', '/password');
    installApi({ login: false, override: (m, url) => (m === 'POST' && url === '/api/auth/password' ? new Response(JSON.stringify({ error: { code: 'rate_limited', message: 'account temporarily locked: try again in 4m0s' } }), { status: 429 }) : undefined) });
    render(ChangePassword);
    await fireEvent.input(screen.getByLabelText('One-time password'), { target: { value: 'Granite-River-42-Lantern' } });
    await fireEvent.input(screen.getByLabelText('New password'), { target: { value: 'a much longer phrase' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(await screen.findByText('Too many tries. Try again in 4 minutes.')).toBeTruthy();
    expect(screen.queryByText('Use at least 12 characters.')).toBeNull();
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

describe('sign in with Google or GitHub (§5.9)', () => {
  const real = away.to;
  let left: string[] = [];
  beforeEach(() => {
    left = [];
    away.to = (url: string) => void left.push(url);
  });
  afterEach(() => {
    away.to = real;
  });

  it('offers no provider until an admin turns one on', async () => {
    const calls = await start();
    render(Login);
    await waitFor(() => expect(calls.some((c) => c.url === '/api/auth/methods')).toBe(true));
    expect(screen.queryByRole('button', { name: /^Continue with/ })).toBeNull();
    expect(screen.queryByText('or sign in with a password')).toBeNull();
  });

  it('puts the providers above the form and leaves for the one chosen, carrying next', async () => {
    const calls = await start('signin-google,signin-github', '/login?next=%2Fapprovals');
    render(Login);
    const google = await screen.findByRole('button', { name: 'Continue with Google' });
    expect(screen.getByRole('button', { name: 'Continue with GitHub' })).toBeTruthy();
    // Each provider button carries its own mark (§5.9), from the sprite.
    expect(google.querySelector('use')?.getAttribute('href')).toBe('#i-google');
    expect(screen.getByRole('button', { name: 'Continue with GitHub' }).querySelector('use')?.getAttribute('href')).toBe('#i-github');
    expect(screen.getByText('or sign in with a password')).toBeTruthy();
    expect(google.compareDocumentPosition(screen.getByLabelText('Username')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await fireEvent.click(google);
    await waitFor(() => expect(left).toEqual(['/api/mock/oauth/google?state=mock']));
    expect(JSON.parse(calls.find((c) => c.url === '/api/auth/oauth/google/start')!.body)).toEqual({ next: '/approvals' });
  });

  it.each([
    ['cancelled', 'google', '', 'Signing in with Google was cancelled.'],
    ['expired', 'github', '', 'That sign-in took too long or was started in another tab. Try again.'],
    ['not_allowed', 'google', 'kim@gmail.com', "kim@gmail.com isn't on this Bothy's sign-in list. Ask an admin to add you."],
    ['not_allowed', 'github', 'kim-gh', "@kim-gh isn't on this Bothy's sign-in list. Ask an admin to add you."],
    ['not_allowed', 'google', '<b>call 555</b>', "That Google account isn't on this Bothy's sign-in list. Ask an admin to add you."],
    ['org_access', 'github', '', "Your GitHub organization hasn't approved Bothy yet. Ask an organization owner to approve it, then try again."],
    ['no_account', 'google', '', 'No Bothy account uses that Google account yet. Sign in with your password and connect it in Account, or ask an admin.'],
    ['disabled', 'github', '', 'This account is turned off. Ask an admin to turn it back on.'],
    ['unavailable', 'google', '', "Google sign-in isn't working right now. Sign in with your username and password, or ask an admin."],
  ])('says why %s (%s) came back, once', async (code, provider, who, text) => {
    const q = new URLSearchParams({ signin_error: code, provider, next: '/chat/ses_1' });
    // The server names a refused account in a short-lived cookie, never in the address.
    if (who) document.cookie = `bothy_signin_who=${encodeURIComponent(who)}; Path=/`;
    await start('', `/login?${q}`);
    render(Login);
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe(text);
    // A reload doesn't repeat the reason; where to go next stays.
    expect(router.query.has('signin_error')).toBe(false);
    expect(router.query.get('next')).toBe('/chat/ses_1');
    expect(document.cookie).not.toContain('bothy_signin_who');
    expect(screen.queryByText('You were signed out. Sign in again to continue.')).toBeNull();
  });

  it('never names an account from the address', async () => {
    await start('', `/login?${new URLSearchParams({ signin_error: 'not_allowed', provider: 'google', who: 'ceo@acme.com' })}`);
    render(Login);
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe("That Google account isn't on this Bothy's sign-in list. Ask an admin to add you.");
  });

  it('leaves the refused account for a refusal only', async () => {
    document.cookie = 'bothy_signin_who=kim%40gmail.com; Path=/';
    await start('', '/login?signin_error=expired&provider=google');
    render(Login);
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe('That sign-in took too long or was started in another tab. Try again.');
    document.cookie = 'bothy_signin_who=; Max-Age=0; Path=/';
  });

  it("sends people to Bothy's own address when they opened another", async () => {
    await start('signin-google,wrong-address');
    render(Login);
    await fireEvent.click(await screen.findByRole('button', { name: 'Continue with Google' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Open Bothy at bothy.acme.com to sign in with Google.');
    await fireEvent.click(screen.getByRole('button', { name: 'Go there' }));
    expect(left).toEqual(['https://bothy.acme.com/login']);
  });

  it('says when there were too many tries', async () => {
    installApi({
      login: false,
      override: (m, url) =>
        m === 'POST' && url === '/api/auth/oauth/google/start'
          ? new Response(JSON.stringify({ error: { code: 'rate_limited', message: 'too many sign-in attempts' } }), { status: 429 })
          : undefined,
    });
    resetMock({ login: false, mock: 'signin-google' });
    session.me = null;
    await session.init();
    router.navigate('/login');
    render(Login);
    await fireEvent.click(await screen.findByRole('button', { name: 'Continue with Google' }));
    expect((await screen.findByRole('alert')).textContent?.trim()).toBe('Too many tries. Wait a minute, then try again.');
    expect(left).toEqual([]);
  });
});
