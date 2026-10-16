import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../../mock/server';
import { state as mockSettings } from '../../../../mock/domains/settings';
import { toaster } from '../../../components/ui';
import { config } from '../../../lib/config.svelte';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import GitHubSignIn from './GitHubSignIn.svelte';
import GoogleSignIn from './GoogleSignIn.svelte';
import Security from './Security.svelte';

async function signedIn(mock = '', override?: (m: string, url: string) => unknown): Promise<Call[]> {
  const calls = installApi({ override });
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  config.view = null;
  return calls;
}

const patches = (calls: Call[], url: string) => calls.filter((c) => c.method === 'PATCH' && c.url === url).map((c) => JSON.parse(c.body));

beforeEach(() => toaster.dismiss());

describe('Security → Sign-in', () => {
  async function open(mock = '', override?: (m: string, url: string) => unknown) {
    const calls = await signedIn(mock, override);
    render(Security);
    const section = (await screen.findByRole('heading', { name: 'Sign-in' })).closest('section')!;
    return { calls, section };
  }

  it('starts with who can sign in, then the account switch, then the providers', async () => {
    const { section } = await open();
    const list = within(section).getByLabelText('Who can sign in') as HTMLTextAreaElement;
    expect(list.value).toBe('');
    expect(
      within(section).getByText(
        'Email addresses or domains, like acme.com or pat@gmail.com. A domain lets in Google Workspace accounts on it. GitHub accounts need their exact address here, or an organization on the GitHub page.',
      ),
    ).toBeTruthy();
    const create = within(section).getByRole('switch', { name: 'Create an account the first time someone on the list signs in' });
    expect(create.getAttribute('aria-disabled')).toBe('true');
    expect(within(section).getByText('Adding people needs team mode. Turn it on in Settings → Advanced.')).toBeTruthy();
    const rows = within(section).getAllByRole('link');
    expect(rows.map((r) => r.textContent?.replace(/\s+/g, ' ').trim())).toEqual(['Google Off', 'GitHub Off']);
    expect(rows[0].getAttribute('href')).toBe('/settings/security/google-sign-in');
    expect(list.compareDocumentPosition(create) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('saves the list when you leave it, one entry per line or separated by commas', async () => {
    const { calls, section } = await open('signin-google');
    const list = within(section).getByLabelText('Who can sign in') as HTMLTextAreaElement;
    await waitFor(() => expect(list.value).toBe('acme.com'));
    expect(within(section).getAllByRole('link')[0].textContent).toContain('On');
    await fireEvent.input(list, { target: { value: 'Acme.com, pat@gmail.com\n@eng.acme.io\n\nacme.com' } });
    await fireEvent.blur(list);
    await waitFor(() => expect(patches(calls, '/api/sign-in')).toEqual([{ allow: ['acme.com', 'pat@gmail.com', 'eng.acme.io'] }]));
    await waitFor(() => expect(list.value).toBe('acme.com\npat@gmail.com\neng.acme.io'));
    expect(await within(section).findByText('Saved')).toBeTruthy();
  });

  it('refuses a public mail domain before sending it', async () => {
    const { calls, section } = await open();
    const list = within(section).getByLabelText('Who can sign in');
    await fireEvent.input(list, { target: { value: 'gmail.com' } });
    await fireEvent.blur(list);
    expect(await within(section).findByText("Anyone can get an address at gmail.com. List each person's address instead.")).toBeTruthy();
    expect(list.getAttribute('aria-invalid')).toBe('true');
    expect(patches(calls, '/api/sign-in')).toEqual([]);
    await fireEvent.input(list, { target: { value: 'not a domain' } });
    await fireEvent.blur(list);
    expect(await within(section).findByText("“not a domain” isn't an email address or a domain. Write it like acme.com or pat@gmail.com.")).toBeTruthy();
  });

  it('says why the server kept the list', async () => {
    const refused = () =>
      new Response(
        JSON.stringify({
          error: { code: 'bad_request', message: 'x' },
          fields: [{ path: 'auth.sign_in.google.enabled', source: { layer: 'ui' }, message: 'bothy.ui.yaml: auth.sign_in.google.enabled: Google sign-in needs at least one address or domain in auth.sign_in.allow' }],
        }),
        { status: 400 },
      );
    const { section } = await open('signin-google', (m, url) => (m === 'PATCH' && url === '/api/sign-in' ? refused() : undefined));
    const list = within(section).getByLabelText('Who can sign in') as HTMLTextAreaElement;
    await waitFor(() => expect(list.value).toBe('acme.com'));
    await fireEvent.input(list, { target: { value: '' } });
    await fireEvent.blur(list);
    expect(await within(section).findByText('Google sign-in is on and needs someone on the list. Turn it off first, or keep at least one entry.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('auth.sign_in');
  });

  it('says GitHub needs an address when only domains would be left', async () => {
    const refused = () =>
      new Response(
        JSON.stringify({
          error: { code: 'bad_request', message: 'x' },
          fields: [
            {
              path: 'auth.sign_in.github.enabled',
              source: { layer: 'ui' },
              message:
                'bothy.ui.yaml: auth.sign_in.github.enabled: GitHub sign-in needs at least one email address in auth.sign_in.allow (domains apply to Google only), or an organization in auth.sign_in.github.orgs',
            },
          ],
        }),
        { status: 400 },
      );
    const { section } = await open('signin-github', (m, url) => (m === 'PATCH' && url === '/api/sign-in' ? refused() : undefined));
    const list = within(section).getByLabelText('Who can sign in') as HTMLTextAreaElement;
    await waitFor(() => expect(list.value).toBe('acme.com'));
    await fireEvent.input(list, { target: { value: 'acme.com, eng.acme.io' } });
    await fireEvent.blur(list);
    expect(await within(section).findByText('GitHub sign-in is on and needs an email address on the list, or an organization. Turn it off first, or keep an address.')).toBeTruthy();
  });

  it('lets people in as members in team mode', async () => {
    const { calls, section } = await open('team-mode');
    const create = within(section).getByRole('switch', { name: 'Create an account the first time someone on the list signs in' });
    expect(within(section).getByText('People who already have an account should connect Google or GitHub in Account first.')).toBeTruthy();
    expect(within(section).queryByText(/GitHub organizations/)).toBeNull();
    await fireEvent.click(create);
    await waitFor(() => expect(patches(calls, '/api/sign-in')).toEqual([{ allow_new: true }]));
    await waitFor(() => expect(create.getAttribute('aria-checked')).toBe('true'));
  });

  it('says members of the GitHub organizations get an account too', async () => {
    const { section } = await open('team-mode,signin-github');
    await within(section).findByText(
      'Members of the GitHub organizations you add also get an account. People who already have an account should connect Google or GitHub in Account first.',
    );
  });

  it('marks a provider that is on but not working', async () => {
    const { section } = await open('signin-no-secret');
    const google = within(section).getAllByRole('link')[0];
    expect(google.textContent).toContain('Not working');
    expect(google.querySelector('.text-danger-text')).toBeTruthy();
  });
});

describe('Google sign-in page', () => {
  it('needs the details before it can be turned on, then saves and checks them', async () => {
    const calls = await signedIn();
    render(GoogleSignIn);
    expect(await screen.findByRole('heading', { level: 1, name: 'Google sign-in' })).toBeTruthy();
    const sw = await screen.findByRole('switch', { name: 'Sign in with Google' });
    expect(sw.getAttribute('aria-disabled')).toBe('true');
    expect(screen.getByText('Save the details below to turn this on.')).toBeTruthy();
    expect(screen.getByText('https://bothy.acme.com/api/auth/oauth/google/callback')).toBeTruthy();
    expect(screen.getByRole('link', { name: /Open Google Cloud/ }).getAttribute('href')).toBe('https://console.cloud.google.com/apis/credentials');

    await fireEvent.input(screen.getByLabelText('Client ID'), { target: { value: ' 1234.apps.googleusercontent.com ' } });
    await fireEvent.input(screen.getByLabelText('Client secret'), { target: { value: 'GOCSPX-abc' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Saved. Google accepted the client ID and secret.')).toBeTruthy();
    expect(patches(calls, '/api/sign-in/google')).toEqual([{ client_id: '1234.apps.googleusercontent.com', client_secret: 'GOCSPX-abc' }]);
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/sign-in/google/check')).toBe(true);
    // The secret is never shown again; it can be replaced.
    expect(screen.queryByLabelText('Client secret')).toBeNull();
    expect(screen.getByRole('button', { name: 'Replace the client secret' })).toBeTruthy();
    // Nobody is on the list yet.
    expect(screen.getByText('Add who can sign in on the Security page to turn this on.')).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patches(calls, '/api/sign-in/google')).toHaveLength(2));
    expect(patches(calls, '/api/sign-in/google')[1]).toEqual({ client_id: '1234.apps.googleusercontent.com' });
  });

  it('turns off and on once everything is in place', async () => {
    const calls = await signedIn('signin-google');
    render(GoogleSignIn);
    const sw = await screen.findByRole('switch', { name: 'Sign in with Google' });
    expect(sw.getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('link', { name: /Who can sign in/ }).textContent).toContain('1 address or domain');
    await fireEvent.click(sw);
    await waitFor(() => expect(patches(calls, '/api/sign-in/google')).toEqual([{ enabled: false }]));
    await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('false'));
    await fireEvent.click(sw);
    await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('true'));
  });

  it("doesn't blame GitHub for a 502 on the Google page", async () => {
    const down = () => new Response(JSON.stringify({ error: { code: 'provider_error', message: 'bad gateway' } }), { status: 502 });
    await signedIn('signin-google', (m, url) => (m === 'PATCH' && url === '/api/sign-in/google' ? down() : undefined));
    render(GoogleSignIn);
    await fireEvent.click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByText("Couldn't save. Try again.")).toBeTruthy();
    expect(screen.queryByText(/GitHub/)).toBeNull();
  });

  it('says when Google refuses the client', async () => {
    await signedIn('signin-google,signin-check-fail');
    render(GoogleSignIn);
    await fireEvent.click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByText("Saved, but Google didn't accept the client ID or secret. Copy them again from Google Cloud.")).toBeTruthy();
  });

  it.each([
    ['signin-no-address', "Set Bothy's web address before turning this on. Google sends people back to it after they sign in."],
    ['signin-http-address', "Bothy's web address must start with https:// for Google sign-in. Put Bothy behind HTTPS, then change the address in General."],
  ])('explains %s and keeps the switch off', async (mock, text) => {
    await signedIn(mock);
    render(GoogleSignIn);
    expect(await screen.findByText(text)).toBeTruthy();
    expect(screen.getByText('Set the web address first.')).toBeTruthy();
    expect(screen.getByRole('switch', { name: 'Sign in with Google' }).getAttribute('aria-disabled')).toBe('true');
    expect(screen.getByRole('button', { name: 'Open General' })).toBeTruthy();
  });

  it('keeps what the server sets out of reach', async () => {
    await signedIn();
    const e = mockSettings.effective.entries.find((x) => x.path === 'auth.sign_in.google.client_id')!;
    e.source = { layer: 'env', env_var: 'BOTHY_AUTH__SIGN_IN__GOOGLE__CLIENT_ID' };
    render(GoogleSignIn);
    await waitFor(() => expect((screen.getByLabelText('Client ID') as HTMLInputElement).disabled).toBe(true));
    expect(screen.getByText("This is set on the server and can't be changed here.")).toBeTruthy();
  });

  it('is for admins only', async () => {
    await signedIn('member');
    render(GoogleSignIn);
    expect(await screen.findByText('Only admins can see this page.')).toBeTruthy();
  });
});

describe('GitHub sign-in page', () => {
  it('names what GitHub calls things and takes organizations', async () => {
    const calls = await signedIn('signin-github');
    render(GitHubSignIn);
    expect(await screen.findByRole('heading', { level: 1, name: 'GitHub sign-in' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'GitHub OAuth app' })).toBeTruthy();
    expect(screen.getByText('Callback URL')).toBeTruthy();
    expect(screen.getByText('https://bothy.acme.com/api/auth/oauth/github/callback')).toBeTruthy();
    expect(screen.getByText('Members of these GitHub organizations can also sign in.')).toBeTruthy();
    const orgs = screen.getByLabelText(/Organizations/) as HTMLInputElement;
    expect(orgs.value).toBe('acme-inc');

    await fireEvent.input(orgs, { target: { value: 'acme-inc, -bad' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Enter organization names like acme-inc, separated by commas.')).toBeTruthy();
    expect(patches(calls, '/api/sign-in/github')).toEqual([]);

    await fireEvent.input(orgs, { target: { value: 'acme-inc, Acme-Labs, ACME-INC' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patches(calls, '/api/sign-in/github')).toEqual([{ client_id: 'Ov23liMock', orgs: ['acme-inc', 'Acme-Labs'] }]));
    expect(await screen.findByText('Saved. GitHub accepted the client ID and secret.')).toBeTruthy();
  });

  it('says which organization GitHub does not know', async () => {
    const calls = await signedIn('signin-github');
    render(GitHubSignIn);
    const orgs = (await screen.findByLabelText(/Organizations/)) as HTMLInputElement;
    await fireEvent.input(orgs, { target: { value: 'acme-inc, no-such-org' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('GitHub has no organization called no-such-org.')).toBeTruthy();
    expect(orgs.getAttribute('aria-invalid')).toBe('true');
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/sign-in/github/check')).toBe(false);
  });

  const answer = (status: number, code: string, message: string) => () => new Response(JSON.stringify({ error: { code, message } }), { status });

  it.each([
    [502, 'provider_error', "Bothy couldn't reach GitHub to check the organizations. Try again."],
    [502, 'provider_rate_limited', 'GitHub is limiting requests from this server right now. Try again in an hour.'],
    // A proxy in front of Bothy answering 502 is not GitHub out of reach.
    [502, 'internal', "Couldn't save. Try again."],
  ])('says why a save failed with %i %s', async (status, code, text) => {
    const refused = answer(status, code, 'from the server');
    await signedIn('signin-github', (m, url) => (m === 'PATCH' && url === '/api/sign-in/github' ? refused() : undefined));
    render(GitHubSignIn);
    await fireEvent.click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByText(text)).toBeTruthy();
  });

  it('counts only email addresses in who can sign in', async () => {
    await signedIn('signin-github');
    render(GitHubSignIn);
    expect((await screen.findByRole('link', { name: /Who can sign in/ })).textContent).toContain('No email addresses');
  });

  it('can rely on organizations alone', async () => {
    await signedIn('signin-github');
    render(GitHubSignIn);
    const sw = await screen.findByRole('switch', { name: 'Sign in with GitHub' });
    expect(sw.getAttribute('aria-disabled')).toBeNull();
  });

  it('needs an exact address or an organization: a domain lets in Google accounts only', async () => {
    const calls = await signedIn('signin-github');
    render(GitHubSignIn);
    const sw = await screen.findByRole('switch', { name: 'Sign in with GitHub' });
    await fireEvent.click(sw);
    await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('false'));
    await fireEvent.input(screen.getByLabelText(/Organizations/), { target: { value: '' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patches(calls, '/api/sign-in/github').at(-1)).toEqual({ client_id: 'Ov23liMock', orgs: [] }));
    expect(await screen.findByText('Add an email address on the Security page, or an organization below, to turn this on.')).toBeTruthy();
    expect(sw.getAttribute('aria-disabled')).toBe('true');
  });
});
