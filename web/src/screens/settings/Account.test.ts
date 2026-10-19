import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetMock } from '../../../mock/server';
import { toaster } from '../../components/ui';
import { setCsrf } from '../../lib/api/client';
import { away } from '../../lib/away';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Settings from './Settings.svelte';

async function open(opts: Parameters<typeof installApi>[0] = {}, role?: string) {
  const calls = installApi(opts);
  session.ready = false;
  await session.init();
  setCsrf('mock-csrf');
  if (role) session.me = { ...session.me!, user: { ...session.me!.user, role } };
  render(Settings, { params: { group: 'account', page: 'tokens' } });
  await screen.findByText('API access');
  return calls;
}

// localZone() reads the browser's zone from here.
function browserIn(timeZone: string) {
  const real = Intl.DateTimeFormat.prototype.resolvedOptions;
  vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockImplementation(function (this: Intl.DateTimeFormat) {
    return { ...real.call(this), timeZone };
  });
}

describe('Settings → Account', () => {
  afterEach(() => vi.restoreAllMocks());

  it('shows a new token once, with how to use it behind Details', async () => {
    const calls = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create token' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(await within(dialog).findByText('Enter a name.')).toBeTruthy();

    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Laptop' } });
    await fireEvent.click(within(dialog).getByLabelText('Read your chats and activity'));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create token' }));
    await within(dialog).findByText("Copy this token now. It won't be shown again.");
    const post = calls.find((c) => c.method === 'POST' && c.url === '/api/tokens')!;
    expect(JSON.parse(post.body)).toMatchObject({ name: 'Laptop', scopes: ['chat', 'read'], ttl: 90 * 86_400e9, on_approval: 'deny' });
    expect(within(dialog).getByText(/^bothy_pat_/)).toBeTruthy();
    expect(within(dialog).queryByText(/curl/)).toBeNull();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Details' }));
    expect(within(dialog).getAllByText(/curl/).length).toBeGreaterThan(0);

    await fireEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(await screen.findByText('Laptop')).toBeTruthy();
    expect(screen.queryByText(/^bothy_pat_/)).toBeNull();
  });

  it('leads members to their own saved keys', async () => {
    await open({}, 'member');
    expect(screen.getByText('Your own keys for connected tools, used instead of the shared ones when Bothy works for you.')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Manage your keys' }).getAttribute('href')).toBe('/settings/account/keys');
  });

  it('describes tokens in plain words and never says "never"', async () => {
    await open();
    // The fixture was used three hours ago, which reads "yesterday …" just after midnight.
    expect(await screen.findByText(/^Can chat · Last used (3 h ago|yesterday)/i)).toBeTruthy();
    expect(screen.getByText('Can chat and read · Not used yet')).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/\bnever\b/i);
  });

  it('revokes a token only after the confirmation', async () => {
    const calls = await open();
    await fireEvent.click((await screen.findAllByRole('button', { name: /^Revoke .+…$/ }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Revoke Open WebUI?' });
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke token' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url.startsWith('/api/tokens/'))).toBe(true));
    await waitFor(() => expect(screen.queryByText('Open WebUI')).toBeNull());
  });

  it('has no chat app linking or approval delivery: the profile is name and time zone', async () => {
    const calls = await open();
    await screen.findByLabelText('Name');
    expect(screen.getByText('Sign out')).toBeTruthy();
    expect(screen.queryByText('Linked chat apps')).toBeNull();
    expect(screen.queryByText('Approvals')).toBeNull();
    expect(screen.queryByLabelText('Requests reach you in')).toBeNull();
    expect(calls.some((c) => c.url.startsWith('/api/identities') || c.url.startsWith('/api/pairing'))).toBe(false);
  });

  it('checks the new password before sending it', async () => {
    const calls = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Change password…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Change password' });
    await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'old password!' } });
    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'too short' } });
    await waitFor(() => expect(within(dialog).getByText('3 more characters needed.')).toBeTruthy(), { timeout: 1500 });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    expect(await within(dialog).findByText('Use at least 12 characters.')).toBeTruthy();
    expect(calls.some((c) => c.url === '/api/auth/password')).toBe(false);

    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'a much longer phrase' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/auth/password')).toBe(true));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Change password' })).toBeNull());
  });

  it("says when the current password isn't right", async () => {
    await open({ override: (m, url) => (m === 'POST' && url === '/api/auth/password' ? new Response(JSON.stringify({ error: { code: 'unauthorized', message: 'wrong password' } }), { status: 401 }) : undefined) });
    await fireEvent.click(screen.getByRole('button', { name: 'Change password…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Change password' });
    await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'not it' } });
    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'a much longer phrase' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    expect(await within(dialog).findByText("That's not your current password.")).toBeTruthy();
  });

  // The server answers 400 for a wrong current password and for the same one again, 429 once locked.
  it.each([
    [400, 'the current password is incorrect', 'Current password', "That's not your current password."],
    [400, 'the new password must differ from the current one', 'New password', 'Choose a password different from your current one.'],
    [429, 'account temporarily locked: try again in 4m0s', '', 'Too many tries. Try again in 4 minutes.'],
  ])('maps a %i "%s" to its field', async (status, message, field, text) => {
    await open({ override: (m, url) => (m === 'POST' && url === '/api/auth/password' ? new Response(JSON.stringify({ error: { code: 'x', message } }), { status }) : undefined) });
    await fireEvent.click(screen.getByRole('button', { name: 'Change password…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Change password' });
    await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'a long old password' } });
    await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'a much longer phrase' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save password' }));
    expect(await within(dialog).findByText(text)).toBeTruthy();
    if (field) expect(within(dialog).getByLabelText(field).getAttribute('aria-describedby')).toMatch(/error/);
    expect(within(dialog).queryByText('Use at least 12 characters.')).toBeNull();
  });

  it('offers the /v1 instructions only to tokens /v1 takes, and caps the name', async () => {
    await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create token' });
    expect(within(dialog).getByLabelText('Name').getAttribute('maxlength')).toBe('64');
    expect(within(dialog).getByText('It can also change or delete your chats, memory, schedules and saved keys.')).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Reader' } });
    await fireEvent.click(within(dialog).getByLabelText(/^Chat with Bothy/));
    await fireEvent.click(within(dialog).getByLabelText('Read your chats and activity'));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create token' }));
    await within(dialog).findByText("Copy this token now. It won't be shown again.");
    expect(within(dialog).queryByRole('button', { name: 'Details' })).toBeNull();
  });

  it('saves your name through your own profile', async () => {
    const calls = await open();
    const name = (await screen.findByLabelText('Name')) as HTMLInputElement;
    await fireEvent.input(name, { target: { value: 'Sam Patel' } });
    await fireEvent.blur(name);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/me' && JSON.parse(c.body).display_name === 'Sam Patel')).toBe(true));
    expect(calls.some((c) => c.url.startsWith('/api/users/'))).toBe(false);
  });

  it('lets a member rename themselves', async () => {
    const calls = await open({}, 'member');
    const name = (await screen.findByLabelText('Name')) as HTMLInputElement;
    await fireEvent.input(name, { target: { value: 'Alex Kim' } });
    await fireEvent.keyDown(name, { key: 'Enter' });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/me' && JSON.parse(c.body).display_name === 'Alex Kim')).toBe(true));
    expect(screen.queryByText('Only an admin can change it.')).toBeNull();
  });

  it("says why the server won't take a name, such as one a colleague has", async () => {
    const refused = () => new Response(JSON.stringify({ error: { code: 'bad_request', message: 'invalid: someone else here goes by "Priya Rao"' } }), { status: 400 });
    await open({ override: (m, url) => (m === 'PATCH' && url === '/api/me' ? refused() : undefined) }, 'member');
    const name = (await screen.findByLabelText('Name')) as HTMLInputElement;
    await fireEvent.input(name, { target: { value: 'Priya Rao' } });
    await fireEvent.blur(name);
    expect(await screen.findByText("Bothy can't use that name. Choose one nobody else here has, up to 64 characters.")).toBeTruthy();
  });

  it("shows your own time zone, offers the browser's and saves a new one to your profile", async () => {
    browserIn('America/Santiago');
    const calls = await open({}, 'member');
    const box = (await screen.findByRole('combobox', { name: 'Time zone' })) as HTMLInputElement;
    expect(box.value).toBe('London (Europe)');
    expect(screen.getByText('Bothy uses it in your chats and for new schedules.')).toBeTruthy();
    expect(screen.getByText(/This browser uses Santiago \(America\)\./)).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Use it' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/me' && JSON.parse(c.body).timezone === 'America/Santiago')).toBe(true));
    await waitFor(() => expect(screen.queryByText(/This browser uses/)).toBeNull());
    expect(box.value).toBe('Santiago (America)');
    // New schedules start in session.me's zone.
    await waitFor(() => expect(session.me?.user.timezone).toBe('America/Santiago'));

    await fireEvent.input(box, { target: { value: 'tokyo' } });
    await fireEvent.keyDown(box, { key: 'Enter' });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/me' && JSON.parse(c.body).timezone === 'Asia/Tokyo')).toBe(true));
    expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/config'))).toBe(false);
  });

  it("offers nothing when the browser's time zone is already yours", async () => {
    browserIn('Europe/London');
    await open();
    await screen.findByRole('combobox', { name: 'Time zone' });
    expect(screen.queryByText(/This browser uses/)).toBeNull();
  });

  it("keeps your time zone and says so when the server won't take it", async () => {
    const refused = () => new Response(JSON.stringify({ error: { code: 'bad_request', message: 'invalid: time zone must be an IANA name, like Europe/London' } }), { status: 400 });
    browserIn('America/Santiago');
    await open({ override: (m, url) => (m === 'PATCH' && url === '/api/me' ? refused() : undefined) });
    await fireEvent.click(await screen.findByRole('button', { name: 'Use it' }));
    expect(await screen.findByText("Bothy doesn't know that time zone. Choose one from the list.")).toBeTruthy();
    expect((screen.getByRole('combobox', { name: 'Time zone' }) as HTMLInputElement).value).toBe('London (Europe)');
  });
});

describe('Settings → Account → Sign-in methods', () => {
  const real = away.to;
  let left: string[] = [];
  beforeEach(() => {
    left = [];
    away.to = (url: string) => void left.push(url);
    toaster.dismiss();
  });
  afterEach(() => {
    away.to = real;
    router.navigate('/settings/account', { replace: true });
  });

  async function openWith(mock: string, path = '/settings/account') {
    const calls = installApi();
    resetMock({ mock });
    router.navigate(path, { replace: true });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    render(Settings, { params: { group: 'account', page: 'tokens' } });
    await screen.findByText('API access');
    return calls;
  }

  it('stays out of the way while neither provider is on', async () => {
    const calls = await openWith('');
    await waitFor(() => expect(calls.some((c) => c.url === '/api/me/sign-ins')).toBe(true));
    expect(screen.queryByText('Sign-in methods')).toBeNull();
    expect(screen.getByRole('button', { name: 'Change password…' })).toBeTruthy();
  });

  it('shows the connected account and connects the other provider', async () => {
    const calls = await openWith('signin-linked,signin-github');
    const section = (await screen.findByRole('heading', { name: 'Sign-in methods' })).closest('section')!;
    expect(within(section).getByText('sam@acme.com')).toBeTruthy();
    expect(within(section).getByText('Not connected')).toBeTruthy();
    expect(within(section).getByRole('button', { name: 'Disconnect Google' })).toBeTruthy();
    await fireEvent.click(within(section).getByRole('button', { name: 'Connect GitHub' }));
    await waitFor(() => expect(left).toEqual(['/api/mock/oauth/github?state=mock']));
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/me/sign-ins/github' && c.headers['x-csrf-token'] === 'mock-csrf')).toBe(true);
  });

  it('disconnects without asking and says so', async () => {
    const calls = await openWith('signin-linked');
    await fireEvent.click(await screen.findByRole('button', { name: 'Disconnect Google' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/me/sign-ins/google')).toBe(true));
    await waitFor(() => expect(toaster.current?.text).toBe('Google disconnected.'));
    expect(await screen.findByRole('button', { name: 'Connect Google' })).toBeTruthy();
  });

  it('keeps the only way into an account without a password', async () => {
    await openWith('signin-only');
    expect(await screen.findByText('Not set. You sign in with Google.')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Change password…' })).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Disconnect Google' }));
    expect(await screen.findByText('This is how you sign in. Ask an admin for a password before you disconnect it.')).toBeTruthy();
  });

  it('says a provider that was turned off is off', async () => {
    // A link stays after its provider is turned off.
    await open({
      override: (m, url) =>
        m === 'GET' && url === '/api/me/sign-ins'
          ? { providers: [], linked: [{ provider: 'github', user_id: 'usr_x', login: 'sam-acme', email: 'sam@acme.com', created_at: '', last_used_at: '' }], has_password: true }
          : undefined,
    });
    expect(await screen.findByText('@sam-acme · sam@acme.com · GitHub sign-in is off')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Disconnect GitHub' })).toBeTruthy();
  });

  it('says when a connection came back connected or refused, once', async () => {
    await openWith('signin-google', '/settings/account?connected=google');
    await waitFor(() => expect(toaster.current?.text).toBe('Google connected.'));
    expect(router.query.has('connected')).toBe(false);
  });

  it('says why a connection did not finish', async () => {
    await openWith('signin-google', '/settings/account?signin_error=taken&provider=google');
    expect(await screen.findByText('That Google account is already connected to someone else here.')).toBeTruthy();
    expect(router.query.has('signin_error')).toBe(false);
  });

  it('names a refused account from the refusal cookie only, once', async () => {
    document.cookie = 'bothy_signin_who=sam-gh; Path=/';
    await openWith('signin-github', '/settings/account?signin_error=not_allowed&provider=github&who=ceo-gh');
    expect(
      await screen.findByText("@sam-gh isn't on this Bothy's sign-in list, so it can't be connected. Admins add people in Settings → Security."),
    ).toBeTruthy();
    expect(document.cookie).not.toContain('bothy_signin_who');
  });
});
